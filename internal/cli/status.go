package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/kube"
	"github.com/dvrkn/khook/internal/state"
)

func newStatusCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{redact: root.redact}
	var output string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the spec's last run from its state record",
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return validationErr(fmt.Errorf("invalid --output %q (want text or json)", output))
			}
			doc, err := flags.load()
			if err != nil {
				return err
			}
			if !doc.StateEnabled() {
				return validationErr(fmt.Errorf("spec %q does not enable state: — there is no record to read", doc.Metadata.Name))
			}

			clients, err := kube.New(root.kubeconfig, root.kubecontext)
			if err != nil {
				return executionErr(err)
			}
			store := state.NewStore(clients.Typed, doc.State.TargetNamespace(), doc.State.SecretName(doc.Metadata.Name))
			rec, err := store.Load(cmd.Context())
			if err != nil {
				return executionErr(err)
			}

			specChanged := false
			if rec != nil {
				hash, err := state.SpecHash(doc)
				if err != nil {
					return executionErr(err)
				}
				specChanged = rec.SpecHash != hash
			}

			out := root.redact.Wrap(cmd.OutOrStdout())
			if output == "json" {
				return printStatusJSON(out, rec, specChanged)
			}
			printStatusText(out, store.Ref(), rec, specChanged)
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	return cmd
}

func printStatusText(w io.Writer, secretRef string, rec *state.Record, specChanged bool) {
	if rec == nil {
		fmt.Fprintf(w, "no state record at secret %s — the spec has not been applied yet (or the record was deleted)\n", secretRef)
		return
	}
	fmt.Fprintf(w, "spec:    %s\n", rec.SpecName)
	fmt.Fprintf(w, "record:  secret %s (khook %s)\n", secretRef, rec.KhookVersion)
	fmt.Fprintf(w, "run:     %s, started %s, updated %s\n",
		rec.RunStatus,
		rec.StartedAt.Local().Format(time.RFC3339),
		rec.UpdatedAt.Local().Format(time.RFC3339))
	if specChanged {
		fmt.Fprintln(w, "spec has changed since this run — the next apply starts fresh")
	} else {
		fmt.Fprintln(w, "spec is unchanged since this run — the next apply resumes past completed steps")
	}
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tTYPE\tSTATUS\tATTEMPTS\tDURATION\tDETAIL")
	for _, sr := range rec.Steps {
		detail := sr.SkipReason
		if sr.Error != "" {
			detail = sr.Error
		}
		attempts := ""
		if sr.Attempts > 0 {
			attempts = fmt.Sprintf("%d", sr.Attempts)
		}
		duration := ""
		if sr.DurationMs > 0 {
			duration = (time.Duration(sr.DurationMs) * time.Millisecond).String()
		}
		status := sr.Status
		if status == "" {
			status = "-" // seeded but never finished (crash mid-run)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", sr.Name, sr.Type, status, attempts, duration, detail)
	}
	tw.Flush()
}

// statusJSON is the --output json document.
type statusJSON struct {
	Found       bool          `json:"found"`
	SpecChanged bool          `json:"specChanged,omitempty"`
	Record      *state.Record `json:"record,omitempty"`
}

func printStatusJSON(w io.Writer, rec *state.Record, specChanged bool) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(statusJSON{Found: rec != nil, SpecChanged: specChanged, Record: rec})
}
