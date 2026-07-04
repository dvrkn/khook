package ops

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/dvrkn/khook/internal/kube"
	"github.com/dvrkn/khook/internal/spec"
)

// testExecutor builds an Executor over fake clients with a mapper that knows
// the core and apps types the tests use.
func testExecutor(t *testing.T, objs ...runtime.Object) (*Executor, *dynamicfake.FakeDynamicClient, *k8sfake.Clientset) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, objs...)
	typed := k8sfake.NewClientset()

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Pod"), meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("ConfigMap"), meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Namespace"), meta.RESTScopeRoot)
	mapper.Add(appsv1.SchemeGroupVersion.WithKind("Deployment"), meta.RESTScopeNamespace)
	mapper.Add(appsv1.SchemeGroupVersion.WithKind("DaemonSet"), meta.RESTScopeNamespace)

	clients := &kube.Clients{Dynamic: dyn, Typed: typed, Mapper: mapper}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewExecutor(clients, log), dyn, typed
}

func unstructuredPod(name, namespace string, labels map[string]string, ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": status},
			},
		},
	}}
	if len(labels) > 0 {
		lbl := map[string]any{}
		for k, v := range labels {
			lbl[k] = v
		}
		obj.Object["metadata"].(map[string]any)["labels"] = lbl
	}
	return obj
}

func applyStep(name string, op *spec.ApplyOp) *spec.Step   { return &spec.Step{Name: name, Apply: op} }
func deleteStep(name string, op *spec.DeleteOp) *spec.Step { return &spec.Step{Name: name, Delete: op} }

const configMapYAML = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  key: value
`

func TestApplyCreatesAndDefaultsNamespace(t *testing.T) {
	e, dyn, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, err := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("target").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("configmap not created in default namespace of op: %v", err)
	}
	if val, _, _ := unstructured.NestedString(got.Object, "data", "key"); val != "value" {
		t.Fatalf("data.key = %q, want value", val)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	e, dyn, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	for i := 0; i < 2; i++ {
		if err := e.Execute(context.Background(), step); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	list, err := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("target").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("got %d configmaps, want 1", len(list.Items))
	}
}

func TestApplyUpdatesExisting(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
		"data":       map[string]any{"key": "old"},
	}}
	e, dyn, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, err := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("target").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if val, _, _ := unstructured.NestedString(got.Object, "data", "key"); val != "value" {
		t.Fatalf("data.key = %q, want updated to value", val)
	}
}

func TestApplySkipIfExists(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
		"data":       map[string]any{"key": "untouched"},
	}}
	e, dyn, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace:    "target",
		SkipIfExists: true,
		Manifests:    []spec.ManifestSource{{Inline: configMapYAML}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("target").Get(context.Background(), "demo", metav1.GetOptions{})
	if val, _, _ := unstructured.NestedString(got.Object, "data", "key"); val != "untouched" {
		t.Fatalf("data.key = %q; skipIfExists must not modify the resource", val)
	}
}

func TestApplyCreateNamespace(t *testing.T) {
	e, _, typed := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace:       "fresh",
		CreateNamespace: true,
		Manifests:       []spec.ManifestSource{{Inline: configMapYAML}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if _, err := typed.CoreV1().Namespaces().Get(context.Background(), "fresh", metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
}

func TestApplyMultiDocument(t *testing.T) {
	e, dyn, _ := testExecutor(t)
	multi := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: one}\n---\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: two}\n"
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: multi}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	list, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("target").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 2 {
		t.Fatalf("got %d configmaps, want 2", len(list.Items))
	}
}

const podYAML = `
apiVersion: v1
kind: Pod
metadata:
  name: waiter
status:
  phase: Running
  conditions:
    - type: Ready
      status: "True"
