package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dvrkn/khook/internal/spec"
)

// StepHash fingerprints one step's inputs: what the step would do to the
// cluster, not how it is scheduled. It hashes the canonical JSON of the
// step's action block (post variable substitution) plus the content of
// every local file the block references — manifest file: sources,
// kustomize: directories, helm valuesFrom files, and local chart paths.
// Name, needs, when, and the timeout/retry/onError policy fields are
// deliberately excluded: changing them does not change what a completed
// step already did. Remote content (url: sources, charts behind a repo/OCI
// reference) participates by reference only — the hash cannot see it change.
//
// An unhashable step (unreadable referenced file) returns "": the empty
// hash never matches a recorded one, so the step re-runs and surfaces the
// real error from its executor.
func StepHash(step *spec.Step) string {
	inputs := struct {
		Type    string            `json:"type"`
		Op      any               `json:"op"`
		Content map[string]string `json:"content,omitempty"`
	}{Type: step.Type()}

	var paths []string
	switch {
	case step.Helm != nil:
		inputs.Op = step.Helm
		for _, vs := range step.Helm.ValuesFrom {
			if vs.File != "" {
				paths = append(paths, vs.File)
			}
		}
		// A local chart path pins the chart to files on disk; hash them the
		// way a version pins a repo chart. Parse errors mean an invalid op —
		// validation already rejected it, but stay hashless if one gets here.
		src, err := spec.ParseChartSource(step.Helm)
		if err != nil {
			return ""
		}
		if src.Form == spec.ChartFormPath {
			paths = append(paths, src.Ref)
		}
	case step.Apply != nil:
		inputs.Op = step.Apply
		paths = append(paths, manifestPaths(step.Apply.Manifests)...)
	case step.Delete != nil:
		inputs.Op = step.Delete
		paths = append(paths, manifestPaths(step.Delete.Manifests)...)
	case step.Patch != nil:
		inputs.Op = step.Patch
	case step.Wait != nil:
		inputs.Op = step.Wait
	case step.Rollout != nil:
		inputs.Op = step.Rollout
	case step.Job != nil:
		inputs.Op = step.Job
	default:
		return ""
	}

	if len(paths) > 0 {
		inputs.Content = make(map[string]string, len(paths))
		for _, p := range paths {
			h, err := hashPath(p)
			if err != nil {
				return ""
			}
			inputs.Content[p] = h
		}
	}

	b, err := json.Marshal(inputs)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(b))
}

// StepHashes computes every step's input hash, keyed by step name. Steps
// excluded by when: are left out — they never resume-skip, and skipping
// them avoids reading their referenced files for nothing.
func StepHashes(doc *spec.Document) map[string]string {
	m := make(map[string]string, len(doc.Steps))
	for i := range doc.Steps {
		if doc.Steps[i].Excluded {
			continue
		}
		m[doc.Steps[i].Name] = StepHash(&doc.Steps[i])
	}
	return m
}

func manifestPaths(sources []spec.ManifestSource) []string {
	var paths []string
	for _, src := range sources {
		switch {
		case src.File != "":
			paths = append(paths, src.File)
		case src.Kustomize != "":
			paths = append(paths, src.Kustomize)
		}
	}
	return paths
}

// hashPath fingerprints a referenced file, or every regular file under a
// referenced directory (kustomize bases, chart directories).
func hashPath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if info.IsDir() {
		err = hashDir(h, path)
	} else {
		err = hashFile(h, path)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// hashDir feeds every regular file under root into h — slash-normalized
// relative path plus content hash, in WalkDir's deterministic lexical
// order. Hashing the whole tree over-approximates what kustomize or helm
// would actually read; the error direction is a spare re-run, never a
// missed change.
func hashDir(h hash.Hash, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		if err := hashFile(h, path); err != nil {
			return err
		}
		h.Write([]byte{0})
		return nil
	})
}

func hashFile(h hash.Hash, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	h.Write(b)
	return nil
}
