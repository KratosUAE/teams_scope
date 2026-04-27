// Package crawler periodically pulls new callRecords from Microsoft Graph
// and persists them to MongoDB. The design is deliberately simple: a single
// ticker goroutine walks an overlapping time window, skips records we have
// already stored, fetches details for new ones, and upserts them via the
// store package. Because Teams call records are immutable once the call
// ends, the whole pipeline is idempotent — running a tick twice is a no-op.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"teams_con/internal/graph"
	"teams_con/internal/quality"
	"teams_con/internal/store"
)

// Tunable defaults applied by New when Config fields are zero. Values match
// the figures quoted in the spec's Crawler Behavior section.
const (
	defaultInterval = 5 * time.Minute
	defaultWindow   = 30 * time.Minute

	// defaultSweepInterval is how often the periodic sweep fires. The sweep
	// catches groupCall records that Graph's listing endpoint doesn't return
	// promptly — they appear in the index with a lag of up to several hours.
	defaultSweepInterval = 1 * time.Hour

	// defaultSweepWindow is the lookback for each sweep tick. 4 hours is
	// generous enough to catch even slow-materialising groupCall records
	// while still being cheap (exists-check skips known calls).
	defaultSweepWindow = 4 * time.Hour

	// minBackfillDays is a guard against `--backfill 0` turning into a
	// no-op with a confusing empty window.
	minBackfillDays = 1

	// maxBackfillDays mirrors the Graph callRecords retention ceiling. The
	// API silently returns an empty page for earlier startDateTime values,
	// so asking for more is always wasted work.
	maxBackfillDays = 30
)

// graphClient is the narrow subset of *graph.Client the crawler actually
// uses. Extracting it here lets crawler_test.go stub the dependency without
// standing up a real Graph HTTP server, and lets production wire up the
// concrete *graph.Client unchanged.
type graphClient interface {
	ListCallRecordsInRange(ctx context.Context, start, end time.Time) ([]graph.CallRecordRef, error)
	GetCallRecord(ctx context.Context, id string) (*graph.CallRecord, error)
}

// callsRepo is the subset of *store.CallsRepo used by the crawler.
//
// GetState replaces the older Exists+streams.HasStreams pair: it returns
// (exists, streamsProjected) in a single round-trip so the tick can decide
// skip-vs-heal without consulting the streams collection. MarkStreamsProjected
// is set to true after a successful ReplaceByCall, including the legitimate
// empty-projection case (audio-only filter, no media, etc.).
type callsRepo interface {
	GetState(ctx context.Context, id string) (exists bool, streamsProjected bool, err error)
	Upsert(ctx context.Context, c store.Call) error
	MarkStreamsProjected(ctx context.Context, id string) error
}

// streamsRepo is the subset of *store.StreamsRepo used by the crawler.
type streamsRepo interface {
	ReplaceByCall(ctx context.Context, callID string, rows []store.StreamRow) error
}

// metaRepo is the subset of *store.MetaRepo used by the crawler.
type metaRepo interface {
	SetLastCrawl(ctx context.Context, at time.Time, crawlErr error) error
	SetLastBackfill(ctx context.Context, at time.Time) error
}

// Config carries the tunable knobs exposed on the `teams_con crawl`
// subcommand. All fields have sensible defaults applied by New.
type Config struct {
	// Interval is the time between tick starts. Defaults to 5m.
	Interval time.Duration
	// Window is the lookback size for each tick. Defaults to 30m; the
	// overlap between Window and Interval is what lets the crawler catch
	// records that Graph materialises with delay.
	Window time.Duration

	// SweepInterval is how often the periodic sweep fires. Defaults to 1h.
	// The sweep re-queries Graph with a larger window to catch groupCall
	// records whose listing materialisation lags behind the live tick
	// window. Set to 0 to disable the sweep.
	SweepInterval time.Duration
	// SweepWindow is the lookback for each sweep tick. Defaults to 4h.
	SweepWindow time.Duration

	// IncludeVideo forwards to quality.ToCallRow / ToStreamRows. The MVP
	// wants audio-only parity with the PowerShell reference and leaves this
	// false.
	IncludeVideo bool
}

