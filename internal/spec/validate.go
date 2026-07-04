package spec

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var stepNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

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
			addf("%s: needs exactly one action key (helm, apply, delete, wait, rollout), got none", where)
		case 1:
			if err := validateAction(step); err != nil {
				addf("%s: %v", where, err)
			}
		default:
			addf("%s: needs exactly one action key (helm, apply, delete, wait, rollout), got %d", where, step.actionCount())
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
	case step.Wait != nil:
		return validateWait(step.Wait)
	case step.Rollout != nil:
		return validateRollout(step.Rollout)
	}
	return nil
}

func validateHelm(op *HelmOp) error {
	if op.Chart == "" {
		return errors.New("helm: chart is required")
	}
	if op.Repo == "" {
		return errors.New("helm: repo is required")
	}
	if !strings.HasPrefix(op.Repo, "http://") && !strings.HasPrefix(op.Repo, "https://") {
		return fmt.Errorf("helm: repo must be an HTTP(S) URL, got %q", op.Repo)
	}
	for i, src := range op.ValuesFrom {
		if src.File == "" {
			return fmt.Errorf("helm: valuesFrom[%d] must set file", i)
		}
	}
	return nil
}

func validateManifests(prefix string, manifests []ManifestSource) error {
	for i := range manifests {
		if manifests[i].sourceCount() != 1 {
			return fmt.Errorf("%s: manifests[%d] must set exactly one of inline, file, url", prefix, i)
		}
	}
	return nil
}

func validateApply(op *ApplyOp) error {
	if len(op.Manifests) == 0 {
		return errors.New("apply: manifests must contain at least one source")
	}
	if op.CreateNamespace && op.Namespace == "" {
		return errors.New("apply: createNamespace requires namespace")
	}
	return validateManifests("apply", op.Manifests)
}

func validateDelete(op *DeleteOp) error {
	byManifests := len(op.Manifests) > 0
	byResource := op.Resource != ""
	if byManifests == byResource {
		return errors.New("delete: exactly one of manifests or resource is required")
	}
	if byManifests {
		if op.Selector != "" || op.FieldSelector != "" || op.AllNamespaces {
			return errors.New("delete: selector, fieldSelector, and allNamespaces apply only to the resource form")
		}
		return validateManifests("delete", op.Manifests)
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
	if op.For != "delete" && !strings.HasPrefix(op.For, "condition=") {
		return fmt.Errorf("wait: for must be \"condition=<Name>[=<value>]\" or \"delete\", got %q", op.For)
	}
	if op.For == "condition=" {
		return errors.New("wait: condition name is empty")
	}
	if op.Namespace != "" && op.AllNamespaces {
		return errors.New("wait: namespace and allNamespaces are mutually exclusive")
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
