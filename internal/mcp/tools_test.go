package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"teams_con/internal/api"
	mcpsrv "teams_con/internal/mcp"
	"teams_con/internal/store"
)

// --- minimal fakes (duplicated from internal/api/service_test.go because
// those types are unexported and we need to build a Service from an
// external _test package). ----------------------------------------------

type fakeCalls struct {
	listResult []store.Call
	listErr    error
	getResult  *store.Call
	getErr     error

	metaResult []store.CallMeta
	metaErr    error
}

func (f *fakeCalls) List(_ context.Context, _ store.CallListParams) ([]store.Call, error) {
	return f.listResult, f.listErr
}
func (f *fakeCalls) Get(_ context.Context, _ string) (*store.Call, error) {
	return f.getResult, f.getErr
}
func (f *fakeCalls) ListMetaInWindow(_ context.Context, _, _ *time.Time) ([]store.CallMeta, error) {
	return f.metaResult, f.metaErr
}

type fakeStreams struct {
	rows []store.StreamRow
	err  error

	flakyRows []store.StreamRow
	flakyErr  error

	userRows []store.StreamRow
	userErr  error

	// Phase 4 hotspots hook: rows returned by StreamsRepo.ListInWindow.
	windowRows []store.StreamRow
	windowErr  error
}

func (f *fakeStreams) ListByCall(_ context.Context, _ string) ([]store.StreamRow, error) {
	return f.rows, f.err
}
func (f *fakeStreams) ListFlakyAudioRaw(_ context.Context, _ []string, _ float64) ([]store.StreamRow, error) {
	return f.flakyRows, f.flakyErr
}
func (f *fakeStreams) ListByUserInCalls(_ context.Context, _ string, _ []string) ([]store.StreamRow, error) {
	return f.userRows, f.userErr
}
func (f *fakeStreams) ListInWindowBySubnets(_ context.Context, _, _ []string) ([]store.StreamRow, error) {
	// Phase 3 peer-baseline hook. MCP-layer tests do not exercise the
	// baseline directly; returning (nil, nil) makes Service treat the
	// cohort as insufficient and the report field stays absent.
	return nil, nil
}

// windowRows / windowErr power the Phase 4 hotspots tool. Like the peer
// baseline hook above, user_health_report / flaky mics tests do not read
// this, so (nil, nil) is the safe default.
func (f *fakeStreams) ListInWindow(_ context.Context, _ []string) ([]store.StreamRow, error) {
	return f.windowRows, f.windowErr
}

type fakeUsers struct {
	result []store.UserStat
	err    error
}

func (f *fakeUsers) List(_ context.Context, _ store.UserListParams) ([]store.UserStat, error) {
	return f.result, f.err
}

type fakeMeta struct {
	meta *store.CrawlerMeta
	err  error
}

func (f *fakeMeta) GetCrawlerMeta(_ context.Context) (*store.CrawlerMeta, error) {
	if f.meta == nil && f.err == nil {
		return &store.CrawlerMeta{}, nil
	}
	return f.meta, f.err
}

type fakePinger struct{ err error }

func (f *fakePinger) Ping(_ context.Context) error { return f.err }

// fakeSubnets is the in-memory subnetsReader for mcp_test. Mirrors the
// internal/api fake; duplicated because that one is package-private.
type fakeSubnets struct {
	entries   map[string]store.SubnetEntry
	listErr   error
	upsertErr error
	deleteErr error
}

func newFakeSubnets(seed ...store.SubnetEntry) *fakeSubnets {
	f := &fakeSubnets{entries: map[string]store.SubnetEntry{}}
	for _, e := range seed {
		f.entries[e.Cidr] = e
	}
	return f
}

func (f *fakeSubnets) List(_ context.Context) ([]store.SubnetEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.SubnetEntry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeSubnets) Get(_ context.Context, cidr string) (*store.SubnetEntry, error) {
	e, ok := f.entries[cidr]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &e, nil
}

func (f *fakeSubnets) Upsert(_ context.Context, e store.SubnetEntry) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.entries[e.Cidr] = e
	return nil
}