// Crawler is the tick engine. Construct one with New and either call Run
// (long-lived loop) or Backfill (one-shot historical pull).
type Crawler struct {
	cfg     Config
	graphc  graphClient
	calls   callsRepo
	streams streamsRepo
	meta    metaRepo
	log     *slog.Logger
}

// New builds a Crawler against a real *graph.Client and *store.Client. It
// applies the Interval/Window defaults so callers can safely pass a zero
// Config.
func New(cfg Config, gc *graph.Client, st *store.Client, log *slog.Logger) *Crawler {
	return newWithDeps(cfg, gc, st.Calls, st.Streams, st.Meta, log)
}

// newWithDeps is the test seam: it accepts the narrow interfaces directly so
// crawler_test.go can wire up fakes without constructing real store/graph
// clients. Defaults are applied here — New is a thin wrapper on top.
func newWithDeps(
	cfg Config,
	gc graphClient,
	calls callsRepo,
	streams streamsRepo,
	meta metaRepo,
	log *slog.Logger,
) *Crawler {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Window <= 0 {
		cfg.Window = defaultWindow
	}
	if cfg.SweepInterval < 0 {
		// Negative means "explicitly disabled" — skip default application.
		cfg.SweepInterval = 0
	} else {
		// Zero means "use defaults". A positive value is kept as-is.
		if cfg.SweepInterval == 0 {
			cfg.SweepInterval = defaultSweepInterval
		}
		if cfg.SweepWindow <= 0 {
			cfg.SweepWindow = defaultSweepWindow
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Crawler{
		cfg:     cfg,
		graphc:  gc,
		calls:   calls,
		streams: streams,
		meta:    meta,
		log:     log,
	}
}

// Run blocks until ctx is cancelled, running tick at Interval cadence. The
// first tick fires immediately so a freshly-started container produces data
// without waiting the full interval. Per-tick errors are logged but never
// abort the loop — the only way Run returns is via ctx cancellation.
//
// When SweepInterval > 0, a second ticker fires at that cadence with a
// larger window (SweepWindow). This catches groupCall records whose Graph
// listing materialisation lags behind the live tick window by hours.
func (c *Crawler) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "crawler run start",
		slog.Duration("interval", c.cfg.Interval),
		slog.Duration("window", c.cfg.Window),
		slog.Duration("sweepInterval", c.cfg.SweepInterval),
		slog.Duration("sweepWindow", c.cfg.SweepWindow),
	)

	// First tick fires immediately so the service is productive from t=0.
	if err := c.runOneTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
		c.log.ErrorContext(ctx, "crawler initial tick failed", slog.Any("err", err))
	}

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	// sweepC is nil when the sweep is disabled, so the select case
	// never fires. When enabled we let the first sweep fire after the
	// full SweepInterval elapses — the initial tick already covered the
	// recent window.
	var sweepC <-chan time.Time
	var sweepTicker *time.Ticker
	if c.cfg.SweepInterval > 0 {
		sweepTicker = time.NewTicker(c.cfg.SweepInterval)
		sweepC = sweepTicker.C
		defer sweepTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			c.log.InfoContext(ctx, "crawler run stop", slog.Any("err", ctx.Err()))
			return ctx.Err()
		case <-ticker.C:
			if err := c.runOneTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				c.log.ErrorContext(ctx, "crawler tick failed", slog.Any("err", err))
			}
		case <-sweepC:
			if err := c.runOneSweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
				c.log.ErrorContext(ctx, "crawler sweep failed", slog.Any("err", err))
			}
		}
	}
}

