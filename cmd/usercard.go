package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// newUserCardCmd builds the `teams_con usercard` parent and its four
// subcommands (ls / show / set / rm). Each child opens a short-lived
// store + Service via withService and dispatches to the matching api
// method. Mirrors cmd/subnet.go in shape.
func newUserCardCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "usercard",
		Short: "Manage operator annotations (notes, tags, location) per user",
		Long: `Manage the usercards collection — per-UPN operator annotations that
persist triage state across sessions.

Each entry stores a free-form notes field, a tag list (vip, remote-only,
mobile-heavy, escalated, ...), a location hint, and an optional display
name override. The annotations show up in user_health_report (via the
MCP get_user_card tool and the HTTP GET /users/{upn}/health response)
and in the TUI portrait "Card" section.

Reads via get_user_card (MCP) or GET /usercards{/upn} (HTTP). Writes via
the subcommands here or PUT/DELETE /usercards/{upn} directly.`,
	}
	parent.AddCommand(newUserCardLsCmd())
	parent.AddCommand(newUserCardShowCmd())
	parent.AddCommand(newUserCardSetCmd())
	parent.AddCommand(newUserCardRmCmd())
	return parent
}

func newUserCardLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List configured user cards",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				cards, err := svc.ListUserCards(ctx)
				if err != nil {
					return err
				}
				printUserCardTable(cmd, cards)
				return nil
			})
		},
	}
}

func newUserCardShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <upn>",
		Short: "Show one user card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				card, err := svc.GetUserCard(ctx, args[0])
				if err != nil {
					return err
				}
				if card == nil {
					// Show returns a friendly message rather than erroring
					// out so an operator poking at the CLI sees the
					// "no card" state instead of a cryptic exit status.
					fmt.Fprintf(cmd.OutOrStdout(), "(no card for %s)\n", args[0])
					return nil
				}
				printUserCardDetail(cmd, card)
				return nil
			})
		},
	}
}

func newUserCardSetCmd() *cobra.Command {
	var name, location, notes, tagsCSV string
	c := &cobra.Command{
		Use:   "set <upn>",
		Short: "Create or update a user card (merge semantics)",
		Long: `Set merges provided flags on top of the existing card. A missing
card is created from scratch. Flags that are NOT passed leave the
corresponding field untouched.

--tags is a comma-separated list that REPLACES the existing tag slice.
For append/remove semantics use --tags with the full desired set (read
the existing card with 'usercard show' first).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				upn := args[0]
				// Start from the existing card so unspecified flags are
				// preserved. GetUserCard returns (nil, nil) for missing
				// by design — treat that as a blank card.
				existing, err := svc.GetUserCard(ctx, upn)
				if err != nil {
					return err
				}
				params := api.UpsertUserCardParams{Upn: upn}
				if existing != nil {
					params.DisplayName = existing.DisplayName
					params.Location = existing.Location
					params.Notes = existing.Notes
					params.Tags = existing.Tags
				}
				// Overlay only the flags that were actually passed.
				if cmd.Flags().Changed("name") {
					params.DisplayName = name
				}
				if cmd.Flags().Changed("location") {
					params.Location = location
				}
				if cmd.Flags().Changed("note") {
					params.Notes = notes
				}
				if cmd.Flags().Changed("tags") {
					params.Tags = splitTagsCSV(tagsCSV)
				}

				stored, err := svc.UpsertUserCard(ctx, params)
				if err != nil {
					return err
				}
				printUserCardDetail(cmd, stored)
				return nil
			})
		},
	}
	c.Flags().StringVar(&name, "name", "", "display name override")
	c.Flags().StringVar(&location, "location", "", "location hint (e.g. Dubai HQ, RU home, roaming)")
	c.Flags().StringVar(&notes, "note", "", "freeform notes (replaces existing when set)")
	c.Flags().StringVar(&tagsCSV, "tags", "", "comma-separated tag list (replaces existing when set)")
	return c
}

func newUserCardRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <upn>",
		Short: "Remove a user card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				if err := svc.DeleteUserCard(ctx, args[0]); err != nil {
					if errors.Is(err, api.ErrNotFound) {
						return fmt.Errorf("usercard: %s not found", args[0])
					}
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", args[0])
				return nil
			})
		},
	}
}

// splitTagsCSV parses a comma-separated tag list. Whitespace around each
// tag is trimmed and empty tokens are dropped so "a, b,, c" collapses to
// ["a","b","c"]. An empty input returns nil so the Service layer can
// normalise further.
func splitTagsCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// printUserCardTable renders cards as a tab-aligned summary table to
// cmd.OutOrStdout. An empty slice prints a friendly placeholder.
func printUserCardTable(cmd *cobra.Command, cards []store.UserCard) {
	if len(cards) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no user cards configured)")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()
	fmt.Fprintln(w, "UPN\tNAME\tLOCATION\tTAGS\tUPDATED")
	for _, c := range cards {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			c.Upn,
			dashIfBlank(c.DisplayName),
			dashIfBlank(c.Location),
			dashIfBlank(strings.Join(c.Tags, ",")),
			c.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
}

// printUserCardDetail renders a single card as a multi-line block. Used
// by both `show` and `set` so the operator always sees the canonical
// post-write state.
func printUserCardDetail(cmd *cobra.Command, c *store.UserCard) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "upn:        %s\n", c.Upn)
	fmt.Fprintf(out, "name:       %s\n", dashIfBlank(c.DisplayName))
	fmt.Fprintf(out, "location:   %s\n", dashIfBlank(c.Location))
	fmt.Fprintf(out, "tags:       %s\n", dashIfBlank(strings.Join(c.Tags, ", ")))
	fmt.Fprintf(out, "notes:      %s\n", dashIfBlank(c.Notes))
	fmt.Fprintf(out, "updatedAt:  %s\n", c.UpdatedAt.UTC().Format(time.RFC3339))
}
