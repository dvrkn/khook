package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Set via -ldflags "-X github.com/dvrkn/khook/internal/cli.Version=..." at
// release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the khook version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "khook %s (commit %s, built %s, %s/%s)\n",
				Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
		},
	}
}
