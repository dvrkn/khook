package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dvrkn/khook/internal/spec"
)

func jobStep(name string, op *spec.JobOp) *spec.Step { return &spec.Step{Name: name, Job: op} }

// finishJobsOnCreate makes every Job created through the fake clientset land
// immediately in the given terminal condition — the fake has no controller to
// run pods.
func finishJobsOnCreate(typed *k8sfake.Clientset, condType batchv1.JobConditionType, message string) {
	typed.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		job := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type: condType, Status: corev1.ConditionTrue, Message: message,
		})
		return false, nil, nil
	})
}

func existingJob(name, namespace string, managed, succeeded bool) *batchv1.Job {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if managed {
		job.Labels = map[string]string{managedByLabelKey: managedByLabelValue}
	}
	if succeeded {
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	}
	return job
}

func jobTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestJobRunsToCompletion(t *testing.T) {
	e, _, typed := testExecutor(t)
	finishJobsOnCreate(typed, batchv1.JobComplete, "")
	step := jobStep("hello", &spec.JobOp{
		Image:     "alpine:3.20",
		Command:   []string{"sh", "-c"},
		Args:      []string{"echo hi"},
		Env:       map[string]string{"B": "2", "A": "1"},
		Namespace: "ns1",
	})
	if err := e.Execute(jobTestContext(t), step); err != nil {
		t.Fatal(err)
	}
	job, err := typed.BatchV1().Jobs("ns1").Get(context.Background(), "hello", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Labels[managedByLabelKey] != managedByLabelValue {
		t.Fatalf("job labels = %v, want managed-by khook", job.Labels)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatalf("backoffLimit = %v, want 0 (khook owns retries)", job.Spec.BackoffLimit)
	}
	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restartPolicy = %q, want Never", pod.RestartPolicy)
	}
	container := pod.Containers[0]
	if container.Image != "alpine:3.20" {
		t.Fatalf("image = %q", container.Image)
	}
	if len(container.Env) != 2 || container.Env[0].Name != "A" || container.Env[1].Name != "B" {
		t.Fatalf("env = %v, want [A B] (sorted)", container.Env)
	}
}

func TestJobSkipIfSucceeded(t *testing.T) {
	e, _, typed := testExecutor(t)
	if _, err := typed.BatchV1().Jobs("ns1").Create(context.Background(),
		existingJob("hello", "ns1", true, true), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// No finishJobsOnCreate: a replacement Job would never complete, so a
	// nil error proves the step skipped.
	step := jobStep("hello", &spec.JobOp{Image: "alpine", Namespace: "ns1", SkipIf: spec.SkipIfSucceeded})
	if err := e.Execute(jobTestContext(t), step); err != nil {
		t.Fatal(err)
	}
}

func TestJobReplacesPreviousRun(t *testing.T) {
	e, _, typed := testExecutor(t)
	if _, err := typed.BatchV1().Jobs("ns1").Create(context.Background(),
		existingJob("hello", "ns1", true, true), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	finishJobsOnCreate(typed, batchv1.JobComplete, "")
	step := jobStep("hello", &spec.JobOp{Image: "alpine", Namespace: "ns1"})
	if err := e.Execute(jobTestContext(t), step); err != nil {
		t.Fatal(err)
	}
	deleted := false
	for _, action := range typed.Actions() {
		if action.Matches("delete", "jobs") {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("previous job was not deleted before the new run")
	}
}

func TestJobRefusesUnmanagedJob(t *testing.T) {
	e, _, typed := testExecutor(t)
	if _, err := typed.BatchV1().Jobs("ns1").Create(context.Background(),
		existingJob("hello", "ns1", false, false), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	step := jobStep("hello", &spec.JobOp{Image: "alpine", Namespace: "ns1"})
	err := e.Execute(jobTestContext(t), step)
	if err == nil || !strings.Contains(err.Error(), "not managed by khook") {
		t.Fatalf("want refusal to replace unmanaged job, got %v", err)
	}
}

func TestJobFailureSurfacesLogs(t *testing.T) {
	e, _, typed := testExecutor(t)
	if _, err := typed.CoreV1().Pods("ns1").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-abc", Namespace: "ns1", Labels: map[string]string{"job-name": "hello"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	finishJobsOnCreate(typed, batchv1.JobFailed, "boom")
	step := jobStep("hello", &spec.JobOp{Image: "alpine", Namespace: "ns1"})
	err := e.Execute(jobTestContext(t), step)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want failure message, got %v", err)
	}
	if !strings.Contains(err.Error(), "fake logs") {
		t.Fatalf("want pod log tail in error, got %v", err)
	}
}

func TestJobCreateNamespace(t *testing.T) {
	e, _, typed := testExecutor(t)
	finishJobsOnCreate(typed, batchv1.JobComplete, "")
	step := jobStep("hello", &spec.JobOp{Image: "alpine", Namespace: "fresh", CreateNamespace: true})
	if err := e.Execute(jobTestContext(t), step); err != nil {
		t.Fatal(err)
	}
	if _, err := typed.CoreV1().Namespaces().Get(context.Background(), "fresh", metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
}

func TestPlanJob(t *testing.T) {
	tests := []struct {
		name       string
		existing   *batchv1.Job
		op         *spec.JobOp
		wantAction Action
		wantSub    string
	}{
		{
			name:       "first run",
			op:         &spec.JobOp{Image: "alpine", Namespace: "ns1"},
			wantAction: ActionRun,
			wantSub:    "alpine",
		},
		{
			name:       "replaces previous run",
			existing:   existingJob("hello", "ns1", true, true),
			op:         &spec.JobOp{Image: "alpine", Namespace: "ns1"},
			wantAction: ActionRun,
			wantSub:    "replaces",
		},
		{
			name:       "skipIf: succeeded",
			existing:   existingJob("hello", "ns1", true, true),
			op:         &spec.JobOp{Image: "alpine", Namespace: "ns1", SkipIf: spec.SkipIfSucceeded},
			wantAction: ActionSkip,
			wantSub:    "already succeeded",
		},
		{
			name:       "unmanaged job",
			existing:   existingJob("hello", "ns1", false, false),
			op:         &spec.JobOp{Image: "alpine", Namespace: "ns1"},
			wantAction: ActionUnknown,
			wantSub:    "not managed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _, typed := testExecutor(t)
			if tt.existing != nil {
				if _, err := typed.BatchV1().Jobs("ns1").Create(context.Background(), tt.existing, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			a := e.Plan(context.Background(), jobStep("hello", tt.op))
			if a.Action != tt.wantAction {
				t.Fatalf("action = %s (%s), want %s", a.Action, a.Detail, tt.wantAction)
			}
			if !strings.Contains(a.Detail, tt.wantSub) {
				t.Fatalf("detail %q does not contain %q", a.Detail, tt.wantSub)
			}
		})
	}
}
