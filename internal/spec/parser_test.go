package spec

import (
	"strings"
	"testing"
	"time"
)

const minimalSpec = `
apiVersion: khook.dvrkn.com/v1
kind: Khook
metadata:
  name: test
steps:
  - name: ns
    apply:
      manifests:
        - inline: |
            apiVersion: v1
            kind: Namespace
            metadata:
              name: demo
`

func TestParseMinimal(t *testing.T) {
	doc, err := Parse([]byte(minimalSpec), nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Name != "test" || len(doc.Steps) != 1 || doc.Steps[0].Type() != "apply" {
		t.Fatalf("unexpected doc: %+v", doc)
	}
}

// The DSL has a field literally named "on"; YAML 1.1 parsers turn the
// unquoted key into boolean true. Guard the yaml.v3 choice.
func TestParseWaitOnKeyword(t *testing.T) {
	src := `
apiVersion: khook.dvrkn.com/v1
kind: Khook
metadata:
  name: test
steps:
  - name: ready
    wait:
      for: condition=Ready
      on: pods
      allNamespaces: true
`
	doc, err := Parse([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Steps[0].Wait.On != "pods" {
		t.Fatalf("wait.on = %q, want pods", doc.Steps[0].Wait.On)
	}
}

func TestParseUnknownFieldRejected(t *testing.T) {
	src := strings.Replace(minimalSpec, "metadata:", "bogus: field\nmetadata:", 1)
	if _, err := Parse([]byte(src), nil); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown-field error mentioning bogus, got %v", err)
	}
}

func TestParseDurationsAndDefaults(t *testing.T) {
	src := `
apiVersion: khook.dvrkn.com/v1
kind: Khook
metadata:
  name: test
defaults:
  timeout: 10m
  retries: 3
  onError: continue
steps:
  - name: a
    timeout: 30s
    apply:
      manifests: [{inline: "apiVersion: v1\nkind: Namespace\nmetadata: {name: x}"}]
  - name: b
    apply:
      manifests: [{inline: "apiVersion: v1\nkind: Namespace\nmetadata: {name: y}"}]
`
	doc, err := Parse([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := &doc.Steps[0], &doc.Steps[1]
	if got := a.EffectiveTimeout(doc.Defaults); got != 30*time.Second {
		t.Errorf("a timeout = %v, want 30s", got)
	}
	if got := b.EffectiveTimeout(doc.Defaults); got != 10*time.Minute {
		t.Errorf("b timeout = %v, want 10m (defaults)", got)
	}
	if got := b.EffectiveRetries(doc.Defaults); got != 3 {
		t.Errorf("b retries = %d, want 3", got)
	}
	if got := b.EffectiveOnError(doc.Defaults); got != OnErrorContinue {
		t.Errorf("b onError = %q, want continue", got)
	}
	if got := b.EffectiveRetryDelay(doc.Defaults); got != DefaultRetryDelay {
		t.Errorf("b retryDelay = %v, want built-in %v", got, DefaultRetryDelay)
	}
}

func TestParseInvalidDuration(t *testing.T) {
	src := strings.Replace(minimalSpec, "steps:", "defaults:\n  timeout: fivemin\nsteps:", 1)
	if _, err := Parse([]byte(src), nil); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("want duration error, got %v", err)
	}
}

// Every example in examples/ must stay valid against the implementation.
func TestParseExamples(t *testing.T) {
	vars := map[string]string{
		"NAMESPACE_NAME_TO_CREATE":   "demo",
		"NAMESPACE_NAME_FOR_INGRESS": "ingress",
		"NAMESPACE":                  "prod",
		"APP_NAME":                   "app",
	}
	for _, path := range []string{
		"../../examples/simple.yaml",
		"../../examples/with-variables.yaml",
		"../../examples/multi-app.yaml",
		"../../examples/real-case.yaml",
	} {
		if _, err := ParseFile(path, vars); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}
