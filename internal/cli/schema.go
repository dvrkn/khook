package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/spf13/cobra"

	"github.com/dvrkn/khook/internal/spec"
)

func newSchemaCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema for the Khook spec",
		RunE: func(cmd *cobra.Command, args []string) error {
			schema, err := generateSchema()
			if err != nil {
				return executionErr(err)
			}
			out, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				return executionErr(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}

func generateSchema() (*jsonschema.Schema, error) {
	reflector := &jsonschema.Reflector{
		// Durations are strings like "5m", not structs.
		Mapper: func(t reflect.Type) *jsonschema.Schema {
			if t == reflect.TypeOf(spec.Duration{}) {
				return &jsonschema.Schema{
					Type:        "string",
					Pattern:     `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`,
					Description: "Go duration string, e.g. \"30s\", \"5m\"",
				}
			}
			if t == reflect.TypeOf(time.Duration(0)) {
				return &jsonschema.Schema{Type: "string"}
			}
			return nil
		},
	}
	schema := reflector.Reflect(&spec.Document{})
	schema.ID = "https://khook.io/schema/v1/khook.json"
	schema.Title = "Khook"

	// The reflector cannot express cross-field rules (exactly-one action or
	// source key, closed value sets) — mirror spec.Validate by hand. def and
	// prop record what they fail to find so a renamed type or field breaks
	// schema generation loudly instead of silently dropping a constraint.
	var missing []string
	def := func(name string) *jsonschema.Schema {
		d, ok := schema.Definitions[name]
		if !ok {
			missing = append(missing, name)
			return &jsonschema.Schema{}
		}
		return d
	}
	prop := func(defName string, d *jsonschema.Schema, name string) *jsonschema.Schema {
		if d.Properties != nil {
			if p, ok := d.Properties.Get(name); ok {
				return p
			}
		}
		missing = append(missing, defName+"."+name)
		return &jsonschema.Schema{}
	}
	exactlyOne := func(keys ...string) []*jsonschema.Schema {
		out := make([]*jsonschema.Schema, len(keys))
		for i, key := range keys {
			out[i] = &jsonschema.Schema{Required: []string{key}}
		}
		return out
	}
	one := uint64(1)
	onError := []any{spec.OnErrorFail, spec.OnErrorContinue}

	doc := def("Document")
	prop("Document", doc, "apiVersion").Const = spec.APIVersion
	prop("Document", doc, "kind").Const = spec.Kind

	prop("Defaults", def("Defaults"), "onError").Enum = onError

	step := def("Step")
	step.OneOf = exactlyOne("helm", "apply", "delete", "patch", "wait", "rollout", "job")
	prop("Step", step, "onError").Enum = onError

	def("ManifestSource").OneOf = exactlyOne("inline", "file", "url", "kustomize")
	def("ValuesSource").OneOf = exactlyOne("file", "url")
	def("RolloutOp").OneOf = exactlyOne("restart", "status")

	deleteOp := def("DeleteOp")
	deleteOp.OneOf = exactlyOne("manifests", "resource", "release")
	prop("DeleteOp", deleteOp, "manifests").MinItems = &one

	prop("ApplyOp", def("ApplyOp"), "manifests").MinItems = &one

	prop("PatchOp", def("PatchOp"), "type").Enum = []any{spec.PatchStrategic, spec.PatchMerge, spec.PatchJSON}

	if len(missing) > 0 {
		return nil, fmt.Errorf("schema generation: missing definitions or properties: %s", strings.Join(missing, ", "))
	}
	return schema, nil
}
