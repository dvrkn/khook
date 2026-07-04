package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dvrkn/khook/internal/spec"
	"github.com/dvrkn/khook/internal/state"
)

func statusFixture() *state.Record {
	start := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	return &state.Record{
		APIVersion:   state.RecordAPIVersion,
		SpecName:     "test",
		SpecHash:     "sha256:abc",
		KhookVersion: "1.2.3",
		RunStatus:    state.RunStatusFailed,
		StartedAt:    start,
		UpdatedAt:    start.Add(time.Minute),
		Steps: []state.StepRecord{
			{Name: "a", Type: "apply", Status: "ok", Attempts: 1, DurationMs: 1500},
			{Name: "b", Type: "job", Status: "failed", Attempts: 2, Error: "image pull failed"},
			{Name: "c", Type: "wait", Status: "skipped", SkipReason: `needs "b" which did not succeed`},
		},
	}
}

func statusDoc() *spec.Document {
	return &spec.Document{Steps: []spec.Step{
		{Name: "a", Apply: &spec.ApplyOp{Manifests: []spec.ManifestSource{{Inline: "x"}}}},
		{Name: "b", Job: &spec.JobOp{Image: "img"}},
		{Name: "c", Wait: &spec.WaitOp{On: "pods", For: "condition=Ready"}},
	}}
}

func TestPrintStatusText(t *testing.T) {
	doc := statusDoc()
	var buf strings.Builder
	printStatusText(&buf, doc, "default/khook-state-test", statusFixture(), false, map[string]bool{"a": true})
	out := buf.String()
	for _, want := range []string{
		"spec:    test",
		"secret default/khook-state-test",
		"run:     failed",
		"unchanged since this run",
		"image pull failed",
		`needs "b" which did not succeed`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "will re-run") {
		t.Errorf("resumable step must not be flagged as changed:\n%s", out)
	}

	// Spec changed, step a's inputs unchanged: a resumes, nothing flagged.
	buf.Reset()
	printStatusText(&buf, doc, "default/khook-state-test", statusFixture(), true, map[string]bool{"a": true})
	out = buf.String()
	if !strings.Contains(out, "1 unchanged completed step(s) still resume") {
		t.Errorf("resumable count not reported:\n%s", out)
	}

	// Spec changed and a's inputs with it: a is flagged for re-run.
	buf.Reset()
	printStatusText(&buf, doc, "default/khook-state-test", statusFixture(), true, nil)
	out = buf.String()
	if !strings.Contains(out, "no completed step is unchanged") {
		t.Errorf("all-changed summary absent:\n%s", out)
	}
	if !strings.Contains(out, "input changed — will re-run") {
		t.Errorf("changed completed step not flagged:\n%s", out)
	}

	// A completed step that left the spec is reported as removed, not changed.
	buf.Reset()
	docWithoutA := statusDoc()
	docWithoutA.Steps = docWithoutA.Steps[1:]
	printStatusText(&buf, docWithoutA, "default/khook-state-test", statusFixture(), true, nil)
	if !strings.Contains(buf.String(), "no longer in the spec") {
		t.Errorf("removed step not reported:\n%s", buf.String())
	}

	buf.Reset()
	printStatusText(&buf, doc, "default/khook-state-test", nil, false, nil)
	if !strings.Contains(buf.String(), "no state record") {
		t.Errorf("missing-record message absent:\n%s", buf.String())
	}
}

func TestPrintStatusJSON(t *testing.T) {
	doc := statusDoc()
	var buf strings.Builder
	if err := printStatusJSON(&buf, doc, statusFixture(), true, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	var got statusJSON
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found || !got.SpecChanged || got.Record == nil || got.Record.Steps[1].Error != "image pull failed" {
		t.Fatalf("unexpected JSON: %s", buf.String())
	}
	if len(got.ResumableSteps) != 1 || got.ResumableSteps[0] != "a" {
		t.Fatalf("resumableSteps = %v, want [a]: %s", got.ResumableSteps, buf.String())
	}

	buf.Reset()
	got = statusJSON{}
	if err := printStatusJSON(&buf, doc, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Found || got.Record != nil {
		t.Fatalf("missing record must render found:false, got %s", buf.String())
	}
}
