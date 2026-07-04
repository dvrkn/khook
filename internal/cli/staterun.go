package cli

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/spec"
	"github.com/dvrkn/khook/internal/state"
)

// maxStateErrLen caps per-step error text in the record so it stays a small
// object regardless of how verbose an executor failure is.
const maxStateErrLen = 2048

// usablePrior reports whether a prior record can drive a resume: same
// format, same spec hash.
func usablePrior(prior *state.Record, hash string) bool {
	return prior != nil && prior.APIVersion == state.RecordAPIVersion && prior.SpecHash == hash
}

// resumableSteps returns the steps a usable prior record proves complete
// (recorded ok, and still present in the current document).
func resumableSteps(prior *state.Record, hash string, doc *spec.Document) map[string]bool {
	if !usablePrior(prior, hash) {
		return nil
	}
	current := map[string]bool{}
	for i := range doc.Steps {
		current[doc.Steps[i].Name] = true
	}
	m := map[string]bool{}
	for _, sr := range prior.Steps {
		if sr.Status == string(engine.StatusOK) && current[sr.Name] {
			m[sr.Name] = true
		}
	}
	return m
}

// seedRecord builds this run's record: every current step in spec order,
// carrying completed entries over from a usable prior record so a resumed
// run's journal still shows when the carried steps actually ran.
func seedRecord(doc *spec.Document, prior *state.Record, hash string) *state.Record {
	now := time.Now().UTC()
	rec := &state.Record{
		APIVersion:   state.RecordAPIVersion,
		SpecName:     doc.Metadata.Name,
		SpecHash:     hash,
		KhookVersion: Version,
		RunStatus:    state.RunStatusRunning,
		StartedAt:    now,
		UpdatedAt:    now,
	}
	carry := usablePrior(prior, hash)
	for i := range doc.Steps {
		step := &doc.Steps[i]
		if carry {
			if sr := prior.Step(step.Name); sr != nil && sr.Status == string(engine.StatusOK) {
				rec.Steps = append(rec.Steps, *sr)
				continue
			}
		}
		rec.Steps = append(rec.Steps, state.StepRecord{Name: step.Name, Type: step.Type()})
	}
	return rec
}

// stateRecorder persists the run record incrementally: it chains in front of
// the runner's event sink and saves the whole record after every step, so a
// crash mid-run loses at most the in-flight step. EventDone fires
// concurrently within a level, hence the mutex.
type stateRecorder struct {
	mu     sync.Mutex
	store  *state.Store
	rec    *state.Record
	redact *redactor
	log    *slog.Logger
	ctx    context.Context
	next   func(engine.Event) // the progress UI or log sink

	writeErr error // first mid-run save failure; surfaced at run end
}

func (r *stateRecorder) Handle(ev engine.Event) {
	if r.next != nil {
		r.next(ev)
	}
	if ev.Kind != engine.EventDone || ev.Result == nil {
		return
	}
	res := ev.Result
	// A resume-skip re-states what the record already knows; overwriting
	// would turn the carried "ok" into "skipped" and break the next resume.
	if res.SkipReason == engine.SkipReasonPriorRun {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	sr := r.rec.Step(res.Step.Name)
	if sr == nil {
		return
	}
	sr.Status = string(res.Status)
	sr.Attempts = res.Attempts
	sr.DurationMs = res.Duration.Milliseconds()
	sr.SkipReason = res.SkipReason
	sr.FinishedAt = time.Now().UTC()
	sr.Error = ""
	if res.Err != nil {
		msg := r.redact.String(res.Err.Error())
		if len(msg) > maxStateErrLen {
			msg = msg[:maxStateErrLen] + "…(truncated)"
		}
		sr.Error = msg
	}
	r.rec.UpdatedAt = time.Now().UTC()
	if err := r.store.Save(r.ctx, r.rec); err != nil {
		if r.writeErr == nil {
			r.writeErr = err
		}
		r.log.Error("state record write failed; continuing", "secret", r.store.Ref(), "error", err)
	}
}

// finalize stamps the run outcome and writes the record one last time. It
// uses a cancellation-free context so Ctrl-C still persists the journal —
// that is the crash-resume story working for interruption too. Returns the
// first write error of the run, if any.
func (r *stateRecorder) finalize(runErr error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.RunStatus = state.RunStatusOK
	if runErr != nil {
		r.rec.RunStatus = state.RunStatusFailed
	}
	r.rec.UpdatedAt = time.Now().UTC()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 30*time.Second)
	defer cancel()
	if err := r.store.Save(ctx, r.rec); err != nil && r.writeErr == nil {
		r.writeErr = err
	}
	return r.writeErr
}
