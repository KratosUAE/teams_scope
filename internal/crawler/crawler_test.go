package crawler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"teams_con/internal/graph"
	"teams_con/internal/store"
)

// fakeGraph implements graphClient for unit tests. listFn and getFn may be
// overridden per-test; the default implementations return empty slices and
// nil so tests only have to set the behaviour they care about.
type fakeGraph struct {
	listFn func(ctx context.Context, start, end time.Time) ([]graph.CallRecordRef, error)
	getFn  func(ctx context.Context, id string) (*graph.CallRecord, error)

	mu       sync.Mutex
	listCall int
	getIDs   []string
}

func (f *fakeGraph) ListCallRecordsInRange(ctx context.Context, start, end time.Time) ([]graph.CallRecordRef, error) {
	f.mu.Lock()
	f.listCall++
	f.mu.Unlock()
	if f.listFn != nil {
		return f.listFn(ctx, start, end)
	}
	return nil, nil
}

func (f *fakeGraph) GetCallRecord(ctx context.Context, id string) (*graph.CallRecord, error) {
	f.mu.Lock()
	f.getIDs = append(f.getIDs, id)
	f.mu.Unlock()
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return &graph.CallRecord{ID: id}, nil
}

// fakeCalls is a tiny in-memory callsRepo. The StreamsProjected flag on
// each stored Call mirrors what GetState would return for the production
// repo, so seeding it lets tests model "fully written" vs "partial write"
// without touching the streams collection.
type fakeCalls struct {
	mu          sync.Mutex
	docs        map[string]store.Call
	existErr    error
	markedN     int // counts MarkStreamsProjected invocations
	markedIDs   []string
}

func newFakeCalls() *fakeCalls { return &fakeCalls{docs: make(map[string]store.Call)} }

func (f *fakeCalls) GetState(_ context.Context, id string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existErr != nil {
		return false, false, f.existErr
	}
	c, ok := f.docs[id]
	if !ok {
		return false, false, nil
	}
	return true, c.StreamsProjected, nil
}

func (f *fakeCalls) Upsert(_ context.Context, c store.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[c.CallId] = c
	return nil
}

func (f *fakeCalls) MarkStreamsProjected(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markedN++
	f.markedIDs = append(f.markedIDs, id)
	if c, ok := f.docs[id]; ok {
		c.StreamsProjected = true
		f.docs[id] = c
	}
	return nil
}

// fakeStreams is a tiny in-memory streamsRepo.
type fakeStreams struct {
	mu       sync.Mutex
	byCall   map[string][]store.StreamRow
	replaceN int // counts ReplaceByCall invocations
}

func newFakeStreams() *fakeStreams { return &fakeStreams{byCall: make(map[string][]store.StreamRow)} }

func (f *fakeStreams) ReplaceByCall(_ context.Context, callID string, rows []store.StreamRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byCall[callID] = rows
	f.replaceN++
	return nil
}

// fakeMeta is a tiny in-memory metaRepo.
type fakeMeta struct {
	mu              sync.Mutex
	lastCrawlAt     time.Time
	lastCrawlErr    error
	lastBackfillAt  time.Time
	setLastCrawlN   int
	setLastBackfill int
}

func (f *fakeMeta) SetLastCrawl(_ context.Context, at time.Time, crawlErr error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCrawlAt = at
	f.lastCrawlErr = crawlErr
	f.setLastCrawlN++
	return nil
}

func (f *fakeMeta) SetLastBackfill(_ context.Context, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastBackfillAt = at
	f.setLastBackfill++
	return nil
}

// discardLog returns a slog.Logger that drops every record so test output
// stays clean.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestCrawler(gc graphClient, calls callsRepo, streams streamsRepo, meta metaRepo) *Crawler {
	return newWithDeps(Config{}, gc, calls, streams, meta, discardLog())
}

