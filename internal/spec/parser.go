package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	yamlv3 "go.yaml.in/yaml/v3"
)

// Parse substitutes variables, unmarshals strictly (unknown fields are
// errors), and validates. It is the single entry point from raw spec bytes
// to a usable Document.
//
// The YAML is decoded with yaml.v3 rather than sigs.k8s.io/yaml because the
// DSL has a field literally named "on" (wait.on): YAML 1.1 parsers resolve
// the unquoted key `on` to boolean true, yaml.v3 keeps it a string.
func Parse(raw []byte, vars map[string]string) (*Document, error) {
	substituted, err := Substitute(raw, vars)
	if err != nil {
		return nil, err
	}

	var tree any
	if err := yamlv3.Unmarshal(substituted, &tree); err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	tree, err = normalizeJSON(tree)
	if err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	jsonBytes, err := json.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}

	var doc Document
	decoder := json.NewDecoder(bytes.NewReader(jsonBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	if err := Validate(&doc); err != nil {
		return nil, err
	}
	// when: conditions depend only on variables, so they are decided here,
	// once, and carried on the steps.
	for i := range doc.Steps {
		step := &doc.Steps[i]
		if step.When == "" {
			continue
		}
		ok, err := EvalWhen(step.When, vars)
		if err != nil {
			return nil, fmt.Errorf("step %q: when: %w", step.Name, err)
		}
		step.Excluded = !ok
	}
	return &doc, nil
}

// normalizeJSON makes a yaml.v3 value JSON-encodable: map keys become
// strings (non-string scalar keys are rendered, non-scalar keys are errors).
func normalizeJSON(v any) (any, error) {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			norm, err := normalizeJSON(item)
			if err != nil {
				return nil, err
			}
			out[k] = norm
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			key, ok := scalarKey(k)
			if !ok {
				return nil, fmt.Errorf("unsupported non-scalar map key %v", k)
			}
			norm, err := normalizeJSON(item)
			if err != nil {
				return nil, err
			}
			out[key] = norm
		}
		return out, nil
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			norm, err := normalizeJSON(item)
			if err != nil {
				return nil, err
			}
			out[i] = norm
		}
		return out, nil
	default:
		return v, nil
	}
}

func scalarKey(k any) (string, bool) {
	switch key := k.(type) {
	case string:
		return key, true
	case bool, int, int64, uint64, float64:
		return fmt.Sprintf("%v", key), true
	}
	return "", false
}

// DecodeYAMLMap decodes a YAML mapping with the same YAML 1.2 semantics as
// the spec itself (keys like "on" or "y" stay strings, unlike YAML 1.1
// parsers). Used for Helm values files and --var-file.
func DecodeYAMLMap(raw []byte) (map[string]any, error) {
	var tree any
	if err := yamlv3.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	if tree == nil {
		return map[string]any{}, nil
	}
	norm, err := normalizeJSON(tree)
	if err != nil {
		return nil, err
	}
	m, ok := norm.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a YAML mapping at the top level")
	}
	return m, nil
}

// ParseFile reads and parses a spec file.
func ParseFile(path string, vars map[string]string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading spec: %w", err)
	}
	doc, err := Parse(raw, vars)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}
