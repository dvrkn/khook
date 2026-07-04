package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	schema.ID = "https://khook.dvrkn.com/schema/v1/khook.json"
	schema.Title = "Khook"

	// The reflector cannot express "exactly one action key" — add the oneOf
	// to the Step definition by hand.
	step, ok := schema.Definitions["Step"]
	if !ok {
		return nil, fmt.Errorf("schema generation: Step definition missing")
	}
	for _, action := range []string{"helm", "apply", "delete", "wait", "rollout"} {
		step.OneOf = append(step.OneOf, &jsonschema.Schema{Required: []string{action}})
	}
	return schema, nil
}
