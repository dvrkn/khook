package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvrkn/khook/internal/spec"
)

func graphTestDoc() *spec.Document {
	return &spec.Document{
		Metadata: spec.Metadata{Name: "demo"},
		Steps: []spec.Step{
			{Name: "namespaces", Apply: &spec.ApplyOp{}},
			{Name: "cert-manager", Needs: []string{"namespaces"}, Helm: &spec.HelmOp{}},
			{Name: "monitoring", Needs: []string{"namespaces"}, Helm: &spec.HelmOp{}, Excluded: true},
			{Name: "ready", Needs: []string{"cert-manager", "monitoring"}, Wait: &spec.WaitOp{}},
		},
	}
}

func TestMermaidGraph(t *testing.T) {
	want := `flowchart TD
    n0["namespaces (apply)"]
    n1["cert-manager (helm)"]
    n2["monitoring (helm, skipped)"]:::skipped
    n3["ready (wait)"]
    n0 --> n1
    n0 --> n2
    n1 --> n3
    n2 --> n3
    classDef skipped stroke-dasharray:5 5,opacity:0.6
`
	if got := mermaidGraph(graphTestDoc()); got != want {
		t.Errorf("mermaidGraph:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMermaidGraphNoSkipped(t *testing.T) {
	doc := graphTestDoc()
	doc.Steps[2].Excluded = false
	got := mermaidGraph(doc)
	if strings.Contains(got, "classDef") || strings.Contains(got, "skipped") {
		t.Errorf("no excluded steps, but output has skip styling:\n%s", got)
	}
}

func TestDotGraph(t *testing.T) {
	want := `digraph "demo" {
    rankdir=TB;
    node [shape=box];
    "namespaces" [label="namespaces (apply)"];
    "cert-manager" [label="cert-manager (helm)"];
    "monitoring" [label="monitoring (helm, skipped)", style=dashed, color=gray];
    "ready" [label="ready (wait)"];
    "namespaces" -> "cert-manager";
    "namespaces" -> "monitoring";
    "cert-manager" -> "ready";
    "monitoring" -> "ready";
}
`
	if got := dotGraph(graphTestDoc()); got != want {
		t.Errorf("dotGraph:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestDotQuoteEscapes(t *testing.T) {
	if got := dotQuote(`a "b" \c`); got != `"a \"b\" \\c"` {
		t.Errorf("dotQuote: got %s", got)
	}
}

const graphTestSpec = `apiVersion: khook.io/v1
kind: Khook
metadata:
  name: graph-test
steps:
  - name: first
    apply:
      manifests:
        - inline: |
            apiVersion: v1
            kind: Namespace
            metadata:
              name: demo
  - name: second
    needs: [first]
    when: "false"
    wait:
      on: namespace/demo
      for: jsonpath={.status.phase}=Active
`

func runGraph(t *testing.T, args ...string) (string, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(file, []byte(graphTestSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"graph", "-f", file}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestGraphCommandMermaid(t *testing.T) {
	out, err := runGraph(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"flowchart TD",
		`n0["first (apply)"]`,
		`n1["second (wait, skipped)"]:::skipped`,
		"n0 --> n1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestGraphCommandDot(t *testing.T) {
	out, err := runGraph(t, "--format", "dot")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`digraph "graph-test" {`,
		`"first" -> "second";`,
		"style=dashed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestGraphCommandBadFormat(t *testing.T) {
	_, err := runGraph(t, "--format", "png")
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != ExitValidation {
		t.Fatalf("want validation error (exit %d), got %v", ExitValidation, err)
	}
}
