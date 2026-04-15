package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"teams_con/internal/version"
)

// newVersionCmd exposes `teams_con version`. The same info is also
// attached to rootCmd.Version so `teams_con --version` / `-v` prints
// the same string without a dedicated subcommand invocation.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build info",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Get().String())
		},
	}
}
