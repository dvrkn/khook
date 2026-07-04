package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	var output string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute a Khook spec against the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return validationErr(fmt.Errorf("invalid --output %q (want text or json)", output))
			}
			doc, err := flags.load()
			if err != nil {
				return err
			}
			// Surface DAG cycles before touching the cluster.
			levels, err := engine.Levels(doc.Steps)
			if err != nil {
				return validationErr(err)
			}

			clients, err := kube.New(root.kubeconfig, root.kubecontext)
			if err != nil {
				return executionErr(err)
			}

			// Live progress lines replace the per-step log stream. Quiet the
			// logger so executor chatter on stderr does not garble the
			// display — unless the user explicitly picked a level.
			interactive := output == "text" && stdoutIsTTY()
			if interactive && !root.logLevelSet {
				if quiet, err := newLogger("warn", root.logFormat); err == nil {
					root.log = quiet
					slog.SetDefault(quiet)
				}
			}

			executor := ops.NewExecutor(clients, root.log)
			runner := engine.NewRunner(doc.Defaults, executor.Execute, root.log)

			var prog *progress
			if interactive {
				prog = newProgress(os.Stdout, levels)
				runner.OnEvent = prog.Handle
				prog.Start()
			} else {
				root.log.Info("applying spec", "name", doc.Metadata.Name, "steps", len(doc.Steps))
			}

			results, runErr := runner.Run(cmd.Context(), doc.Steps)

			switch {
			case interactive:
				prog.Stop(results)
			case output == "json":
				if err := printJSONResults(os.Stdout, doc.Metadata.Name, results, runErr); err != nil {
					return executionErr(err)
				}
			default:
				printSummary(results)
			}
			if runErr != nil {
				return executionErr(runErr)
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "text", "final results format: text or json")
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

// runJSON is the --output json document: one object for the run, one entry
// per step, mirroring the summary table.
type runJSON struct {
	Name   string     `json:"name"`
	Status string     `json:"status"`
	Steps  []stepJSON `json:"steps"`
}

type stepJSON struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
}

func printJSONResults(w io.Writer, name string, results []engine.Result, runErr error) error {
	out := runJSON{Name: name, Status: "ok"}
	if runErr != nil {
		out.Status = "failed"
	}
	for _, res := range results {
		step := stepJSON{
			Name:       res.Step.Name,
			Type:       res.Step.Type(),
			Status:     string(res.Status),
			Attempts:   res.Attempts,
			DurationMs: res.Duration.Milliseconds(),
			SkipReason: res.SkipReason,
		}
		if res.Err != nil {
			step.Error = res.Err.Error()
		}
		out.Steps = append(out.Steps, step)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
