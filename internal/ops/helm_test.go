package ops

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/action"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/dvrkn/khook/internal/spec"
)

func memoryConfig(t *testing.T) *action.Configuration {
	t.Helper()
	cfg := &action.Configuration{
		Releases:   storage.Init(driver.NewMemory()),
		KubeClient: &kubefake.PrintingKubeClient{Out: io.Discard},
	}
	cfg.SetLogger(slog.DiscardHandler)
	return cfg
}

func storedRelease(name string) *releasev1.Release {
	return &releasev1.Release{
		Name:      name,
		Namespace: "default",
		Version:   1,
		Info:      &releasev1.Info{Status: common.StatusDeployed},
		Chart: &chartv2.Chart{Metadata: &chartv2.Metadata{
			Name: "test", Version: "0.1.0", APIVersion: chartv2.APIVersionV2,
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
	got, err := e.helmValues(t.Context(), op)
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

func TestHelmValuesFromURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/values.yaml":
			fmt.Fprint(w, "a: url\nb: url\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	e, _, _ := testExecutor(t)
	op := &spec.HelmOp{
		ValuesFrom: []spec.ValuesSource{{URL: srv.URL + "/values.yaml"}},
		Values:     map[string]any{"b": "inline"},
	}
	got, err := e.helmValues(t.Context(), op)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "url", "b": "inline"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	op = &spec.HelmOp{ValuesFrom: []spec.ValuesSource{{URL: srv.URL + "/missing.yaml"}}}
	if _, err := e.helmValues(t.Context(), op); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("want HTTP 404 error, got %v", err)
	}
}

func TestLoadChartLocalDirectory(t *testing.T) {
	e, _, _ := testExecutor(t)
	op := &spec.HelmOp{Chart: "./testdata/testchart", Values: map[string]any{"greeting": "hi"}}
	chrt, values, _, err := e.loadChart(t.Context(), memoryConfig(t), op, mustChartSource(t, op))
	if err != nil {
		t.Fatal(err)
	}
	if chrt.Name() != "testchart" {
		t.Fatalf("chart name = %q, want testchart", chrt.Name())
	}
	if values["greeting"] != "hi" {
		t.Fatalf("values not merged: %v", values)
	}
}

func TestLoadChartLocalTarball(t *testing.T) {
	e, _, _ := testExecutor(t)
	tgz, err := chartutil.Save(loadTestChart(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	op := &spec.HelmOp{Chart: tgz}
	chrt, _, _, err := e.loadChart(t.Context(), memoryConfig(t), op, mustChartSource(t, op))
	if err != nil {
		t.Fatal(err)
	}
	if chrt.Name() != "testchart" || chrt.Metadata.Version != "0.1.0" {
		t.Fatalf("got %s-%s, want testchart-0.1.0", chrt.Name(), chrt.Metadata.Version)
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
