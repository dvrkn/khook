package ops

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/jsonpath"

	"github.com/dvrkn/khook/internal/spec"
)

func (e *Executor) runWait(ctx context.Context, step *spec.Step) error {
	op := step.Wait
	typeArg, name, hasName := strings.Cut(op.On, "/")
	resolved, err := e.Clients.ResolveResourceArg(typeArg)
	if err != nil {
		return err
	}

	ri := e.scopedClient(resolved, op.Namespace, op.AllNamespaces)

	wf, err := spec.ParseWaitFor(op.For)
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	if wf.Mode == spec.WaitForDelete {
		return e.waitDeleted(ctx, ri, op, name, hasName)
	}

	check, err := waitChecker(wf)
	if err != nil {
		return err
	}
	e.Log.Info("waiting", "on", op.On, "for", wf)
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
		return allMeet(objs, check)
	})
}

// allMeet reports whether every object passes the checker.
func allMeet(objs []*unstructured.Unstructured, check objectChecker) (bool, error) {
	for _, obj := range objs {
		ok, err := check(obj)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
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
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: op.Selector, FieldSelector: op.FieldSelector})
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", op.On, err)
	}
	objs := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		objs = append(objs, &list.Items[i])
	}
	return objs, nil
}

// objectChecker reports whether one object currently meets a wait target.
type objectChecker func(*unstructured.Unstructured) (bool, error)

// waitChecker builds the per-object check for a parsed wait.for / waitFor
// expression (condition or jsonpath — delete is handled separately).
func waitChecker(wf *spec.WaitFor) (objectChecker, error) {
	if wf.Mode == spec.WaitForCondition {
		return func(obj *unstructured.Unstructured) (bool, error) {
			return hasCondition(obj, wf.Expr, wf.Value)
		}, nil
	}
	// Missing keys mean "not yet", not an error — the field appears once
	// the resource reaches the awaited state.
	jp := jsonpath.New("wait").AllowMissingKeys(true)
	if err := jp.Parse(wf.Expr); err != nil {
		return nil, fmt.Errorf("invalid jsonpath %q: %w", wf.Expr, err)
	}
	return func(obj *unstructured.Unstructured) (bool, error) {
		results, err := jp.FindResults(obj.Object)
		if err != nil {
			return false, fmt.Errorf("%s: evaluating %s: %w", describe(obj), wf.Expr, err)
		}
		// kubectl semantics: no expected value — any result counts; with a
		// value — any result rendering to it counts.
		for _, values := range results {
			for _, v := range values {
				rendered := fmt.Sprintf("%v", v.Interface())
				if wf.Value == "" && rendered != "" {
					return true, nil
				}
				if wf.Value != "" && rendered == wf.Value {
					return true, nil
				}
			}
		}
		return false, nil
	}, nil
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
