package cli

import (
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
)

const redactedMask = "***"

// redactor masks registered secret values in khook's own output (logs, plan,
// diff, summaries). Substitution is textual, so a secret injected into a
// manifest would otherwise be printed back verbatim by plan --diff or error
// messages. Values are registered once variables are loaded; wrapped writers
// created earlier pick them up on subsequent writes.
type redactor struct {
	mu     sync.RWMutex
	values []string
}

// Add registers secret values for masking. Empty values are ignored.
func (r *redactor) Add(values ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if v == "" {
			continue
		}
		r.values = append(r.values, v)
		// A secret with JSON-special characters appears escaped in JSON logs
		// and --output json; mask that rendering too.
		if enc, err := json.Marshal(v); err == nil {
			if escaped := string(enc[1 : len(enc)-1]); escaped != v {
				r.values = append(r.values, escaped)
			}
		}
	}
	// Longest first, so a secret containing another secret masks fully.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// String returns s with every registered secret value masked.
func (r *redactor) String(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, redactedMask)
	}
	return s
}

// Wrap returns a writer that masks secrets in everything written through it.
// Masking is per Write call; values split across writes are not matched, which
// is fine for the line-at-a-time writers khook uses.
func (r *redactor) Wrap(w io.Writer) io.Writer {
	if r == nil {
		return w
	}
	return &redactWriter{r: r, w: w}
}

type redactWriter struct {
	r *redactor
	w io.Writer
}

func (rw *redactWriter) Write(p []byte) (int, error) {
	masked := rw.r.String(string(p))
	if _, err := rw.w.Write([]byte(masked)); err != nil {
		return 0, err
	}
	// Report the caller's byte count: masking changes length, and short
	// writes would make fmt.Fprintf callers mis-report errors.
	return len(p), nil
}