func (f *fakeSubnets) Delete(_ context.Context, cidr string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.entries[cidr]; !ok {
		return store.ErrNotFound
	}
	delete(f.entries, cidr)
	return nil
}

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(
	calls *fakeCalls,
	streams *fakeStreams,
	users *fakeUsers,
	meta *fakeMeta,
	pinger *fakePinger,
) *mcpsrv.Server {
	return newTestServerFull(calls, streams, users, meta, nil, nil, pinger)
}

// newTestServerFull is the variant that lets a test inject its own
// fakeSubnets / fakeUserCards — used by list_subnets (Phase 1) and
// get_user_card (Phase 2) coverage. Existing tests keep using the 5-arg
// newTestServer so they do not have to grow parameters for unrelated
// readers.
func newTestServerFull(
	calls *fakeCalls,
	streams *fakeStreams,
	users *fakeUsers,
	meta *fakeMeta,
	subnets *fakeSubnets,
	userCards *fakeUserCards,
	pinger *fakePinger,
) *mcpsrv.Server {
	if calls == nil {
		calls = &fakeCalls{}
	}
	if streams == nil {
		streams = &fakeStreams{}
	}
	if users == nil {
		users = &fakeUsers{}
	}
	if meta == nil {
		meta = &fakeMeta{meta: &store.CrawlerMeta{}}
	}
	if subnets == nil {
		subnets = newFakeSubnets()
	}
	if userCards == nil {
		userCards = newFakeUserCards()
	}
	if pinger == nil {
		pinger = &fakePinger{}
	}
	svc := api.NewServiceFromReaders(calls, streams, users, meta, subnets, userCards, pinger, silentLog())
	return mcpsrv.NewServer(svc, silentLog())
}

// fakeUserCards is the in-memory userCardsStore for mcp_test. Mirrors the
// internal/api fake; duplicated because that one is package-private.
type fakeUserCards struct {
	entries   map[string]store.UserCard
	getErr    error
	listErr   error
	upsertErr error
	deleteErr error
}

func newFakeUserCards(seed ...store.UserCard) *fakeUserCards {
	f := &fakeUserCards{entries: map[string]store.UserCard{}}
	for _, c := range seed {
		f.entries[c.Upn] = c
	}
	return f
}

func (f *fakeUserCards) List(_ context.Context) ([]store.UserCard, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.UserCard, 0, len(f.entries))
	for _, c := range f.entries {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeUserCards) Get(_ context.Context, upn string) (*store.UserCard, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	c, ok := f.entries[upn]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &c, nil
}

func (f *fakeUserCards) Upsert(_ context.Context, c store.UserCard) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.entries[c.Upn] = c
	return nil
}

func (f *fakeUserCards) Delete(_ context.Context, upn string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.entries[upn]; !ok {
		return store.ErrNotFound
	}
	delete(f.entries, upn)
	return nil
}

// callTool dispatches a tools/call JSON-RPC message through the server's
// HandleMessage and extracts the CallToolResult. It returns the result's
// text blocks so tests can assert on their contents. errors from the
// transport layer fail the test.
func callTool(t *testing.T, srv *mcpsrv.Server, name string, args map[string]any) (text []string, isError bool) {
	t.Helper()

	// initialize first — required before tools/call per MCP spec.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	}
	initBytes, _ := json.Marshal(initReq)
	if resp := srv.MCP().HandleMessage(context.Background(), initBytes); resp == nil {
		t.Fatal("initialize returned nil")
	}

	call := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	b, _ := json.Marshal(call)
	resp := srv.MCP().HandleMessage(context.Background(), b)
	if resp == nil {
		t.Fatalf("tools/call %s returned nil", name)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw=%s)", err, string(raw))
	}
	if env.Error != nil {
		t.Fatalf("protocol error: %s", env.Error.Message)
	}
	var result mcpsdk.CallToolResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("unmarshal CallToolResult: %v (raw=%s)", err, string(env.Result))
	}
	for _, c := range result.Content {
		if tc, ok := c.(mcpsdk.TextContent); ok {
			text = append(text, tc.Text)
		}
	}
	return text, result.IsError
}

