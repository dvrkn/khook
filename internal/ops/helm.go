package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"helm.sh/helm/v4/pkg/action"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	helmcli "helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/kube"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/dvrkn/khook/internal/spec"
)

func (e *Executor) runHelm(ctx context.Context, step *spec.Step) error {
	op := step.Helm
	release := op.ReleaseName(step.Name)
	namespace := op.TargetNamespace()

	cfg, err := e.helmConfig(namespace)
	if err != nil {
		return err
	}

	installed, err := releaseExists(cfg, release)
	if err != nil {
		return fmt.Errorf("checking release %q history: %w", release, err)
	}
	if installed && op.SkipIfInstalled {
		e.Log.Info("release already installed, skipping", "release", release, "namespace", namespace)
		return nil
	}

	if op.CreateNamespace {
		if err := e.ensureNamespace(ctx, namespace); err != nil {
			return err
		}
	}

	chrt, values, pathOpts, err := e.loadChart(op)
	if err != nil {
		return err
	}

	// Helm bounds waiting/rollback with its own timeout; derive it from the
	// step's context so both agree.
	timeout := spec.DefaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	// v4 requires an explicit wait strategy; HookOnly matches v3's
	// wait=false behavior (only hooks are awaited).
	waitStrategy := kube.HookOnlyStrategy
	if op.Wait {
		waitStrategy = kube.StatusWatcherStrategy
	}

	if !installed {
		client := action.NewInstall(cfg)
		client.ReleaseName = release
		client.Namespace = namespace
		client.RollbackOnFailure = op.Atomic
		client.WaitStrategy = waitStrategy
		client.Timeout = timeout
		client.ChartPathOptions = pathOpts
		e.Log.Info("helm install", "release", release, "chart", op.Chart, "version", op.Version, "namespace", namespace)
		rel, err := client.RunWithContext(ctx, chrt, values)
		if err != nil {
			return fmt.Errorf("installing release %q: %w", release, err)
		}
		e.logRelease("helm installed", rel)
		return nil
	}

	client := action.NewUpgrade(cfg)
	client.Namespace = namespace
	client.RollbackOnFailure = op.Atomic
	client.WaitStrategy = waitStrategy
	client.Timeout = timeout
	client.ChartPathOptions = pathOpts
	e.Log.Info("helm upgrade", "release", release, "chart", op.Chart, "version", op.Version, "namespace", namespace)
	rel, err := client.RunWithContext(ctx, release, chrt, values)
	if err != nil {
		return fmt.Errorf("upgrading release %q: %w", release, err)
	}
	e.logRelease("helm upgraded", rel)
	return nil
}

// logRelease logs the outcome of an install/upgrade. Helm v4 actions return
// an opaque release.Releaser; today it is always a *releasev1.Release.
func (e *Executor) logRelease(msg string, rel any) {
	if r, ok := rel.(*releasev1.Release); ok && r != nil {
		e.Log.Info(msg, "release", r.Name, "version", r.Chart.Metadata.Version, "revision", r.Version)
		return
	}
	e.Log.Info(msg)
}

// loadChart locates and loads the step's chart and merges its values —
// everything install, upgrade, and dry-run rendering need.
func (e *Executor) loadChart(op *spec.HelmOp) (*chartv2.Chart, map[string]any, action.ChartPathOptions, error) {
	settings := helmcli.New()
	pathOpts := action.ChartPathOptions{RepoURL: op.Repo, Version: op.Version}
	chartPath, err := pathOpts.LocateChart(op.Chart, settings)
	if err != nil {
		return nil, nil, pathOpts, fmt.Errorf("locating chart %q in %s: %w", op.Chart, op.Repo, err)
	}
	chrt, err := loader.Load(chartPath)
	if err != nil {
		return nil, nil, pathOpts, fmt.Errorf("loading chart %q: %w", op.Chart, err)
	}
	if err := checkChartInstallable(chrt); err != nil {
		return nil, nil, pathOpts, err
	}
	values, err := e.helmValues(op)
	if err != nil {
		return nil, nil, pathOpts, err
	}
	return chrt, values, pathOpts, nil
}

func (e *Executor) helmConfig(namespace string) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	if err := cfg.Init(e.Clients.HelmGetter(namespace), namespace, os.Getenv("HELM_DRIVER")); err != nil {
		return nil, fmt.Errorf("initializing helm: %w", err)
	}
	cfg.SetLogger(e.Log.Handler())
	return cfg, nil
}

// releaseExists reports whether the release has any history (the
// install-vs-upgrade decision, v0-proven).
func releaseExists(cfg *action.Configuration, release string) (bool, error) {
	history := action.NewHistory(cfg)
	history.Max = 1
	_, err := history.Run(release)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// helmValues merges valuesFrom files in order, then inline values on top;
// later sources override earlier ones.
func (e *Executor) helmValues(op *spec.HelmOp) (map[string]any, error) {
	merged := map[string]any{}
	for _, src := range op.ValuesFrom {
		raw, err := os.ReadFile(src.File)
		if err != nil {
			return nil, fmt.Errorf("reading values file: %w", err)
		}
		vals, err := spec.DecodeYAMLMap(raw)
		if err != nil {
			return nil, fmt.Errorf("parsing values file %s: %w", src.File, err)
		}
		merged = mergeMaps(merged, vals)
	}
	if op.Values != nil {
		merged = mergeMaps(merged, op.Values)
	}
	return merged, nil
}

// mergeMaps deep-merges b over a (Helm's values merge semantics).
func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if bMap, ok := v.(map[string]any); ok {
			if aMap, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(aMap, bMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func checkChartInstallable(chrt *chartv2.Chart) error {
	switch chrt.Metadata.Type {
	case "", "application":
		return nil
	}
	return fmt.Errorf("chart %q is of type %q and cannot be installed", chrt.Name(), chrt.Metadata.Type)
}
