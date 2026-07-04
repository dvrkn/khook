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

// Runner executes a validated document.
type Runner struct {
	Defaults spec.Defaults
	Exec     ExecFunc
	Log      *slog.Logger

	// sleep is swapped in tests to avoid real retry delays.
	sleep func(ctx context.Context, d time.Duration) error
}

func NewRunner(defaults spec.Defaults, exec ExecFunc, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{Defaults: defaults, Exec: exec, Log: log, sleep: sleepCtx}
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
				results = append(results, Result{Step: step, Status: StatusSkipped, SkipReason: reason})
				r.Log.Info("step skipped", "step", step.Name, "reason", reason)
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
	for attempt := 1; attempt <= retries+1; attempt++ {
		res.Attempts = attempt
		r.Log.Info("step running", "step", step.Name, "type", step.Type(), "attempt", attempt, "timeout", timeout.String())

		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		err := r.Exec(attemptCtx, step)
		cancel()

		if err == nil {
			res.Status = StatusOK
			res.Duration = time.Since(start)
			r.Log.Info("step ok", "step", step.Name, "duration", res.Duration.Round(time.Millisecond).String())
			return res
		}
		res.Err = err
		r.Log.Error("step attempt failed", "step", step.Name, "attempt", attempt, "error", err)

		if attempt <= retries {
			if sleepErr := r.sleep(ctx, retryDelay); sleepErr != nil {
				break // run canceled while waiting to retry
			}
		}
	}
	res.Status = StatusFailed
	res.Duration = time.Since(start)
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
