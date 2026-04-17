package cmd

import (
	"context"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"teams_con/internal/api"
	"teams_con/internal/geo"
	mcpsrv "teams_con/internal/mcp"
	"teams_con/internal/store"
)

// newMcpCmd constructs the `teams_con mcp` subcommand — an MCP server
// over stdio for LLM clients (Claude Code / Claude Desktop). It wraps the
// same read-only *api.Service as `serve`, but speaks JSON-RPC on stdin/
// stdout instead of HTTP. Graph credentials are NOT required: mcp only
// reads from Mongo.
//
// Stdout is RESERVED for the MCP protocol. All logging goes to stderr,
// and we hard-redirect the stdlib log package defensively in case any
// third-party dependency writes to the default stdlib logger.
func newMcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server over stdio",
		Long:  "Expose the read-only call-quality API as MCP tools for LLM clients (Claude Code, Claude Desktop). Communicates JSON-RPC over stdin/stdout; all logs go to stderr.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Defensive: any stdlib log.Print* must NOT leak to stdout
			// since stdout is the JSON-RPC transport.
			stdlog.SetOutput(os.Stderr)

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Local stderr-only slog — do NOT reuse newLogger() even
			// though it also writes to stderr, because we want to make
			// the stderr choice explicit at the one place where it is
			// load-bearing for protocol correctness.
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level: parseLogLevel(flagLogLevel),
			}))

			ctx, cancel := signalContext()
			defer cancel()

			st, err := store.New(ctx, cfg.MongoUri, "", log)
			if err != nil {
				return fmt.Errorf("mcp: store: %w", err)
			}
			// cmd.Context() is cancelled when RunE returns, so we use a
			// fresh background context to give Mongo Disconnect a chance to
			// flush before the process exits.
			defer func() {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer closeCancel()
				_ = st.Close(closeCtx)
			}()

			if err := st.EnsureIndexes(ctx); err != nil {
				return fmt.Errorf("mcp: ensure indexes: %w", err)
			}

			svc := api.NewService(st, log)
			geoResolver := geo.New(st.RelayGeo, log)
			srv := mcpsrv.NewServer(svc, geoResolver, log)

			log.Info("mcp: serving over stdio")
			return srv.Run(ctx)
		},
	}
}
