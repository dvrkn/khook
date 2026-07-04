package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dvrkn/khook/internal/spec"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder is a fake ExecFunc failing the named steps.
type recorder struct {
	mu       sync.Mutex
	executed []string
	fail     map[string]int // step -> number of times to fail before succeeding (-1: always)
}

func (r *recorder) exec(ctx context.Context, step *spec.Step) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executed = append(r.executed, step.Name)
	remaining, ok := r.fail[step.Name]
	if !ok || remaining == 0 {
		return nil
	}
	if remaining > 0 {
		r.fail[step.Name] = remaining - 1
	}
	return fmt.Errorf("step %s failed", step.Name)
}

func newTestRunner(defaults spec.Defaults, exec ExecFunc) *Runner {
	r := NewRunner(defaults, exec, discardLogger())
	r.sleep = func(ctx context.Context, d time.Duration) error { return nil }
	return r
}

func resultsByName(results []Result) map[string]Result {
	m := map[string]Result{}
	for _, res := range results {
		m[res.Step.Name] = res
	}
	return m
}

func TestRunAllOK(t *testing.T) {
	rec := &recorder{}
	r := newTestRunner(spec.Defaults{}, rec.exec)
	results, err := r.Run(context.Background(), steps(
		step("top"),
		step("left", "top"),
		step("right", "top"),
		step("bottom", "left", "right"),
	))
	if err != nil {
		t.Fatal(err)
	}
	for name, res := range resultsByName(results) {
		if res.Status != StatusOK {
			t.Errorf("%s = %s, want ok", name, res.Status)
		}
	}
	if len(rec.executed) != 4 {
		t.Fatalf("executed %v, want 4 steps", rec.executed)
	}
}

func TestRunOnErrorFailSkipsEverythingNotYetRun(t *testing.T) {
	rec := &recorder{fail: map[string]int{"left": -1}}
	r := newTestRunner(spec.Defaults{}, rec.exec)
	results, err := r.Run(context.Background(), steps(
		step("top"),
		step("left", "top"),
		step("right", "top"),
		step("after-right", "right"),
		step("bottom", "left", "right"),
	))
	if err == nil {
		t.Fatal("want run error")
	}
	byName := resultsByName(results)
	if byName["left"].Status != StatusFailed {
		t.Errorf("left = %s, want failed", byName["left"].Status)
	}
	// Same level as the failure: already started, finishes normally.
	if byName["right"].Status != StatusOK {
		t.Errorf("right = %s, want ok", byName["right"].Status)
	}
	// Everything not yet started is skipped — even with satisfied needs.
	for _, name := range []string{"after-right", "bottom"} {
		if byName[name].Status != StatusSkipped {
			t.Errorf("%s = %s, want skipped", name, byName[name].Status)
		}
	}
}

func TestRunOnErrorContinue(t *testing.T) {
	rec := &recorder{fail: map[string]int{"left": -1}}
	defaults := spec.Defaults{OnError: spec.OnErrorContinue}
	r := newTestRunner(defaults, rec.exec)
	results, err := r.Run(context.Background(), steps(
		step("top"),
		step("left", "top"),
		step("right", "top"),
		step("after-right", "right"),
		step("bottom", "left", "right"),
	))
	if err == nil {
		t.Fatal("want run error (a step still failed)")
	}
	byName := resultsByName(results)
	// Unrelated branch keeps going.
	if byName["after-right"].Status != StatusOK {
		t.Errorf("after-right = %s, want ok", byName["after-right"].Status)
	}
	// Dependents of the failed step are skipped.
	if byName["bottom"].Status != StatusSkipped {
		t.Errorf("bottom = %s, want skipped", byName["bottom"].Status)
	}
}

func TestRunWhenExcludedStepSkipsButSatisfiesNeeds(t *testing.T) {
	rec := &recorder{}
	excluded := step("optional")
	excluded.When = `vars.FLAG == "true"`
	excluded.Excluded = true
	r := newTestRunner(spec.Defaults{}, rec.exec)
	results, err := r.Run(context.Background(), steps(
		step("top"),
		excluded,
		step("dependent", "optional"),
	))
	if err != nil {
		t.Fatal(err)
	}
	byName := resultsByName(results)
	if byName["optional"].Status != StatusSkipped {
		t.Errorf("optional = %s, want skipped", byName["optional"].Status)
	}
	if got := byName["optional"].SkipReason; !strings.Contains(got, "when") {
		t.Errorf("skip reason = %q, want the when condition mentioned", got)
	}
	// The exclusion is deliberate: needs stays satisfied, dependents run.
	if byName["dependent"].Status != StatusOK {
		t.Errorf("dependent = %s, want ok", byName["dependent"].Status)
	}
	if len(rec.executed) != 2 {
		t.Fatalf("executed %v, want top and dependent only", rec.executed)
	}
}

