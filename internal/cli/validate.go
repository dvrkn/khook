package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/engine"
)

func newValidateCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Parse and validate a spec (variables resolved, DAG checked); no cluster access",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := flags.load()
			if err != nil {
				return err
			}
			if _, err := engine.Levels(doc.Steps); err != nil {
				return validationErr(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%d steps)\n", flags.file, len(doc.Steps))
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}
