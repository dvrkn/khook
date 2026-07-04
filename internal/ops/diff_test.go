package ops

import (
	"context"
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dvrkn/khook/internal/spec"
)

// diffConfig is a memoryConfig whose capabilities do not require a cluster,
// so dry-run rendering works in tests.
func diffConfig(t *testing.T) *action.Configuration {
	t.Helper()
	cfg := memoryConfig(t)
	cfg.Capabilities = common.DefaultCapabilities
	return cfg
}

func loadTestChart(t *testing.T) *chartv2.Chart {
	t.Helper()
	chrt, err := loader.Load("testdata/testchart")
	if err != nil {
		t.Fatal(err)
	}
	return chrt
}

func TestDiffHelmInstallRendersAllNew(t *testing.T) {
	cfg := diffConfig(t)
	op := &spec.HelmOp{Chart: "testchart", Repo: "https://charts.example.com", Version: "0.1.0"}
	src, err := spec.ParseChartSource(op)
	if err != nil {
		t.Fatal(err)
	}
	got, err := diffHelmRelease(context.Background(), cfg, op, src, "web", loadTestChart(t), nil, action.ChartPathOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--- live/release web (not installed)",
		"+++ planned/release web (testchart@0.1.0)",
		`+  greeting: "hello"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n-  ") {
		t.Fatalf("install diff must have no removals:\n%s", got)
	}
}

func TestDiffHelmUpgradeShowsManifestChange(t *testing.T) {
	cfg := diffConfig(t)
	rel := storedRelease("web")
	rel.Manifest = "---\n# Source: testchart/templates/configmap.yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web-cm\n  namespace: default\ndata:\n  greeting: \"old\"\n"
	if err := cfg.Releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	op := &spec.HelmOp{Chart: "testchart", Repo: "https://charts.example.com"}
	src, err := spec.ParseChartSource(op)
	if err != nil {
		t.Fatal(err)
	}
	got, err := diffHelmRelease(context.Background(), cfg, op, src, "web", loadTestChart(t),
		map[string]any{"greeting": "new"}, action.ChartPathOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--- live/release web revision 1 (test-0.1.0)",
		"+++ planned/release web (testchart@latest)",
		`-  greeting: "old"`,
		`+  greeting: "new"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
}

func TestDiffApplyCreate(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	got, err := e.Diff(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--- live/configmap/demo@target",
		"+++ planned/configmap/demo@target",
		"+  key: value",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
}

func TestDiffApplyUpdate(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
		"data":       map[string]any{"key": "old"},
	}}
	e, _, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	got, err := e.Diff(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-  key: old", "+  key: value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff missing %q:\n%s", want, got)
		}
	}
}

func TestDiffApplyNoChanges(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
		"data":       map[string]any{"key": "value"},
	}}
	e, _, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	got, err := e.Diff(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("want empty diff for identical object, got:\n%s", got)
	}
}

func TestDiffApplyUnknownKindDegrades(t *testing.T) {
	e, _, _ := testExecutor(t)
	crd := "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: w}\n"
	step := applyStep("w", &spec.ApplyOp{Manifests: []spec.ManifestSource{{Inline: crd}}})
	got, err := e.Diff(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "cannot diff widget/w") {
		t.Fatalf("want degrade note, got:\n%s", got)
	}
}

func TestDiffStepTypesWithoutObjects(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/victim", Namespace: "ns1"})
	got, err := e.Diff(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("delete steps have no rendered objects, got:\n%s", got)
	}
}