`

func TestApplyWaitForMet(t *testing.T) {
	// The fake tracker persists status from the manifest itself, so the
	// applied object immediately meets the condition.
	e, _, _ := testExecutor(t)
	step := applyStep("pod", &spec.ApplyOp{
		Namespace: "ns1",
		WaitFor:   "condition=Ready",
		Manifests: []spec.ManifestSource{{Inline: podYAML}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
}

func TestApplyWaitForTimesOut(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "ns1",
		WaitFor:   "jsonpath={.status.ready}=true",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.Execute(ctx, step); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestApplyWaitForRunsOnSkipIfExists(t *testing.T) {
	existing := unstructuredPod("waiter", "ns1", nil, false) // Ready=False
	e, _, _ := testExecutor(t, existing)
	step := applyStep("pod", &spec.ApplyOp{
		Namespace:    "ns1",
		SkipIfExists: true,
		WaitFor:      "condition=Ready",
		Manifests:    []spec.ManifestSource{{Inline: podYAML}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.Execute(ctx, step); err == nil {
		t.Fatal("waitFor must still gate the step when skipIfExists short-circuits")
	}
}

func TestApplyKustomize(t *testing.T) {
	e, dyn, _ := testExecutor(t)
	step := applyStep("kz", &spec.ApplyOp{
		Namespace: "ns1",
		Manifests: []spec.ManifestSource{{Kustomize: "./testdata/kustomize"}},
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, err := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("ns1").Get(context.Background(), "prod-settings", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("kustomized configmap (namePrefix applied) not created: %v", err)
	}
	if env, _, _ := unstructured.NestedString(got.Object, "data", "env"); env != "prod" {
		t.Fatalf("data.env = %q, want patched value prod", env)
	}
	if color, _, _ := unstructured.NestedString(got.Object, "data", "color"); color != "blue" {
		t.Fatalf("data.color = %q, want base value blue", color)
	}
}

func TestKustomizeRenderError(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := applyStep("kz", &spec.ApplyOp{
		Manifests: []spec.ManifestSource{{Kustomize: "./testdata/does-not-exist"}},
	})
	err := e.Execute(context.Background(), step)
	if err == nil || !strings.Contains(err.Error(), "rendering kustomization") {
		t.Fatalf("want kustomize render error, got %v", err)
	}
}

func TestDeleteByName(t *testing.T) {
	e, dyn, _ := testExecutor(t, unstructuredPod("victim", "ns1", nil, true))
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/victim", Namespace: "ns1"})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	list, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("pods")).
		Namespace("ns1").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatalf("pod still present")
	}
}

func TestDeleteMissingIgnoreNotFoundDefault(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/ghost", Namespace: "ns1"})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatalf("ignoreNotFound defaults true; got %v", err)
	}
}

func TestDeleteMissingIgnoreNotFoundFalse(t *testing.T) {
	e, _, _ := testExecutor(t)
	no := false
	step := deleteStep("del", &spec.DeleteOp{Resource: "pod/ghost", Namespace: "ns1", IgnoreNotFound: &no})
	if err := e.Execute(context.Background(), step); err == nil {
		t.Fatal("want not-found error")
	}
}

func TestDeleteBySelector(t *testing.T) {
	e, dyn, _ := testExecutor(t,
		unstructuredPod("keep", "ns1", map[string]string{"app": "keep"}, true),
		unstructuredPod("kill-1", "ns1", map[string]string{"app": "kill"}, true),
		unstructuredPod("kill-2", "ns1", map[string]string{"app": "kill"}, true),
	)
	step := deleteStep("del", &spec.DeleteOp{Resource: "pods", Namespace: "ns1", Selector: "app=kill"})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	list, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("pods")).
		Namespace("ns1").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 || list.Items[0].GetName() != "keep" {
		t.Fatalf("got %d pods, want only keep", len(list.Items))
	}
}

func patchStep(name string, op *spec.PatchOp) *spec.Step { return &spec.Step{Name: name, Patch: op} }

func existingConfigMap() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "ns1"},
		"data":       map[string]any{"keep": "yes", "drop": "no"},
	}}
}

func TestPatchMerge(t *testing.T) {
	e, dyn, _ := testExecutor(t, existingConfigMap())
	step := patchStep("p", &spec.PatchOp{
		Target:    "configmap/demo",
		Namespace: "ns1",
		Type:      spec.PatchMerge,
		Patch:     []byte(`{"data":{"added":"new"}}`),
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("ns1").Get(context.Background(), "demo", metav1.GetOptions{})
	if val, _, _ := unstructured.NestedString(got.Object, "data", "added"); val != "new" {
		t.Fatalf("data.added = %q, want new", val)
	}
	if val, _, _ := unstructured.NestedString(got.Object, "data", "keep"); val != "yes" {
		t.Fatalf("data.keep = %q; merge patch must not clobber siblings", val)
	}
}

func TestPatchJSON(t *testing.T) {
	e, dyn, _ := testExecutor(t, existingConfigMap())
	step := patchStep("p", &spec.PatchOp{
		Target:    "configmap/demo",
		Namespace: "ns1",
		Type:      spec.PatchJSON,
		Patch:     []byte(`[{"op":"remove","path":"/data/drop"}]`),
	})
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("ns1").Get(context.Background(), "demo", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedString(got.Object, "data", "drop"); found {
		t.Fatal("data.drop still present after json remove")
	}
}

func TestPatchMissingTargetFails(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := patchStep("p", &spec.PatchOp{
		Target:    "configmap/ghost",
		Namespace: "ns1",
		Type:      spec.PatchMerge,
		Patch:     []byte(`{"data":{}}`),
	})
	err := e.Execute(context.Background(), step)
	if err == nil || !strings.Contains(err.Error(), "must exist") {
		t.Fatalf("want missing-target error, got %v", err)
	}
}

func TestWaitConditionAlreadyMet(t *testing.T) {
	e, _, _ := testExecutor(t,
		unstructuredPod("a", "ns1", nil, true),
		unstructuredPod("b", "ns2", nil, true),
	)
	step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: "condition=Ready", On: "pods", AllNamespaces: true}}
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
}

func TestWaitConditionTimesOut(t *testing.T) {
	e, _, _ := testExecutor(t, unstructuredPod("a", "ns1", nil, false))
	step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: "condition=Ready", On: "pods", Namespace: "ns1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.Execute(ctx, step); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestWaitJSONPath(t *testing.T) {
	pod := unstructuredPod("a", "ns1", nil, true)
	pod.Object["status"].(map[string]any)["phase"] = "Running"
	e, _, _ := testExecutor(t, pod)

	for _, forExpr := range []string{
		"jsonpath={.status.phase}=Running",
		"jsonpath=.status.phase=Running",
		"jsonpath={.status.phase}", // existence: any non-empty value
		`jsonpath={.status.conditions[?(@.type=="Ready")].status}=True`,
	} {
		step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: forExpr, On: "pods", Namespace: "ns1"}}
		if err := e.Execute(context.Background(), step); err != nil {
			t.Fatalf("%s: %v", forExpr, err)
		}
	}
}

func TestWaitJSONPathTimesOut(t *testing.T) {
	pod := unstructuredPod("a", "ns1", nil, true)
	pod.Object["status"].(map[string]any)["phase"] = "Pending"
	e, _, _ := testExecutor(t, pod)
	for _, forExpr := range []string{
		"jsonpath={.status.phase}=Running", // wrong value
		"jsonpath={.status.hostIP}",        // missing key: not yet, keeps polling
	} {
		step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: forExpr, On: "pods", Namespace: "ns1"}}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if err := e.Execute(ctx, step); err == nil {
			t.Fatalf("%s: want timeout error", forExpr)
		}
		cancel()
	}
}

func TestWaitForDelete(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := &spec.Step{Name: "w", Wait: &spec.WaitOp{For: "delete", On: "pod/gone", Namespace: "ns1"}}
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
}

func TestRolloutRestartSetsAnnotation(t *testing.T) {
	e, _, typed := testExecutor(t)
	if _, err := typed.AppsV1().Deployments("ns1").Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns1"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "r", Rollout: &spec.RolloutOp{Restart: "deployment/app", Namespace: "ns1"}}
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	dep, err := typed.AppsV1().Deployments("ns1").Get(context.Background(), "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dep.Spec.Template.Annotations[restartedAtAnnotation] == "" {
		t.Fatal("restartedAt annotation not set")
	}
}

func TestRolloutStatusComplete(t *testing.T) {
	replicas := int32(2)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns1", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           2,
			UpdatedReplicas:    2,
			AvailableReplicas:  2,
		},
	}
	e, _, typed := testExecutor(t)
	if _, err := typed.AppsV1().Deployments("ns1").Create(context.Background(), dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "r", Rollout: &spec.RolloutOp{Status: "deployment/app", Namespace: "ns1"}}
	if err := e.Execute(context.Background(), step); err != nil {
		t.Fatal(err)
	}
}
