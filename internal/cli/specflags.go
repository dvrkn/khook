package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/spec"
)

// specFlags are the flags shared by every command that reads a spec file.
type specFlags struct {
	file         string
	sets         []string
	varFile      string
	varPrefix    string
	secretPrefix string

	// redact receives the values of secret-prefixed variables so they are
	// masked in all output; set by the command constructors.
	redact *redactor
	// secretNames holds the variable names loaded via the secret prefix;
	// populated by variables(), consumed by load() for derived-value tracking.
	secretNames map[string]bool
}

func (f *specFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.file, "file", "f", "", "path to the Khook spec file (required)")
	cmd.Flags().StringArrayVar(&f.sets, "set", nil, "set a variable NAME=value (repeatable, highest precedence)")
	cmd.Flags().StringVar(&f.varFile, "var-file", "", "YAML file with a flat NAME: value variable map")
	cmd.Flags().StringVar(&f.varPrefix, "var-prefix", spec.DefaultVarPrefix, "environment variable prefix consumed as spec variables")
	cmd.Flags().StringVar(&f.secretPrefix, "secret-prefix", spec.DefaultSecretPrefix, "environment variable prefix consumed as secret variables (values redacted in output)")
	_ = cmd.MarkFlagRequired("file")
}

// load merges variable sources (--set > --var-file > secret env > prefixed
// env) and parses + validates the spec file. Failures are validation errors
// (exit 2).
func (f *specFlags) load() (*spec.Document, error) {
	vars, err := f.variables()
	if err != nil {
		return nil, validationErr(err)
	}
	doc, derived, err := spec.ParseFileTracking(f.file, vars, f.secretNames)
	// Pipeline outputs of secret variables (e.g. ${TOKEN|b64enc}) are as
	// sensitive as the raw values; register them before the error can be
	// printed — parse errors may quote substituted content.
	f.redact.Add(derived...)
	if err != nil {
		return nil, validationErr(err)
	}
	return doc, nil
}

func (f *specFlags) variables() (map[string]string, error) {
	envVars := spec.VarsFromEnviron(os.Environ(), f.varPrefix)

	secretVars := spec.VarsFromEnviron(os.Environ(), f.secretPrefix)
	f.secretNames = map[string]bool{}
	for name, v := range secretVars {
		f.secretNames[name] = true
		f.redact.Add(v)
	}

	fileVars := map[string]string{}
	if f.varFile != "" {
		raw, err := os.ReadFile(f.varFile)
		if err != nil {
			return nil, fmt.Errorf("reading --var-file: %w", err)
		}
		// Accept any scalar values and render them as strings.
		loose, err := spec.DecodeYAMLMap(raw)
		if err != nil {
			return nil, fmt.Errorf("parsing --var-file %s: %w", f.varFile, err)
		}
		for k, v := range loose {
			if _, isMap := v.(map[string]any); isMap {
				return nil, fmt.Errorf("--var-file %s: %q must be a scalar, got a map", f.varFile, k)
			}
			if _, isList := v.([]any); isList {
				return nil, fmt.Errorf("--var-file %s: %q must be a scalar, got a list", f.varFile, k)
			}
			fileVars[k] = fmt.Sprintf("%v", v)
		}
	}

	setVars := map[string]string{}
	for _, kv := range f.sets {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid --set %q (want NAME=value)", kv)
		}
		setVars[name] = value
	}

	return spec.MergeVars(envVars, secretVars, fileVars, setVars), nil
}
