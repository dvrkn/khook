package ops

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dvrkn/khook/internal/spec"
)

func assertPlan(t *testing.T, got Assessment, want Action, detailContains string) {
	t.Helper()
	if got.Action != want {
		t.Fatalf("action = %q (detail: %s), want %q", got.Action, got.Detail, want)
	}
	if detailContains != "" && !strings.Contains(got.Detail, detailContains) {
		t.Fatalf("detail %q does not contain %q", got.Detail, detailContains)
	}
}

func mustChartSource(t *testing.T, op *spec.HelmOp) *spec.ChartSource {
	t.Helper()
	src, err := spec.ParseChartSource(op)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestPlanHelmReleaseInstall(t *testing.T) {
	cfg := memoryConfig(t)
	op := &spec.HelmOp{Chart: "nginx", Repo: "https://charts.example.com", Version: "1.2.3"}
	assertPlan(t, planHelmRelease(cfg, op, "web", mustChartSource(t, op)), ActionInstall, "nginx@1.2.3")
}

func TestPlanHelmReleaseUpgrade(t *testing.T) {
	cfg := memoryConfig(t)
	if err := cfg.Releases.Create(storedRelease("web")); err != nil {
		t.Fatal(err)
	}
	op := &spec.HelmOp{Chart: "nginx", Repo: "https://charts.example.com"}
	got := planHelmRelease(cfg, op, "web", mustChartSource(t, op))
	assertPlan(t, got, ActionUpgrade, "revision 1")
	if !strings.Contains(got.Detail, "nginx@latest") {
		t.Fatalf("detail %q should name the target chart", got.Detail)
	}
}

func TestPlanHelmReleaseSkipIfInstalled(t *testing.T) {
	cfg := memoryConfig(t)
	if err := cfg.Releases.Create(storedRelease("web")); err != nil {
		t.Fatal(err)
	}
	op := &spec.HelmOp{Chart: "nginx", Repo: "https://charts.example.com", SkipIfInstalled: true}
	assertPlan(t, planHelmRelease(cfg, op, "web", mustChartSource(t, op)), ActionSkip, "skipIfInstalled")
}

func TestPlanHelmUninstall(t *testing.T) {
	cfg := memoryConfig(t)
	if err := cfg.Releases.Create(storedRelease("web")); err != nil {
		t.Fatal(err)
	}
	got := planHelmUninstallRelease(cfg, &spec.DeleteOp{Release: "web"})
	assertPlan(t, got, ActionDelete, `uninstalls release "web" revision 1 (test-0.1.0)`)
}

func TestPlanHelmUninstallAbsent(t *testing.T) {
	cfg := memoryConfig(t)
	assertPlan(t, planHelmUninstallRelease(cfg, &spec.DeleteOp{Release: "ghost"}),
		ActionNone, "already absent")

	ignore := false
	strict := &spec.DeleteOp{Release: "ghost", IgnoreNotFound: &ignore}
	assertPlan(t, planHelmUninstallRelease(cfg, strict), ActionUnknown, "step fails")
}

func TestPlanApplyCreate(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	assertPlan(t, e.Plan(context.Background(), step), ActionCreate, "configmap/demo@target")
}

func TestPlanApplyConfigure(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
	}}
	e, _, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	assertPlan(t, e.Plan(context.Background(), step), ActionConfigure, "updates configmap/demo@target")
}

func TestPlanApplySkipIfExists(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
	}}
	e, _, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace:    "target",
		SkipIfExists: true,
		Manifests:    []spec.ManifestSource{{Inline: configMapYAML}},
	})
	assertPlan(t, e.Plan(context.Background(), step), ActionSkip, "skipIfExists")
}

func TestPlanApplyUnknownKind(t *testing.T) {
	e, _, _ := testExecutor(t)
	crd := "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: w}\n"
	step := applyStep("w", &spec.ApplyOp{Manifests: []spec.ManifestSource{{Inline: crd}}})
	assertPlan(t, e.Plan(context.Background(), step), ActionUnknown, "earlier step")
}

func TestPlanApplyMutatesNothing(t *testing.T) {
	e, dyn, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	e.Plan(context.Background(), step)
	list, err := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).Namespace("target").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatal("plan must not create resources")
	}
}

func TestPlanDeleteNamedPresent(t *testing.T) {
	e, _, _ := testExecutor(t, unstructuredPod("victim", "ns1", nil, true))
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/victim", Namespace: "ns1"})
	assertPlan(t, e.Plan(context.Background(), step), ActionDelete, "pod/victim")
}

func TestPlanDeleteNamedAbsent(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/ghost", Namespace: "ns1"})
	assertPlan(t, e.Plan(context.Background(), step), ActionNone, "already absent")
}

func TestPlanDeleteNamedAbsentStrict(t *testing.T) {
	no := false
	e, _, _ := testExecutor(t)
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/ghost", Namespace: "ns1", IgnoreNotFound: &no})
	assertPlan(t, e.Plan(context.Background(), step), ActionUnknown, "ignoreNotFound")
}

func TestPlanDeleteBySelector(t *testing.T) {
	e, _, _ := testExecutor(t,
		unstructuredPod("kill-1", "ns1", map[string]string{"app": "kill"}, true),
		unstructuredPod("kill-2", "ns1", map[string]string{"app": "kill"}, true),
	)
	step := deleteStep("del", &spec.DeleteOp{Resource: "pods", Namespace: "ns1", Selector: "app=kill"})
	assertPlan(t, e.Plan(context.Background(), step), ActionDelete, "2 matching")
}

func TestPlanWaitConditionAlreadyMet(t *testing.T) {
	e, _, _ := testExecutor(t, unstructuredPod("a", "ns1", nil, true))
	step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: "condition=Ready", On: "pods", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionNone, "already holds")
}

func TestPlanWaitConditionPending(t *testing.T) {
	e, _, _ := testExecutor(t, unstructuredPod("a", "ns1", nil, false))
	step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: "condition=Ready", On: "pods", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionWait, "0/1")
}

func TestPlanWaitForDeleteStillPresent(t *testing.T) {
	e, _, _ := testExecutor(t, unstructuredPod("a", "ns1", nil, true))
	step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: "delete", On: "pod/a", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionWait, "still present")
}

func TestPlanRolloutStatusComplete(t *testing.T) {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns1", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
		},
	}
	e, _, typed := testExecutor(t)
	if _, err := typed.AppsV1().Deployments("ns1").Create(context.Background(), dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "r", Rollout: &spec.RolloutOp{Status: "deployment/app", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionNone, "already complete")
}

func TestPlanRolloutStatusMissing(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := &spec.Step{Name: "r", Rollout: &spec.RolloutOp{Status: "deployment/app", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionWait, "not found yet")
}

func TestPlanRolloutRestart(t *testing.T) {
	e, _, typed := testExecutor(t)
	if _, err := typed.AppsV1().Deployments("ns1").Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns1"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "r", Rollout: &spec.RolloutOp{Restart: "deployment/app", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionRestart, "deployment/app")
}

func TestPlanRolloutRestartMissing(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := &spec.Step{Name: "r", Rollout: &spec.RolloutOp{Restart: "deployment/app", Namespace: "ns1"}}
	assertPlan(t, e.Plan(context.Background(), step), ActionUnknown, "not found")
}