func TestHealth_Happy(t *testing.T) {
	last := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	meta := &fakeMeta{meta: &store.CrawlerMeta{LastCrawlAt: last}}
	srv := newTestServer(nil, nil, nil, meta, &fakePinger{})

	blocks, isErr := callTool(t, srv, "health", map[string]any{})
	if isErr {
		t.Fatalf("unexpected tool error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json blocks, got %d", len(blocks))
	}
	if !strings.Contains(blocks[0], "mongo=ok") {
		t.Errorf("summary = %q", blocks[0])
	}
	if !strings.Contains(blocks[1], "\"mongoOk\":true") {
		t.Errorf("json = %q", blocks[1])
	}
}

func TestListCalls_Happy(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "a", Verdict: "Bad", StartTimeUtc: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)},
		{CallId: "b", Verdict: "Good", StartTimeUtc: time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)},
	}}
	srv := newTestServer(calls, nil, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "list_calls", map[string]any{})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "2 calls") || !strings.Contains(blocks[0], "1 Bad") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestListCalls_BadVerdict(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "list_calls", map[string]any{"verdict": "Weird"})
	if !isErr {
		t.Fatalf("want tool error, got %v", blocks)
	}
	if !strings.Contains(blocks[0], "bad request") {
		t.Errorf("message = %q", blocks[0])
	}
}

func TestGetCall_KnownWithIncludeGoodFilter(t *testing.T) {
	lossHigh := 10.0
	call := &store.Call{CallId: "abc", Verdict: "Bad", WorstUser: "alice"}
	rows := []store.StreamRow{
		{CallId: "abc", User: "alice", Verdict: "Bad", AvgLossPct: &lossHigh},
		{CallId: "abc", User: "bob", Verdict: "Good"},
		{CallId: "abc", User: "carol", Verdict: "Good"},
	}
	calls := &fakeCalls{getResult: call}
	streams := &fakeStreams{rows: rows}
	srv := newTestServer(calls, streams, nil, nil, nil)

	// Default (include_good=false) should drop the two Good rows.
	blocks, isErr := callTool(t, srv, "get_call", map[string]any{"call_id": "abc"})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	var detail api.CallDetail
	if err := json.Unmarshal([]byte(blocks[1]), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if len(detail.Streams) != 1 || detail.Streams[0].Verdict != "Bad" {
		t.Errorf("want 1 Bad stream, got %+v", detail.Streams)
	}
	if !strings.Contains(blocks[0], "showing 1/3") || !strings.Contains(blocks[0], "2 Good hidden") {
		t.Errorf("header = %q", blocks[0])
	}

	// include_good=true keeps all 3.
	blocks2, _ := callTool(t, srv, "get_call", map[string]any{"call_id": "abc", "include_good": true})
	var detail2 api.CallDetail
	if err := json.Unmarshal([]byte(blocks2[1]), &detail2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(detail2.Streams) != 3 {
		t.Errorf("want 3 streams, got %d", len(detail2.Streams))
	}
}

func TestGetCall_Unknown(t *testing.T) {
	calls := &fakeCalls{getErr: store.ErrNotFound}
	srv := newTestServer(calls, nil, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "get_call", map[string]any{"call_id": "missing"})
	if !isErr {
		t.Fatalf("want tool error")
	}
	if !strings.Contains(blocks[0], "not found") {
		t.Errorf("msg = %q", blocks[0])
	}
}

func TestListUsers_Happy(t *testing.T) {
	users := &fakeUsers{result: []store.UserStat{
		{Upn: "alice", CallCount: 12, BadCount: 8},
		{Upn: "bob", CallCount: 10, BadCount: 6},
	}}
	srv := newTestServer(nil, nil, users, nil, nil)
	blocks, isErr := callTool(t, srv, "list_users", map[string]any{})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "2 users") || !strings.Contains(blocks[0], "alice 8/12") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestListUserCalls_EmptyUpn(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil)
	// RequireString fails at the SDK level if the arg is absent, so we
	// pass an empty string to reach the Service's ErrBadRequest path.
	blocks, isErr := callTool(t, srv, "list_user_calls", map[string]any{"upn": ""})
	if !isErr {
		t.Fatalf("want tool error")
	}
	if !strings.Contains(blocks[0], "bad") {
		t.Errorf("msg = %q", blocks[0])
	}
}

func TestSummarizeCall_TextOnly(t *testing.T) {
	lossHigh := 12.5
	jitter := 20.0
	call := &store.Call{
		CallId:           "xyz",
		CallType:         "meeting",
		DurationSec:      2700,
		Organizer:        "org@x.com",
		ParticipantCount: 5,
		Verdict:          "Bad",
		WorstUser:        "alice",
		WorstDirection:   "send",
		WorstStreamLabel: "audio/send",
	}
	rows := []store.StreamRow{
		{User: "alice", Direction: "send", Verdict: "Bad", StreamLabel: "audio/send",
			AvgLossPct: &lossHigh, AvgJitterMs: &jitter},
		{User: "bob", Direction: "recv", Verdict: "Good"},
	}
	srv := newTestServer(&fakeCalls{getResult: call}, &fakeStreams{rows: rows}, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "summarize_call", map[string]any{"call_id": "xyz"})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) != 1 {
		t.Fatalf("summarize_call must be text-only, got %d blocks", len(blocks))
	}
	text := blocks[0]
	if !strings.Contains(text, "Call xyz") {
		t.Errorf("missing call header: %q", text)
	}
	if !strings.Contains(text, "verdict=Bad") {
		t.Errorf("missing verdict: %q", text)
	}
	if !strings.Contains(text, "high packet loss") {
		t.Errorf("missing root-cause hint: %q", text)
	}
}

