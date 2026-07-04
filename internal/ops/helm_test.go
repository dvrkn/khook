package ops

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"

	"github.com/dvrkn/khook/internal/spec"
)

func memoryConfig(t *testing.T) *action.Configuration {
	t.Helper()
	return &action.Configuration{
		Releases:   storage.Init(driver.NewMemory()),
		KubeClient: &kubefake.PrintingKubeClient{Out: io.Discard},
		Log:        func(string, ...any) {},
	}
}

func storedRelease(name string) *release.Release {
	return &release.Release{
		Name:      name,
		Namespace: "default",
		Version:   1,
		Info:      &release.Info{Status: release.StatusDeployed},
		Chart: &chart.Chart{Metadata: &chart.Metadata{
			Name: "test", Version: "0.1.0", APIVersion: chart.APIVersionV2,
		}},
	}
}

func TestReleaseExists(t *testing.T) {
	cfg := memoryConfig(t)
	if err := cfg.Releases.Create(storedRelease("present")); err != nil {
		t.Fatal(err)
	}

	exists, err := releaseExists(cfg, "present")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("want exists=true for stored release")
	}

	exists, err = releaseExists(cfg, "absent")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("want exists=false for unknown release")
	}
}

func TestHelmValuesMergeOrder(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	override := filepath.Join(dir, "override.yaml")
	if err := os.WriteFile(base, []byte("a: 1\nnested:\n  x: base\n  y: base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("nested:\n  x: override\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, _, _ := testExecutor(t)
	op := &spec.HelmOp{
		ValuesFrom: []spec.ValuesSource{{File: base}, {File: override}},
		Values:     map[string]any{"nested": map[string]any{"y": "inline"}},
	}
	got, err := e.helmValues(op)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"a":      1,
		"nested": map[string]any{"x": "override", "y": "inline"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeMapsReplacesNonMapValues(t *testing.T) {
	a := map[string]any{"list": []any{1, 2}, "keep": true}
	b := map[string]any{"list": []any{3}}
	got := mergeMaps(a, b)
	if !reflect.DeepEqual(got["list"], []any{3}) {
		t.Fatalf("lists must be replaced, not merged: %v", got["list"])
	}
	if got["keep"] != true {
		t.Fatal("unrelated keys must survive")
	}
}
