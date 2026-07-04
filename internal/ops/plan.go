package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"helm.sh/helm/v4/pkg/action"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dvrkn/khook/internal/spec"
)

// Action is the effect plan predicts a step will have on the cluster.
type Action string

const (
	ActionInstall   Action = "install"   // helm: release has no history
	ActionUpgrade   Action = "upgrade"   // helm: release exists
	ActionCreate    Action = "create"    // apply: every object is new
	ActionConfigure Action = "configure" // apply: at least one object already exists
	ActionDelete    Action = "delete"    // delete: matching resources exist
	ActionRestart   Action = "restart"   // rollout restart
	ActionRun       Action = "run"       // job: creates (or replaces) the Job and runs it
	ActionWait      Action = "wait"      // wait / rollout status: condition not met yet
	ActionSkip      Action = "skip"      // a skipIf* field or a false when: short-circuits the step
	ActionNone      Action = "no-op"     // nothing to do (already absent / already satisfied)
	ActionUnknown   Action = "unknown"   // cannot assess against the current cluster
)

// Assessment is plan's prediction for one step.
type Assessment struct {
	Action Action
	Detail string
}

// Plan predicts what Execute would do for a step without mutating the
// cluster. Problems assessing a step (resource kinds that an earlier step
// will create, unreadable manifests) degrade to ActionUnknown rather than
// failing the plan.
func (e *Executor) Plan(ctx context.Context, step *spec.Step) Assessment {
	switch {
	case step.Helm != nil:
		return e.planHelm(ctx, step)
	case step.Apply != nil:
		return e.planApply(ctx, step)
	case step.Delete != nil:
		return e.planDelete(ctx, step)
	case step.Wait != nil:
		return e.planWait(ctx, step)
	case step.Rollout != nil:
		return e.planRollout(ctx, step)
	case step.Job != nil:
		return e.planJob(ctx, step)
	}
	return Assessment{Action: ActionUnknown, Detail: "step has no action"}
}

func unknown(err error) Assessment {
	return Assessment{Action: ActionUnknown, Detail: err.Error()}
}

func (e *Executor) planHelm(ctx context.Context, step *spec.Step) Assessment {
	op := step.Helm
	release := op.ReleaseName(step.Name)
	namespace := op.TargetNamespace()

	cfg, err := e.helmConfig(namespace)
	if err != nil {
		return unknown(err)
	}
	a := planHelmRelease(cfg, op, release)
	if a.Action == ActionInstall {
		if note := e.namespaceNote(ctx, namespace, op.CreateNamespace); note != "" {
			a.Detail += "; " + note
		}
	}
	return a
}

// planHelmRelease decides install/upgrade/skip from the release history —
// the same check runHelm makes before choosing an action.
func planHelmRelease(cfg *action.Configuration, op *spec.HelmOp, release string) Assessment {
	last, err := cfg.Releases.Last(release)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return Assessment{ActionInstall, fmt.Sprintf("release %q not found; installs %s", release, chartRef(op))}
	}
	if err != nil {
		return unknown(fmt.Errorf("reading release %q history: %w", release, err))
	}
	current := fmt.Sprintf("release %q exists", release)
	if rel, ok := last.(*releasev1.Release); ok {
		current = fmt.Sprintf("revision %d (%s-%s, %s)",
			rel.Version, rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, rel.Info.Status)
	}
	if op.SkipIfInstalled {
		return Assessment{ActionSkip, "skipIfInstalled: " + current}
	}
	return Assessment{ActionUpgrade, fmt.Sprintf("%s -> %s", current, chartRef(op))}
}

// chartRef renders "chart@version" with the spec's target version.
func chartRef(op *spec.HelmOp) string {
	version := op.Version
	if version == "" {
		version = "latest"
	}
	return op.Chart + "@" + version
}

