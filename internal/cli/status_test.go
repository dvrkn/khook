package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestPrintStatusText(t *testing.T) {
	var buf strings.Builder
	printStatusText(&buf, "default/khook-state-test", statusFixture(), false)
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

	buf.Reset()
	printStatusText(&buf, "default/khook-state-test", statusFixture(), true)
	if !strings.Contains(buf.String(), "changed since this run") {
		t.Errorf("changed spec not reported:\n%s", buf.String())
	}

	buf.Reset()
	printStatusText(&buf, "default/khook-state-test", nil, false)
	if !strings.Contains(buf.String(), "no state record") {
		t.Errorf("missing-record message absent:\n%s", buf.String())
	}
}

func TestPrintStatusJSON(t *testing.T) {
	var buf strings.Builder
	if err := printStatusJSON(&buf, statusFixture(), true); err != nil {
		t.Fatal(err)
	}
	var got statusJSON
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found || !got.SpecChanged || got.Record == nil || got.Record.Steps[1].Error != "image pull failed" {
		t.Fatalf("unexpected JSON: %s", buf.String())
	}

	buf.Reset()
	got = statusJSON{}
	if err := printStatusJSON(&buf, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Found || got.Record != nil {
		t.Fatalf("missing record must render found:false, got %s", buf.String())
	}
}
