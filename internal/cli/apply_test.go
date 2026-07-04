package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/spec"
)

func TestPrintJSONResults(t *testing.T) {
	steps := []spec.Step{
		{Name: "cni", Helm: &spec.HelmOp{}},
		{Name: "crds", Apply: &spec.ApplyOp{}},
		{Name: "argo", Helm: &spec.HelmOp{}, Needs: []string{"crds"}},
	}
	results := []engine.Result{
		{Step: &steps[0], Status: engine.StatusOK, Attempts: 1, Duration: 1500 * time.Millisecond},
		{Step: &steps[1], Status: engine.StatusFailed, Attempts: 2, Duration: 3 * time.Second, Err: errors.New("boom")},
		{Step: &steps[2], Status: engine.StatusSkipped, SkipReason: `needs "crds" which did not succeed`},
	}

	var buf bytes.Buffer
	if err := printJSONResults(&buf, "bootstrap", results, errors.New("1 step(s) failed")); err != nil {
		t.Fatal(err)
	}
	var got runJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	if got.Name != "bootstrap" || got.Status != "failed" {
		t.Errorf("run = %s/%s, want bootstrap/failed", got.Name, got.Status)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(got.Steps))
	}
	ok, failed, skipped := got.Steps[0], got.Steps[1], got.Steps[2]
	if ok.Status != "ok" || ok.Type != "helm" || ok.DurationMs != 1500 {
		t.Errorf("ok step = %+v", ok)
	}
	if failed.Status != "failed" || failed.Error != "boom" || failed.Attempts != 2 {
		t.Errorf("failed step = %+v", failed)
	}
	if skipped.Status != "skipped" || skipped.SkipReason == "" || skipped.Attempts != 0 {
		t.Errorf("skipped step = %+v", skipped)
	}
}

func TestPrintJSONResultsOKStatus(t *testing.T) {
	steps := []spec.Step{{Name: "cni", Helm: &spec.HelmOp{}}}
	results := []engine.Result{{Step: &steps[0], Status: engine.StatusOK, Attempts: 1, Duration: time.Second}}
	var buf bytes.Buffer
	if err := printJSONResults(&buf, "bootstrap", results, nil); err != nil {
		t.Fatal(err)
	}
	var got runJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %s, want ok", got.Status)
	}
}

func TestProgressLifecycle(t *testing.T) {
	steps := []spec.Step{
		{Name: "cni", Helm: &spec.HelmOp{}},
		{Name: "argo", Helm: &spec.HelmOp{}, Needs: []string{"cni"}},
	}
	levels, err := engine.Levels(steps)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	p := newProgress(&buf, levels)
	p.color = false
	p.width = func() int { return 120 }

	p.Start()
	p.Handle(engine.Event{Kind: engine.EventRunning, Step: &steps[0], Attempt: 1, MaxAttempts: 1})
	okRes := engine.Result{Step: &steps[0], Status: engine.StatusOK, Attempts: 1, Duration: time.Second}
	p.Handle(engine.Event{Kind: engine.EventDone, Step: &steps[0], Result: &okRes})
	failRes := engine.Result{Step: &steps[1], Status: engine.StatusFailed, Attempts: 1, Duration: time.Second, Err: errors.New("chart not found")}
	p.Handle(engine.Event{Kind: engine.EventRunning, Step: &steps[1], Attempt: 1, MaxAttempts: 1})
	p.Handle(engine.Event{Kind: engine.EventDone, Step: &steps[1], Result: &failRes})
	p.Stop([]engine.Result{okRes, failRes})

	out := buf.String()
	for _, want := range []string{"✓ cni (helm)", "✗ argo (helm)", "chart not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("x", 50)
	got := truncate(long, 20)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 20 {
		t.Errorf("truncate = %q (%d runes), want 20 runes ending in …", got, len([]rune(got)))
	}
	if truncate("short", 20) != "short" {
		t.Error("short strings must pass through")
	}
	// ANSI escapes are zero-width: a colored short string must not be cut.
	colored := ansiGreen + "ok" + ansiReset
	if truncate(colored, 10) != colored {
		t.Errorf("colored short string was cut: %q", truncate(colored, 10))
	}
}
