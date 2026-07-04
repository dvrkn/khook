package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/spec"
)

func newPlanCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Validate a spec and print its execution plan without touching the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := flags.load()
			if err != nil {
				return err
			}
			levels, err := engine.Levels(doc.Steps)
			if err != nil {
				return validationErr(err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s: %d step(s) in %d level(s)\n", doc.Metadata.Name, len(doc.Steps), len(levels))
			for i, level := range levels {
				fmt.Fprintf(out, "\nLevel %d (parallel):\n", i+1)
				for _, step := range level {
					fmt.Fprintf(out, "  - %s  [%s] %s\n", step.Name, step.Type(), describeStep(step))
				}
			}
			return nil
		},
	}
	flags.register(cmd)
	return cmd
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
	}
	if len(step.Needs) > 0 {
		desc += fmt.Sprintf("  (needs: %s)", strings.Join(step.Needs, ", "))
	}
	return desc
}
