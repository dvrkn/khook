package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/kube"
	"github.com/dvrkn/khook/internal/ops"
	"github.com/dvrkn/khook/internal/spec"
	"github.com/dvrkn/khook/internal/state"
)

func newDestroyCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{redact: root.redact}
	var output string
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Tear down what the spec created, in reverse dependency order",
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return validationErr(fmt.Errorf("invalid --output %q (want text or json)", output))
			}
			doc, err := flags.load()
			if err != nil {
				return err
			}
			// Teardown runs the DAG backwards: dependents are removed before
			// the steps they needed.
			reversed := engine.Reversed(doc.Steps)
			levels, err := engine.Levels(reversed)
			if err != nil {
				return validationErr(err)
			}

			clients, err := kube.New(root.kubeconfig, root.kubecontext)
			if err != nil {
				return executionErr(err)
			}

			// Live progress lines replace the per-step log stream, exactly as
			// in apply.
			interactive := output == "text" && stdoutIsTTY()
			if interactive && !root.logLevelSet {
				if quiet, err := newLogger("warn", root.logFormat, root.redact.Wrap(os.Stderr)); err == nil {
					root.log = quiet
					slog.SetDefault(quiet)
				}
			}

			executor := ops.NewExecutor(clients, root.log)
			runner := engine.NewRunner(doc.Defaults, executor.Destroy, root.log)
			runner.Skip = destroySkips(reversed)

			stdout := root.redact.Wrap(os.Stdout)
			var prog *progress
			if interactive {
				prog = newProgress(stdout, levels)
				runner.OnEvent = prog.Handle
				prog.Start()
			} else {
				root.log.Info("destroying spec", "name", doc.Metadata.Name, "steps", len(reversed))
			}

			results, runErr := runner.Run(cmd.Context(), reversed)

			// A clean teardown retires the run-state record too, so the next
			// apply starts fresh instead of resuming into an empty cluster.
			var stateErr error
			if runErr == nil && doc.StateEnabled() {
				store := state.NewStore(clients.Typed, doc.State.TargetNamespace(), doc.State.SecretName(doc.Metadata.Name))
				if stateErr = store.Delete(cmd.Context()); stateErr == nil {
					root.log.Info("state record removed", "secret", store.Ref())
				}
			}

			switch {
			case interactive:
				prog.Stop(results)
			case output == "json":
				if err := printJSONResults(stdout, doc.Metadata.Name, results, runErr); err != nil {
					return executionErr(err)
				}
			default:
				printSummary(stdout, results)
			}
			if runErr != nil {
				return executionErr(runErr)
			}
			if stateErr != nil {
				return executionErr(fmt.Errorf("teardown succeeded but the state record could not be removed: %w", stateErr))
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "text", "final results format: text or json")
	return cmd
}

// destroySkips names the steps destroy has nothing to do for — step types
// that create nothing (or whose effect khook does not reverse). They are
// reported skipped and still satisfy the teardown ordering.
func destroySkips(steps []spec.Step) map[string]string {
	skip := map[string]string{}
	for i := range steps {
		if reason := ops.DestroySkipReason(&steps[i]); reason != "" {
			skip[steps[i].Name] = reason
		}
	}
	if len(skip) == 0 {
		return nil
	}
	return skip
}