// runOneTick computes the current sliding window and delegates to tick.
// Split from Run so the loop stays small and testable.
func (c *Crawler) runOneTick(ctx context.Context) error {
	now := time.Now().UTC()
	start := now.Add(-c.cfg.Window)
	return c.tick(ctx, start, now)
}

// runOneSweep is the periodic catch-up for slow-materialising records
// (typically groupCall). It uses the same tick() code path but with the
// wider SweepWindow, so exists-check skips records the live ticks already
// persisted. The sweep reuses the same meta.SetLastCrawl path — there is
// no separate "last sweep" timestamp because the sweep is just a
// larger-window tick. tick() logs its own "tick start" with start/end times
// so we don't duplicate the log here.
func (c *Crawler) runOneSweep(ctx context.Context) error {
	now := time.Now().UTC()
	start := now.Add(-c.cfg.SweepWindow)
	return c.tick(ctx, start, now)
}

// Backfill performs a one-shot pull of the last `days` days via the same
// tick code path. days is clamped to [minBackfillDays, maxBackfillDays];
// anything outside the Graph 30-day retention window is wasted work.
func (c *Crawler) Backfill(ctx context.Context, days int) error {
	days = clampDays(days)
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -days)

	c.log.InfoContext(ctx, "crawler backfill start",
		slog.Int("days", days),
		slog.Time("start", start),
		slog.Time("end", end),
	)

	if err := c.tick(ctx, start, end); err != nil {
		return fmt.Errorf("crawler: backfill: %w", err)
	}

	if err := c.meta.SetLastBackfill(ctx, time.Now().UTC()); err != nil {
		// Don't fail the backfill just because we couldn't stamp the
		// meta document — the data itself is already persisted.
		c.log.WarnContext(ctx, "crawler: set last backfill failed", slog.Any("err", err))
	}
	return nil
}

// tick is the core pull-and-persist routine shared by Run and Backfill.
// Tick-level errors (list failure) propagate to the caller; per-record
// errors (single Get/upsert) are logged and the loop moves on so one bad
// record cannot poison an entire tick.
func (c *Crawler) tick(ctx context.Context, start, end time.Time) error {
	log := c.log.With(
		slog.String("op", "tick"),
		slog.Time("start", start),
		slog.Time("end", end),
	)
	log.InfoContext(ctx, "tick start")

	refs, err := c.graphc.ListCallRecordsInRange(ctx, start, end)
	if err != nil {
		listErr := fmt.Errorf("list call records: %w", err)
		log.ErrorContext(ctx, "tick list failed", slog.Any("err", listErr))
		// Surface the failure via meta so /health can report it. Use a
		// fresh background context when the original was cancelled so
		// we still manage to record the last-error state on shutdown.
		metaCtx := ctx
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			metaCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		if metaErr := c.meta.SetLastCrawl(metaCtx, time.Now().UTC(), listErr); metaErr != nil {
			log.WarnContext(ctx, "tick set last crawl failed", slog.Any("err", metaErr))
		}
		return listErr
	}

	var (
		skipped  int
		inserted int
		errs     int
	)

	for i := range refs {
		if ctx.Err() != nil {
			log.InfoContext(ctx, "tick cancelled mid-loop",
				slog.Int("processed", i),
				slog.Int("total", len(refs)),
			)
			// Stamp meta using a fresh background context so /health can
			// reflect the interrupted tick even after the original ctx is done.
			metaCtx, metaCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer metaCancel()
			if metaErr := c.meta.SetLastCrawl(metaCtx, time.Now().UTC(), ctx.Err()); metaErr != nil {
				log.WarnContext(context.Background(), "tick set last crawl failed on cancel", slog.Any("err", metaErr))
			}
			return ctx.Err()
		}
		ref := refs[i]
		if c.processRef(ctx, ref, log, &skipped, &inserted, &errs) {
			continue
		}
	}

	if err := c.meta.SetLastCrawl(ctx, time.Now().UTC(), nil); err != nil {
		log.WarnContext(ctx, "tick set last crawl failed", slog.Any("err", err))
	}

	log.InfoContext(ctx, "tick done",
		slog.Int("total", len(refs)),
		slog.Int("skipped", skipped),
		slog.Int("inserted", inserted),
		slog.Int("errors", errs),
	)
	return nil
}

