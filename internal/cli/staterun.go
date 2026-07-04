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

// resumableSteps returns the steps the prior record proves both complete
// and unchanged: recorded ok, still present in the current document, and
// with a recorded input hash matching the step's current one. The
// comparison is per step — the rest of the spec changing does not stop an
// unchanged step from resuming. A prior entry without an input hash (a
// record written by a khook that predates them) never resumes.
func resumableSteps(prior *state.Record, hashes map[string]string, doc *spec.Document) map[string]bool {
	if prior == nil || prior.APIVersion != state.RecordAPIVersion {
		return nil
	}
	m := map[string]bool{}
	for i := range doc.Steps {
		name := doc.Steps[i].Name
		sr := prior.Step(name)
		if sr != nil && sr.Status == string(engine.StatusOK) &&
			sr.InputHash != "" && sr.InputHash == hashes[name] {
			m[name] = true
		}
	}
	return m
}

// seedRecord builds this run's record: every current step in spec order
// with its current input hash, carrying resumable entries over verbatim so
// a resumed run's journal still shows when the carried steps actually ran.
func seedRecord(doc *spec.Document, prior *state.Record, specHash string, hashes map[string]string, resumable map[string]bool) *state.Record {
	now := time.Now().UTC()
	rec := &state.Record{
		APIVersion:   state.RecordAPIVersion,
		SpecName:     doc.Metadata.Name,
		SpecHash:     specHash,
		KhookVersion: Version,
		RunStatus:    state.RunStatusRunning,
		StartedAt:    now,
		UpdatedAt:    now,
	}
	for i := range doc.Steps {
		step := &doc.Steps[i]
		if resumable[step.Name] {
			rec.Steps = append(rec.Steps, *prior.Step(step.Name))
			continue
		}
		rec.Steps = append(rec.Steps, state.StepRecord{Name: step.Name, Type: step.Type(), InputHash: hashes[step.Name]})
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
