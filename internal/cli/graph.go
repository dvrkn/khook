package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/spec"
)

func newGraphCommand(root *rootOptions) *cobra.Command {
	flags := &specFlags{redact: root.redact}
	var format string
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Emit the step DAG as Mermaid or DOT for docs and review; no cluster access",
		RunE: func(cmd *cobra.Command, args []string) error {
			render, ok := map[string]func(*spec.Document) string{
				"mermaid": mermaidGraph,
				"dot":     dotGraph,
			}[format]
			if !ok {
				return validationErr(fmt.Errorf("invalid --format %q (want mermaid or dot)", format))
			}
			doc, err := flags.load()
			if err != nil {
				return err
			}
			if _, err := engine.Levels(doc.Steps); err != nil {
				return validationErr(err)
			}
			fmt.Fprint(root.redact.Wrap(cmd.OutOrStdout()), render(doc))
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&format, "format", "mermaid", "output format: mermaid or dot")
	return cmd
}

// stepLabel renders a node's display text: name, type, and whether a false
// when: condition excludes it from the run.
func stepLabel(step *spec.Step) string {
	if step.Excluded {
		return fmt.Sprintf("%s (%s, skipped)", step.Name, step.Type())
	}
	return fmt.Sprintf("%s (%s)", step.Name, step.Type())
}

// mermaidGraph renders the DAG as a Mermaid flowchart. Node ids are
// positional (n0, n1, ...) rather than step names, which could collide with
// Mermaid keywords such as "end".
func mermaidGraph(doc *spec.Document) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	id := map[string]string{}
	for i := range doc.Steps {
		id[doc.Steps[i].Name] = fmt.Sprintf("n%d", i)
	}
	anySkipped := false
	for i := range doc.Steps {
		step := &doc.Steps[i]
		fmt.Fprintf(&b, "    %s[\"%s\"]", id[step.Name], stepLabel(step))
		if step.Excluded {
			b.WriteString(":::skipped")
			anySkipped = true
		}
		b.WriteString("\n")
	}
	for i := range doc.Steps {
		for _, need := range doc.Steps[i].Needs {
			fmt.Fprintf(&b, "    %s --> %s\n", id[need], id[doc.Steps[i].Name])
		}
	}
	if anySkipped {
		b.WriteString("    classDef skipped stroke-dasharray:5 5,opacity:0.6\n")
	}
	return b.String()
}

// dotGraph renders the DAG in Graphviz DOT. Step names are DNS-label-like,
// so quoting alone makes them safe identifiers; metadata.name is free-form
// and needs escaping.
func dotGraph(doc *spec.Document) string {
	var b strings.Builder
	fmt.Fprintf(&b, "digraph %s {\n", dotQuote(doc.Metadata.Name))
	b.WriteString("    rankdir=TB;\n")
	b.WriteString("    node [shape=box];\n")
	for i := range doc.Steps {
		step := &doc.Steps[i]
		attrs := fmt.Sprintf("label=%s", dotQuote(stepLabel(step)))
		if step.Excluded {
			attrs += ", style=dashed, color=gray"
		}
		fmt.Fprintf(&b, "    %s [%s];\n", dotQuote(step.Name), attrs)
	}
	for i := range doc.Steps {
		for _, need := range doc.Steps[i].Needs {
			fmt.Fprintf(&b, "    %s -> %s;\n", dotQuote(need), dotQuote(doc.Steps[i].Name))
		}
	}
	b.WriteString("}\n")
	return b.String()
}

func dotQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
