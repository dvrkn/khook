package ops

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/dvrkn/khook/internal/spec"
)

// loadManifests renders every source in order into unstructured objects.
// Every source may contain multi-document YAML; empty documents are skipped.
func (e *Executor) loadManifests(ctx context.Context, sources []spec.ManifestSource) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	for i, src := range sources {
		raw, origin, err := readSource(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("manifests[%d]: %w", i, err)
		}
		docs, err := splitDocuments(raw)
		if err != nil {
			return nil, fmt.Errorf("manifests[%d] (%s): %w", i, origin, err)
		}
		for j, doc := range docs {
			obj := &unstructured.Unstructured{}
			if err := utilyaml.Unmarshal(doc, &obj.Object); err != nil {
				return nil, fmt.Errorf("manifests[%d] (%s) document %d: %w", i, origin, j, err)
			}
			if len(obj.Object) == 0 {
				continue
			}
			if obj.GetKind() == "" || obj.GetAPIVersion() == "" {
				return nil, fmt.Errorf("manifests[%d] (%s) document %d: missing apiVersion or kind", i, origin, j)
			}
			objs = append(objs, obj)
		}
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("manifests contained no resources")
	}
	return objs, nil
}

func readSource(ctx context.Context, src spec.ManifestSource) (raw []byte, origin string, err error) {
	switch {
	case src.Inline != "":
		return []byte(src.Inline), "inline", nil
	case src.File != "":
		raw, err := os.ReadFile(src.File)
		if err != nil {
			return nil, src.File, err
		}
		return raw, src.File, nil
	case src.URL != "":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
		if err != nil {
			return nil, src.URL, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, src.URL, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, src.URL, fmt.Errorf("fetching %s: HTTP %d", src.URL, resp.StatusCode)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, src.URL, err
		}
		return raw, src.URL, nil
	}
	return nil, "", fmt.Errorf("manifest source is empty")
}

// splitDocuments splits multi-document YAML on "---" boundaries.
func splitDocuments(raw []byte) ([][]byte, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var docs [][]byte
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		docs = append(docs, doc)
	}
}

// describe renders an object as kind/name[@namespace] for logs and errors.
func describe(obj *unstructured.Unstructured) string {
	id := strings.ToLower(obj.GetKind()) + "/" + obj.GetName()
	if ns := obj.GetNamespace(); ns != "" {
		id += "@" + ns
	}
	return id
}
