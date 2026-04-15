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
type callsRepo interface {
	Exists(ctx context.Context, id string) (bool, error)
	Upsert(ctx context.Context, c store.Call) error
}

// streamsRepo is the subset of *store.StreamsRepo used by the crawler.
type streamsRepo interface {
	HasStreams(ctx context.Context, id string) (bool, error)
	ReplaceByCall(ctx context.Context, callID string, rows []store.StreamRow) error
}

// metaRepo is the subset of *store.MetaRepo used by the crawler.
type metaRepo interface {
	SetLastCrawl(ctx context.Context, at time.Time, crawlErr error) error
	SetLastBackfill(ctx context.Context, at time.Time) error
}

// Config carries the tunable knobs exposed on the `teams_con crawl`
// subcommand. Both fields have sensible defaults applied by New.
type Config struct {
	// Interval is the time between tick starts. Defaults to 5m.
	Interval time.Duration
	// Window is the lookback size for each tick. Defaults to 30m; the
	// overlap between Window and Interval is what lets the crawler catch
	// records that Graph materialises with delay.
	Window time.Duration
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
func (c *Crawler) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "crawler run start",
		slog.Duration("interval", c.cfg.Interval),
		slog.Duration("window", c.cfg.Window),
	)

	// First tick fires immediately so the service is productive from t=0.
	if err := c.runOneTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
		c.log.ErrorContext(ctx, "crawler initial tick failed", slog.Any("err", err))
	}

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.InfoContext(ctx, "crawler run stop", slog.Any("err", ctx.Err()))
			return ctx.Err()
		case <-ticker.C:
			if err := c.runOneTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				c.log.ErrorContext(ctx, "crawler tick failed", slog.Any("err", err))
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
	// Layer 1: cheap existence check skips the expensive detail call.
	exists, err := c.calls.Exists(ctx, ref.ID)
	if err != nil {
		*errs++
		log.WarnContext(ctx, "exists check failed",
			slog.String("callId", ref.ID),
			slog.Any("err", err),
		)
		return true
	}
	if exists {
		// Layer 2: guard against partial-write gap — a previous tick may have
		// upserted the call document but then crashed before writing streams.
		// If streams are already present we can skip normally; if they are
		// missing we fall through and re-run the full upsert (idempotent for
		// immutable Graph records, so the extra work is cheap).
		hasStreams, streamErr := c.streams.HasStreams(ctx, ref.ID)
		if streamErr != nil {
			// Can't determine stream state; skip conservatively.
			*errs++
			log.WarnContext(ctx, "has-streams check failed",
				slog.String("callId", ref.ID),
				slog.Any("err", streamErr),
			)
			return true
		}
		if hasStreams {
			*skipped++
			return true
		}
		// streams are missing despite the call existing — fall through to
		// re-fetch and re-persist, healing the partial write.
		log.DebugContext(ctx, "healing partial write: call exists but streams missing",
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