func TestFindFlakyMicrophones_Happy(t *testing.T) {
	start := time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)
	conc := func(v float64) *float64 { return &v }
	calls := &fakeCalls{
		metaResult: []store.CallMeta{
			{CallId: "c1", StartTimeUtc: start, Verdict: "Bad"},
			{CallId: "c2", StartTimeUtc: start, Verdict: "Poor"},
			{CallId: "c3", StartTimeUtc: start, Verdict: "Bad"},
		},
	}
	mkRow := func(cid string, pct float64) store.StreamRow {
		return store.StreamRow{
			CallId: cid, User: "alice@corp.com", Direction: "send",
			StreamLabel: "audio/send", CaptureDevice: "HeadsetPro",
			ConcealedPct: conc(pct), SegmentStart: start,
		}
	}
	streams := &fakeStreams{flakyRows: []store.StreamRow{
		mkRow("c1", 16.0),
		mkRow("c2", 10.0),
		mkRow("c3", 8.0),
	}}
	srv := newTestServer(calls, streams, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "find_flaky_microphones", map[string]any{
		"min_incidents": 3,
	})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json, got %d blocks", len(blocks))
	}
	if !strings.Contains(blocks[0], "1 flaky microphone") ||
		!strings.Contains(blocks[0], "HeadsetPro") {
		t.Errorf("summary = %q", blocks[0])
	}
	var out []api.FlakyMic
	if err := json.Unmarshal([]byte(blocks[1]), &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(out) != 1 || out[0].Severity != "Bad" {
		t.Errorf("want 1 Bad finding, got %+v", out)
	}
}

