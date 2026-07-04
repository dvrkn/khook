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

func TestValidateKubectlDepthOK(t *testing.T) {
	doc := validDoc()
	doc.Steps[0].Apply.WaitFor = "condition=Established"
	doc.Steps = append(doc.Steps,
		Step{Name: "kustomized", Apply: &ApplyOp{
			WaitFor:   "jsonpath={.status.readyReplicas}=1",
			Manifests: []ManifestSource{{Kustomize: "./overlays/prod"}},
		}},
		Step{Name: "evict", Patch: &PatchOp{
			Target: "daemonset/aws-node", Namespace: "kube-system",
			Patch: []byte(`{"spec":{}}`),
		}},
		Step{Name: "surgery", Patch: &PatchOp{
			Target: "configmap/argocd-cm", Type: PatchJSON,
			Patch: []byte(`[{"op":"remove","path":"/data/x"}]`),
		}},
		Step{Name: "phase", Wait: &WaitOp{
			For: "jsonpath={.status.phase}=Running", On: "pods",
			FieldSelector: "status.phase!=Succeeded",
		}},
	)
	if err := Validate(doc); err != nil {
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
		{"bad state namespace", func(d *Document) { d.State = &StateSpec{Namespace: "Bad_NS"} }, "state: namespace"},
		{"bad state name", func(d *Document) { d.State = &StateSpec{Name: "Bad_Name"} }, "state: name"},
		{
			"metadata.name derives bad state name",
			func(d *Document) { d.Metadata.Name = "My Cluster"; d.State = &StateSpec{} },
			"set state.name explicitly",
		},
		{"step missing name", func(d *Document) { d.Steps[0].Name = "" }, "name is required"},
		{"bad step name", func(d *Document) { d.Steps[0].Name = "Bad_Name" }, "must match"},
		{"negative retries", func(d *Document) { d.Steps[0].Retries = ptrTo(-1) }, "retries"},
		{"when syntax error", func(d *Document) { d.Steps[0].When = `vars.X ==` }, "when"},
		{"when not a bool", func(d *Document) { d.Steps[0].When = `vars.X` }, "bool"},
		{"when unknown identifier", func(d *Document) { d.Steps[0].When = `flag == "on"` }, "undeclared reference"},
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
		{
			"helm valuesFrom with file and url",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Helm = &HelmOp{Chart: "c", Repo: "https://x",
					ValuesFrom: []ValuesSource{{File: "v.yaml", URL: "https://example.com/v.yaml"}}}
			},
			"exactly one of file, url",
		},
		{
			"helm valuesFrom non-http url",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Helm = &HelmOp{Chart: "c", Repo: "https://x",
					ValuesFrom: []ValuesSource{{URL: "ftp://example.com/v.yaml"}}}
			},
			"url must be HTTP(S)",
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
			"exactly one of manifests, resource, or release",
		},
		{
			"delete both forms",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Resource: "pods", Manifests: []ManifestSource{{Inline: "x"}}}
			},
			"exactly one of manifests, resource, or release",
		},
		{
			"delete release with resource",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Release: "argocd", Resource: "pods"}
			},
			"exactly one of manifests, resource, or release",
		},
		{
			"delete release with selector",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Release: "argocd", Selector: "a=b"}
			},
			"apply only to the resource form",
		},
		{
			"delete release with allNamespaces",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Delete = &DeleteOp{Release: "argocd", AllNamespaces: true}
			},
			"apply only to the resource form",
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
			"wait empty condition name",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Wait = &WaitOp{For: "condition=", On: "pods"}
			},
			"condition name is empty",
		},
		{
			"wait bad jsonpath",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Wait = &WaitOp{For: "jsonpath={.status.phase", On: "pods"}
			},
			"closing brace",
		},
		{
			"wait jsonpath parse error",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Wait = &WaitOp{For: "jsonpath={.status[}", On: "pods"}
			},
			"invalid jsonpath",
		},
		{
			"apply waitFor delete",
			func(d *Document) { d.Steps[0].Apply.WaitFor = "delete" },
			"cannot be \"delete\"",
		},
		{
			"apply waitFor bad grammar",
			func(d *Document) { d.Steps[0].Apply.WaitFor = "ready" },
			"waitFor",
		},
		{
			"kustomize bare path",
			func(d *Document) {
				d.Steps[0].Apply.Manifests = []ManifestSource{{Kustomize: "overlays/prod"}}
			},
			"local path",
		},
		{
			"patch bad target",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Patch = &PatchOp{Target: "aws-node", Patch: []byte(`{"a":1}`)}
			},
			"target must be <kind>/<name>",
		},
		{
			"patch bad type",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Patch = &PatchOp{Target: "daemonset/aws-node", Type: "smart", Patch: []byte(`{"a":1}`)}
			},
			"type must be",
		},
		{
			"patch missing body",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Patch = &PatchOp{Target: "daemonset/aws-node"}
			},
			"patch body is required",
		},
		{
			"patch json body not a list",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Patch = &PatchOp{Target: "daemonset/aws-node", Type: "json", Patch: []byte(`{"a":1}`)}
			},
			"list of operations",
		},
		{
			"patch merge body not a mapping",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Patch = &PatchOp{Target: "daemonset/aws-node", Type: "merge", Patch: []byte(`[1]`)}
			},
			"must be a mapping",
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
		{
			"job missing image",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Job = &JobOp{}
			},
			"image is required",
		},
		{
			"job empty env key",
			func(d *Document) {
				d.Steps[0].Apply = nil
				d.Steps[0].Job = &JobOp{Image: "alpine", Env: map[string]string{"": "x"}}
			},
			"env keys",
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

func TestStateSpecResolvers(t *testing.T) {
	doc := validDoc()
	if doc.StateEnabled() {
		t.Fatal("absent state: block must mean disabled")
	}

	doc.State = &StateSpec{}
	if err := Validate(doc); err != nil {
		t.Fatal(err)
	}
	if !doc.StateEnabled() {
		t.Fatal("present state: block must default to enabled")
	}
	if got := doc.State.TargetNamespace(); got != "default" {
		t.Fatalf("default namespace = %q, want %q", got, "default")
	}
	if got := doc.State.SecretName(doc.Metadata.Name); got != "khook-state-test" {
		t.Fatalf("derived name = %q, want %q", got, "khook-state-test")
	}

	doc.State = &StateSpec{Enabled: ptrTo(false), Namespace: "kube-system", Name: "my-state"}
	if doc.StateEnabled() {
		t.Fatal("enabled: false must disable")
	}
	if got := doc.State.TargetNamespace(); got != "kube-system" {
		t.Fatalf("namespace = %q, want %q", got, "kube-system")
	}
	if got := doc.State.SecretName(doc.Metadata.Name); got != "my-state" {
		t.Fatalf("name = %q, want %q", got, "my-state")
	}
}
