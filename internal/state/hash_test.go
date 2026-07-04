package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dvrkn/khook/internal/spec"
)

func helmStep(mutate func(*spec.Step)) *spec.Step {
	step := &spec.Step{
		Name: "cilium",
		Helm: &spec.HelmOp{
			Chart:   "cilium",
			Repo:    "https://helm.cilium.io",
			Version: "1.16.0",
			Values:  map[string]any{"kubeProxyReplacement": true},
		},
	}
	if mutate != nil {
		mutate(step)
	}
	return step
}

func TestStepHashCoversActionInputs(t *testing.T) {
	base := StepHash(helmStep(nil))
	if base == "" {
		t.Fatal("hash must not be empty for a valid step")
	}
	if StepHash(helmStep(nil)) != base {
		t.Fatal("hash must be deterministic")
	}

	changed := map[string]func(*spec.Step){
		"version": func(s *spec.Step) { s.Helm.Version = "1.17.0" },
		"values":  func(s *spec.Step) { s.Helm.Values["kubeProxyReplacement"] = false },
		"chart":   func(s *spec.Step) { s.Helm.Chart = "other" },
	}
	for name, mutate := range changed {
		if StepHash(helmStep(mutate)) == base {
			t.Errorf("changing %s must change the hash", name)
		}
	}
}

func TestStepHashIgnoresSchedulingFields(t *testing.T) {
	base := StepHash(helmStep(nil))
	retries := 3
	scheduling := map[string]func(*spec.Step){
		"name":    func(s *spec.Step) { s.Name = "renamed" },
		"needs":   func(s *spec.Step) { s.Needs = []string{"other"} },
		"when":    func(s *spec.Step) { s.When = `vars.ENV == "prod"` },
		"timeout": func(s *spec.Step) { s.Timeout = &spec.Duration{Duration: time.Hour} },
		"retries": func(s *spec.Step) { s.Retries = &retries },
		"onError": func(s *spec.Step) { s.OnError = spec.OnErrorContinue },
	}
	for name, mutate := range scheduling {
		if StepHash(helmStep(mutate)) != base {
			t.Errorf("changing %s must not change the hash", name)
		}
	}
}

func TestStepHashDiffersAcrossTypes(t *testing.T) {
	// Two ops whose JSON bodies are identical must still hash apart: the
	// step type is part of the input.
	waitStep := &spec.Step{Name: "x", Wait: &spec.WaitOp{On: "pods", For: "condition=Ready"}}
	if StepHash(waitStep) == StepHash(helmStep(nil)) {
		t.Fatal("different step types must hash differently")
	}
	if StepHash(&spec.Step{Name: "empty"}) != "" {
		t.Fatal("a step with no action must not hash")
	}
}

func TestStepHashTracksFileContent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cm.yaml")
	if err := os.WriteFile(file, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "cm", Apply: &spec.ApplyOp{
		Manifests: []spec.ManifestSource{{File: file}},
	}}

	base := StepHash(step)
	if base == "" {
		t.Fatal("hash must not be empty when the file is readable")
	}
	if StepHash(step) != base {
		t.Fatal("hash must be stable while the file is unchanged")
	}

	if err := os.WriteFile(file, []byte("a: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if StepHash(step) == base {
		t.Fatal("editing a referenced file must change the hash")
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if StepHash(step) != "" {
		t.Fatal("an unreadable referenced file must yield an empty (never-matching) hash")
	}
}

func TestStepHashTracksDirectoryContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources: [a.yaml]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	step := &spec.Step{Name: "kz", Apply: &spec.ApplyOp{
		Manifests: []spec.ManifestSource{{Kustomize: dir}},
	}}

	base := StepHash(step)
	if base == "" {
		t.Fatal("hash must not be empty for a readable directory")
	}

	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("a: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if StepHash(step) == base {
		t.Fatal("editing a file inside a referenced directory must change the hash")
	}
}

func TestStepHashesSkipsExcludedSteps(t *testing.T) {
	doc := &spec.Document{Steps: []spec.Step{
		{Name: "a", Wait: &spec.WaitOp{On: "pods", For: "condition=Ready"}},
		{Name: "b", Excluded: true, Apply: &spec.ApplyOp{Manifests: []spec.ManifestSource{{File: "/does/not/exist"}}}},
	}}
	hashes := StepHashes(doc)
	if hashes["a"] == "" {
		t.Fatal("included steps must hash")
	}
	if _, ok := hashes["b"]; ok {
		t.Fatal("excluded steps must not hash (their files must not even be read)")
	}
}
