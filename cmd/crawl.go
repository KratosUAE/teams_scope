package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"teams_con/internal/crawler"
	"teams_con/internal/graph"
	"teams_con/internal/store"
)

// newCrawlCmd constructs the `teams_con crawl` subcommand. The subcommand
// is the Graph→Mongo daemon: it loops on a ticker (Run) or pulls a fixed
// historical window once (Backfill).
func newCrawlCmd() *cobra.Command {
	var (
		interval      time.Duration
		window        time.Duration
		sweepInterval time.Duration
		sweepWindow   time.Duration
		backfillDays  int
	)

	cmd := &cobra.Command{
		Use:   "crawl",
		Short: "Run the crawler daemon (pulls Graph callRecords into Mongo)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := cfg.requireGraphCreds(); err != nil {
				return err
			}

			log := newLogger(flagLogLevel)
			ctx, cancel := signalContext()
			defer cancel()

			ts, err := graph.NewTokenSource(ctx, cfg.TenantId, cfg.ClientId, cfg.ClientSecret)
			if err != nil {
				return fmt.Errorf("crawl: token source: %w", err)
			}
			gc := graph.New(ts, log)

			st, err := store.New(ctx, cfg.MongoUri, "", log)
			if err != nil {
				return fmt.Errorf("crawl: store: %w", err)
			}
			defer func() {
				// Use a fresh context for shutdown so we still close even
				// when the signal context is already cancelled.
				_ = st.Close(cmd.Context())
			}()

			if err := st.EnsureIndexes(ctx); err != nil {
				return fmt.Errorf("crawl: ensure indexes: %w", err)
			}

			cr := crawler.New(crawler.Config{
				Interval:      interval,
				Window:        window,
				SweepInterval: sweepInterval,
				SweepWindow:   sweepWindow,
			}, gc, st, log)

			if backfillDays > 0 {
				log.Info("crawl: starting one-shot backfill", "days", backfillDays)
				return cr.Backfill(ctx, backfillDays)
			}
			log.Info("crawl: starting daemon",
				"interval", interval.String(),
				"window", window.String(),
			)
			return cr.Run(ctx)
		},
	}

	cmd.Flags().DurationVar(&interval, "interval", 5*time.Minute, "tick interval")
	cmd.Flags().DurationVar(&window, "window", 30*time.Minute, "time window per tick")
	cmd.Flags().DurationVar(&sweepInterval, "sweep-interval", 0, "periodic sweep interval to catch slow-materialising groupCalls (0 = use default 1h, negative e.g. -1ns = disable)")
	cmd.Flags().DurationVar(&sweepWindow, "sweep-window", 0, "sweep lookback window (0 = use default 4h)")
	cmd.Flags().IntVar(&backfillDays, "backfill", 0, "run one-shot backfill for N days (0 = disabled)")
	return cmd
}
