// Package cmd hosts the cobra CLI for teams_con: the root command plus the
// crawl / serve / tui subcommands.
package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"teams_con/internal/version"
)

// flagLogLevel is the persistent --log-level flag value, shared by all
// subcommands. Kept package-level because that is cobra's idiomatic pattern
// for persistent flags.
var flagLogLevel string

const defaultLogLevel = "info"

var rootCmd = &cobra.Command{
	Use:   "teams_con",
	Short: "Teams Call Quality monitor",
	Long: `teams_con is a Microsoft Teams call quality monitor.

It crawls Microsoft Graph callRecords into MongoDB (bypassing Graph's 30-day
retention limit), exposes a read-only HTTP API over the stored history, and
ships a bubbletea TUI client for interactive drill-down.

Subcommands:
  crawl    - periodically fetch callRecords from Graph into MongoDB
  serve    - HTTP API over the stored call history
  tui      - terminal UI client for the API
  mcp      - stdio MCP server for LLM clients (Claude Code / Desktop)
  version  - print version and build info`,
	Version:      version.Get().String(),
	SilenceUsage: true,
}

// Execute runs the root command. Called from main.
func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

func init() {
	rootCmd.PersistentFlags().StringVar(
		&flagLogLevel,
		"log-level",
		defaultLogLevel,
		"log level: debug, info, warn, error",
	)

	rootCmd.AddCommand(
		newCrawlCmd(),
		newServeCmd(),
		newTUICmd(),
		newMcpCmd(),
		newSubnetCmd(),
		newUserCardCmd(),
		newVersionCmd(),
	)

	// Custom template so `-v` / `--version` renders the full build
	// descriptor on one line without the default cobra prefix.
	rootCmd.SetVersionTemplate("{{.Version}}\n")
}

// newLogger builds a JSON slog logger writing to stderr at the requested
// level. Stderr is used (not stdout) so container logging and CLI output
// composition work cleanly; crawl/serve use stderr for logs and leave stdout
// free for anything cobra itself might emit.
func newLogger(level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(level),
	}))
}

// parseLogLevel converts a textual level to slog.Level. Unknown values fall
// back to Info so a typo never silences the process.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// signalContext returns a context cancelled on SIGINT or SIGTERM plus the
// cancel function. Every long-running subcommand uses this so Ctrl+C
// unwinds the whole process cleanly through ctx propagation.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
