package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/spec"
	"github.com/dvrkn/khook/internal/state"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func stateTestDoc() *spec.Document {
	return &spec.Document{
		APIVersion: spec.APIVersion,
		Kind:       spec.Kind,
		Metadata:   spec.Metadata{Name: "test"},
		State:      &spec.StateSpec{},
		Steps: []spec.Step{
			{Name: "a", Apply: &spec.ApplyOp{Manifests: []spec.ManifestSource{{Inline: "x"}}}},
			{Name: "b", Wait: &spec.WaitOp{On: "pods", For: "condition=Ready"}},
		},
	}
}

func priorRecord(hash string) *state.Record {
	return &state.Record{
		APIVersion: state.RecordAPIVersion,
		SpecName:   "test",
		SpecHash:   hash,
		RunStatus:  state.RunStatusFailed,
		Steps: []state.StepRecord{
			{Name: "a", Type: "apply", Status: "ok", Attempts: 1},
			{Name: "b", Type: "wait", Status: "failed"},
		},
	}
}

func TestSeedRecordAndResume(t *testing.T) {
	doc := stateTestDoc()
	hash, err := state.SpecHash(doc)
	if err != nil {
		t.Fatal(err)
	}

	// Hash match: recorded-ok steps resume and carry over.
	prior := priorRecord(hash)
	skip := resumableSteps(prior, hash, doc)
	if !skip["a"] || skip["b"] {
		t.Fatalf("skip = %v, want only a", skip)
	}
	rec := seedRecord(doc, prior, hash)
	if rec.RunStatus != state.RunStatusRunning || len(rec.Steps) != 2 {
		t.Fatalf("seed = %+v", rec)
	}
	if got := rec.Step("a"); got.Status != "ok" || got.Attempts != 1 {
		t.Fatalf("carried a = %+v, want prior ok entry", got)
	}
	if got := rec.Step("b"); got.Status != "" {
		t.Fatalf("b = %+v, want fresh entry (prior failure not carried)", got)
	}

	// Hash mismatch: nothing resumes, nothing carries.
	skip = resumableSteps(prior, "sha256:other", doc)
	if len(skip) != 0 {
		t.Fatalf("hash mismatch must not resume, got %v", skip)
	}
	rec = seedRecord(doc, prior, "sha256:other")
	if got := rec.Step("a"); got.Status != "" {
		t.Fatalf("hash mismatch must not carry entries, got %+v", got)
	}

	// Prior step no longer in the spec: dropped, not resumed.
	prior.Steps = append(prior.Steps, state.StepRecord{Name: "gone", Status: "ok"})
	skip = resumableSteps(prior, hash, doc)
	if skip["gone"] {
		t.Fatal("removed steps must not resume")
	}
	if seedRecord(doc, prior, hash).Step("gone") != nil {
		t.Fatal("removed steps must not be carried into the new record")
	}
}

func newTestRecorder(t *testing.T, doc *spec.Document, prior *state.Record, hash string) (*stateRecorder, *state.Store) {
	t.Helper()
	store := state.NewStore(k8sfake.NewClientset(), "default", "khook-state-test")
	rec := seedRecord(doc, prior, hash)
	if err := store.Save(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	red := &redactor{}
	red.Add("hunter2")
	return &stateRecorder{
		store:  store,
		rec:    rec,
		redact: red,
		log:    discardLogger(),
		ctx:    context.Background(),
	}, store
}

func doneEvent(step *spec.Step, res engine.Result) engine.Event {
	res.Step = step
	return engine.Event{Kind: engine.EventDone, Step: step, Result: &res}
}

func TestStateRecorderHandle(t *testing.T) {
	doc := stateTestDoc()
	hash, _ := state.SpecHash(doc)
	prior := priorRecord(hash)
	recorder, store := newTestRecorder(t, doc, prior, hash)

	forwarded := 0
	recorder.next = func(engine.Event) { forwarded++ }

	// Resume-skip of "a" must forward but not clobber the carried ok entry.
	recorder.Handle(doneEvent(&doc.Steps[0], engine.Result{
		Status: engine.StatusSkipped, SkipReason: engine.SkipReasonPriorRun,
	}))
	// "b" fails with a secret in the error; it must be redacted + recorded.
	recorder.Handle(doneEvent(&doc.Steps[1], engine.Result{
		Status: engine.StatusFailed, Attempts: 2, Duration: 3 * time.Second,
		Err: errors.New("wait failed: token hunter2 rejected"),
	}))

	if forwarded != 2 {
		t.Fatalf("forwarded = %d, want 2", forwarded)
	}
	stored, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Step("a"); got.Status != "ok" || got.SkipReason != "" {
		t.Fatalf("a = %+v, want carried ok entry untouched", got)
	}
	b := stored.Step("b")
	if b.Status != "failed" || b.Attempts != 2 || b.DurationMs != 3000 {
		t.Fatalf("b = %+v", b)
	}
	if strings.Contains(b.Error, "hunter2") || !strings.Contains(b.Error, "***") {
		t.Fatalf("b.Error = %q, want secret redacted", b.Error)
	}

	if err := recorder.finalize(errors.New("1 step(s) failed")); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.Load(context.Background())
	if stored.RunStatus != state.RunStatusFailed {
		t.Fatalf("runStatus = %q, want failed", stored.RunStatus)
	}
}

func TestStateRecorderErrorTruncated(t *testing.T) {
	doc := stateTestDoc()
	hash, _ := state.SpecHash(doc)
	recorder, store := newTestRecorder(t, doc, nil, hash)

	recorder.Handle(doneEvent(&doc.Steps[0], engine.Result{
		Status: engine.StatusFailed,
		Err:    errors.New(strings.Repeat("x", 3*maxStateErrLen)),
	}))
	stored, _ := store.Load(context.Background())
	if got := stored.Step("a").Error; len(got) > maxStateErrLen+32 || !strings.HasSuffix(got, "(truncated)") {
		t.Fatalf("error not truncated: len=%d", len(got))
	}
}

func TestStateRecorderStickyWriteError(t *testing.T) {
	doc := stateTestDoc()
	hash, _ := state.SpecHash(doc)
	recorder, store := newTestRecorder(t, doc, nil, hash)

	// Make every further write fail by stripping the ownership label.
	ctx := context.Background()
	sec, err := store.Client.CoreV1().Secrets("default").Get(ctx, "khook-state-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sec.Labels = nil
	if _, err := store.Client.CoreV1().Secrets("default").Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	recorder.Handle(doneEvent(&doc.Steps[0], engine.Result{Status: engine.StatusOK, Attempts: 1}))
	if recorder.writeErr == nil {
		t.Fatal("mid-run write failure must stick")
	}
	if err := recorder.finalize(nil); err == nil || !strings.Contains(err.Error(), "not managed by khook") {
		t.Fatalf("finalize must surface the sticky error, got %v", err)
	}
}