func TestClampDays(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero clamps to min", 0, minBackfillDays},
		{"negative clamps to min", -7, minBackfillDays},
		{"one stays one", 1, 1},
		{"mid range passes through", 15, 15},
		{"thirty stays thirty", 30, 30},
		{"above max clamps to max", 99, maxBackfillDays},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampDays(tt.in); got != tt.want {
				t.Errorf("clampDays(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	c := newWithDeps(Config{}, &fakeGraph{}, newFakeCalls(), newFakeStreams(), &fakeMeta{}, nil)
	if c.cfg.Interval != defaultInterval {
		t.Errorf("Interval default: got %v, want %v", c.cfg.Interval, defaultInterval)
	}
	if c.cfg.Window != defaultWindow {
		t.Errorf("Window default: got %v, want %v", c.cfg.Window, defaultWindow)
	}
	if c.log == nil {
		t.Error("log should not be nil after New")
	}
}

func TestNewPreservesExplicitConfig(t *testing.T) {
	cfg := Config{Interval: 42 * time.Second, Window: 13 * time.Minute, IncludeVideo: true}
	c := newWithDeps(cfg, &fakeGraph{}, newFakeCalls(), newFakeStreams(), &fakeMeta{}, discardLog())
	if c.cfg.Interval != 42*time.Second {
		t.Errorf("Interval: got %v, want 42s", c.cfg.Interval)
	}
	if c.cfg.Window != 13*time.Minute {
		t.Errorf("Window: got %v, want 13m", c.cfg.Window)
	}
	if !c.cfg.IncludeVideo {
		t.Error("IncludeVideo should be true")
	}
}

func TestTickSkipsExistingCalls(t *testing.T) {
	existing := "call-already-stored"
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: existing}}, nil
		},
	}
	calls := newFakeCalls()
	// StreamsProjected=true marks the doc as fully written by a previous
	// tick — GetState returns (true, true) and the skip path is exercised.
	calls.docs[existing] = store.Call{CallId: existing, StreamsProjected: true}
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick returned error: %v", err)
	}

	if len(gc.getIDs) != 0 {
		t.Errorf("GetCallRecord should not be called for existing id, got %v", gc.getIDs)
	}
	streams.mu.Lock()
	n := streams.replaceN
	streams.mu.Unlock()
	if n != 0 {
		t.Errorf("streams.ReplaceByCall should not be called for fully-written id, got %d call(s)", n)
	}
	if meta.setLastCrawlN != 1 {
		t.Errorf("SetLastCrawl calls: got %d, want 1", meta.setLastCrawlN)
	}
	if meta.lastCrawlErr != nil {
		t.Errorf("lastCrawlErr: got %v, want nil", meta.lastCrawlErr)
	}
}

func TestTickUpsertsNewCalls(t *testing.T) {
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: "new-1"}, {ID: "new-2"}}, nil
		},
		getFn: func(_ context.Context, id string) (*graph.CallRecord, error) {
			return &graph.CallRecord{
				ID:            id,
				StartDateTime: start,
				EndDateTime:   end,
			}, nil
		},
	}
	calls := newFakeCalls()
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	if err := c.tick(context.Background(), start, end); err != nil {
		t.Fatalf("tick returned error: %v", err)
	}

	if len(calls.docs) != 2 {
		t.Errorf("expected 2 upserted calls, got %d", len(calls.docs))
	}
	if _, ok := calls.docs["new-1"]; !ok {
		t.Error("new-1 should be upserted")
	}
	if _, ok := streams.byCall["new-1"]; !ok {
		t.Error("streams for new-1 should be replaced")
	}
	if meta.lastCrawlErr != nil {
		t.Errorf("lastCrawlErr: got %v, want nil", meta.lastCrawlErr)
	}
}

