// Package spec defines the khook DSL (apiVersion khook.io/v1, kind
// Khook), its parser, variable substitution, and validation. docs/dsl.md is
// the normative reference for every field here.
package spec

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	APIVersion = "khook.io/v1"
	Kind       = "Khook"
)

// Duration is a time.Duration that unmarshals from YAML/JSON strings like
// "5m" or "30s".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration %q must not be negative", s)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// OnError values.
const (
	OnErrorFail     = "fail"
	OnErrorContinue = "continue"
)

// Document is the root of a Khook spec.
type Document struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Defaults   Defaults `json:"defaults,omitempty"`
	Steps      []Step   `json:"steps"`
}

type Metadata struct {
	Name string `json:"name"`
}

// Defaults are fallbacks for the per-step fields of the same name.
type Defaults struct {
	Timeout    *Duration `json:"timeout,omitempty"`
	Retries    *int      `json:"retries,omitempty"`
	RetryDelay *Duration `json:"retryDelay,omitempty"`
	OnError    string    `json:"onError,omitempty"`
}

// Built-in fallbacks when defaults: omits a field.
var (
	DefaultTimeout    = 5 * time.Minute
	DefaultRetries    = 0
	DefaultRetryDelay = 10 * time.Second
	DefaultOnError    = OnErrorFail
)

// Step is one node of the DAG. Exactly one action key (Helm, Apply, Delete,
// Wait, Rollout, Job) must be set.
type Step struct {
	Name  string   `json:"name"`
	Needs []string `json:"needs,omitempty"`
	// When is a CEL expression over the merged variables (see when.go);
	// false excludes the step from the run.
	When       string    `json:"when,omitempty"`
	Timeout    *Duration `json:"timeout,omitempty"`
	Retries    *int      `json:"retries,omitempty"`
	RetryDelay *Duration `json:"retryDelay,omitempty"`
	OnError    string    `json:"onError,omitempty"`

	Helm    *HelmOp    `json:"helm,omitempty"`
	Apply   *ApplyOp   `json:"apply,omitempty"`
	Delete  *DeleteOp  `json:"delete,omitempty"`
	Wait    *WaitOp    `json:"wait,omitempty"`
	Rollout *RolloutOp `json:"rollout,omitempty"`
	Job     *JobOp     `json:"job,omitempty"`

	// Excluded records a false When result. Set by Parse, read by the
	// engine and plan; never part of the spec itself.
	Excluded bool `json:"-"`
}

// Type returns the step's action type name ("helm", "apply", ...), or "" if
// no action key is set.
func (s *Step) Type() string {
	switch {
	case s.Helm != nil:
		return "helm"
	case s.Apply != nil:
		return "apply"
	case s.Delete != nil:
		return "delete"
	case s.Wait != nil:
		return "wait"
	case s.Rollout != nil:
		return "rollout"
	case s.Job != nil:
		return "job"
	}
	return ""
}

// actionCount returns how many action keys are set (validation requires 1).
func (s *Step) actionCount() int {
	n := 0
	for _, set := range []bool{s.Helm != nil, s.Apply != nil, s.Delete != nil, s.Wait != nil, s.Rollout != nil, s.Job != nil} {
		if set {
			n++
		}
	}
	return n
}

// EffectiveTimeout resolves step > defaults > built-in.
func (s *Step) EffectiveTimeout(d Defaults) time.Duration {
	if s.Timeout != nil {
		return s.Timeout.Duration
	}
	if d.Timeout != nil {
		return d.Timeout.Duration
	}
	return DefaultTimeout
}

func (s *Step) EffectiveRetries(d Defaults) int {
	if s.Retries != nil {
		return *s.Retries
	}
	if d.Retries != nil {
		return *d.Retries
	}
	return DefaultRetries
}

func (s *Step) EffectiveRetryDelay(d Defaults) time.Duration {
	if s.RetryDelay != nil {
		return s.RetryDelay.Duration
	}
	if d.RetryDelay != nil {
		return d.RetryDelay.Duration
	}
	return DefaultRetryDelay
}

func (s *Step) EffectiveOnError(d Defaults) string {
	if s.OnError != "" {
		return s.OnError
	}
	if d.OnError != "" {
		return d.OnError
	}
	return DefaultOnError
}

