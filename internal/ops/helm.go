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
	"helm.sh/helm/v4/pkg/registry"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/dvrkn/khook/internal/spec"
)

func (e *Executor) runHelm(ctx context.Context, step *spec.Step) error {
	op := step.Helm
	src, err := spec.ParseChartSource(op)
	if err != nil {
		return err
	}
	release := op.ReleaseName(step.Name)
	namespace := op.TargetNamespace()

	cfg, err := e.helmConfig(namespace, src)
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

	chrt, values, pathOpts, err := e.loadChart(ctx, cfg, op, src)
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
		e.Log.Info("helm install", "release", release, "chart", src.String(), "namespace", namespace)
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
	e.Log.Info("helm upgrade", "release", release, "chart", src.String(), "namespace", namespace)
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
// everything install, upgrade, and dry-run rendering need. The returned
// path options carry repo, version, credentials, and (for oci://) the
// registry client.
func (e *Executor) loadChart(ctx context.Context, cfg *action.Configuration, op *spec.HelmOp, src *spec.ChartSource) (*chartv2.Chart, map[string]any, action.ChartPathOptions, error) {
	settings := helmcli.New()
	// Borrow an Install action's ChartPathOptions: NewInstall copies
	// cfg.RegistryClient into the unexported registryClient field that
	// LocateChart requires for oci:// refs.
	pathOpts := action.NewInstall(cfg).ChartPathOptions
	pathOpts.RepoURL = src.Repo
	pathOpts.Version = src.Version
	pathOpts.Username = src.Username
	pathOpts.Password = src.Password
	chartPath, err := pathOpts.LocateChart(src.Ref, settings)
	if err != nil {
		return nil, nil, pathOpts, fmt.Errorf("locating chart %s: %w", src, err)
	}
	chrt, err := loader.Load(chartPath)
	if err != nil {
		return nil, nil, pathOpts, fmt.Errorf("loading chart %s: %w", src, err)
	}
	if err := checkChartInstallable(chrt); err != nil {
		return nil, nil, pathOpts, err
	}
	values, err := e.helmValues(ctx, op)
	if err != nil {
		return nil, nil, pathOpts, err
	}
	return chrt, values, pathOpts, nil
}

// helmConfig initializes a Helm action configuration for a namespace. src
// may be nil (uninstall, plan); an oci:// source additionally gets a
// registry client carrying its credentials.
func (e *Executor) helmConfig(namespace string, src *spec.ChartSource) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	if err := cfg.Init(e.Clients.HelmGetter(namespace), namespace, os.Getenv("HELM_DRIVER")); err != nil {
		return nil, fmt.Errorf("initializing helm: %w", err)
	}
	cfg.SetLogger(e.Log.Handler())
	if src != nil && src.Form == spec.ChartFormOCI {
		var opts []registry.ClientOption
		if src.Username != "" {
			opts = append(opts, registry.ClientOptBasicAuth(src.Username, src.Password))
		}
		rc, err := registry.NewClient(opts...)
		if err != nil {
			return nil, fmt.Errorf("initializing registry client: %w", err)
		}
		cfg.RegistryClient = rc
	}
	return cfg, nil
}

// runHelmUninstall handles delete:'s release form — the inverse of runHelm.
// The Uninstall action has no context variant, so the step timeout is
// enforced through the action's own Timeout.
func (e *Executor) runHelmUninstall(ctx context.Context, op *spec.DeleteOp) error {
	namespace := op.TargetNamespace()
	cfg, err := e.helmConfig(namespace, nil)
	if err != nil {
		return err
	}
	installed, err := releaseExists(cfg, op.Release)
	if err != nil {
		return fmt.Errorf("checking release %q history: %w", op.Release, err)
	}
	if !installed {
		if op.IgnoreNotFoundOrDefault() {
			e.Log.Info("release not found, nothing to uninstall", "release", op.Release, "namespace", namespace)
			return nil
		}
		return fmt.Errorf("uninstalling release %q: release not found", op.Release)
	}

	client := action.NewUninstall(cfg)
	client.IgnoreNotFound = op.IgnoreNotFoundOrDefault()
	// Wait until the release's resources are gone, matching the other
	// delete forms' delete-and-wait semantics.
	client.WaitStrategy = kube.StatusWatcherStrategy
	client.Timeout = spec.DefaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		client.Timeout = time.Until(deadline)
	}
	e.Log.Info("helm uninstall", "release", op.Release, "namespace", namespace)
	res, err := client.Run(op.Release)
	if err != nil {
		return fmt.Errorf("uninstalling release %q: %w", op.Release, err)
	}
	if res != nil && res.Info != "" {
		e.Log.Info("helm uninstalled", "release", op.Release, "note", res.Info)
		return nil
	}
	e.Log.Info("helm uninstalled", "release", op.Release)
	return nil
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

// helmValues merges valuesFrom sources in order, then inline values on top;
// later sources override earlier ones.
func (e *Executor) helmValues(ctx context.Context, op *spec.HelmOp) (map[string]any, error) {
	merged := map[string]any{}
	for i, src := range op.ValuesFrom {
		raw, origin, err := readValuesSource(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("valuesFrom[%d]: %w", i, err)
		}
		vals, err := spec.DecodeYAMLMap(raw)
		if err != nil {
			return nil, fmt.Errorf("parsing values from %s: %w", origin, err)
		}
		merged = mergeMaps(merged, vals)
	}
	if op.Values != nil {
		merged = mergeMaps(merged, op.Values)
	}
	return merged, nil
}

func readValuesSource(ctx context.Context, src spec.ValuesSource) (raw []byte, origin string, err error) {
	if src.URL != "" {
		raw, err := fetchURL(ctx, src.URL)
		return raw, src.URL, err
	}
	raw, err = os.ReadFile(src.File)
	return raw, src.File, err
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
