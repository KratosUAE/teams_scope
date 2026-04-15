package graph

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubToken is a tokenProvider that returns a fixed string. Tests use it to
// bypass MSAL entirely.
type stubToken struct{ val string }

func (s stubToken) Token(_ context.Context) (string, error) { return s.val, nil }

// newTestClient builds a Client wired to the given test server URL with a
// discard logger. We override base after construction so the production
// constructor stays unaware of test paths.
func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c := newWithProvider(stubToken{val: "test-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.base = baseURL
	return c
}

func TestListCallRecordsInRange_Pagination(t *testing.T) {
	var page int32

	mux := http.NewServeMux()
	mux.HandleFunc("/communications/callRecords", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/wrong Authorization header: %q", got)
		}
		n := atomic.AddInt32(&page, 1)
		switch n {
		case 1:
			// First page must include the $filter from the caller.
			if f := r.URL.Query().Get("$filter"); !strings.Contains(f, "startDateTime ge ") {
				t.Errorf("missing $filter on page 1: %q", f)
			}
			// Point nextLink at /page2 on the same test server.
			body := `{
				"value": [
					{"id":"call-1","startDateTime":"2026-04-10T10:00:00Z","endDateTime":"2026-04-10T10:05:00Z"},
					{"id":"call-2","startDateTime":"2026-04-10T10:06:00Z","endDateTime":"2026-04-10T10:07:00Z"}
				],
				"@odata.nextLink": "` + serverURL(r) + `/page2"
			}`
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		default:
			t.Errorf("unexpected extra hit on /callRecords (page %d)", n)
		}
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, _ *http.Request) {
		body := `{
			"value":[
				{"id":"call-3","startDateTime":"2026-04-10T11:00:00Z","endDateTime":"2026-04-10T11:01:00Z"}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	start := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	got, err := c.ListCallRecordsInRange(context.Background(), start, end)
	if err != nil {
		t.Fatalf("ListCallRecordsInRange: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 records across 2 pages, got %d", len(got))
	}
	wantIDs := []string{"call-1", "call-2", "call-3"}
	for i, wantID := range wantIDs {
		if got[i].ID != wantID {
			t.Errorf("record[%d].ID = %q, want %q", i, got[i].ID, wantID)
		}
	}
	if !got[0].StartDateTime.Equal(time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("call-1 startDateTime not parsed: %v", got[0].StartDateTime)
	}
}

func TestGetCallRecord_TwoSeparateRequests(t *testing.T) {
	var detailHits, partsHits int32

	mux := http.NewServeMux()
	mux.HandleFunc("/communications/callRecords/call-xyz", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&detailHits, 1)
		// The expand must be present and must NOT include participants_v2.
		exp := r.URL.Query().Get("$expand")
		if !strings.Contains(exp, "sessions($expand=segments)") {
			t.Errorf("$expand on detail call missing sessions/segments: %q", exp)
		}
		if strings.Contains(exp, "participants_v2") {
			t.Errorf("detail call must NOT expand participants_v2 (got %q)", exp)
		}
		body := `{
			"id":"call-xyz",
			"type":"groupCall",
			"startDateTime":"2026-04-10T12:00:00Z",
			"endDateTime":"2026-04-10T12:30:00Z",
			"modalities":["audio"],
			"sessions":[
				{
					"id":"sess-1",
					"caller":{"associatedIdentity":{"user":{"userPrincipalName":"alice@corp.com"}}},
					"callee":{"associatedIdentity":{"user":{"userPrincipalName":"bob@corp.com"}}},
					"segments":[{"startDateTime":"2026-04-10T12:00:00Z","endDateTime":"2026-04-10T12:30:00Z"}]
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/communications/callRecords/call-xyz/participants_v2", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&partsHits, 1)
		body := `{
			"value":[
				{"identity":{"user":{"userPrincipalName":"alice@corp.com"}}},
				{"identity":{"user":{"userPrincipalName":"bob@corp.com"}}},
				{"identity":{"user":{"userPrincipalName":"carol@corp.com"}}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	rec, err := c.GetCallRecord(context.Background(), "call-xyz")
	if err != nil {
		t.Fatalf("GetCallRecord: %v", err)
	}
	if detailHits != 1 || partsHits != 1 {
		t.Fatalf("expected exactly 1 detail + 1 participants_v2 hit, got %d + %d", detailHits, partsHits)
	}
	if rec.ID != "call-xyz" {
		t.Errorf("ID = %q, want call-xyz", rec.ID)
	}
	if len(rec.Sessions) != 1 || rec.Sessions[0].Caller == nil ||
		rec.Sessions[0].Caller.AssociatedIdentity.User.UserPrincipalName != "alice@corp.com" {
		t.Errorf("session caller UPN not decoded: %+v", rec.Sessions)
	}
	if len(rec.ParticipantsV2) != 3 {
		t.Errorf("expected 3 participants_v2, got %d", len(rec.ParticipantsV2))
	}
	if rec.ParticipantsV2[2].Identity.User.UserPrincipalName != "carol@corp.com" {
		t.Errorf("participants_v2[2] UPN not decoded: %+v", rec.ParticipantsV2[2])
	}
}

func TestGetCallRecord_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/communications/callRecords/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.GetCallRecord(context.Background(), "missing")
	if !errors.Is(err, ErrCallNotFound) {
		t.Fatalf("expected ErrCallNotFound, got %v", err)
	}
}

func TestDo_RetryAfterOn429(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/communications/callRecords/throttled", func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Second attempt: succeed.
		body := `{"id":"throttled","type":"peerToPeer","startDateTime":"2026-04-10T13:00:00Z","endDateTime":"2026-04-10T13:01:00Z"}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	// participants_v2 returns empty so the test exercises only the 429 retry
	// on the primary fetch.
	mux.HandleFunc("/communications/callRecords/throttled/participants_v2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"value":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	// Cap the Retry-After sleep to 10ms so the test does not consume 1 real
	// second. Production behavior (no cap) is unchanged because retryAfterCap
	// defaults to zero.
	c.retryAfterCap = 10 * time.Millisecond

	start := time.Now()
	rec, err := c.GetCallRecord(context.Background(), "throttled")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetCallRecord: %v", err)
	}
	if rec.ID != "throttled" {
		t.Errorf("ID = %q, want throttled", rec.ID)
	}
	if hits != 2 {
		t.Errorf("expected 2 hits (one 429 + one success), got %d", hits)
	}
	// The test-only cap ensures we slept at least the cap duration but far
	// less than the raw Retry-After value (1 s from the server).
	if elapsed < 10*time.Millisecond {
		t.Errorf("expected retry to sleep at least retryAfterCap=10ms, but elapsed=%v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("retryAfterCap should keep test well under 500ms, but elapsed=%v", elapsed)
	}
}

func TestParseISO8601Duration(t *testing.T) {
	tests := []struct {
		in     string
		wantOK bool
		wantMs float64
	}{
		{"", false, 0},
		{"PT0.016S", true, 16},
		{"PT1.5S", true, 1500},
		{"PT2M3S", true, 123000},
		{"PT1H", true, 3600000},
		{"garbage", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			d, ok := ParseISO8601Duration(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			gotMs := float64(d) / float64(time.Millisecond)
			if gotMs != tt.wantMs {
				t.Errorf("ms = %v, want %v", gotMs, tt.wantMs)
			}
		})
	}
}

// serverURL extracts the test server's base URL from an incoming request so
// the page-1 handler can build a same-origin nextLink without having to
// close over httptest.Server.URL (which is not yet bound at handler-define
// time inside a closure).
func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
