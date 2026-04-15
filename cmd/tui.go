package cmd

import (
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"teams_con/internal/tui"
)

// tuiLogFile is the fallback destination when the TUI is run with
// --log-level != "info". Bubbletea owns stdout for its alt-screen render,
// and writing slog to stderr interleaves garbage into the terminal frame,
// so we route logs to a file instead.
const tuiLogFile = "/tmp/teams_con-tui.log"

// newTUICmd constructs the `teams_con tui` subcommand. The TUI is a
// read-only client that talks to a running `serve` instance — it never
// touches Mongo or Graph directly.
func newTUICmd() *cobra.Command {
	var apiURL string

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Run the interactive TUI client",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			base := apiURL
			if base == "" {
				base = cfg.ApiUrl
			}

			ctx, cancel := signalContext()
			defer cancel()

			// Route logs away from stdout/stderr so they cannot corrupt
			// the alt-screen render. At the default "info" level we
			// discard entirely; above that we tee to a file so debug
			// sessions are still productive.
			var log *slog.Logger
			if parseLogLevel(flagLogLevel) >= slog.LevelInfo {
				log = slog.New(slog.NewJSONHandler(io.Discard, nil))
			} else {
				f, ferr := os.OpenFile(tuiLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if ferr != nil {
					// If the log file cannot be opened we still want the
					// TUI to start — fall back to discard.
					log = slog.New(slog.NewJSONHandler(io.Discard, nil))
				} else {
					// Closing on command return is fine; the process exit
					// that follows will flush the OS buffer anyway.
					defer f.Close()
					log = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
						Level: parseLogLevel(flagLogLevel),
					}))
				}
			}

			return tui.Run(ctx, base, log)
		},
	}

	cmd.Flags().StringVar(&apiURL, "api", "", "API base URL (default from env ApiUrl or http://localhost:8080)")
	return cmd
}