func TestTickToleratesNotFound(t *testing.T) {
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: "missing"}, {ID: "ok"}}, nil
		},
		getFn: func(_ context.Context, id string) (*graph.CallRecord, error) {
			if id == "missing" {
				return nil, graph.ErrCallNotFound
			}
			return &graph.CallRecord{ID: id}, nil
		},
	}
	calls := newFakeCalls()
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick returned error: %v", err)
	}

	if _, ok := calls.docs["missing"]; ok {
		t.Error("missing call should not be persisted")
	}
	if _, ok := calls.docs["ok"]; !ok {
		t.Error("ok call should be persisted alongside the missing one")
	}
	// lastCrawlErr should be nil — ErrCallNotFound is an expected race,
	// not a tick-level failure.
	if meta.lastCrawlErr != nil {
		t.Errorf("lastCrawlErr: got %v, want nil", meta.lastCrawlErr)
	}
}

func TestTickRecordsLastErrorOnListFailure(t *testing.T) {
	boom := errors.New("graph boom")
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return nil, boom
		},
	}
	calls := newFakeCalls()
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("tick should return an error on list failure")
	}
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped list error, got %v", err)
	}
	if meta.setLastCrawlN != 1 {
		t.Errorf("SetLastCrawl calls: got %d, want 1", meta.setLastCrawlN)
	}
	if meta.lastCrawlErr == nil {
		t.Error("lastCrawlErr should be set after list failure")
	}
}

func TestTickContinuesAfterStoreError(t *testing.T) {
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: "a"}, {ID: "b"}}, nil
		},
	}
	calls := &fakeCalls{docs: make(map[string]store.Call), existErr: errors.New("mongo down")}
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	// Even though every Exists call fails, the tick must complete and
	// still set lastCrawl to nil — individual record errors are logged,
	// not escalated.
	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick should tolerate per-record errors, got %v", err)
	}
	if meta.lastCrawlErr != nil {
		t.Errorf("lastCrawlErr: got %v, want nil (per-record errors shouldn't bubble)", meta.lastCrawlErr)
	}
}

func TestBackfillStampsMeta(t *testing.T) {
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return nil, nil
		},
	}
	meta := &fakeMeta{}
	c := newTestCrawler(gc, newFakeCalls(), newFakeStreams(), meta)

	if err := c.Backfill(context.Background(), 7); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if meta.setLastBackfill != 1 {
		t.Errorf("SetLastBackfill calls: got %d, want 1", meta.setLastBackfill)
	}
	if meta.lastBackfillAt.IsZero() {
		t.Error("lastBackfillAt should be stamped")
	}
}

func TestBackfillClampsDays(t *testing.T) {
	var seenStart, seenEnd time.Time
	gc := &fakeGraph{
		listFn: func(_ context.Context, start, end time.Time) ([]graph.CallRecordRef, error) {
			seenStart = start
			seenEnd = end
			return nil, nil
		},
	}
	c := newTestCrawler(gc, newFakeCalls(), newFakeStreams(), &fakeMeta{})

	if err := c.Backfill(context.Background(), 999); err != nil {
		t.Fatalf("Backfill: %v", err)
	}

	diff := seenEnd.Sub(seenStart)
	// Allow a little slop because wallclock advances between the fake's
	// start capture and the assertion.
	wantMin := time.Duration(maxBackfillDays)*24*time.Hour - time.Minute
	wantMax := time.Duration(maxBackfillDays)*24*time.Hour + time.Minute
	if diff < wantMin || diff > wantMax {
		t.Errorf("backfill window span: got %v, want ~%v", diff, time.Duration(maxBackfillDays)*24*time.Hour)
	}
}