func (e *Executor) planApply(ctx context.Context, step *spec.Step) Assessment {
	op := step.Apply
	objs, err := e.loadManifests(ctx, op.Manifests)
	if err != nil {
		return unknown(err)
	}

	var creates, updates, unresolved []string
	for _, obj := range objs {
		ri, err := e.resourceClient(obj, op.Namespace)
		if err != nil {
			unresolved = append(unresolved, describe(obj))
			continue
		}
		_, err = ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
		switch {
		case err == nil:
			updates = append(updates, describe(obj))
		case apierrors.IsNotFound(err):
			creates = append(creates, describe(obj))
		default:
			unresolved = append(unresolved, describe(obj))
		}
	}

	if op.SkipIfExists && len(creates) == 0 && len(unresolved) == 0 {
		return Assessment{ActionSkip, fmt.Sprintf("skipIfExists: all %d resource(s) exist", len(updates))}
	}

	var parts []string
	if len(creates) > 0 {
		parts = append(parts, "creates "+listResources(creates))
	}
	if len(updates) > 0 {
		parts = append(parts, "updates "+listResources(updates))
	}
	if len(unresolved) > 0 {
		parts = append(parts, "cannot assess "+listResources(unresolved)+" (kind may arrive in an earlier step)")
	}
	if op.CreateNamespace && op.Namespace != "" {
		if note := e.namespaceNote(ctx, op.Namespace, true); note != "" {
			parts = append(parts, note)
		}
	}

	action := ActionConfigure
	switch {
	case len(creates) == 0 && len(updates) == 0:
		action = ActionUnknown
	case len(updates) == 0:
		action = ActionCreate
	}
	return Assessment{action, strings.Join(parts, "; ")}
}

func (e *Executor) planDelete(ctx context.Context, step *spec.Step) Assessment {
	op := step.Delete
	if len(op.Manifests) > 0 {
		return e.planDeleteByManifests(ctx, op)
	}

	typeArg, name, hasName := strings.Cut(op.Resource, "/")
	resolved, err := e.Clients.ResolveResourceArg(typeArg)
	if err != nil {
		return unknown(fmt.Errorf("unknown resource type %q (kind may arrive in an earlier step)", typeArg))
	}
	ri := e.scopedClient(resolved, op.Namespace, op.AllNamespaces)

	if hasName {
		_, err := ri.Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			return Assessment{ActionDelete, "deletes " + op.Resource}
		case apierrors.IsNotFound(err):
			if op.IgnoreNotFoundOrDefault() {
				return Assessment{ActionNone, op.Resource + " already absent"}
			}
			return Assessment{ActionUnknown, op.Resource + " not found — step fails unless an earlier step creates it (ignoreNotFound: false)"}
		default:
			return unknown(fmt.Errorf("checking %s: %w", op.Resource, err))
		}
	}

	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: op.Selector, FieldSelector: op.FieldSelector})
	if err != nil {
		return unknown(fmt.Errorf("listing %s: %w", op.Resource, err))
	}
	if len(list.Items) == 0 {
		detail := "nothing matches"
		if op.Selector != "" {
			detail += " selector " + op.Selector
		}
		if op.IgnoreNotFoundOrDefault() {
			return Assessment{ActionNone, detail}
		}
		return Assessment{ActionUnknown, detail + " — step fails unless an earlier step creates a match (ignoreNotFound: false)"}
	}
	return Assessment{ActionDelete, fmt.Sprintf("deletes %d matching object(s)", len(list.Items))}
}

func (e *Executor) planDeleteByManifests(ctx context.Context, op *spec.DeleteOp) Assessment {
	objs, err := e.loadManifests(ctx, op.Manifests)
	if err != nil {
		return unknown(err)
	}
	var present []string
	absent := 0
	for _, obj := range objs {
		ri, err := e.resourceClient(obj, op.Namespace)
		if err != nil {
			absent++
			continue
		}
		if _, err := ri.Get(ctx, obj.GetName(), metav1.GetOptions{}); err != nil {
			absent++
			continue
		}
		present = append(present, describe(obj))
	}
	if len(present) == 0 {
		return Assessment{ActionNone, fmt.Sprintf("all %d resource(s) already absent", absent)}
	}
	detail := "deletes " + listResources(present)
	if absent > 0 {
		detail += fmt.Sprintf("; %d already absent", absent)
	}
	return Assessment{ActionDelete, detail}
}

