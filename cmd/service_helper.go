package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// cliCmdTimeout bounds every short-lived `teams_con <subcommand>`
// operation that opens a one-shot Service (subnet, usercard, and any
// future v1.2+ admin subcommands). The CLI is interactive, so anything
// slower than this almost certainly means Mongo is unreachable — fail
// fast and let the operator retry.
const cliCmdTimeout = 15 * time.Second

// cliCloseTimeout bounds the deferred store.Close on the way out of a
// subcommand. We do not want the unwind path to ever outlive a request.
const cliCloseTimeout = 5 * time.Second

// withService is the shared boilerplate every short-lived admin
// subcommand uses: load config, open store, build Service, run fn, close
// store. It accepts the cobra.Command so it can use cmd.Context() /
// cmd.OutOrStdout for testability. Phase 1's subnet commands and Phase
// 2's usercard commands share this; Phase 4+ admin commands will too.
func withService(cmd *cobra.Command, fn func(ctx context.Context, svc *api.Service) error) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := newLogger(flagLogLevel)

	ctx, cancel := context.WithTimeout(cmd.Context(), cliCmdTimeout)
	defer cancel()

	st, err := store.New(ctx, cfg.MongoUri, "", log)
	if err != nil {
		return fmt.Errorf("cli: store: %w", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), cliCloseTimeout)
		defer closeCancel()
		_ = st.Close(closeCtx)
	}()

	svc := api.NewService(st, log)
	return fn(ctx, svc)
}
