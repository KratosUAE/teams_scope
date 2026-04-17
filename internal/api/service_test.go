package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"teams_con/internal/store"
)

// fakeCalls is an in-memory implementation of callsReader used by service
// tests. It records the params of the last List call so assertions can
// verify that service-level clamping and filter conversion reached the
// store untouched.
type fakeCalls struct {
	listParams store.CallListParams
	listResult []store.Call
	listErr    error

	getResult *store.Call
	getErr    error

	metaResult []store.CallMeta
	metaErr    error
}

func (f *fakeCalls) List(_ context.Context, p store.CallListParams) ([]store.Call, error) {
	f.listParams = p
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

	// Phase 3: peer-baseline support. peerRows is returned verbatim by
	// ListInWindowBySubnets; peerFn (when non-nil) takes precedence so a
	// test can assert on the arguments the Service passed through.
	peerRows []store.StreamRow
	peerErr  error
	peerFn   func(callIDs, subnets []string) ([]store.StreamRow, error)

	// Phase 4: hotspots support. windowRows is returned verbatim by
	// ListInWindow; windowFn (when non-nil) takes precedence so tests can
	// assert on the callIDs argument the Service forwarded.
	windowRows []store.StreamRow
	windowErr  error
	windowFn   func(callIDs []string) ([]store.StreamRow, error)
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

func (f *fakeStreams) ListInWindowBySubnets(_ context.Context, callIDs, subnets []string) ([]store.StreamRow, error) {
	if f.peerFn != nil {
		return f.peerFn(callIDs, subnets)
	}
	return f.peerRows, f.peerErr
}

func (f *fakeStreams) ListInWindow(_ context.Context, callIDs []string) ([]store.StreamRow, error) {
	if f.windowFn != nil {
		return f.windowFn(callIDs)
	}
	return f.windowRows, f.windowErr
}

type fakeUsers struct {
	result []store.UserStat
	err    error
	params store.UserListParams
}

func (f *fakeUsers) List(_ context.Context, p store.UserListParams) ([]store.UserStat, error) {
	f.params = p
	return f.result, f.err
}

type fakeMeta struct {
	meta *store.CrawlerMeta
	err  error
}

func (f *fakeMeta) GetCrawlerMeta(_ context.Context) (*store.CrawlerMeta, error) {
	return f.meta, f.err
}

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping(_ context.Context) error { return f.err }

// fakeSubnets is the in-memory test double for subnetsReader. It supports
// the full CRUD surface so the same fake can drive both write-side
// Service tests and SubnetResolver tests. Wrap it with mu when the test
// needs concurrent reads/writes; the simple cases are single-goroutine.
type fakeSubnets struct {
	entries map[string]store.SubnetEntry

	listErr   error
	getErr    error
	upsertErr error
	deleteErr error

	listCalls int
}

func newFakeSubnets(seed ...store.SubnetEntry) *fakeSubnets {
	f := &fakeSubnets{entries: map[string]store.SubnetEntry{}}
	for _, e := range seed {
		f.entries[e.Cidr] = e
	}
	return f
}

func (f *fakeSubnets) List(_ context.Context) ([]store.SubnetEntry, error) {
	f.listCalls++
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
	if f.getErr != nil {
		return nil, f.getErr
	}
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

// fakeUserCards is the in-memory test double for userCardsStore. Mirrors
// fakeSubnets in shape — same CRUD surface, same error-injection hooks —
// because the user-card write methods follow the subnet-write template
// exactly.
type fakeUserCards struct {
	entries map[string]store.UserCard

	listErr   error
	getErr    error
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

// fakeDailySummary is the in-memory test double for dailySummaryReader.
type fakeDailySummary struct {
	rows []store.DaySummary
	err  error
}

func (f *fakeDailySummary) Summary(_ context.Context, _, _ time.Time) ([]store.DaySummary, error) {
	return f.rows, f.err
}

// newTestService assembles a Service with test doubles. Tests pass only the
// fakes they care about; the rest get zero-value stubs so calls do not
// panic when an unrelated method is invoked.
func newTestService(
	calls *fakeCalls,
	streams *fakeStreams,
	users *fakeUsers,
	meta *fakeMeta,
	pinger *fakePinger,
) *Service {
	return newTestServiceFull(calls, streams, users, meta, nil, nil, pinger)
}

// newTestServiceFull is the variant that lets a test inject its own
// fakeSubnets / fakeUserCards. Phase 1 added the subnets parameter; Phase
// 2 added userCards alongside. The regular newTestService stays a 5-arg
// wrapper so unrelated existing tests do not need to grow new parameters.
func newTestServiceFull(
	calls *fakeCalls,
	streams *fakeStreams,
	users *fakeUsers,
	meta *fakeMeta,
	subnets *fakeSubnets,
	userCards *fakeUserCards,
	pinger *fakePinger,
) *Service {
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
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newServiceFromDeps(calls, streams, users, meta, subnets, userCards, &fakeDailySummary{}, pinger, silent)
}

func TestListCalls_LimitClamping(t *testing.T) {
	tests := []struct {
		name      string
		inLimit   int
		wantLimit int
	}{
		{"zero → default", 0, defaultListLimit},
		{"negative → default", -5, defaultListLimit},
		{"below max stays", 50, 50},
		{"equal to max stays", maxListLimit, maxListLimit},
		{"above max clamps", maxListLimit + 1, maxListLimit},
		{"way above clamps", 100_000, maxListLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := &fakeCalls{}
			svc := newTestService(calls, nil, nil, nil, nil)
			_, err := svc.ListCalls(context.Background(), ListCallsParams{Limit: tt.inLimit})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls.listParams.Limit != tt.wantLimit {
				t.Errorf("store Limit = %d, want %d", calls.listParams.Limit, tt.wantLimit)
			}
		})
	}
}

func TestListCalls_VerdictValidation(t *testing.T) {
	tests := []struct {
		name    string
		verdict string
		wantErr error
	}{
		{"empty ok", "", nil},
		{"Good ok", "Good", nil},
		{"Poor ok", "Poor", nil},
		{"Bad ok", "Bad", nil},
		{"lowercase rejected", "good", ErrBadRequest},
		{"garbage rejected", "zzz", ErrBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(nil, nil, nil, nil, nil)
			_, err := svc.ListCalls(context.Background(), ListCallsParams{Verdict: tt.verdict})
			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want wrap of %v", err, tt.wantErr)
			}
		})
	}
}