func (e *Executor) planWait(ctx context.Context, step *spec.Step) Assessment {
	op := step.Wait
	typeArg, name, hasName := strings.Cut(op.On, "/")
	resolved, err := e.Clients.ResolveResourceArg(typeArg)
	if err != nil {
		return Assessment{ActionWait, fmt.Sprintf("resource type %q not on the cluster yet; waits for %s", typeArg, op.For)}
	}
	ri := e.scopedClient(resolved, op.Namespace, op.AllNamespaces)
	objs, err := waitTargets(ctx, ri, op, name, hasName)
	if err != nil {
		return unknown(err)
	}

	if op.For == "delete" {
		if len(objs) == 0 {
			return Assessment{ActionNone, op.On + " already absent"}
		}
		return Assessment{ActionWait, fmt.Sprintf("%d object(s) still present", len(objs))}
	}

	if len(objs) == 0 {
		return Assessment{ActionWait, fmt.Sprintf("no matches for %s yet; waits for %s", op.On, op.For)}
	}
	condName, condValue := parseCondition(op.For)
	met := 0
	for _, obj := range objs {
		ok, err := hasCondition(obj, condName, condValue)
		if err != nil {
			return unknown(err)
		}
		if ok {
			met++
		}
	}
	if met == len(objs) {
		return Assessment{ActionNone, fmt.Sprintf("%s already holds on all %d object(s)", op.For, met)}
	}
	return Assessment{ActionWait, fmt.Sprintf("%d/%d object(s) meet %s", met, len(objs), op.For)}
}

func (e *Executor) planRollout(ctx context.Context, step *spec.Step) Assessment {
	op := step.Rollout
	if op.Restart != "" {
		kind, name, err := spec.ParseWorkloadRef(op.Restart)
		if err != nil {
			return unknown(err)
		}
		found, _, err := e.workloadStatus(ctx, kind, name, op.Namespace)
		if err != nil {
			return unknown(err)
		}
		if !found {
			return Assessment{ActionUnknown, op.Restart + " not found — must exist by the time this step runs"}
		}
		return Assessment{ActionRestart, "restarts " + op.Restart}
	}

	kind, name, err := spec.ParseWorkloadRef(op.Status)
	if err != nil {
		return unknown(err)
	}
	found, complete, err := e.workloadStatus(ctx, kind, name, op.Namespace)
	if err != nil {
		return unknown(err)
	}
	switch {
	case !found:
		return Assessment{ActionWait, op.Status + " not found yet; waits for its rollout"}
	case complete:
		return Assessment{ActionNone, op.Status + " rollout already complete"}
	}
	return Assessment{ActionWait, op.Status + " rollout in progress"}
}

// workloadStatus fetches a rollout target and reports whether it exists and
// whether its rollout is currently complete.
func (e *Executor) workloadStatus(ctx context.Context, kind, name, namespace string) (found, complete bool, err error) {
	apps := e.Clients.Typed.AppsV1()
	switch kind {
	case "deployment":
		d, err := apps.Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, false, ignoreNotFound(err, kind, name, namespace)
		}
		return true, deploymentComplete(d), nil
	case "daemonset":
		d, err := apps.DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, false, ignoreNotFound(err, kind, name, namespace)
		}
		return true, daemonSetComplete(d), nil
	case "statefulset":
		s, err := apps.StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, false, ignoreNotFound(err, kind, name, namespace)
		}
		return true, statefulSetComplete(s), nil
	}
	return false, false, fmt.Errorf("unsupported rollout kind %q", kind)
}

func ignoreNotFound(err error, kind, name, namespace string) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("getting %s/%s in %s: %w", kind, name, namespace, err)
}

// namespaceNote reports what happens to a target namespace that does not
// exist yet; empty when the namespace exists (or its state can't be read).
func (e *Executor) namespaceNote(ctx context.Context, namespace string, create bool) string {
	_, err := e.Clients.Typed.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return ""
	}
	if create {
		return fmt.Sprintf("creates namespace %q", namespace)
	}
	return fmt.Sprintf("warning: namespace %q does not exist", namespace)
}

// listResources joins resource descriptions, eliding after the first three.
func listResources(items []string) string {
	const max = 3
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(items[:max], ", "), len(items)-max)
}
