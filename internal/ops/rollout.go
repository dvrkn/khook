package ops

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dvrkn/khook/internal/spec"
)

// restartedAtAnnotation is the annotation kubectl uses for rollout restart;
// changing it triggers a new rollout of the pod template.
const restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

func (e *Executor) runRollout(ctx context.Context, step *spec.Step) error {
	op := step.Rollout
	if op.Restart != "" {
		kind, name, err := spec.ParseWorkloadRef(op.Restart)
		if err != nil {
			return err
		}
		return e.rolloutRestart(ctx, kind, name, op.Namespace)
	}
	kind, name, err := spec.ParseWorkloadRef(op.Status)
	if err != nil {
		return err
	}
	return e.rolloutStatus(ctx, kind, name, op.Namespace)
}

func (e *Executor) rolloutRestart(ctx context.Context, kind, name, namespace string) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		restartedAtAnnotation, time.Now().UTC().Format(time.RFC3339),
	)
	opts := metav1.PatchOptions{FieldManager: fieldManager}
	apps := e.Clients.Typed.AppsV1()

	var err error
	switch kind {
	case "deployment":
		_, err = apps.Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), opts)
	case "daemonset":
		_, err = apps.DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), opts)
	case "statefulset":
		_, err = apps.StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), opts)
	}
	if err != nil {
		return fmt.Errorf("restarting %s/%s in %s: %w", kind, name, namespace, err)
	}
	e.Log.Info("rollout restarted", "kind", kind, "name", name, "namespace", namespace)
	return nil
}

// rolloutStatus blocks until the workload's rollout is complete (the same
// checks kubectl rollout status performs), bounded by the step timeout.
func (e *Executor) rolloutStatus(ctx context.Context, kind, name, namespace string) error {
	e.Log.Info("waiting for rollout", "kind", kind, "name", name, "namespace", namespace)
	apps := e.Clients.Typed.AppsV1()
	return poll(ctx, func(ctx context.Context) (bool, error) {
		switch kind {
		case "deployment":
			d, err := apps.Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("getting deployment %s/%s: %w", namespace, name, err)
			}
			return deploymentComplete(d), nil
		case "daemonset":
			d, err := apps.DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("getting daemonset %s/%s: %w", namespace, name, err)
			}
			return daemonSetComplete(d), nil
		case "statefulset":
			s, err := apps.StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("getting statefulset %s/%s: %w", namespace, name, err)
			}
			return statefulSetComplete(s), nil
		}
		return false, fmt.Errorf("unsupported rollout kind %q", kind)
	})
}

func deploymentComplete(d *appsv1.Deployment) bool {
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedReplicas == replicas &&
		d.Status.Replicas == replicas &&
		d.Status.AvailableReplicas == replicas
}

func daemonSetComplete(d *appsv1.DaemonSet) bool {
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedNumberScheduled == d.Status.DesiredNumberScheduled &&
		d.Status.NumberAvailable == d.Status.DesiredNumberScheduled
}

func statefulSetComplete(s *appsv1.StatefulSet) bool {
	replicas := int32(1)
	if s.Spec.Replicas != nil {
		replicas = *s.Spec.Replicas
	}
	return s.Status.ObservedGeneration >= s.Generation &&
		s.Status.UpdatedReplicas == replicas &&
		s.Status.ReadyReplicas == replicas &&
		(s.Status.UpdateRevision == "" || s.Status.UpdateRevision == s.Status.CurrentRevision)
}
