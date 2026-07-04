package ops

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/dvrkn/khook/internal/spec"
)

// patchContentTypes maps the DSL patch type names to API content types.
var patchContentTypes = map[string]types.PatchType{
	spec.PatchStrategic: types.StrategicMergePatchType,
	spec.PatchMerge:     types.MergePatchType,
	spec.PatchJSON:      types.JSONPatchType,
}

func (e *Executor) runPatch(ctx context.Context, step *spec.Step) error {
	op := step.Patch
	ri, name, err := e.patchClient(op)
	if err != nil {
		return err
	}
	if _, err := e.doPatch(ctx, ri, op, name, false); err != nil {
		return err
	}
	e.Log.Info("patched", "target", op.Target, "type", op.PatchType())
	return nil
}

// doPatch sends the patch, optionally as a server dry-run (for diff), and
// normalizes the two errors that need context: a missing target and a
// strategic patch against a kind that does not support it (CRs).
func (e *Executor) doPatch(ctx context.Context, ri dynamic.ResourceInterface, op *spec.PatchOp, name string, dryRun bool) (*unstructured.Unstructured, error) {
	opts := metav1.PatchOptions{FieldManager: fieldManager}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	obj, err := ri.Patch(ctx, name, patchContentTypes[op.PatchType()], op.Patch, opts)
	switch {
	case err == nil:
		return obj, nil
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("patching %s: not found — the target must exist by the time this step runs", op.Target)
	case apierrors.IsUnsupportedMediaType(err) && op.PatchType() == spec.PatchStrategic:
		return nil, fmt.Errorf("patching %s: %w (custom resources do not support strategic merge — use type: merge)", op.Target, err)
	}
	return nil, fmt.Errorf("patching %s: %w", op.Target, err)
}

// patchClient resolves the target's kind/name into a scoped resource client.
func (e *Executor) patchClient(op *spec.PatchOp) (dynamic.ResourceInterface, string, error) {
	kindArg, name, _ := strings.Cut(op.Target, "/")
	resolved, err := e.Clients.ResolveResourceArg(kindArg)
	if err != nil {
		return nil, "", err
	}
	return e.scopedClient(resolved, op.Namespace, false), name, nil
}
