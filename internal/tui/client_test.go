package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"teams_con/internal/store"
)

// newTestClient wires a Client against an httptest.Server. The helper
// centralises base URL trimming and ensures every test uses the short
// timeout that keeps the suite fast.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	c.http.Timeout = 2 * time.Second
	return c
}

func TestClient_Health_OK(t *testing.T) {
	ts := time.Date(2026, 4, 10, 12, 30, 0, 0, time.UTC)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(HealthDTO{
			LastCrawlAt: &ts,
			MongoOk:     true,
		})
	})
	c := newTestClient(t, h)

	got, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got == nil || !got.MongoOk {
		t.Fatalf("MongoOk=false, want true: %+v", got)
	}
	if got.LastCrawlAt == nil || !got.LastCrawlAt.Equal(ts) {
		t.Fatalf("LastCrawlAt = %v, want %v", got.LastCrawlAt, ts)
	}
}

func TestClient_Health_ErrorBody(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(errorDTO{Error: "mongo down"})
	})
	c := newTestClient(t, h)

	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "mongo down") {
		t.Fatalf("error missing body msg: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error missing status: %v", err)
	}
}

func TestClient_ListCalls_QueryBuild(t *testing.T) {
	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	var gotQuery string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/calls" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]store.Call{
			{CallId: "c1", Verdict: "Good", Organizer: "a@b"},
			{CallId: "c2", Verdict: "Bad", Organizer: "c@d"},
		})
	})
	c := newTestClient(t, h)

	calls, err := c.ListCalls(context.Background(), ListCallsParams{
		From:    &from,
		To:      &to,
		Verdict: "Bad",
		Upn:     "alice@example.com",
		Limit:   50,
		Offset:  10,
	})
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}
	if calls[0].CallId != "c1" {
		t.Fatalf("calls[0].CallId = %q", calls[0].CallId)
	}

	want := []string{
		"from=" + from.Format(time.RFC3339),
		"to=" + to.Format(time.RFC3339),
		"verdict=Bad",
		"upn=alice%40example.com",
		"limit=50",
		"offset=10",
	}
	for _, sub := range want {
		if !strings.Contains(gotQuery, strings.ReplaceAll(sub, ":", "%3A")) &&
			!strings.Contains(gotQuery, sub) {
			t.Errorf("query %q missing %q", gotQuery, sub)
		}
	}
}

func TestClient_ListCalls_EmptyFilters(t *testing.T) {
	var gotQuery string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]store.Call{})
	})
	c := newTestClient(t, h)

	_, err := c.ListCalls(context.Background(), ListCallsParams{})
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("empty filter produced query %q, want empty", gotQuery)
	}
}

func TestClient_GetCall_OK(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/calls/abc123" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(callDetailDTO{
			Call:    store.Call{CallId: "abc123", Verdict: "Poor"},
			Streams: []store.StreamRow{{CallId: "abc123", User: "u1"}},
		})
	})
	c := newTestClient(t, h)

	call, streams, err := c.GetCall(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetCall: %v", err)
	}
	if call.CallId != "abc123" || call.Verdict != "Poor" {
		t.Fatalf("call = %+v", call)
	}
	if len(streams) != 1 || streams[0].User != "u1" {
		t.Fatalf("streams = %+v", streams)
	}
}

func TestClient_GetCall_EmptyID(t *testing.T) {
	c := NewClient("http://localhost:0")
	_, _, err := c.GetCall(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestClient_GetCall_NotFound(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(errorDTO{Error: "not found"})
	})
	c := newTestClient(t, h)

	_, _, err := c.GetCall(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("want 404 in err, got %v", err)
	}
}

func TestClient_ListUsers_OK(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]store.UserStat{
			{Upn: "a@x", CallCount: 5, GoodCount: 4, PoorCount: 1},
			{Upn: "b@x", CallCount: 2, BadCount: 2},
		})
	})
	c := newTestClient(t, h)

	users, err := c.ListUsers(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("len = %d", len(users))
	}
	if users[0].Upn != "a@x" || users[1].BadCount != 2 {
		t.Fatalf("users = %+v", users)
	}
}

func TestClient_ListUsers_WithWindow(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	var gotQuery string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]store.UserStat{})
	})
	c := newTestClient(t, h)

	_, err := c.ListUsers(context.Background(), &from, &to)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if !strings.Contains(gotQuery, "from=") || !strings.Contains(gotQuery, "to=") {
		t.Fatalf("query %q missing from/to", gotQuery)
	}
}

func TestClient_NonJSONError(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream dead"))
	})
	c := newTestClient(t, h)

	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream dead") {
		t.Errorf("err = %v", err)
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://localhost:8080/")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("baseURL = %q, want trimmed", c.baseURL)
	}
}