// TestTick_HealsPartialWrite verifies that when a call document is already
// stored but StreamsProjected is false (partial-write or pre-flag legacy
// doc), the crawler falls through and re-runs GetCallRecord + ReplaceByCall
// to heal the gap, then marks the doc as projected.
func TestTick_HealsPartialWrite(t *testing.T) {
	partialID := "call-partial"
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: partialID}}, nil
		},
		getFn: func(_ context.Context, id string) (*graph.CallRecord, error) {
			return &graph.CallRecord{ID: id}, nil
		},
	}
	calls := newFakeCalls()
	// Call document exists but StreamsProjected=false — previous tick
	// crashed between Upsert and ReplaceByCall, or the doc predates the
	// flag. Either way, this tick must heal it.
	calls.docs[partialID] = store.Call{CallId: partialID}

	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick returned error: %v", err)
	}

	// GetCallRecord must have been called to heal the gap.
	gc.mu.Lock()
	gotIDs := gc.getIDs
	gc.mu.Unlock()
	if len(gotIDs) == 0 || gotIDs[0] != partialID {
		t.Errorf("GetCallRecord should be called to heal partial write, got %v", gotIDs)
	}

	// ReplaceByCall must have been invoked — streams entry should now exist.
	streams.mu.Lock()
	_, replaced := streams.byCall[partialID]
	streams.mu.Unlock()
	if !replaced {
		t.Error("ReplaceByCall should be invoked to write missing streams")
	}

	// MarkStreamsProjected must have been invoked exactly once so the next
	// tick takes the skip branch instead of looping through the heal path.
	calls.mu.Lock()
	markedN := calls.markedN
	projected := calls.docs[partialID].StreamsProjected
	calls.mu.Unlock()
	if markedN != 1 {
		t.Errorf("MarkStreamsProjected calls: got %d, want 1", markedN)
	}
	if !projected {
		t.Error("StreamsProjected should be true after heal")
	}
}

// TestTick_SkipsFullyWrittenCall verifies that when StreamsProjected=true,
// the crawler skips without calling GetCallRecord — even if the streams
// collection is empty (which is now a legitimate end-state for calls whose
// projection produced zero rows).
func TestTick_SkipsFullyWrittenCall(t *testing.T) {
	fullID := "call-complete"
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: fullID}}, nil
		},
	}
	calls := newFakeCalls()
	calls.docs[fullID] = store.Call{CallId: fullID, StreamsProjected: true}

	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)
	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick returned error: %v", err)
	}

	gc.mu.Lock()
	gotIDs := gc.getIDs
	gc.mu.Unlock()
	if len(gotIDs) != 0 {
		t.Errorf("GetCallRecord should NOT be called for fully-written record, got %v", gotIDs)
	}
}

// TestTick_EmptyStreamProjectionStillMarksProjected pins the bug fix:
// a call whose stream projection legitimately produces zero rows (e.g.
// audio-only call when IncludeVideo is false, or a call with no media at
// all) must still get StreamsProjected=true so subsequent ticks skip it
// instead of re-fetching the same record forever.
func TestTick_EmptyStreamProjectionStillMarksProjected(t *testing.T) {
	id := "call-no-streams"
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			return []graph.CallRecordRef{{ID: id}}, nil
		},
		// Bare CallRecord — no Sessions/Segments/Media, so quality.ToStreamRows
		// returns an empty slice, ReplaceByCall is a no-op insert, and the old
		// HasStreams-based dedup would loop on this call every tick.
		getFn: func(_ context.Context, recID string) (*graph.CallRecord, error) {
			return &graph.CallRecord{ID: recID}, nil
		},
	}
	calls := newFakeCalls()
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newTestCrawler(gc, calls, streams, meta)

	// First tick: call is new, gets fully processed.
	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick #1 returned error: %v", err)
	}

	calls.mu.Lock()
	doc, ok := calls.docs[id]
	calls.mu.Unlock()
	if !ok {
		t.Fatal("call should have been upserted")
	}
	if !doc.StreamsProjected {
		t.Error("StreamsProjected should be true even when projection is empty")
	}

	// Second tick: same call returned by Graph again. With the flag set,
	// GetCallRecord must NOT be called — proving the loop is broken.
	gc.mu.Lock()
	gc.getIDs = nil
	gc.mu.Unlock()

	if err := c.tick(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("tick #2 returned error: %v", err)
	}

	gc.mu.Lock()
	gotIDs := gc.getIDs
	gc.mu.Unlock()
	if len(gotIDs) != 0 {
		t.Errorf("tick #2 should skip the call without re-fetching, got GetCallRecord(%v)", gotIDs)
	}
}