// HelmOp installs or upgrades a chart release (the release history decides
// which). The shape of Chart implies its source — bare name in Repo,
// oci:// reference, or local path; ParseChartSource owns that logic.
type HelmOp struct {
	Chart           string         `json:"chart"`
	Repo            string         `json:"repo,omitempty"`
	Version         string         `json:"version,omitempty"`
	Auth            *HelmAuth      `json:"auth,omitempty"`
	Release         string         `json:"release,omitempty"`
	Namespace       string         `json:"namespace,omitempty"`
	CreateNamespace bool           `json:"createNamespace,omitempty"`
	SkipIfInstalled bool           `json:"skipIfInstalled,omitempty"`
	Atomic          bool           `json:"atomic,omitempty"`
	Wait            bool           `json:"wait,omitempty"`
	Values          map[string]any `json:"values,omitempty"`
	ValuesFrom      []ValuesSource `json:"valuesFrom,omitempty"`
}

// HelmAuth is basic-auth credentials for a private chart repository or OCI
// registry — the block alternative to URL userinfo (mutually exclusive).
type HelmAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ReleaseName defaults to the step name.
func (h *HelmOp) ReleaseName(stepName string) string {
	if h.Release != "" {
		return h.Release
	}
	return stepName
}

// TargetNamespace defaults to "default".
func (h *HelmOp) TargetNamespace() string {
	if h.Namespace != "" {
		return h.Namespace
	}
	return "default"
}

// ValuesSource is one entry of valuesFrom: exactly one of File / URL.
type ValuesSource struct {
	File string `json:"file,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (v *ValuesSource) sourceCount() int {
	n := 0
	for _, set := range []bool{v.File != "", v.URL != ""} {
		if set {
			n++
		}
	}
	return n
}

// ManifestSource holds exactly one of Inline / File / URL.
type ManifestSource struct {
	Inline string `json:"inline,omitempty"`
	File   string `json:"file,omitempty"`
	URL    string `json:"url,omitempty"`
}

func (m *ManifestSource) sourceCount() int {
	n := 0
	for _, set := range []bool{m.Inline != "", m.File != "", m.URL != ""} {
		if set {
			n++
		}
	}
	return n
}

// ApplyOp declaratively applies manifests.
type ApplyOp struct {
	Manifests       []ManifestSource `json:"manifests"`
	Namespace       string           `json:"namespace,omitempty"`
	CreateNamespace bool             `json:"createNamespace,omitempty"`
	SkipIfExists    bool             `json:"skipIfExists,omitempty"`
	ServerSide      bool             `json:"serverSide,omitempty"`
}

// DeleteOp removes resources: by manifests, by reference/selector, or —
// the release form — by uninstalling a Helm release.
type DeleteOp struct {
	Manifests []ManifestSource `json:"manifests,omitempty"`

	Resource string `json:"resource,omitempty"`

	Release string `json:"release,omitempty"`

	Namespace      string `json:"namespace,omitempty"`
	AllNamespaces  bool   `json:"allNamespaces,omitempty"`
	Selector       string `json:"selector,omitempty"`
	FieldSelector  string `json:"fieldSelector,omitempty"`
	IgnoreNotFound *bool  `json:"ignoreNotFound,omitempty"`
}

// TargetNamespace defaults to "default" (release form only — the other
// forms treat an empty namespace as "not scoped").
func (d *DeleteOp) TargetNamespace() string {
	if d.Namespace != "" {
		return d.Namespace
	}
	return "default"
}

// IgnoreNotFoundOrDefault defaults to true.
func (d *DeleteOp) IgnoreNotFoundOrDefault() bool {
	if d.IgnoreNotFound != nil {
		return *d.IgnoreNotFound
	}
	return true
}

// WaitOp blocks until a condition holds. The step-level timeout bounds the
// wait.
type WaitOp struct {
	For           string `json:"for"`
	On            string `json:"on"`
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"allNamespaces,omitempty"`
	Selector      string `json:"selector,omitempty"`
}

// RolloutOp runs an imperative rollout command: exactly one of Restart /
// Status.
type RolloutOp struct {
	Restart   string `json:"restart,omitempty"`
	Status    string `json:"status,omitempty"`
	Namespace string `json:"namespace"`
}

// JobOp runs a container image to completion as a batch/v1 Job — the escape
// hatch for anything the DSL does not model. The Job is named after the step;
// each run replaces the previous Job of that name.
type JobOp struct {
	Image           string            `json:"image"`
	Command         []string          `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	Namespace       string            `json:"namespace,omitempty"`
	CreateNamespace bool              `json:"createNamespace,omitempty"`
	ServiceAccount  string            `json:"serviceAccount,omitempty"`
	SkipIfSucceeded bool              `json:"skipIfSucceeded,omitempty"`
}

// TargetNamespace defaults to "default".
func (j *JobOp) TargetNamespace() string {
	if j.Namespace != "" {
		return j.Namespace
	}
	return "default"
}