func TestRunRetriesUntilSuccess(t *testing.T) {
	rec := &recorder{fail: map[string]int{"flaky": 2}}
	retries := 3
	s := step("flaky")
	s.Retries = &retries
	r := newTestRunner(spec.Defaults{}, rec.exec)
	results, err := r.Run(context.Background(), steps(s))
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != StatusOK || results[0].Attempts != 3 {
		t.Fatalf("got status=%s attempts=%d, want ok after 3 attempts", results[0].Status, results[0].Attempts)
	}
}

func TestRunRetriesExhausted(t *testing.T) {
	rec := &recorder{fail: map[string]int{"flaky": -1}}
	retries := 2
	s := step("flaky")
	s.Retries = &retries
	r := newTestRunner(spec.Defaults{}, rec.exec)
	results, err := r.Run(context.Background(), steps(s))
	if err == nil {
		t.Fatal("want run error")
	}
	if results[0].Status != StatusFailed || results[0].Attempts != 3 {
		t.Fatalf("got status=%s attempts=%d, want failed after 3 attempts", results[0].Status, results[0].Attempts)
	}
}

func TestRunStepTimeout(t *testing.T) {
	exec := func(ctx context.Context, s *spec.Step) error {
		<-ctx.Done()
		return ctx.Err()
	}
	timeout := spec.Duration{Duration: 20 * time.Millisecond}
	s := step("slow")
	s.Timeout = &timeout
	r := newTestRunner(spec.Defaults{}, exec)
	results, err := r.Run(context.Background(), steps(s))
	if err == nil {
		t.Fatal("want run error")
	}
	if results[0].Status != StatusFailed || !errors.Is(results[0].Err, context.DeadlineExceeded) {
		t.Fatalf("got %s / %v, want failed with deadline exceeded", results[0].Status, results[0].Err)
	}
}

func TestRunCanceledContextSkipsSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &recorder{}
	r := newTestRunner(spec.Defaults{}, rec.exec)
	results, err := r.Run(ctx, steps(step("a"), step("b", "a")))
	if err != nil {
		t.Fatalf("canceled run should report skips, not fail: %v", err)
	}
	for _, res := range results {
		if res.Status != StatusSkipped {
			t.Errorf("%s = %s, want skipped", res.Step.Name, res.Status)
		}
	}
	if len(rec.executed) != 0 {
		t.Fatalf("executed %v, want none", rec.executed)
	}
}

func TestRunEmitsEvents(t *testing.T) {
	rec := &recorder{fail: map[string]int{"flaky": 1, "broken": -1}}
	retries := 1
	flaky := step("flaky")
	flaky.Retries = &retries
	broken := step("broken")
	dependent := step("dependent", "broken")

	var mu sync.Mutex
	events := map[string][]EventKind{}
	r := newTestRunner(spec.Defaults{OnError: spec.OnErrorContinue}, rec.exec)
	r.OnEvent = func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		events[ev.Step.Name] = append(events[ev.Step.Name], ev.Kind)
		if ev.Kind == EventDone && ev.Result == nil {
			t.Errorf("done event for %s has no result", ev.Step.Name)
		}
	}
	if _, err := r.Run(context.Background(), steps(flaky, broken, dependent)); err == nil {
		t.Fatal("want run error")
	}

	want := map[string][]EventKind{
		// fails once, retried, succeeds
		"flaky": {EventRunning, EventAttemptFailed, EventRunning, EventDone},
		// no retries configured: one attempt, failed
		"broken": {EventRunning, EventAttemptFailed, EventDone},
		// skipped: done only
		"dependent": {EventDone},
	}
	for name, kinds := range want {
		got := events[name]
		if fmt.Sprint(got) != fmt.Sprint(kinds) {
			t.Errorf("%s events = %v, want %v", name, got, kinds)
		}
	}
}

func TestRunLevelParallelism(t *testing.T) {
	var mu sync.Mutex
	running := 0
	maxRunning := 0
	exec := func(ctx context.Context, s *spec.Step) error {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	r := newTestRunner(spec.Defaults{}, exec)
	if _, err := r.Run(context.Background(), steps(step("a"), step("b"), step("c"))); err != nil {
		t.Fatal(err)
	}
	if maxRunning < 2 {
		t.Fatalf("max concurrent = %d, want level to run in parallel", maxRunning)
	}
}
