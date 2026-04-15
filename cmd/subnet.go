package cmd

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// newSubnetCmd builds the `teams_con subnet` parent and its three
// subcommands (ls / add / rm). Each child opens a short-lived store +
// Service, dispatches to the matching api method, prints the result, and
// closes the connection on the way out.
func newSubnetCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "subnet",
		Short: "Manage subnet labels (CIDR → friendly office/kind)",
		Long: `Manage the subnets collection that drives user_health_report enrichment.

Each entry maps a CIDR block to a human label (name + office + kind),
which the api layer attaches to per-user reports so operators see
"Xpanceo Dubai HQ wired" instead of raw IP literals.

Reads via list_subnets (MCP) or GET /subnets (HTTP). Writes via the
subcommands here or POST/DELETE /subnets directly.`,
	}
	parent.AddCommand(newSubnetLsCmd())
	parent.AddCommand(newSubnetAddCmd())
	parent.AddCommand(newSubnetRmCmd())
	return parent
}

func newSubnetLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List configured subnet labels",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				entries, err := svc.ListSubnets(ctx)
				if err != nil {
					return err
				}
				printSubnetTable(cmd, entries)
				return nil
			})
		},
	}
}

func newSubnetAddCmd() *cobra.Command {
	var name, office, kind, notes string
	c := &cobra.Command{
		Use:   "add <cidr>",
		Short: "Add or update a subnet label",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				entry, err := svc.UpsertSubnet(ctx, api.UpsertSubnetParams{
					Cidr:   args[0],
					Name:   name,
					Office: office,
					Kind:   kind,
					Notes:  notes,
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "stored: %s  name=%q  office=%q  kind=%q\n",
					entry.Cidr, entry.Name, entry.Office, entry.Kind)
				return nil
			})
		},
	}
	c.Flags().StringVar(&name, "name", "", "human label (required)")
	c.Flags().StringVar(&office, "office", "", "office grouping key (e.g. Dubai, RU, home)")
	c.Flags().StringVar(&kind, "kind", "", "connection kind (wifi, wired, mixed, home, vpn)")
	c.Flags().StringVar(&notes, "notes", "", "freeform notes")
	return c
}

func newSubnetRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <cidr>",
		Short: "Remove a subnet label",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withService(cmd, func(ctx context.Context, svc *api.Service) error {
				if err := svc.DeleteSubnet(ctx, args[0]); err != nil {
					if errors.Is(err, api.ErrNotFound) {
						return fmt.Errorf("subnet: %s not found", args[0])
					}
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", args[0])
				return nil
			})
		},
	}
}

// printSubnetTable renders entries as a tab-aligned table to cmd.OutOrStdout.
// An empty slice prints a friendly "(no subnets)" line so the operator does
// not see a confusing blank.
func printSubnetTable(cmd *cobra.Command, entries []store.SubnetEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no subnets configured)")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()
	fmt.Fprintln(w, "CIDR\tNAME\tOFFICE\tKIND\tUPDATED")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			e.Cidr,
			dashIfBlank(e.Name),
			dashIfBlank(e.Office),
			dashIfBlank(e.Kind),
			e.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
}

func dashIfBlank(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
