package ops

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dvrkn/khook/internal/spec"
)

// DestroySkipReason explains why destroy has nothing to do for a step type,
// or "" for the types it reverses (helm, apply, job). The CLI skips these
// steps up front; they still satisfy the teardown ordering.
func DestroySkipReason(step *spec.Step) string {
	switch {
	case step.Helm != nil, step.Apply != nil, step.Job != nil:
		return ""
	case step.Delete != nil:
		return "not reversed: khook does not restore deleted resources"
	case step.Patch != nil:
		return "not reversed: khook does not revert patches"
	default: // wait, rollout
		return fmt.Sprintf("nothing to tear down for a %s step", step.Type())
	}
}

// Destroy removes what a step created — the inverse of Execute for the step
// types that create something. Absent resources are success (a teardown is
// idempotent); namespaces created via createNamespace are left in place.
func (e *Executor) Destroy(ctx context.Context, step *spec.Step) error {
	switch {
	case step.Helm != nil:
		return e.destroyHelm(ctx, step)
	case step.Apply != nil:
		return e.destroyApply(ctx, step)
	case step.Job != nil:
		return e.destroyJob(ctx, step)
	}
	return nil
}

// destroyHelm uninstalls the step's release — delete:'s release form with
// the release name and namespace the helm step resolves to.
func (e *Executor) destroyHelm(ctx context.Context, step *spec.Step) error {
	op := step.Helm
	return e.runHelmUninstall(ctx, &spec.DeleteOp{
		Release:   op.ReleaseName(step.Name),
		Namespace: op.Namespace,
	})
}

// destroyApply deletes the objects the step's manifests describe, in reverse
// manifest order (dependents were applied last, so they go first).
func (e *Executor) destroyApply(ctx context.Context, step *spec.Step) error {
	op := step.Apply
	objs, err := e.loadManifests(ctx, op.Manifests)
	if err != nil {
		return err
	}
	for i := len(objs) - 1; i >= 0; i-- {
		obj := objs[i]
		ri, err := e.resourceClient(obj, op.Namespace)
		if err != nil {
			return err
		}
		if err := e.deleteAndWait(ctx, ri, obj.GetName(), describe(obj), true); err != nil {
			return err
		}
	}
	return nil
}

// destroyJob deletes the step's Job. Like every khook Job mutation it
// refuses to touch a Job it does not own.
func (e *Executor) destroyJob(ctx context.Context, step *spec.Step) error {
	namespace := step.Job.TargetNamespace()
	existing, err := e.Clients.Typed.BatchV1().Jobs(namespace).Get(ctx, step.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		e.Log.Info("job not found, nothing to delete", "job", step.Name, "namespace", namespace)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting job %q: %w", step.Name, err)
	}
	if existing.Labels[managedByLabelKey] != managedByLabelValue {
		return fmt.Errorf("job %q in namespace %q exists but is not managed by khook; refusing to delete it", step.Name, namespace)
	}
	e.Log.Info("deleting job", "job", step.Name, "namespace", namespace)
	return e.deleteJobAndWait(ctx, namespace, step.Name)
}
