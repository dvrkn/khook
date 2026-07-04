package cli

import (
	"testing"

	"github.com/dvrkn/khook/internal/spec"
)

func TestDestroySkips(t *testing.T) {
	steps := []spec.Step{
		{Name: "cni", Helm: &spec.HelmOp{}},
		{Name: "crds", Apply: &spec.ApplyOp{}},
		{Name: "smoke", Job: &spec.JobOp{}},
		{Name: "cleanup", Delete: &spec.DeleteOp{}},
		{Name: "tune", Patch: &spec.PatchOp{}},
		{Name: "ready", Wait: &spec.WaitOp{}},
		{Name: "bounce", Rollout: &spec.RolloutOp{}},
	}
	skip := destroySkips(steps)
	for _, name := range []string{"cni", "crds", "smoke"} {
		if _, ok := skip[name]; ok {
			t.Errorf("%s must be torn down, not skipped", name)
		}
	}
	for _, name := range []string{"cleanup", "tune", "ready", "bounce"} {
		if skip[name] == "" {
			t.Errorf("%s must be skipped with a reason", name)
		}
	}
}