func TestNewAppliesSweepDefaults(t *testing.T) {
	c := newWithDeps(Config{}, &fakeGraph{}, newFakeCalls(), newFakeStreams(), &fakeMeta{}, nil)
	if c.cfg.SweepInterval != defaultSweepInterval {
		t.Errorf("SweepInterval default: got %v, want %v", c.cfg.SweepInterval, defaultSweepInterval)
	}
	if c.cfg.SweepWindow != defaultSweepWindow {
		t.Errorf("SweepWindow default: got %v, want %v", c.cfg.SweepWindow, defaultSweepWindow)
	}
}

func TestNewPreservesExplicitSweepConfig(t *testing.T) {
	cfg := Config{SweepInterval: 2 * time.Hour, SweepWindow: 6 * time.Hour}
	c := newWithDeps(cfg, &fakeGraph{}, newFakeCalls(), newFakeStreams(), &fakeMeta{}, discardLog())
	if c.cfg.SweepInterval != 2*time.Hour {
		t.Errorf("SweepInterval: got %v, want 2h", c.cfg.SweepInterval)
	}
	if c.cfg.SweepWindow != 6*time.Hour {
		t.Errorf("SweepWindow: got %v, want 6h", c.cfg.SweepWindow)
	}
}

func TestNewDisablesSweepWithNegativeInterval(t *testing.T) {
	cfg := Config{SweepInterval: -1}
	c := newWithDeps(cfg, &fakeGraph{}, newFakeCalls(), newFakeStreams(), &fakeMeta{}, discardLog())
	if c.cfg.SweepInterval != 0 {
		t.Errorf("SweepInterval: got %v, want 0 (disabled)", c.cfg.SweepInterval)
	}
}

func TestRunSweepFiresAndCatchesNewRecords(t *testing.T) {
	// Simulate a groupCall that only appears in the sweep window (wider
	// lookback), not in the live tick window.
	swept := make(chan struct{}, 1)

	gc := &fakeGraph{
		listFn: func(_ context.Context, start, end time.Time) ([]graph.CallRecordRef, error) {
			window := end.Sub(start)
			// Only return the "late" groupCall when the window is wide (sweep).
			if window > time.Hour {
				select {
				case swept <- struct{}{}:
				default:
				}
				return []graph.CallRecordRef{{ID: "late-group-call"}}, nil
			}
			return nil, nil
		},
		getFn: func(_ context.Context, id string) (*graph.CallRecord, error) {
			return &graph.CallRecord{ID: id}, nil
		},
	}
	calls := newFakeCalls()
	streams := newFakeStreams()
	meta := &fakeMeta{}

	c := newWithDeps(
		Config{
			Interval:      50 * time.Millisecond,
			Window:        time.Minute, // live tick: short window
			SweepInterval: 150 * time.Millisecond,
			SweepWindow:   2 * time.Hour, // sweep: wide window
		},
		gc, calls, streams, meta, discardLog(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	// Wait for the sweep to fire (signal-based, not sleep-based).
	select {
	case <-swept:
		// Good: sweep fired.
	case <-time.After(5 * time.Second):
		t.Fatal("sweep did not fire within 5s")
	}

	cancel()
	<-done

	// The late groupCall should have been picked up by the sweep.
	calls.mu.Lock()
	_, found := calls.docs["late-group-call"]
	calls.mu.Unlock()
	if !found {
		t.Error("sweep should have caught the late-materialising groupCall")
	}
}

func TestRunFirstTickFiresImmediatelyThenCancels(t *testing.T) {
	ticked := make(chan struct{}, 1)
	gc := &fakeGraph{
		listFn: func(_ context.Context, _, _ time.Time) ([]graph.CallRecordRef, error) {
			select {
			case ticked <- struct{}{}:
			default:
			}
			return nil, nil
		},
	}
	meta := &fakeMeta{}
	c := newWithDeps(
		Config{Interval: time.Hour, Window: time.Minute},
		gc, newFakeCalls(), newFakeStreams(), meta, discardLog(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case <-ticked:
		// Good: the initial tick fired without waiting an hour.
	case <-time.After(2 * time.Second):
		t.Fatal("initial tick did not fire")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run error: got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
