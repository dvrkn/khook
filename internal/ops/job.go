package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dvrkn/khook/internal/spec"
)

// Jobs created by khook carry this label; runJob refuses to replace a Job it
// does not own.
const (
	managedByLabelKey   = "app.kubernetes.io/managed-by"
	managedByLabelValue = "khook"
)

// jobLogTailLines is how many trailing pod log lines a failed job's error
// message carries.
const jobLogTailLines = 20

func (e *Executor) runJob(ctx context.Context, step *spec.Step) error {
	op := step.Job
	namespace := op.TargetNamespace()
	name := step.Name

	if op.CreateNamespace {
		if err := e.ensureNamespace(ctx, namespace); err != nil {
			return err
		}
	}

	jobs := e.Clients.Typed.BatchV1().Jobs(namespace)
	existing, err := jobs.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("getting job %q: %w", name, err)
	case existing.Labels[managedByLabelKey] != managedByLabelValue:
		return fmt.Errorf("job %q in namespace %q exists but is not managed by khook; refusing to replace it", name, namespace)
	case op.SkipIf == spec.SkipIfSucceeded && jobSucceeded(existing):
		e.Log.Info("job already succeeded, skipping", "job", name, "namespace", namespace)
		return nil
	default:
		if err := e.replaceJob(ctx, namespace, name); err != nil {
			return err
		}
	}

	if _, err := jobs.Create(ctx, buildJob(name, op, deadlineSeconds(ctx)), metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating job %q: %w", name, err)
	}
	e.Log.Info("job created", "job", name, "namespace", namespace, "image", op.Image)

	return poll(ctx, func(ctx context.Context) (bool, error) {
		current, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("getting job %q: %w", name, err)
		}
		if jobSucceeded(current) {
			return true, nil
		}
		if msg, failed := jobFailed(current); failed {
			return false, fmt.Errorf("job %q failed: %s%s", name, msg, e.jobLogs(ctx, namespace, name))
		}
		return false, nil
	})
}

// replaceJob deletes a previous run's Job and waits until it is gone, so the
// new Job can be created under the same name.
func (e *Executor) replaceJob(ctx context.Context, namespace, name string) error {
	e.Log.Info("replacing previous job", "job", name, "namespace", namespace)
	return e.deleteJobAndWait(ctx, namespace, name)
}

// deleteJobAndWait deletes a Job (foreground, so its pods go with it) and
// polls until it is gone. A missing Job is success.
func (e *Executor) deleteJobAndWait(ctx context.Context, namespace, name string) error {
	jobs := e.Clients.Typed.BatchV1().Jobs(namespace)
	foreground := metav1.DeletePropagationForeground
	err := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting job %q: %w", name, err)
	}
	return poll(ctx, func(ctx context.Context) (bool, error) {
		_, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("waiting for job %q to be deleted: %w", name, err)
		}
		return false, nil
	})
}

func buildJob(name string, op *spec.JobOp, activeDeadline *int64) *batchv1.Job {
	keys := make([]string, 0, len(op.Env))
	for key := range op.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]corev1.EnvVar, 0, len(keys))
	for _, key := range keys {
		env = append(env, corev1.EnvVar{Name: key, Value: op.Env[key]})
	}

	// khook's step retries own the retry model: every attempt is a fresh
	// Job, so the Job itself never restarts pods.
	backoffLimit := int32(0)
	labels := map[string]string{managedByLabelKey: managedByLabelValue}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: op.ServiceAccount,
					Containers: []corev1.Container{{
						Name:    name,
						Image:   op.Image,
						Command: op.Command,
						Args:    op.Args,
						Env:     env,
					}},
				},
			},
		},
	}
}

// deadlineSeconds converts the context deadline (the step timeout) into the
// Job's activeDeadlineSeconds, so a Job khook stops waiting on cannot keep
// running in-cluster.
func deadlineSeconds(ctx context.Context) *int64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	secs := int64(time.Until(deadline).Seconds())
	if secs < 1 {
		secs = 1
	}
	return &secs
}

func jobSucceeded(job *batchv1.Job) bool {
	return jobCondition(job, batchv1.JobComplete) != nil
}

func jobFailed(job *batchv1.Job) (msg string, failed bool) {
	c := jobCondition(job, batchv1.JobFailed)
	if c == nil {
		return "", false
	}
	switch {
	case c.Message != "":
		return c.Message, true
	case c.Reason != "":
		return c.Reason, true
	}
	return "pod did not succeed", true
}

func jobCondition(job *batchv1.Job, want batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		c := &job.Status.Conditions[i]
		if c.Type == want && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

// jobLogs returns the tail of the job's pod log formatted for appending to an
// error message. Best-effort: any problem yields "".
func (e *Executor) jobLogs(ctx context.Context, namespace, name string) string {
	podsClient := e.Clients.Typed.CoreV1().Pods(namespace)
	pods, err := podsClient.List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	pod := pods.Items[len(pods.Items)-1]
	tail := int64(jobLogTailLines)
	raw, err := podsClient.GetLogs(pod.Name, &corev1.PodLogOptions{TailLines: &tail}).Do(ctx).Raw()
	if err != nil || len(raw) == 0 {
		return ""
	}
	return fmt.Sprintf("; last log lines of pod %s:\n%s", pod.Name, strings.TrimRight(string(raw), "\n"))
}

func (e *Executor) planJob(ctx context.Context, step *spec.Step) Assessment {
	op := step.Job
	namespace := op.TargetNamespace()
	existing, err := e.Clients.Typed.BatchV1().Jobs(namespace).Get(ctx, step.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		a := Assessment{ActionRun, fmt.Sprintf("runs image %s to completion", op.Image)}
		if note := e.namespaceNote(ctx, namespace, op.CreateNamespace); note != "" {
			a.Detail += "; " + note
		}
		return a
	}
	if err != nil {
		return unknown(fmt.Errorf("getting job %q: %w", step.Name, err))
	}
	if existing.Labels[managedByLabelKey] != managedByLabelValue {
		return unknown(fmt.Errorf("job %q exists but is not managed by khook; apply would fail", step.Name))
	}
	if op.SkipIf == spec.SkipIfSucceeded && jobSucceeded(existing) {
		return Assessment{ActionSkip, fmt.Sprintf("job %q already succeeded (skipIf: succeeded)", step.Name)}
	}
	return Assessment{ActionRun, fmt.Sprintf("replaces the previous Job and runs image %s to completion", op.Image)}
}
