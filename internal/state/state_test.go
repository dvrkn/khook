package state

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dvrkn/khook/internal/spec"
)

const specA = `
apiVersion: khook.io/v1
kind: Khook
metadata:
  name: test
state: {}
steps:
  - name: ns
    apply:
      manifests:
        - inline: "apiVersion: v1\nkind: Namespace\nmetadata: {name: ${NS}}"
`

// specB is specA with cosmetic-only edits: comments, key order, quoting.
const specB = `
# a comment
kind: "Khook"
apiVersion: khook.io/v1
metadata: {name: test}
state: {}
steps:
  - apply:
      manifests:
        - inline: "apiVersion: v1\nkind: Namespace\nmetadata: {name: ${NS}}"
    name: "ns"
`

func TestSpecHash(t *testing.T) {
	vars := map[string]string{"NS": "demo"}
	docA, err := spec.Parse([]byte(specA), vars)
	if err != nil {
		t.Fatal(err)
	}
	docB, err := spec.Parse([]byte(specB), vars)
	if err != nil {
		t.Fatal(err)
	}

	hashA, err := SpecHash(docA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hashA, "sha256:") {
		t.Fatalf("hash %q lacks sha256: prefix", hashA)
	}
	hashB, _ := SpecHash(docB)
	if hashA != hashB {
		t.Fatal("cosmetic YAML edits must not change the hash")
	}

	docC, err := spec.Parse([]byte(specA), map[string]string{"NS": "other"})
	if err != nil {
		t.Fatal(err)
	}
	hashC, _ := SpecHash(docC)
	if hashA == hashC {
		t.Fatal("a changed substituted value must change the hash")
	}
}

func testRecord() *Record {
	return &Record{
		APIVersion: RecordAPIVersion,
		SpecName:   "test",
		SpecHash:   "sha256:abc",
		RunStatus:  RunStatusOK,
		Steps:      []StepRecord{{Name: "ns", Type: "apply", Status: "ok"}},
	}
}

func TestStoreLoadMissing(t *testing.T) {
	store := NewStore(k8sfake.NewClientset(), "default", "khook-state-test")
	rec, err := store.Load(context.Background())
	if err != nil || rec != nil {
		t.Fatalf("want (nil, nil) for missing record, got (%v, %v)", rec, err)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	client := k8sfake.NewClientset()
	store := NewStore(client, "default", "khook-state-test")
	ctx := context.Background()

	if err := store.Save(ctx, testRecord()); err != nil {
		t.Fatal(err)
	}
	sec, err := client.CoreV1().Secrets("default").Get(ctx, "khook-state-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Labels[managedByLabelKey] != managedByLabelValue {
		t.Fatal("record secret must carry the managed-by label")
	}
	if sec.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret type = %q, want Opaque", sec.Type)
	}

	rec, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.SpecName != "test" || rec.Step("ns") == nil || rec.Step("ns").Status != "ok" {
		t.Fatalf("round-trip mismatch: %+v", rec)
	}

	// Update path.
	rec.RunStatus = RunStatusFailed
	if err := store.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec, _ = store.Load(ctx)
	if rec.RunStatus != RunStatusFailed {
		t.Fatalf("update lost: %+v", rec)
	}
}

func TestStoreRefusesForeignSecret(t *testing.T) {
	client := k8sfake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "khook-state-test", Namespace: "default"},
	})
	store := NewStore(client, "default", "khook-state-test")

	if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "not managed by khook") {
		t.Fatalf("Load must refuse a foreign secret, got %v", err)
	}
	if err := store.Save(context.Background(), testRecord()); err == nil || !strings.Contains(err.Error(), "not managed by khook") {
		t.Fatalf("Save must refuse a foreign secret, got %v", err)
	}
}

func TestStoreLoadCorrupt(t *testing.T) {
	client := k8sfake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "khook-state-test", Namespace: "default",
			Labels: map[string]string{managedByLabelKey: managedByLabelValue},
		},
		Data: map[string][]byte{DataKey: []byte("{not json")},
	})
	store := NewStore(client, "default", "khook-state-test")
	if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("want corrupt-record error, got %v", err)
	}
}

func TestStoreSaveConflictRetries(t *testing.T) {
	client := k8sfake.NewClientset()
	store := NewStore(client, "default", "khook-state-test")
	ctx := context.Background()
	if err := store.Save(ctx, testRecord()); err != nil {
		t.Fatal(err)
	}

	conflicts := 1
	client.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts > 0 {
			conflicts--
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "khook-state-test", nil)
		}
		return false, nil, nil
	})
	if err := store.Save(ctx, testRecord()); err != nil {
		t.Fatalf("Save must retry once on conflict, got %v", err)
	}
}
