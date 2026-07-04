package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	yamlv3 "go.yaml.in/yaml/v3"
)

const committedSchema = "../../schema/v1/khook.json"

// The committed artifact is the published face of the frozen v1 schema — it
// must always equal what `khook schema` prints.
func TestCommittedSchemaCurrent(t *testing.T) {
	schema, err := generateSchema()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	generated = append(generated, '\n') // the command prints with a trailing newline

	committed, err := os.ReadFile(committedSchema)
	if err != nil {
		t.Fatalf("reading committed schema (run hack/gen-schema.sh?): %v", err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("schema/v1/khook.json is stale — run hack/gen-schema.sh and commit the result")
	}
}

// Every example must validate against the published schema as authored —
// before variable substitution, exactly as an editor sees it.
func TestExamplesValidateAgainstSchema(t *testing.T) {
	schema, err := generateSchema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("khook.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("khook.json")
	if err != nil {
		t.Fatal(err)
	}

	examples, err := filepath.Glob("../../examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) == 0 {
		t.Fatal("no examples found")
	}
	for _, path := range examples {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Decode with yaml.v3 like the parser does — sigs.k8s.io/yaml
			// would turn the unquoted key `on` into boolean true.
			var tree any
			if err := yamlv3.Unmarshal(src, &tree); err != nil {
				t.Fatal(err)
			}
			asJSON, err := json.Marshal(tree)
			if err != nil {
				t.Fatal(err)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
			if err != nil {
				t.Fatal(err)
			}
			if err := compiled.Validate(instance); err != nil {
				t.Errorf("does not validate against the schema:\n%v", err)
			}
		})
	}
}
