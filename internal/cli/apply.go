package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/kube"
	"github.com/dvrkn/khook/internal/ops"
)

func newApplyCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute a Khook spec against the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := flags.load()
			if err != nil {
				return err
			}
			// Surface DAG cycles before touching the cluster.
			if _, err := engine.Levels(doc.Steps); err != nil {
				return validationErr(err)
			}

			clients, err := kube.New(root.kubeconfig, root.kubecontext)
			if err != nil {
				return executionErr(err)
			}

			root.log.Info("applying spec", "name", doc.Metadata.Name, "steps", len(doc.Steps))
			executor := ops.NewExecutor(clients, root.log)
			runner := engine.NewRunner(doc.Defaults, executor.Execute, root.log)
			results, runErr := runner.Run(cmd.Context(), doc.Steps)

			printSummary(results)
			if runErr != nil {
				return executionErr(runErr)
			}
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}

func printSummary(results []engine.Result) {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STEP\tTYPE\tSTATUS\tATTEMPTS\tDURATION\tDETAIL")
	for _, res := range results {
		detail := ""
		switch {
		case res.Err != nil:
			detail = res.Err.Error()
		case res.SkipReason != "":
			detail = res.SkipReason
		}
		attempts := ""
		if res.Attempts > 0 {
			attempts = fmt.Sprintf("%d", res.Attempts)
		}
		duration := ""
		if res.Duration > 0 {
			duration = res.Duration.Round(time.Millisecond).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			res.Step.Name, res.Step.Type(), res.Status, attempts, duration, detail)
	}
	w.Flush()
}
