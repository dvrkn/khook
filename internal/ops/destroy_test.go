package ops

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dvrkn/khook/internal/spec"
)

func TestDestroyApplyDeletesObjects(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo", "namespace": "target"},
	}}
	e, dyn, _ := testExecutor(t, existing)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	if err := e.Destroy(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	list, _ := dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
		Namespace("target").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatalf("configmap still present after destroy")
	}
}

func TestDestroyApplyMissingObjectsIsSuccess(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := applyStep("cm", &spec.ApplyOp{
		Namespace: "target",
		Manifests: []spec.ManifestSource{{Inline: configMapYAML}},
	})
	if err := e.Destroy(context.Background(), step); err != nil {
		t.Fatalf("destroy of absent resources must succeed, got %v", err)
	}
}

func TestDestroyJobDeletesManagedJob(t *testing.T) {
	e, _, typed := testExecutor(t)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "hello", Namespace: "ns1",
		Labels: map[string]string{managedByLabelKey: managedByLabelValue},
	}}
	if _, err := typed.BatchV1().Jobs("ns1").Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "hello", Job: &spec.JobOp{Image: "busybox", Namespace: "ns1"}}
	if err := e.Destroy(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if _, err := typed.BatchV1().Jobs("ns1").Get(context.Background(), "hello", metav1.GetOptions{}); err == nil {
		t.Fatal("job still present after destroy")
	}
}

func TestDestroyJobMissingIsSuccess(t *testing.T) {
	e, _, _ := testExecutor(t)
	step := &spec.Step{Name: "ghost", Job: &spec.JobOp{Image: "busybox", Namespace: "ns1"}}
	if err := e.Destroy(context.Background(), step); err != nil {
		t.Fatalf("destroy of an absent job must succeed, got %v", err)
	}
}

func TestDestroyJobRefusesForeignJob(t *testing.T) {
	e, _, typed := testExecutor(t)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "ns1"}}
	if _, err := typed.BatchV1().Jobs("ns1").Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "foreign", Job: &spec.JobOp{Image: "busybox", Namespace: "ns1"}}
	err := e.Destroy(context.Background(), step)
	if err == nil || !strings.Contains(err.Error(), "not managed by khook") {
		t.Fatalf("want ownership refusal, got %v", err)
	}
	if _, err := typed.BatchV1().Jobs("ns1").Get(context.Background(), "foreign", metav1.GetOptions{}); err != nil {
		t.Fatal("foreign job must not be deleted")
	}
}

func TestDestroySkipReason(t *testing.T) {
	reversible := []*spec.Step{
		{Name: "h", Helm: &spec.HelmOp{}},
		{Name: "a", Apply: &spec.ApplyOp{}},
		{Name: "j", Job: &spec.JobOp{}},
	}
	for _, s := range reversible {
		if reason := DestroySkipReason(s); reason != "" {
			t.Errorf("%s step: reason = %q, want reversible", s.Type(), reason)
		}
	}
	skipped := []*spec.Step{
		{Name: "d", Delete: &spec.DeleteOp{}},
		{Name: "p", Patch: &spec.PatchOp{}},
		{Name: "w", Wait: &spec.WaitOp{}},
		{Name: "r", Rollout: &spec.RolloutOp{}},
	}
	for _, s := range skipped {
		if reason := DestroySkipReason(s); reason == "" {
			t.Errorf("%s step: want a skip reason", s.Type())
		}
	}
}
