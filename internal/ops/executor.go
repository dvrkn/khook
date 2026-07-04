// Package ops implements the executors for the five step types. Everything
// goes through the Kubernetes and Helm SDKs — khook never shells out.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/dvrkn/khook/internal/kube"
	"github.com/dvrkn/khook/internal/spec"
)

// pollInterval is how often wait-style loops re-check cluster state.
const pollInterval = 2 * time.Second

// Executor runs steps against one cluster.
type Executor struct {
	Clients *kube.Clients
	Log     *slog.Logger
}

func NewExecutor(clients *kube.Clients, log *slog.Logger) *Executor {
	if log == nil {
		log = slog.Default()
	}
	return &Executor{Clients: clients, Log: log}
}

// Execute dispatches a step to its action's executor. The context carries
// the step's timeout.
func (e *Executor) Execute(ctx context.Context, step *spec.Step) error {
	switch {
	case step.Helm != nil:
		return e.runHelm(ctx, step)
	case step.Apply != nil:
		return e.runApply(ctx, step)
	case step.Delete != nil:
		return e.runDelete(ctx, step)
	case step.Wait != nil:
		return e.runWait(ctx, step)
	case step.Rollout != nil:
		return e.runRollout(ctx, step)
	}
	return fmt.Errorf("step %q has no action", step.Name)
}

// scopedClient returns a resource client scoped to the given namespace
// ("default" when empty), unless the resource is cluster-scoped or the op
// spans all namespaces.
func (e *Executor) scopedClient(resolved *kube.ResolvedResource, namespace string, allNamespaces bool) dynamic.ResourceInterface {
	base := e.Clients.Dynamic.Resource(resolved.GVR)
	if !resolved.Namespaced || allNamespaces {
		return base
	}
	if namespace == "" {
		namespace = "default"
	}
	return base.Namespace(namespace)
}

// ensureNamespace creates a namespace if it does not exist.
func (e *Executor) ensureNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := e.Clients.Typed.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating namespace %q: %w", name, err)
	}
	e.Log.Info("namespace created", "namespace", name)
	return nil
}

// poll runs check every pollInterval until it reports done or the context
// expires. It runs one check immediately.
func poll(ctx context.Context, check func(ctx context.Context) (done bool, err error)) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
