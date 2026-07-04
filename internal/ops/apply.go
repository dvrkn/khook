package ops

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/dvrkn/khook/internal/spec"
)

// fieldManager identifies khook in server-side apply and managed fields.
const fieldManager = "khook"

func (e *Executor) runApply(ctx context.Context, step *spec.Step) error {
	op := step.Apply
	objs, err := e.loadManifests(ctx, op.Manifests)
	if err != nil {
		return err
	}

	if op.CreateNamespace {
		if err := e.ensureNamespace(ctx, op.Namespace); err != nil {
			return err
		}
	}

	if op.SkipIfExists {
		allExist := true
		for _, obj := range objs {
			ri, err := e.resourceClient(obj, op.Namespace)
			if err != nil {
				return err
			}
			if _, err := ri.Get(ctx, obj.GetName(), metav1.GetOptions{}); err != nil {
				if apierrors.IsNotFound(err) {
					allExist = false
					break
				}
				return fmt.Errorf("checking %s: %w", describe(obj), err)
			}
		}
		if allExist {
			e.Log.Info("all resources exist, skipping apply", "step", step.Name)
			return nil
		}
	}

	for _, obj := range objs {
		if err := e.applyObject(ctx, obj, op); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) applyObject(ctx context.Context, obj *unstructured.Unstructured, op *spec.ApplyOp) error {
	ri, err := e.resourceClient(obj, op.Namespace)
	if err != nil {
		return err
	}
	data, err := json.Marshal(obj.Object)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", describe(obj), err)
	}

	if op.ServerSide {
		_, err := ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: fieldManager,
			Force:        ptr(true),
		})
		if err != nil {
			return fmt.Errorf("server-side applying %s: %w", describe(obj), err)
		}
		e.Log.Info("applied (server-side)", "resource", describe(obj))
		return nil
	}

	_, err = ri.Create(ctx, obj, metav1.CreateOptions{FieldManager: fieldManager})
	if err == nil {
		e.Log.Info("created", "resource", describe(obj))
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating %s: %w", describe(obj), err)
	}
	// Exists: converge with a merge patch of the full manifest (idempotent;
	// kubectl-style 3-way apply is roadmap).
	if _, err := ri.Patch(ctx, obj.GetName(), types.MergePatchType, data, metav1.PatchOptions{FieldManager: fieldManager}); err != nil {
		return fmt.Errorf("updating %s: %w", describe(obj), err)
	}
	e.Log.Info("updated", "resource", describe(obj))
	return nil
}

// resourceClient maps an object's GVK to a dynamic resource client, filling
// in the namespace for namespaced objects that omit it.
func (e *Executor) resourceClient(obj *unstructured.Unstructured, defaultNamespace string) (dynamic.ResourceInterface, error) {
	mapping, err := e.mappingFor(obj)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return e.Clients.Dynamic.Resource(mapping.Resource), nil
	}
	ns := obj.GetNamespace()
	if ns == "" {
		ns = defaultNamespace
		if ns == "" {
			ns = "default"
		}
		obj.SetNamespace(ns)
	}
	return e.Clients.Dynamic.Resource(mapping.Resource).Namespace(ns), nil
}

// mappingFor resolves an object's REST mapping, resetting the discovery
// cache once on a miss so CRDs created earlier in the same run resolve.
func (e *Executor) mappingFor(obj *unstructured.Unstructured) (*meta.RESTMapping, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := e.Clients.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) {
		if resettable, ok := e.Clients.Mapper.(meta.ResettableRESTMapper); ok {
			resettable.Reset()
			mapping, err = e.Clients.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("no API resource for %s: %w", gvk, err)
	}
	return mapping, nil
}

func ptr[T any](v T) *T { return &v }