func TestFindFlakyMicrophones_Empty(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "find_flaky_microphones", map[string]any{})
	if isErr {
		t.Fatalf("empty window must not be an error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "no flaky") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestUserHealthReport_Happy(t *testing.T) {
	pct := func(v float64) *float64 { return &v }
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: "Bad", Participants: []string{"alice@corp.com"}},
		{CallId: "c2", Verdict: "Bad", Participants: []string{"alice@corp.com"}},
		{CallId: "c3", Verdict: "Poor", Participants: []string{"alice@corp.com"}},
		{CallId: "c4", Verdict: "Good", Participants: []string{"alice@corp.com"}},
	}}
	mkRow := func(cid string, c float64) store.StreamRow {
		return store.StreamRow{
			CallId: cid, User: "alice@corp.com", Direction: "send",
			StreamLabel: "audio/send", CaptureDevice: "HeadsetPro",
			Subnet: "10.0.0.0/24", ConnType: "wired",
			Platform: "Windows", UserAgent: "Teams/1.6",
			ConcealedPct: pct(c),
		}
	}
	streams := &fakeStreams{userRows: []store.StreamRow{
		mkRow("c1", 18.0),
		mkRow("c2", 15.0),
		mkRow("c3", 12.0),
		mkRow("c4", 1.0),
	}}
	srv := newTestServer(calls, streams, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "user_health_report",
		map[string]any{"upn": "alice@corp.com"})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json, got %d blocks", len(blocks))
	}
	if !strings.Contains(blocks[0], "alice@corp.com") ||
		!strings.Contains(blocks[0], "pattern=chronic_mic") {
		t.Errorf("summary = %q", blocks[0])
	}
	var report api.UserHealthReport
	if err := json.Unmarshal([]byte(blocks[1]), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if report.TotalCalls != 4 {
		t.Errorf("totalCalls = %d, want 4", report.TotalCalls)
	}
	if report.Pattern != "chronic_mic" {
		t.Errorf("pattern = %q", report.Pattern)
	}
}

func TestUserHealthReport_EmptyUpnRejected(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "user_health_report", map[string]any{"upn": ""})
	if !isErr {
		t.Fatalf("want tool error")
	}
	if !strings.Contains(blocks[0], "bad") {
		t.Errorf("msg = %q", blocks[0])
	}
}

func TestListSubnets_Happy(t *testing.T) {
	subs := newFakeSubnets(
		store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "Office A", Office: "Dubai"},
		store.SubnetEntry{Cidr: "10.1.0.0/24", Name: "Office B", Office: "Dubai"},
		store.SubnetEntry{Cidr: "192.168.0.0/24", Name: "Home", Office: "home"},
	)
	srv := newTestServerFull(nil, nil, nil, nil, subs, nil, nil)

	blocks, isErr := callTool(t, srv, "list_subnets", map[string]any{})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json, got %d blocks", len(blocks))
	}
	if !strings.Contains(blocks[0], "3 subnets") || !strings.Contains(blocks[0], "Dubai(2)") {
		t.Errorf("summary = %q", blocks[0])
	}
	var out []store.SubnetEntry
	if err := json.Unmarshal([]byte(blocks[1]), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("len = %d, want 3", len(out))
	}
}

func TestListSubnets_Empty(t *testing.T) {
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "list_subnets", map[string]any{})
	if isErr {
		t.Fatalf("empty must not error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "no subnets") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestGetUserCard_Present(t *testing.T) {
	cards := newFakeUserCards(store.UserCard{
		Upn:      "alice@corp.com",
		Location: "Dubai HQ",
		Tags:     []string{"vip", "mobile-heavy"},
		Notes:    "escalated",
	})
	srv := newTestServerFull(nil, nil, nil, nil, nil, cards, nil)

	blocks, isErr := callTool(t, srv, "get_user_card", map[string]any{"upn": "alice@corp.com"})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json, got %d blocks", len(blocks))
	}
	if !strings.Contains(blocks[0], "alice@corp.com") ||
		!strings.Contains(blocks[0], "Dubai HQ") ||
		!strings.Contains(blocks[0], "vip") {
		t.Errorf("summary = %q", blocks[0])
	}
	var out store.UserCard
	if err := json.Unmarshal([]byte(blocks[1]), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Location != "Dubai HQ" {
		t.Errorf("card = %+v", out)
	}
}

func TestGetUserCard_Absent(t *testing.T) {
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "get_user_card", map[string]any{"upn": "ghost@corp.com"})
	// Absent card is a successful result, not an error.
	if isErr {
		t.Fatalf("absent card must not error: %v", blocks)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0], "no card for ghost@corp.com") {
		t.Errorf("summary = %q", blocks)
	}
	// Absent card must produce text-only: no "null" JSON block.
	if len(blocks) != 1 {
		t.Fatalf("want text-only response for absent card, got %d blocks", len(blocks))
	}
}

