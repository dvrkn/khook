package ops

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/dvrkn/khook/internal/spec"
)

func (e *Executor) runWait(ctx context.Context, step *spec.Step) error {
	op := step.Wait
	typeArg, name, hasName := strings.Cut(op.On, "/")
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

	if op.For == "delete" {
		return e.waitDeleted(ctx, ri, op, name, hasName)
	}

	condName, condValue := parseCondition(op.For)
	e.Log.Info("waiting for condition", "on", op.On, "condition", condName, "value", condValue)
	return poll(ctx, func(ctx context.Context) (bool, error) {
		objs, err := waitTargets(ctx, ri, op, name, hasName)
		if err != nil {
			return false, err
		}
		// No matches yet: keep polling — during bootstrap the resources
		// this step waits on are often still being created.
		if len(objs) == 0 {
			return false, nil
		}
		for _, obj := range objs {
			ok, err := hasCondition(obj, condName, condValue)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	})
}

func (e *Executor) waitDeleted(ctx context.Context, ri dynamic.ResourceInterface, op *spec.WaitOp, name string, hasName bool) error {
	e.Log.Info("waiting for deletion", "on", op.On)
	return poll(ctx, func(ctx context.Context) (bool, error) {
		objs, err := waitTargets(ctx, ri, op, name, hasName)
		if err != nil {
			return false, err
		}
		return len(objs) == 0, nil
	})
}

// waitTargets fetches the current match set: a single named object (absent →
// empty set) or a filtered list.
func waitTargets(ctx context.Context, ri dynamic.ResourceInterface, op *spec.WaitOp, name string, hasName bool) ([]*unstructured.Unstructured, error) {
	if hasName {
		obj, err := ri.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("getting %s: %w", op.On, err)
		}
		return []*unstructured.Unstructured{obj}, nil
	}
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: op.Selector})
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", op.On, err)
	}
	objs := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		objs = append(objs, &list.Items[i])
	}
	return objs, nil
}

// parseCondition splits "condition=Ready" / "condition=Ready=False" into
// name and expected status value (default "True").
func parseCondition(forExpr string) (name, value string) {
	expr := strings.TrimPrefix(forExpr, "condition=")
	name, value, ok := strings.Cut(expr, "=")
	if !ok {
		value = "True"
	}
	return name, value
}

// hasCondition checks status.conditions for type==name with status==value
// (case-insensitive on the value, matching kubectl wait).
func hasCondition(obj *unstructured.Unstructured, name, value string) (bool, error) {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		return false, fmt.Errorf("%s: reading status.conditions: %w", describe(obj), err)
	}
	if !found {
		return false, nil
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		condStatus, _ := cond["status"].(string)
		if condType == name {
			return strings.EqualFold(condStatus, value), nil
		}
	}
	return false, nil
}