// processRef handles a single CallRecordRef: existence check, detail fetch,
// quality projection, and store upsert. The int counters are updated in
// place. The return value is unused today but kept so future callers can
// distinguish "handled" vs "needs retry" without another signature change.
func (c *Crawler) processRef(
	ctx context.Context,
	ref graph.CallRecordRef,
	log *slog.Logger,
	skipped, inserted, errs *int,
) bool {
	// One projection-only round-trip tells us both whether the call exists
	// and whether the previous tick finished writing its streams.
	exists, streamsProjected, err := c.calls.GetState(ctx, ref.ID)
	if err != nil {
		*errs++
		log.WarnContext(ctx, "get call state failed",
			slog.String("callId", ref.ID),
			slog.Any("err", err),
		)
		return true
	}
	if exists {
		if streamsProjected {
			*skipped++
			return true
		}
		// streamsProjected==false means either the previous tick crashed
		// after Upsert but before ReplaceByCall, or this is a legacy doc
		// from before the flag existed. Either way, fall through to the
		// full upsert path; MarkStreamsProjected at the end will mark the
		// doc as healed and prevent the same call from looping forever.
		log.DebugContext(ctx, "healing: call exists but streams not yet projected",
			slog.String("callId", ref.ID),
		)
	}

	rec, err := c.graphc.GetCallRecord(ctx, ref.ID)
	if err != nil {
		if errors.Is(err, graph.ErrCallNotFound) {
			// Expected race: Graph sometimes hasn't materialised the
			// detail yet even though the ref appears in the list. The
			// next tick's overlap window will pick it up.
			log.DebugContext(ctx, "call not found yet",
				slog.String("callId", ref.ID),
			)
			return true
		}
		*errs++
		log.WarnContext(ctx, "get call failed",
			slog.String("callId", ref.ID),
			slog.Any("err", err),
		)
		return true
	}

	callRow := quality.ToCallRow(rec, c.cfg.IncludeVideo)
	streamRows := quality.ToStreamRows(rec, c.cfg.IncludeVideo)

	storeCall := store.CallFromQuality(callRow)
	storeStreams := store.StreamRowsFromQuality(streamRows)

	if err := c.calls.Upsert(ctx, storeCall); err != nil {
		*errs++
		log.WarnContext(ctx, "calls upsert failed",
			slog.String("callId", ref.ID),
			slog.Any("err", err),
		)
		return true
	}
	if err := c.streams.ReplaceByCall(ctx, ref.ID, storeStreams); err != nil {
		*errs++
		log.WarnContext(ctx, "streams replace failed",
			slog.String("callId", ref.ID),
			slog.Any("err", err),
		)
		return true
	}

	// Mark the doc as fully processed so the next tick's GetState returns
	// (true, true) and the call is skipped — including the legitimate case
	// where the projection produced zero stream rows. Without this flag the
	// call would loop through the heal branch on every single tick.
	if err := c.calls.MarkStreamsProjected(ctx, ref.ID); err != nil {
		*errs++
		log.WarnContext(ctx, "mark streams projected failed",
			slog.String("callId", ref.ID),
			slog.Any("err", err),
		)
		return true
	}

	*inserted++
	return true
}

// clampDays keeps backfill windows inside [minBackfillDays, maxBackfillDays].
// Exposed as a standalone helper so it can be unit-tested without a Crawler.
func clampDays(days int) int {
	if days < minBackfillDays {
		return minBackfillDays
	}
	if days > maxBackfillDays {
		return maxBackfillDays
	}
	return days
}
