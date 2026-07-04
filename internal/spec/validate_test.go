package spec

import (
	"strings"
	"testing"
)

func validDoc() *Document {
	return &Document{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "test"},
		Steps: []Step{
			{Name: "one", Apply: &ApplyOp{Manifests: []ManifestSource{{Inline: "x"}}}},
		},
	}
}

func TestValidateOK(t *testing.T) {
	if err := Validate(validDoc()); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Document)
		wantSub string
	}{
		{"wrong apiVersion", func(d *Document) { d.APIVersion = "v2" }, "apiVersion"},
		{"wrong kind", func(d *Document) { d.Kind = "ClusterBootstrap" }, "kind"},
		{"missing name", func(d *Document) { d.Metadata.Name = "" }, "metadata.name"},
		{"no steps", func(d *Document) { d.Steps = nil }, "at least one step"},
		{"bad defaults onError", func(d *Document) { d.Defaults.OnError = "retry" }, "onError"},
		{"step missing name", func(d *Document) { d.Steps[0].Name = "" }, "name is required"},
		{"bad step name", func(d *Document) { d.Steps[0].Name = "Bad_Name" }, "must match"},
		{"negative retries", func(d *Document) { d.Steps[0].Retries = ptrTo(-1) }, "retries"},
		{
			"duplicate names",
			func(d *Document) { d.Steps = append(d.Steps, d.Steps[0]) },
			"duplicate",
		},
		{
			"unknown needs",
			func(d *Document) { d.Steps[0].Needs = []string{"ghost"} },
			`needs unknown step "ghost"`,
		},
		{"no action", func(d *Document) { d.Steps[0].Apply = nil }, "got none"},
		{
			"two actions",
			func(d *Document) { d.Steps[0].Wait = &WaitOp{For: "condition=Ready", On: "pods"} },
			"got 2",
		},
		{
			"helm missing chart",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Helm = &HelmOp{Repo: "https://example.com"}
			},
			"chart is required",
		},
		{
			"helm bad repo",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Helm = &HelmOp{Chart: "c", Repo: "oci://example.com"}
			},
			"HTTP(S)",
		},
		{
			"helm valuesFrom without file",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Helm = &HelmOp{Chart: "c", Repo: "https://x", ValuesFrom: []ValuesSource{{}}}
			},
			"valuesFrom[0]",
		},
		{"apply no manifests", func(d *Document) { d.Steps[0].Apply.Manifests = nil }, "at least one source"},
		{
			"apply createNamespace without namespace",
			func(d *Document) { d.Steps[0].Apply.CreateNamespace = true },
			"requires namespace",
		},
		{
			"manifest with two sources",
			func(d *Document) { d.Steps[0].Apply.Manifests[0].File = "f.yaml" },
			"exactly one of inline, file, url",
		},
		{
			"delete neither form",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{}
			},
			"exactly one of manifests or resource",
		},
		{
			"delete both forms",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Resource: "pods", Manifests: []ManifestSource{{Inline: "x"}}}
			},
			"exactly one of manifests or resource",
		},
		{
			"delete ns and allNamespaces",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Resource: "pods", Namespace: "a", AllNamespaces: true}
			},
			"mutually exclusive",
		},
		{
			"delete kind/name with selector",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Resource: "daemonset/aws-node", Selector: "a=b"}
			},
			"selectors cannot be combined",
		},
		{
			"wait missing on",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Wait = &WaitOp{For: "condition=Ready"}
			},
			"on is required",
		},
		{
			"wait bad for",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Wait = &WaitOp{For: "ready", On: "pods"}
			},
			"condition=",
		},
		{
			"rollout both restart and status",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Rollout = &RolloutOp{Restart: "deployment/a", Status: "deployment/a", Namespace: "x"}
			},
			"exactly one of restart or status",
		},
		{
			"rollout missing namespace",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Rollout = &RolloutOp{Restart: "deployment/a"}
			},
			"namespace is required",
		},
		{
			"rollout bad kind",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Rollout = &RolloutOp{Restart: "pod/a", Namespace: "x"}
			},
			"expected deployment/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := validDoc()
			tt.mutate(doc)
			err := Validate(doc)
			if err == nil {
				t.Fatal("want validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidateAggregatesProblems(t *testing.T) {
	doc := validDoc()
	doc.APIVersion = "bogus"
	doc.Kind = "bogus"
	doc.Metadata.Name = ""
	err := Validate(doc)
	verr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(verr.Problems) != 3 {
		t.Fatalf("want 3 problems, got %d: %v", len(verr.Problems), verr.Problems)
	}
}

func ptrTo[T any](v T) *T { return &v }
