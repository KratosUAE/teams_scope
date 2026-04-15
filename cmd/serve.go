package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// newServeCmd constructs the `teams_con serve` subcommand — the read-only
// HTTP API over the Mongo-backed call history. Graph credentials are NOT
// required: serve only reads from Mongo.
func newServeCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			// Flag wins over env so ops can override ad-hoc without
			// touching .env.
			listenAddr := addr
			if listenAddr == "" {
				listenAddr = cfg.ApiAddr
			}

			log := newLogger(flagLogLevel)
			ctx, cancel := signalContext()
			defer cancel()

			st, err := store.New(ctx, cfg.MongoUri, "", log)
			if err != nil {
				return fmt.Errorf("serve: store: %w", err)
			}
			defer func() {
				_ = st.Close(cmd.Context())
			}()

			if err := st.EnsureIndexes(ctx); err != nil {
				return fmt.Errorf("serve: ensure indexes: %w", err)
			}

			svc := api.NewService(st, log)
			handlers := api.NewHandlers(svc, log)
			srv := api.NewServer(listenAddr, handlers, log)

			log.Info("serve: starting api", "addr", listenAddr)
			return srv.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default from env ApiAddr or :8080)")
	return cmd
}
