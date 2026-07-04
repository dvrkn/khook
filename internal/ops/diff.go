package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"helm.sh/helm/v4/pkg/action"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/kube"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/dvrkn/khook/internal/spec"
)

// Diff renders kubectl-diff-style unified diffs of the objects Execute would
// change: a server-side dry-run per manifest for apply steps, a chart dry-run
// render against the stored release manifest for helm steps. Empty output
// means no changes; step types without rendered objects (delete, wait,
// rollout) always yield "". Like Plan, Diff never mutates the cluster.
func (e *Executor) Diff(ctx context.Context, step *spec.Step) (string, error) {
	switch {
	case step.Helm != nil:
		return e.diffHelm(ctx, step)
	case step.Apply != nil:
		return e.diffApply(ctx, step)
	}
	return "", nil
}

func (e *Executor) diffHelm(ctx context.Context, step *spec.Step) (string, error) {
	op := step.Helm
	src, err := spec.ParseChartSource(op)
	if err != nil {
		return "", err
	}
	cfg, err := e.helmConfig(op.TargetNamespace(), src)
	if err != nil {
		return "", err
	}
	chrt, values, pathOpts, err := e.loadChart(ctx, cfg, op, src)
	if err != nil {
		return "", err
	}
	return diffHelmRelease(ctx, cfg, op, src, op.ReleaseName(step.Name), chrt, values, pathOpts)
}

// diffHelmRelease dry-run renders the target manifest (server dry-run: real
// capabilities and lookups, no mutations, nothing stored) and diffs it
// against the manifest of the release's last revision.
func diffHelmRelease(ctx context.Context, cfg *action.Configuration, op *spec.HelmOp, src *spec.ChartSource, release string, chrt *chartv2.Chart, values map[string]any, pathOpts action.ChartPathOptions) (string, error) {
	namespace := op.TargetNamespace()
	liveManifest := ""
	liveLabel := fmt.Sprintf("live/release %s (not installed)", release)
	installed := true
	last, err := cfg.Releases.Last(release)
	switch {
	case errors.Is(err, driver.ErrReleaseNotFound):
		installed = false
	case err != nil:
		return "", fmt.Errorf("reading release %q history: %w", release, err)
	default:
		rel, ok := last.(*releasev1.Release)
		if !ok {
			return "", fmt.Errorf("unexpected release type %T", last)
		}
		liveManifest = rel.Manifest
		liveLabel = fmt.Sprintf("live/release %s revision %d (%s-%s)",
			release, rel.Version, rel.Chart.Metadata.Name, rel.Chart.Metadata.Version)
	}

	var rendered any
	if installed {
		client := action.NewUpgrade(cfg)
		client.Namespace = namespace
		client.DryRunStrategy = action.DryRunServer
		client.WaitStrategy = kube.HookOnlyStrategy
		client.ChartPathOptions = pathOpts
		rendered, err = client.RunWithContext(ctx, release, chrt, values)
	} else {
		client := action.NewInstall(cfg)
		client.ReleaseName = release
		client.Namespace = namespace
		client.DryRunStrategy = action.DryRunServer
		client.WaitStrategy = kube.HookOnlyStrategy
		client.ChartPathOptions = pathOpts
		rendered, err = client.RunWithContext(ctx, chrt, values)
	}
	if err != nil {
		return "", fmt.Errorf("dry-run rendering %s: %w", src, err)
	}
	rel, ok := rendered.(*releasev1.Release)
	if !ok {
		return "", fmt.Errorf("unexpected dry-run release type %T", rendered)
	}
	return unifiedDiff(liveManifest, rel.Manifest, liveLabel,
		fmt.Sprintf("planned/release %s (%s)", release, src))
}

func (e *Executor) diffApply(ctx context.Context, step *spec.Step) (string, error) {
	op := step.Apply
	objs, err := e.loadManifests(ctx, op.Manifests)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, obj := range objs {
		ri, err := e.resourceClient(obj, op.Namespace)
		if err != nil {
			fmt.Fprintf(&out, "cannot diff %s: %v\n", describe(obj), err)
			continue
		}
		var live *unstructured.Unstructured
		got, err := ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
		switch {
		case err == nil:
			live = got
		case apierrors.IsNotFound(err):
		default:
			return "", fmt.Errorf("reading %s: %w", describe(obj), err)
		}
		planned, err := dryRunApply(ctx, ri, obj, op, live != nil)
		if err != nil {
			fmt.Fprintf(&out, "cannot diff %s: %v\n", describe(obj), err)
			continue
		}
		d, err := objectDiff(live, planned, describe(obj))
		if err != nil {
			return "", err
		}
		out.WriteString(d)
	}
	return out.String(), nil
}

// dryRunApply asks the API server what applying obj would produce, sending
// the same request Execute would (create / merge patch / server-side apply)
// with DryRun set so nothing persists.
func dryRunApply(ctx context.Context, ri dynamic.ResourceInterface, obj *unstructured.Unstructured, op *spec.ApplyOp, exists bool) (*unstructured.Unstructured, error) {
	if !exists && !op.ServerSide {
		return ri.Create(ctx, obj, metav1.CreateOptions{
			FieldManager: fieldManager, DryRun: []string{metav1.DryRunAll},
		})
	}
	data, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", describe(obj), err)
	}
	if op.ServerSide {
		return ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: fieldManager, Force: ptr(true), DryRun: []string{metav1.DryRunAll},
		})
	}
	return ri.Patch(ctx, obj.GetName(), types.MergePatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager, DryRun: []string{metav1.DryRunAll},
	})
}

// objectDiff diffs two objects as sorted-key YAML, hiding managed fields the
// way kubectl diff does. A nil live object means everything is new.
func objectDiff(live, planned *unstructured.Unstructured, id string) (string, error) {
	a := ""
	if live != nil {
		y, err := diffYAML(live)
		if err != nil {
			return "", err
		}
		a = y
	}
	b, err := diffYAML(planned)
	if err != nil {
		return "", err
	}
	return unifiedDiff(a, b, "live/"+id, "planned/"+id)
}

func diffYAML(obj *unstructured.Unstructured) (string, error) {
	o := obj.DeepCopy()
	unstructured.RemoveNestedField(o.Object, "metadata", "managedFields")
	raw, err := sigsyaml.Marshal(o.Object)
	if err != nil {
		return "", fmt.Errorf("encoding %s: %w", describe(obj), err)
	}
	return string(raw), nil
}

// unifiedDiff renders a unified diff with three context lines; "" when the
// sides are identical.
func unifiedDiff(a, b, fromLabel, toLabel string) (string, error) {
	if a == b {
		return "", nil
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(a), B: difflib.SplitLines(b),
		FromFile: fromLabel, ToFile: toLabel, Context: 3,
	})
}
