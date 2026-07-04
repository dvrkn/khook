// Package state persists a run-state record — a journal of one spec's last
// run (spec hash + per-step outcomes) — in an in-cluster Secret, so a re-run
// can resume past steps that already succeeded. The record stores only a
// hash of the spec, never its content: the rendered document can contain
// secret values, and nothing that leaves the process is redacted except
// terminal output.
package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dvrkn/khook/internal/spec"
)

// RecordAPIVersion discriminates the record format; a record with an
// unknown version is ignored (treated as absent) rather than misread.
const RecordAPIVersion = "khook.io/state.v1"

// DataKey is the Secret data key holding the JSON record.
const DataKey = "record.json"

// Run statuses. A crash mid-run leaves "running" behind — it is a marker
// for status output, not a lock.
const (
	RunStatusRunning = "running"
	RunStatusOK      = "ok"
	RunStatusFailed  = "failed"
)

// Record is one spec's run journal.
type Record struct {
	APIVersion   string       `json:"apiVersion"`
	SpecName     string       `json:"specName"`
	SpecHash     string       `json:"specHash"`
	KhookVersion string       `json:"khookVersion"`
	RunStatus    string       `json:"runStatus"`
	StartedAt    time.Time    `json:"startedAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	Steps        []StepRecord `json:"steps"`
}

// StepRecord is one step's outcome, in spec order.
type StepRecord struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Status     string    `json:"status,omitempty"`
	Attempts   int       `json:"attempts,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Error      string    `json:"error,omitempty"`
	SkipReason string    `json:"skipReason,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// Step returns the record entry for name, or nil.
func (r *Record) Step(name string) *StepRecord {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			return &r.Steps[i]
		}
	}
	return nil
}

// SpecHash fingerprints the parsed document. Hashing the canonical JSON
// form (encoding/json sorts map keys) rather than the source bytes means
// cosmetic YAML edits — comments, key order, quoting — do not invalidate
// the record, while any effective change (including rotated secret values
// substituted into the document) forces a fresh run.
func SpecHash(doc *spec.Document) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("hashing spec: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(b)), nil
}