func TestListCalls_NegativeOffset(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.ListCalls(context.Background(), ListCallsParams{Offset: -1})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want wrap of ErrBadRequest", err)
	}
}

func TestListCalls_PointerConversion(t *testing.T) {
	calls := &fakeCalls{}
	svc := newTestService(calls, nil, nil, nil, nil)

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	_, err := svc.ListCalls(context.Background(), ListCallsParams{
		From:    &from,
		To:      &to,
		Verdict: "Poor",
		Upn:     "alice@corp.com",
		Limit:   25,
		Offset:  10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := calls.listParams
	if p.From == nil || !p.From.Equal(from) {
		t.Errorf("From not propagated: %+v", p.From)
	}
	if p.To == nil || !p.To.Equal(to) {
		t.Errorf("To not propagated: %+v", p.To)
	}
	if p.Verdict == nil || *p.Verdict != "Poor" {
		t.Errorf("Verdict not propagated: %+v", p.Verdict)
	}
	if p.Upn == nil || *p.Upn != "alice@corp.com" {
		t.Errorf("Upn not propagated: %+v", p.Upn)
	}
	if p.Limit != 25 || p.Offset != 10 {
		t.Errorf("limit/offset = %d/%d, want 25/10", p.Limit, p.Offset)
	}
}

func TestListCalls_EmptyStringsNotPassedAsPointers(t *testing.T) {
	calls := &fakeCalls{}
	svc := newTestService(calls, nil, nil, nil, nil)

	_, err := svc.ListCalls(context.Background(), ListCallsParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.listParams.Verdict != nil {
		t.Errorf("empty verdict should stay nil, got %+v", calls.listParams.Verdict)
	}
	if calls.listParams.Upn != nil {
		t.Errorf("empty upn should stay nil, got %+v", calls.listParams.Upn)
	}
}

func TestGetCall_NotFoundMapping(t *testing.T) {
	calls := &fakeCalls{getErr: store.ErrNotFound}
	svc := newTestService(calls, nil, nil, nil, nil)
	_, err := svc.GetCall(context.Background(), "abc")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetCall_EmptyID(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.GetCall(context.Background(), "")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestGetCall_Success(t *testing.T) {
	call := &store.Call{CallId: "abc", Verdict: "Good"}
	row := store.StreamRow{CallId: "abc", User: "alice@corp.com"}
	calls := &fakeCalls{getResult: call}
	streams := &fakeStreams{rows: []store.StreamRow{row}}
	svc := newTestService(calls, streams, nil, nil, nil)

	detail, err := svc.GetCall(context.Background(), "abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Call.CallId != "abc" {
		t.Errorf("call id = %q", detail.Call.CallId)
	}
	if len(detail.Streams) != 1 || detail.Streams[0].User != "alice@corp.com" {
		t.Errorf("streams not propagated: %+v", detail.Streams)
	}
}

func TestGetCall_WrapsGenericStoreError(t *testing.T) {
	boom := errors.New("boom")
	calls := &fakeCalls{getErr: boom}
	svc := newTestService(calls, nil, nil, nil, nil)
	_, err := svc.GetCall(context.Background(), "abc")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want wrapped generic", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("underlying error not wrapped: %v", err)
	}
}

func TestHealth_Ok(t *testing.T) {
	last := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	meta := &fakeMeta{meta: &store.CrawlerMeta{LastCrawlAt: last}}
	svc := newTestService(nil, nil, nil, meta, &fakePinger{})

	h, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.MongoOk {
		t.Errorf("want MongoOk=true")
	}
	if h.LastCrawlAt == nil || !h.LastCrawlAt.Equal(last) {
		t.Errorf("LastCrawlAt = %+v, want %v", h.LastCrawlAt, last)
	}
}

func TestHealth_PingFailureStillReturns200Shape(t *testing.T) {
	meta := &fakeMeta{meta: &store.CrawlerMeta{LastCrawlError: "previous boom"}}
	svc := newTestService(nil, nil, nil, meta, &fakePinger{err: errors.New("no mongo")})

	h, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("ping failure should not surface as error, got %v", err)
	}
	if h == nil {
		t.Fatal("health must be non-nil")
	}
	if h.MongoOk {
		t.Errorf("want MongoOk=false")
	}
	if h.LastCrawlError != "previous boom" {
		t.Errorf("LastCrawlError = %q", h.LastCrawlError)
	}
	if h.LastCrawlAt != nil {
		t.Errorf("zero time should leave pointer nil, got %v", h.LastCrawlAt)
	}
}

func TestHealth_MetaError(t *testing.T) {
	meta := &fakeMeta{err: errors.New("meta boom")}
	svc := newTestService(nil, nil, nil, meta, &fakePinger{})
	_, err := svc.Health(context.Background())
	if err == nil {
		t.Fatal("meta error must surface")
	}
}

func TestListUserCalls_EmptyUpn(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.ListUserCalls(context.Background(), "", nil, nil, 0, 0)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestListUserCalls_SetsUpn(t *testing.T) {
	calls := &fakeCalls{}
	svc := newTestService(calls, nil, nil, nil, nil)
	_, err := svc.ListUserCalls(context.Background(), "bob@corp.com", nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.listParams.Upn == nil || *calls.listParams.Upn != "bob@corp.com" {
		t.Errorf("upn not propagated: %+v", calls.listParams.Upn)
	}
}

func TestListUsers_Propagates(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	users := &fakeUsers{result: []store.UserStat{{Upn: "alice", CallCount: 3}}}
	svc := newTestService(nil, nil, users, nil, nil)
	out, err := svc.ListUsers(context.Background(), &from, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Upn != "alice" {
		t.Errorf("unexpected result: %+v", out)
	}
	if users.params.From == nil || !users.params.From.Equal(from) {
		t.Errorf("From not propagated: %+v", users.params.From)
	}
}
