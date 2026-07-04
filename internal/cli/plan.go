package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/kube"
	"github.com/dvrkn/khook/internal/ops"
	"github.com/dvrkn/khook/internal/spec"
)

// planStepTimeout bounds the cluster reads for a single step's assessment.
const planStepTimeout = 30 * time.Second

// diffStepTimeout bounds one step's diff: dry-run requests plus, for helm
// steps, downloading the chart.
const diffStepTimeout = 2 * time.Minute

func newPlanCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{redact: root.redact}
	var offline, diff bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what apply would do: the execution plan, checked against the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if diff && offline {
				return validationErr(errors.New("--diff needs cluster access and cannot be combined with --offline"))
			}
			doc, err := flags.load()
			if err != nil {
				return err
			}
			levels, err := engine.Levels(doc.Steps)
			if err != nil {
				return validationErr(err)
			}

			out := root.redact.Wrap(cmd.OutOrStdout())
			var executor *ops.Executor
			if !offline {
				clients, err := kube.New(root.kubeconfig, root.kubecontext)
				if err != nil {
					return executionErr(fmt.Errorf("%w (use --offline for a cluster-free plan)", err))
				}
				version, err := clients.Typed.Discovery().ServerVersion()
				if err != nil {
					return executionErr(fmt.Errorf("cluster unreachable (use --offline for a cluster-free plan): %w", err))
				}
				fmt.Fprintf(out, "cluster: Kubernetes %s\n", version.GitVersion)
				executor = ops.NewExecutor(clients, root.log)
			}

			fmt.Fprintf(out, "%s: %d step(s) in %d level(s)\n", doc.Metadata.Name, len(doc.Steps), len(levels))
			counts := map[ops.Action]int{}
			for i, level := range levels {
				fmt.Fprintf(out, "\nLevel %d (parallel):\n", i+1)
				for _, step := range level {
					fmt.Fprintf(out, "  - %s  [%s] %s\n", step.Name, step.Type(), describeStep(step))
					if step.Excluded {
						counts[ops.ActionSkip]++
						fmt.Fprintf(out, "      plan: %s — when condition is false (%s)\n", ops.ActionSkip, step.When)
						continue
					}
					if executor == nil {
						continue
					}
					stepCtx, cancel := context.WithTimeout(cmd.Context(), planStepTimeout)
					a := executor.Plan(stepCtx, step)
					cancel()
					counts[a.Action]++
					if a.Detail != "" {
						fmt.Fprintf(out, "      plan: %s — %s\n", a.Action, a.Detail)
					} else {
						fmt.Fprintf(out, "      plan: %s\n", a.Action)
					}
					if diff && diffable(a.Action) {
						printStepDiff(cmd.Context(), out, executor, step)
					}
				}
			}
			if executor != nil {
				fmt.Fprintf(out, "\nPlan: %s\n", summarizePlan(counts))
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVar(&offline, "offline", false, "skip all cluster access and print the DAG-only plan")
	cmd.Flags().BoolVar(&diff, "diff", false, "also print rendered object diffs (server-side dry-run; never mutates)")
	return cmd
}

// diffable reports whether a predicted action has rendered objects worth
// diffing (helm installs/upgrades and manifest applies).
func diffable(a ops.Action) bool {
	switch a {
	case ops.ActionInstall, ops.ActionUpgrade, ops.ActionCreate, ops.ActionConfigure:
		return true
	}
	return false
}

// printStepDiff renders one step's object diff under its plan line. Diff
// problems degrade to a note, mirroring how Plan degrades to unknown.
func printStepDiff(ctx context.Context, out io.Writer, executor *ops.Executor, step *spec.Step) {
	stepCtx, cancel := context.WithTimeout(ctx, diffStepTimeout)
	text, err := executor.Diff(stepCtx, step)
	cancel()
	switch {
	case err != nil:
		fmt.Fprintf(out, "      diff: unavailable — %v\n", err)
	case text == "":
		fmt.Fprintln(out, "      diff: no changes")
	default:
		fmt.Fprintln(out, "      diff:")
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			if line == "" {
				fmt.Fprintln(out)
				continue
			}
			fmt.Fprintf(out, "        %s\n", line)
		}
	}
}

// summarizePlan renders the action counts in execution-relevant order.
func summarizePlan(counts map[ops.Action]int) string {
	order := []ops.Action{
		ops.ActionInstall, ops.ActionUpgrade, ops.ActionCreate, ops.ActionConfigure,
		ops.ActionDelete, ops.ActionRestart, ops.ActionRun, ops.ActionWait,
		ops.ActionSkip, ops.ActionNone, ops.ActionUnknown,
	}
	var parts []string
	for _, action := range order {
		if n := counts[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, action))
		}
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, ", ")
}

// describeStep renders a one-line human summary of what the step will do.
func describeStep(step *spec.Step) string {
	var desc string
	switch {
	case step.Helm != nil:
		h := step.Helm
		version := h.Version
		if version == "" {
			version = "latest"
		}
		desc = fmt.Sprintf("release %q: %s@%s -> namespace %s", h.ReleaseName(step.Name), h.Chart, version, h.TargetNamespace())
	case step.Apply != nil:
		desc = fmt.Sprintf("apply %d manifest source(s)", len(step.Apply.Manifests))
	case step.Delete != nil:
		if step.Delete.Resource != "" {
			desc = "delete " + step.Delete.Resource
		} else {
			desc = fmt.Sprintf("delete %d manifest source(s)", len(step.Delete.Manifests))
		}
	case step.Wait != nil:
		desc = fmt.Sprintf("wait on %s for %s", step.Wait.On, step.Wait.For)
	case step.Rollout != nil:
		if step.Rollout.Restart != "" {
			desc = "restart " + step.Rollout.Restart
		} else {
			desc = "status " + step.Rollout.Status
		}
	case step.Job != nil:
		desc = fmt.Sprintf("run image %s -> namespace %s", step.Job.Image, step.Job.TargetNamespace())
	}
	if len(step.Needs) > 0 {
		desc += fmt.Sprintf("  (needs: %s)", strings.Join(step.Needs, ", "))
	}
	return desc
}
