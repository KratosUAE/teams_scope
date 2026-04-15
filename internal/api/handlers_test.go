package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"teams_con/internal/store"
)

// newTestServer wires real Handlers over a real Service constructed from
// fakes. This gives handler+service end-to-end coverage (including
// sentinel-to-HTTP mapping) without a second layer of mocks.
func newTestServer(
	calls *fakeCalls,
	streams *fakeStreams,
	users *fakeUsers,
	meta *fakeMeta,
	pinger *fakePinger,
) *httptest.Server {
	return newTestServerFull(calls, streams, users, meta, nil, nil, pinger)
}

// newTestServerFull is the variant that injects a fakeSubnets / fakeUserCards
// too — for the write-route tests added in Phase 1 and extended in Phase 2.
func newTestServerFull(
	calls *fakeCalls,
	streams *fakeStreams,
	users *fakeUsers,
	meta *fakeMeta,
	subnets *fakeSubnets,
	userCards *fakeUserCards,
	pinger *fakePinger,
) *httptest.Server {
	svc := newTestServiceFull(calls, streams, users, meta, subnets, userCards, pinger)
	h := NewHandlers(svc, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	return httptest.NewServer(mux)
}

func doGet(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestHandler_Health_Ok(t *testing.T) {
	meta := &fakeMeta{meta: &store.CrawlerMeta{}}
	ts := newTestServer(nil, nil, nil, meta, &fakePinger{})
	defer ts.Close()

	resp := doGet(t, ts, "/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	h := decode[Health](t, resp)
	if !h.MongoOk {
		t.Errorf("MongoOk = false, want true")
	}
}

func TestHandler_Health_PingFailureStill200(t *testing.T) {
	meta := &fakeMeta{meta: &store.CrawlerMeta{}}
	ts := newTestServer(nil, nil, nil, meta, &fakePinger{err: errors.New("no mongo")})
	defer ts.Close()

	resp := doGet(t, ts, "/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	h := decode[Health](t, resp)
	if h.MongoOk {
		t.Errorf("MongoOk = true, want false")
	}
}

func TestHandler_Health_MetaErrorIs500(t *testing.T) {
	meta := &fakeMeta{err: errors.New("boom")}
	ts := newTestServer(nil, nil, nil, meta, &fakePinger{})
	defer ts.Close()

	resp := doGet(t, ts, "/health")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestHandler_ListCalls_Ok(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{{CallId: "a"}, {CallId: "b"}}}
	ts := newTestServer(calls, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls?verdict=Bad&limit=50")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decode[[]store.Call](t, resp)
	if len(out) != 2 {
		t.Errorf("len = %d, want 2", len(out))
	}
	if calls.listParams.Verdict == nil || *calls.listParams.Verdict != "Bad" {
		t.Errorf("verdict filter not propagated: %+v", calls.listParams.Verdict)
	}
	if calls.listParams.Limit != 50 {
		t.Errorf("limit = %d, want 50", calls.listParams.Limit)
	}
}

func TestHandler_ListCalls_BadVerdict(t *testing.T) {
	ts := newTestServer(nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls?verdict=nope")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	errBody := decode[errorResponse](t, resp)
	if errBody.Error == "" {
		t.Errorf("error body empty")
	}
}

func TestHandler_ListCalls_BadTime(t *testing.T) {
	ts := newTestServer(nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls?from=not-a-time")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_ListCalls_BadLimit(t *testing.T) {
	ts := newTestServer(nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls?limit=abc")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_ListCalls_StoreErrorIs500(t *testing.T) {
	calls := &fakeCalls{listErr: errors.New("boom")}
	ts := newTestServer(calls, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestHandler_GetCall_Ok(t *testing.T) {
	calls := &fakeCalls{getResult: &store.Call{CallId: "abc", Verdict: "Good"}}
	streams := &fakeStreams{rows: []store.StreamRow{{CallId: "abc", User: "alice"}}}
	ts := newTestServer(calls, streams, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls/abc")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	detail := decode[CallDetail](t, resp)
	if detail.Call.CallId != "abc" {
		t.Errorf("call id = %q", detail.Call.CallId)
	}
	if len(detail.Streams) != 1 || detail.Streams[0].User != "alice" {
		t.Errorf("streams missing: %+v", detail.Streams)
	}
}

func TestHandler_GetCall_NotFound(t *testing.T) {
	calls := &fakeCalls{getErr: store.ErrNotFound}
	ts := newTestServer(calls, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/calls/missing")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_ListUsers_Ok(t *testing.T) {
	users := &fakeUsers{result: []store.UserStat{
		{Upn: "alice", CallCount: 3, GoodCount: 2, PoorCount: 1},
	}}
	ts := newTestServer(nil, nil, users, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/users?from=2026-04-01T00:00:00Z")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decode[[]store.UserStat](t, resp)
	if len(out) != 1 || out[0].Upn != "alice" {
		t.Errorf("unexpected: %+v", out)
	}
	if users.params.From == nil {
		t.Errorf("from not propagated")
	}
}

func TestHandler_ListUsers_BadTime(t *testing.T) {
	ts := newTestServer(nil, nil, nil, nil, nil)
	defer ts.Close()
	resp := doGet(t, ts, "/users?from=bogus")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_ListUserCalls_Ok(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{{CallId: "a"}}}
	ts := newTestServer(calls, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/users/bob@corp.com/calls")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if calls.listParams.Upn == nil || *calls.listParams.Upn != "bob@corp.com" {
		t.Errorf("upn not propagated: %+v", calls.listParams.Upn)
	}
}

// --- Phase 1 subnet write routes ---------------------------------------

func TestHandler_ListSubnets_Empty(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/subnets")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decode[[]store.SubnetEntry](t, resp)
	if out == nil {
		t.Errorf("body must be [] not null")
	}
}

func TestHandler_UpsertSubnet_RoundTrip(t *testing.T) {
	subs := newFakeSubnets()
	ts := newTestServerFull(nil, nil, nil, nil, subs, nil, nil)
	defer ts.Close()

	body := strings.NewReader(`{"cidr":"10.16.0.5/16","name":"Dubai HQ","office":"Dubai","kind":"wired"}`)
	resp, err := http.Post(ts.URL+"/subnets", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decode[store.SubnetEntry](t, resp)
	if got.Cidr != "10.16.0.0/16" {
		t.Errorf("Cidr = %q, want canonical 10.16.0.0/16", got.Cidr)
	}
	if _, ok := subs.entries["10.16.0.0/16"]; !ok {
		t.Errorf("upsert did not reach the store")
	}
}

func TestHandler_UpsertSubnet_BadCidr(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	body := strings.NewReader(`{"cidr":"garbage","name":"x"}`)
	resp, err := http.Post(ts.URL+"/subnets", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_UpsertSubnet_UnknownField(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	body := strings.NewReader(`{"cidr":"10.0.0.0/24","name":"x","mystery":1}`)
	resp, err := http.Post(ts.URL+"/subnets", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown fields must reject, status = %d", resp.StatusCode)
	}
}

func TestHandler_DeleteSubnet_NotFound(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/subnets?cidr=10.0.0.0/24", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_DeleteSubnet_Happy(t *testing.T) {
	subs := newFakeSubnets(store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "x"})
	ts := newTestServerFull(nil, nil, nil, nil, subs, nil, nil)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/subnets?cidr=10.0.0.0/24", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if _, ok := subs.entries["10.0.0.0/24"]; ok {
		t.Errorf("entry not removed")
	}
}

func TestHandler_DeleteSubnet_MissingCidrParam(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/subnets", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// M1: prove that a syntactically broken JSON body returns 400 with a
// non-empty error field, not a 500 or a silent success.
func TestHandler_UpsertSubnet_MalformedJSON(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/subnets", "application/json", strings.NewReader(`{broken`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decode[errorResponse](t, resp)
	if body.Error == "" {
		t.Errorf("error field must be non-empty for malformed JSON")
	}
}

// M2: prove that POSTing with a non-JSON Content-Type returns 400.
func TestHandler_UpsertSubnet_WrongContentType(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/subnets", "text/plain", strings.NewReader(`{"cidr":"10.0.0.0/24","name":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decode[errorResponse](t, resp)
	if body.Error == "" {
		t.Errorf("error field must be non-empty for wrong Content-Type")
	}
}

// H2: prove that DELETE with a percent-encoded slash (%2F) in the cidr query
// param round-trips correctly through r.URL.Query().Get("cidr").
func TestHandler_DeleteSubnet_PercentEncodedCidr(t *testing.T) {
	subs := newFakeSubnets(store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "x"})
	ts := newTestServerFull(nil, nil, nil, nil, subs, nil, nil)
	defer ts.Close()

	// url.QueryEscape encodes "/" as "%2F" — the same form cobra / curl produce.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/subnets?cidr=10.0.0.0%2F24", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for percent-encoded cidr", resp.StatusCode)
	}
	if _, ok := subs.entries["10.0.0.0/24"]; ok {
		t.Errorf("entry should have been removed")
	}
}

// -- Usercards (Phase 2) ----------------------------------------------

func putJSON(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return resp
}

func TestHandler_UpsertUserCard_RoundTrip(t *testing.T) {
	cards := newFakeUserCards()
	ts := newTestServerFull(nil, nil, nil, nil, nil, cards, nil)
	defer ts.Close()

	resp := putJSON(t, ts, "/usercards/alice@corp.com",
		`{"displayName":"Alice","location":"Dubai HQ","tags":["vip"],"notes":"escalated"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decode[store.UserCard](t, resp)
	if got.Upn != "alice@corp.com" || got.DisplayName != "Alice" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if _, ok := cards.entries["alice@corp.com"]; !ok {
		t.Errorf("upsert did not reach the store")
	}
}

func TestHandler_UpsertUserCard_PathOverridesBody(t *testing.T) {
	cards := newFakeUserCards()
	ts := newTestServerFull(nil, nil, nil, nil, nil, cards, nil)
	defer ts.Close()

	// Body claims "wrong@corp.com" but path says "alice@corp.com" — path wins.
	resp := putJSON(t, ts, "/usercards/alice@corp.com",
		`{"upn":"wrong@corp.com","displayName":"Alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, ok := cards.entries["alice@corp.com"]; !ok {
		t.Errorf("path upn ignored; store has %+v", cards.entries)
	}
	if _, ok := cards.entries["wrong@corp.com"]; ok {
		t.Errorf("body upn leaked into store")
	}
}

func TestHandler_UpsertUserCard_MalformedJSON(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := putJSON(t, ts, "/usercards/alice@corp.com", `{broken`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_UpsertUserCard_WrongContentType(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/usercards/alice@corp.com",
		strings.NewReader(`{"displayName":"Alice"}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_GetUserCard_NotFound(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/usercards/ghost@corp.com")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_GetUserCard_Happy(t *testing.T) {
	cards := newFakeUserCards(store.UserCard{Upn: "alice@corp.com", DisplayName: "Alice"})
	ts := newTestServerFull(nil, nil, nil, nil, nil, cards, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/usercards/alice@corp.com")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decode[store.UserCard](t, resp)
	if got.DisplayName != "Alice" {
		t.Errorf("got = %+v", got)
	}
}

func TestHandler_DeleteUserCard_Happy(t *testing.T) {
	cards := newFakeUserCards(store.UserCard{Upn: "alice@corp.com"})
	ts := newTestServerFull(nil, nil, nil, nil, nil, cards, nil)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/usercards/alice@corp.com", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if _, ok := cards.entries["alice@corp.com"]; ok {
		t.Errorf("entry not removed")
	}
}

func TestHandler_DeleteUserCard_NotFound(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/usercards/ghost@corp.com", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_ListUserCards_Empty(t *testing.T) {
	ts := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	defer ts.Close()

	resp := doGet(t, ts, "/usercards")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decode[[]store.UserCard](t, resp)
	if got == nil {
		t.Errorf("want [], got nil")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestHandler_ResponseContentType(t *testing.T) {
	meta := &fakeMeta{meta: &store.CrawlerMeta{}}
	ts := newTestServer(nil, nil, nil, meta, &fakePinger{})
	defer ts.Close()

	resp := doGet(t, ts, "/health")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHandler_NetworkHotspots_Happy(t *testing.T) {
	now := time.Now().UTC()
	calls := &fakeCalls{metaResult: []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
	}}
	streams := &fakeStreams{windowRows: []store.StreamRow{
		{CallId: "c1", User: "a@x.com", Subnet: "10.0.0.0/24"},
		{CallId: "c2", User: "b@x.com", Subnet: "10.0.0.0/24"},
		{CallId: "c3", User: "c@x.com", Subnet: "10.0.0.0/24"},
	}}
	ts := newTestServer(calls, streams, nil, nil, &fakePinger{})
	defer ts.Close()

	resp := doGet(t, ts, "/network/hotspots?min_calls=2&min_bad_ratio=0.5")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := decode[[]Hotspot](t, resp)
	if len(got) != 1 || got[0].Subnet != "10.0.0.0/24" {
		t.Errorf("got %+v", got)
	}
}

func TestHandler_NetworkHotspots_BadGroupBy(t *testing.T) {
	calls := &fakeCalls{metaResult: []store.CallMeta{{CallId: "c1", Verdict: "Bad"}}}
	ts := newTestServer(calls, &fakeStreams{}, nil, nil, &fakePinger{})
	defer ts.Close()

	resp := doGet(t, ts, "/network/hotspots?group_by=subnet_relay")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
