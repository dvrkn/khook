package ops

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/dvrkn/khook/internal/spec"
)

func (e *Executor) runDelete(ctx context.Context, step *spec.Step) error {
	op := step.Delete
	if len(op.Manifests) > 0 {
		return e.deleteByManifests(ctx, op)
	}
	return e.deleteByResource(ctx, op)
}

func (e *Executor) deleteByManifests(ctx context.Context, op *spec.DeleteOp) error {
	objs, err := e.loadManifests(ctx, op.Manifests)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		ri, err := e.resourceClient(obj, op.Namespace)
		if err != nil {
			return err
		}
		if err := e.deleteAndWait(ctx, ri, obj.GetName(), describe(obj), op.IgnoreNotFoundOrDefault()); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) deleteByResource(ctx context.Context, op *spec.DeleteOp) error {
	typeArg, name, hasName := strings.Cut(op.Resource, "/")
	resolved, err := e.Clients.ResolveResourceArg(typeArg)
	if err != nil {
		return err
	}

	base := e.Clients.Dynamic.Resource(resolved.GVR)
	var ri dynamic.ResourceInterface = base
	if resolved.Namespaced && !op.AllNamespaces {
		ns := op.Namespace
		if ns == "" {
			ns = "default"
		}
		ri = base.Namespace(ns)
	}

	if hasName {
		return e.deleteAndWait(ctx, ri, name, op.Resource, op.IgnoreNotFoundOrDefault())
	}

	list, err := ri.List(ctx, metav1.ListOptions{
		LabelSelector: op.Selector,
		FieldSelector: op.FieldSelector,
	})
	if err != nil {
		return fmt.Errorf("listing %s: %w", op.Resource, err)
	}
	if len(list.Items) == 0 {
		if op.IgnoreNotFoundOrDefault() {
			e.Log.Info("nothing matched, nothing to delete", "resource", op.Resource, "selector", op.Selector)
			return nil
		}
		return fmt.Errorf("no %s matched selector %q", op.Resource, op.Selector)
	}
	for i := range list.Items {
		item := &list.Items[i]
		target := ri
		if resolved.Namespaced && op.AllNamespaces {
			target = base.Namespace(item.GetNamespace())
		}
		if err := e.deleteAndWait(ctx, target, item.GetName(), describe(item), true); err != nil {
			return err
		}
	}
	return nil
}

// deleteAndWait issues the delete and polls until the object is gone
// (kubectl delete waits by default; bootstrap steps rely on it — e.g.
// removing aws-node before installing a CNI).
func (e *Executor) deleteAndWait(ctx context.Context, ri dynamic.ResourceInterface, name, desc string, ignoreNotFound bool) error {
	err := ri.Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		if ignoreNotFound {
			e.Log.Info("not found, nothing to delete", "resource", desc)
			return nil
		}
		return fmt.Errorf("deleting %s: %w", desc, err)
	}
	if err != nil {
		return fmt.Errorf("deleting %s: %w", desc, err)
	}
	e.Log.Info("delete issued, waiting until gone", "resource", desc)
	return poll(ctx, func(ctx context.Context) (bool, error) {
		_, err := ri.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("waiting for %s to be deleted: %w", desc, err)
		}
		return false, nil
	})
}
