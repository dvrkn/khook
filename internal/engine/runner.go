package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dvrkn/khook/internal/spec"
)

// Status is a step's final outcome.
type Status string

const (
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Result is one step's outcome.
type Result struct {
	Step     *spec.Step
	Status   Status
	Err      error
	Attempts int
	Duration time.Duration
	// SkipReason explains a StatusSkipped result.
	SkipReason string
}

// ExecFunc executes a single step. The context carries the per-attempt
// timeout.
type ExecFunc func(ctx context.Context, step *spec.Step) error

// EventKind classifies a step lifecycle notification.
type EventKind string

const (
	// EventRunning fires when a step attempt starts.
	EventRunning EventKind = "running"
	// EventAttemptFailed fires when an attempt fails; Attempt < MaxAttempts
	// means the step will be retried.
	EventAttemptFailed EventKind = "attempt-failed"
	// EventDone fires once per step with its final Result.
	EventDone EventKind = "done"
)

// Event is a step lifecycle notification. Steps in the same level run in
// parallel, so the sink is called concurrently and must be safe for that.
type Event struct {
	Kind        EventKind
	Step        *spec.Step
	Attempt     int // running / attempt-failed
	MaxAttempts int
	Err         error   // attempt-failed
	Result      *Result // done
}

// Runner executes a validated document.
type Runner struct {
	Defaults spec.Defaults
	Exec     ExecFunc
	Log      *slog.Logger
	// OnEvent receives step lifecycle events; defaults to LogEvents(Log).
	OnEvent func(Event)

	// sleep is swapped in tests to avoid real retry delays.
	sleep func(ctx context.Context, d time.Duration) error
}

func NewRunner(defaults spec.Defaults, exec ExecFunc, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{Defaults: defaults, Exec: exec, Log: log, OnEvent: LogEvents(log), sleep: sleepCtx}
}

// LogEvents is the default event sink: one structured log line per state
// change.
func LogEvents(log *slog.Logger) func(Event) {
	return func(ev Event) {
		switch ev.Kind {
		case EventRunning:
			log.Info("step running", "step", ev.Step.Name, "type", ev.Step.Type(), "attempt", ev.Attempt)
		case EventAttemptFailed:
			log.Error("step attempt failed", "step", ev.Step.Name, "attempt", ev.Attempt, "error", ev.Err)
		case EventDone:
			switch ev.Result.Status {
			case StatusOK:
				log.Info("step ok", "step", ev.Step.Name, "duration", ev.Result.Duration.Round(time.Millisecond).String())
			case StatusSkipped:
				log.Info("step skipped", "step", ev.Step.Name, "reason", ev.Result.SkipReason)
			}
		}
	}
}

func (r *Runner) notify(ev Event) {
	if r.OnEvent != nil {
		r.OnEvent(ev)
	}
}

// Run executes steps in DAG levels. Within a level steps run in parallel. A
// step runs only if every step it needs succeeded. When a step fails with
// onError=fail, steps already running finish, nothing new starts, and every
// not-yet-run step is reported skipped. The returned error is non-nil if any
// step failed; results always cover every step.
func (r *Runner) Run(ctx context.Context, steps []spec.Step) ([]Result, error) {
	levels, err := Levels(steps)
	if err != nil {
		return nil, err
	}

	succeeded := map[string]bool{}
	var results []Result
	stopped := false // a step failed with onError=fail

	for _, level := range levels {
		var runnable []*spec.Step
		for _, step := range level {
			if reason := r.skipReason(ctx, step, succeeded, stopped); reason != "" {
				res := Result{Step: step, Status: StatusSkipped, SkipReason: reason}
				results = append(results, res)
				r.notify(Event{Kind: EventDone, Step: step, Result: &res})
				continue
			}
			runnable = append(runnable, step)
		}

		levelResults := make([]Result, len(runnable))
		var wg sync.WaitGroup
		for i, step := range runnable {
			wg.Add(1)
			go func() {
				defer wg.Done()
				levelResults[i] = r.runStep(ctx, step)
			}()
		}
		wg.Wait()

		for _, res := range levelResults {
			results = append(results, res)
			switch res.Status {
			case StatusOK:
				succeeded[res.Step.Name] = true
			case StatusFailed:
				if res.Step.EffectiveOnError(r.Defaults) == spec.OnErrorFail {
					stopped = true
				}
			}
		}
	}

	failed := 0
	for _, res := range results {
		if res.Status == StatusFailed {
			failed++
		}
	}
	if failed > 0 {
		return results, fmt.Errorf("%d step(s) failed", failed)
	}
	return results, nil
}

func (r *Runner) skipReason(ctx context.Context, step *spec.Step, succeeded map[string]bool, stopped bool) string {
	if ctx.Err() != nil {
		return "run canceled"
	}
	if stopped {
		return "a previous step failed with onError=fail"
	}
	for _, need := range step.Needs {
		if !succeeded[need] {
			return fmt.Sprintf("needs %q which did not succeed", need)
		}
	}
	return ""
}

// runStep executes one step with retries; each attempt is bounded by the
// step's effective timeout.
func (r *Runner) runStep(ctx context.Context, step *spec.Step) Result {
	timeout := step.EffectiveTimeout(r.Defaults)
	retries := step.EffectiveRetries(r.Defaults)
	retryDelay := step.EffectiveRetryDelay(r.Defaults)

	start := time.Now()
	res := Result{Step: step}
	maxAttempts := retries + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res.Attempts = attempt
		r.notify(Event{Kind: EventRunning, Step: step, Attempt: attempt, MaxAttempts: maxAttempts})

		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		err := r.Exec(attemptCtx, step)
		cancel()

		if err == nil {
			res.Status = StatusOK
			res.Duration = time.Since(start)
			r.notify(Event{Kind: EventDone, Step: step, Result: &res})
			return res
		}
		res.Err = err
		r.notify(Event{Kind: EventAttemptFailed, Step: step, Attempt: attempt, MaxAttempts: maxAttempts, Err: err})

		if attempt <= retries {
			if sleepErr := r.sleep(ctx, retryDelay); sleepErr != nil {
				break // run canceled while waiting to retry
			}
		}
	}
	res.Status = StatusFailed
	res.Duration = time.Since(start)
	r.notify(Event{Kind: EventDone, Step: step, Result: &res})
	return res
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
