package state

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The record Secret carries this label; Load and Save refuse to touch a
// Secret khook does not own (mirrors the job executor's ownership rule).
const (
	managedByLabelKey   = "app.kubernetes.io/managed-by"
	managedByLabelValue = "khook"
)

// Store reads and writes one spec's record Secret.
type Store struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
}

func NewStore(client kubernetes.Interface, namespace, name string) *Store {
	return &Store{Client: client, Namespace: namespace, Name: name}
}

// Ref renders "namespace/name" for messages.
func (s *Store) Ref() string {
	return s.Namespace + "/" + s.Name
}

// Load returns the stored record, (nil, nil) when no Secret exists, and an
// error for anything else — an unreadable Secret, one not owned by khook,
// or a corrupt record.
func (s *Store) Load(ctx context.Context) (*Record, error) {
	sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state record %s: %w", s.Ref(), err)
	}
	if sec.Labels[managedByLabelKey] != managedByLabelValue {
		return nil, fmt.Errorf("secret %s exists but is not managed by khook — delete it or set state.name", s.Ref())
	}
	raw, ok := sec.Data[DataKey]
	if !ok {
		return nil, fmt.Errorf("state record %s has no %q key", s.Ref(), DataKey)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("state record %s is corrupt: %w — delete the secret to start fresh", s.Ref(), err)
	}
	return &rec, nil
}

// Save creates or updates the record Secret. On a resourceVersion conflict
// it re-reads and retries once — the in-memory record is authoritative
// (last-write-wins; concurrent runners are not supported).
func (s *Store) Save(ctx context.Context, rec *Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding state record: %w", err)
	}

	secrets := s.Client.CoreV1().Secrets(s.Namespace)
	for attempt := 0; ; attempt++ {
		existing, err := secrets.Get(ctx, s.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = secrets.Create(ctx, s.newSecret(raw), metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) && attempt == 0 {
				continue // lost a create race; retry as update
			}
			if err != nil {
				return fmt.Errorf("writing state record %s: %w", s.Ref(), err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading state record %s: %w", s.Ref(), err)
		}
		if existing.Labels[managedByLabelKey] != managedByLabelValue {
			return fmt.Errorf("secret %s exists but is not managed by khook — delete it or set state.name", s.Ref())
		}
		existing.Data = map[string][]byte{DataKey: raw}
		_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) && attempt == 0 {
			continue
		}
		if err != nil {
			return fmt.Errorf("writing state record %s: %w", s.Ref(), err)
		}
		return nil
	}
}

func (s *Store) newSecret(raw []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.Name,
			Namespace: s.Namespace,
			Labels:    map[string]string{managedByLabelKey: managedByLabelValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{DataKey: raw},
	}
}