// TestGetUserCard_GetError verifies that a store-layer failure in GetUserCard
// (e.g. mongo timeout) is routed through mapServiceErr and surfaces as an
// isError=true tool result whose message is masked to "internal error" without
// leaking the raw store error string.
func TestGetUserCard_GetError(t *testing.T) {
	t.Parallel()
	cards := newFakeUserCards()
	cards.getErr = errors.New("mongo boom")
	srv := newTestServerFull(nil, nil, nil, nil, nil, cards, nil)

	blocks, isErr := callTool(t, srv, "get_user_card", map[string]any{"upn": "alice@corp.com"})
	if !isErr {
		t.Fatalf("want tool error, got blocks=%v", blocks)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0], "internal error") {
		t.Errorf("want 'internal error' in message, got %q", blocks)
	}
	if len(blocks) > 0 && strings.Contains(blocks[0], "mongo boom") {
		t.Errorf("raw error must not leak through mapServiceErr, got %q", blocks[0])
	}
}

// Sanity: mapServiceErr wrap via list_users path — ensure non-sentinel
// errors mask to "internal error".
func TestInternalErrorMasked(t *testing.T) {
	users := &fakeUsers{err: errors.New("boom db")}
	srv := newTestServer(nil, nil, users, nil, nil)
	blocks, isErr := callTool(t, srv, "list_users", map[string]any{})
	if !isErr {
		t.Fatalf("want error")
	}
	if !strings.Contains(blocks[0], "internal error") {
		t.Errorf("msg = %q", blocks[0])
	}
	if strings.Contains(blocks[0], "boom db") {
		t.Errorf("leaked internal detail: %q", blocks[0])
	}
}

// --- Phase 4: find_bad_network_hotspots -------------------------------

func TestFindBadNetworkHotspots_Happy(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c4", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c5", Verdict: "Bad", StartTimeUtc: now},
	}
	mk := func(cid, user string) store.StreamRow {
		return store.StreamRow{CallId: cid, User: user, Subnet: "10.0.0.0/24"}
	}
	streams := &fakeStreams{windowRows: []store.StreamRow{
		mk("c1", "alice@x.com"),
		mk("c2", "alice@x.com"),
		mk("c3", "bob@x.com"),
		mk("c4", "carol@x.com"),
		mk("c5", "dave@x.com"),
	}}
	calls := &fakeCalls{metaResult: metas}
	srv := newTestServer(calls, streams, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "find_bad_network_hotspots", map[string]any{
		"min_calls":     float64(3),
		"min_bad_ratio": 0.5,
	})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json, got %d blocks", len(blocks))
	}
	if !strings.Contains(blocks[0], "1 hotspot") || !strings.Contains(blocks[0], "10.0.0.0/24") {
		t.Errorf("summary = %q", blocks[0])
	}
	var out []api.Hotspot
	if err := json.Unmarshal([]byte(blocks[1]), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 hotspot, got %d", len(out))
	}
	if out[0].Subnet != "10.0.0.0/24" || out[0].BadCalls != 5 || out[0].TotalCalls != 5 {
		t.Errorf("hotspot = %+v", out[0])
	}
}

func TestFindBadNetworkHotspots_Empty(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "find_bad_network_hotspots", map[string]any{})
	if isErr {
		t.Fatalf("empty window must not be an error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "no network hotspots") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestFindBadNetworkHotspots_BadGroupBy(t *testing.T) {
	// Service-level validation: unknown group_by → ErrBadRequest → mapped
	// to a tool error response.
	metas := []store.CallMeta{{CallId: "c1", Verdict: "Bad"}}
	srv := newTestServer(&fakeCalls{metaResult: metas}, &fakeStreams{}, nil, nil, nil)
	blocks, isErr := callTool(t, srv, "find_bad_network_hotspots", map[string]any{
		"group_by": "subnet_relay", // underscore instead of '+'
	})
	if !isErr {
		t.Fatalf("want tool error, got %v", blocks)
	}
	if !strings.Contains(blocks[0], "group_by") {
		t.Errorf("msg should mention group_by: %q", blocks[0])
	}
}
