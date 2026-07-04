package spec

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var stepNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// RFC 1123: a label (namespace names) and a subdomain (Secret names).
var (
	rfc1123LabelPattern     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	rfc1123SubdomainPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// workloadKinds are the kinds rollout: accepts, normalized to lowercase
// singular.
var workloadKinds = map[string]bool{
	"deployment":  true,
	"daemonset":   true,
	"statefulset": true,
}

// ValidationError aggregates every problem found in a document so users fix
// a spec in one pass.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "invalid spec: " + e.Problems[0]
	}
	return fmt.Sprintf("invalid spec (%d problems):\n  - %s", len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// Validate checks a parsed Document against docs/dsl.md. It does not detect
// dependency cycles — the engine's topological sort owns that.
func Validate(doc *Document) error {
	var problems []string
	addf := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if doc.APIVersion != APIVersion {
		addf("apiVersion must be %q, got %q", APIVersion, doc.APIVersion)
	}
	if doc.Kind != Kind {
		addf("kind must be %q, got %q", Kind, doc.Kind)
	}
	if doc.Metadata.Name == "" {
		addf("metadata.name is required")
	}
	if err := validateOnError(doc.Defaults.OnError); err != nil {
		addf("defaults: %v", err)
	}
	for _, p := range validateState(doc) {
		addf("state: %s", p)
	}
	if len(doc.Steps) == 0 {
		addf("steps must contain at least one step")
	}

	names := map[string]bool{}
	for i := range doc.Steps {
		step := &doc.Steps[i]
		where := fmt.Sprintf("steps[%d]", i)
		if step.Name != "" {
			where = fmt.Sprintf("step %q", step.Name)
		}

		switch {
		case step.Name == "":
			addf("%s: name is required", where)
		case !stepNamePattern.MatchString(step.Name):
			addf("%s: name must match %s", where, stepNamePattern)
		case names[step.Name]:
			addf("%s: duplicate step name", where)
		default:
			names[step.Name] = true
		}

		if err := validateOnError(step.OnError); err != nil {
			addf("%s: %v", where, err)
		}
		if step.When != "" {
			if err := CheckWhen(step.When); err != nil {
				addf("%s: when: %v", where, err)
			}
		}
		if step.Retries != nil && *step.Retries < 0 {
			addf("%s: retries must not be negative", where)
		}

		switch step.actionCount() {
		case 0:
			addf("%s: needs exactly one action key (helm, apply, delete, patch, wait, rollout, job), got none", where)
		case 1:
			if err := validateAction(step); err != nil {
				addf("%s: %v", where, err)
			}
		default:
			addf("%s: needs exactly one action key (helm, apply, delete, patch, wait, rollout, job), got %d", where, step.actionCount())
		}
	}

	// needs references — checked after all names are known.
	for i := range doc.Steps {
		step := &doc.Steps[i]
		for _, need := range step.Needs {
			if !names[need] {
				addf("step %q: needs unknown step %q", step.Name, need)
			}
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// validateState checks the state: block. The effective values are checked so
// a derived Secret name (khook-state-<metadata.name>) is caught here rather
// than at apply time.
func validateState(doc *Document) []string {
	s := doc.State
	if s == nil {
		return nil
	}
	var problems []string
	if ns := s.TargetNamespace(); !rfc1123LabelPattern.MatchString(ns) || len(ns) > 63 {
		problems = append(problems, fmt.Sprintf("namespace must be a valid namespace name (RFC 1123 label), got %q", ns))
	}
	name := s.SecretName(doc.Metadata.Name)
	if !rfc1123SubdomainPattern.MatchString(name) || len(name) > 253 {
		if s.Name == "" {
			problems = append(problems, fmt.Sprintf("metadata.name derives an invalid Secret name %q — set state.name explicitly", name))
		} else {
			problems = append(problems, fmt.Sprintf("name must be a valid Secret name (RFC 1123 subdomain), got %q", name))
		}
	}
	return problems
}

func validateOnError(v string) error {
	if v != "" && v != OnErrorFail && v != OnErrorContinue {
		return fmt.Errorf("onError must be %q or %q, got %q", OnErrorFail, OnErrorContinue, v)
	}
	return nil
}

func validateAction(step *Step) error {
	switch {
	case step.Helm != nil:
		return validateHelm(step.Helm)
	case step.Apply != nil:
		return validateApply(step.Apply)
	case step.Delete != nil:
		return validateDelete(step.Delete)
	case step.Patch != nil:
		return validatePatch(step.Patch)
	case step.Wait != nil:
		return validateWait(step.Wait)
	case step.Rollout != nil:
		return validateRollout(step.Rollout)
	case step.Job != nil:
		return validateJob(step.Job)
	}
	return nil
}

// validateSkipIf checks skipIf against the one predicate the step type
// accepts.
func validateSkipIf(prefix, got, allowed string) error {
	if got != "" && got != allowed {
		return fmt.Errorf("%s: skipIf must be %q, got %q", prefix, allowed, got)
	}
	return nil
}

func validateHelm(op *HelmOp) error {
	// ParseChartSource owns every chart/repo/version/auth rule.
	if _, err := ParseChartSource(op); err != nil {
		return err
	}
	if err := validateSkipIf("helm", op.SkipIf, SkipIfInstalled); err != nil {
		return err
	}
	for i, src := range op.ValuesFrom {
		if src.sourceCount() != 1 {
			return fmt.Errorf("helm: valuesFrom[%d] must set exactly one of file, url", i)
		}
		if src.URL != "" && !strings.HasPrefix(src.URL, "http://") && !strings.HasPrefix(src.URL, "https://") {
			return fmt.Errorf("helm: valuesFrom[%d] url must be HTTP(S), got %q", i, src.URL)
		}
	}
	return nil
}

func validateManifests(prefix string, manifests []ManifestSource) error {
	for i := range manifests {
		if manifests[i].sourceCount() != 1 {
			return fmt.Errorf("%s: manifests[%d] must set exactly one of inline, file, url, kustomize", prefix, i)
		}
		if k := manifests[i].Kustomize; k != "" && !isLocalPath(k) {
			return fmt.Errorf("%s: manifests[%d] kustomize must be a local path (./, ../, or /), got %q — remote kustomizations are not supported", prefix, i, k)
		}
	}
	return nil
}

// isLocalPath mirrors isChartPath: only explicit path shapes count.
func isLocalPath(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/")
}

func validateApply(op *ApplyOp) error {
	if len(op.Manifests) == 0 {
		return errors.New("apply: manifests must contain at least one source")
	}
	if err := validateSkipIf("apply", op.SkipIf, SkipIfExists); err != nil {
		return err
	}
	if op.CreateNamespace && op.Namespace == "" {
		return errors.New("apply: createNamespace requires namespace")
	}
	if op.WaitFor != "" {
		wf, err := ParseWaitFor(op.WaitFor)
		if err != nil {
			return fmt.Errorf("apply: waitFor: %w", err)
		}
		if wf.Mode == WaitForDelete {
			return errors.New("apply: waitFor cannot be \"delete\" — use a delete: or wait: step")
		}
	}
	return validateManifests("apply", op.Manifests)
}

func validateDelete(op *DeleteOp) error {
	forms := 0
	for _, set := range []bool{len(op.Manifests) > 0, op.Resource != "", op.Release != ""} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return errors.New("delete: exactly one of manifests, resource, or release is required")
	}
	switch {
	case len(op.Manifests) > 0:
		if op.Selector != "" || op.FieldSelector != "" || op.AllNamespaces {
			return errors.New("delete: selector, fieldSelector, and allNamespaces apply only to the resource form")
		}
		return validateManifests("delete", op.Manifests)
	case op.Release != "":
		if op.Selector != "" || op.FieldSelector != "" || op.AllNamespaces {
			return errors.New("delete: selector, fieldSelector, and allNamespaces apply only to the resource form")
		}
		return nil
	}
	if op.Namespace != "" && op.AllNamespaces {
		return errors.New("delete: namespace and allNamespaces are mutually exclusive")
	}
	if strings.Contains(op.Resource, "/") {
		if op.Selector != "" || op.FieldSelector != "" {
			return errors.New("delete: selectors cannot be combined with a kind/name resource")
		}
	}
	return nil
}

func validateWait(op *WaitOp) error {
	if op.On == "" {
		return errors.New("wait: on is required")
	}
	if _, err := ParseWaitFor(op.For); err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	if op.Namespace != "" && op.AllNamespaces {
		return errors.New("wait: namespace and allNamespaces are mutually exclusive")
	}
	return nil
}

func validatePatch(op *PatchOp) error {
	kind, name, ok := strings.Cut(op.Target, "/")
	if !ok || kind == "" || name == "" {
		return fmt.Errorf("patch: target must be <kind>/<name>, got %q", op.Target)
	}
	switch op.Type {
	case "", PatchStrategic, PatchMerge, PatchJSON:
	default:
		return fmt.Errorf("patch: type must be %q, %q, or %q, got %q", PatchStrategic, PatchMerge, PatchJSON, op.Type)
	}
	if len(op.Patch) == 0 {
		return errors.New("patch: patch body is required")
	}
	body := strings.TrimSpace(string(op.Patch))
	if op.PatchType() == PatchJSON {
		if !strings.HasPrefix(body, "[") {
			return errors.New("patch: type json requires a list of operations (op/path/value)")
		}
	} else if !strings.HasPrefix(body, "{") {
		return fmt.Errorf("patch: a %s patch must be a mapping", op.PatchType())
	}
	return nil
}

func validateRollout(op *RolloutOp) error {
	if (op.Restart == "") == (op.Status == "") {
		return errors.New("rollout: exactly one of restart or status is required")
	}
	if op.Namespace == "" {
		return errors.New("rollout: namespace is required")
	}
	ref := op.Restart
	if ref == "" {
		ref = op.Status
	}
	if _, _, err := ParseWorkloadRef(ref); err != nil {
		return fmt.Errorf("rollout: %w", err)
	}
	return nil
}

func validateJob(op *JobOp) error {
	if op.Image == "" {
		return errors.New("job: image is required")
	}
	if err := validateSkipIf("job", op.SkipIf, SkipIfSucceeded); err != nil {
		return err
	}
	for key := range op.Env {
		if key == "" {
			return errors.New("job: env keys must not be empty")
		}
	}
	return nil
}

// ParseWorkloadRef parses "deployment/name" into a normalized lowercase kind
// and a name, restricted to the workload kinds rollout: supports.
func ParseWorkloadRef(ref string) (kind, name string, err error) {
	kind, name, ok := strings.Cut(ref, "/")
	kind = strings.ToLower(kind)
	if !ok || name == "" || !workloadKinds[kind] {
		return "", "", fmt.Errorf("expected deployment/<name>, daemonset/<name>, or statefulset/<name>, got %q", ref)
	}
	return kind, name, nil
}
