package spec

import (
	"fmt"
	"strings"

	"k8s.io/client-go/util/jsonpath"
)

// WaitFor modes.
const (
	WaitForDelete    = "delete"
	WaitForCondition = "condition"
	WaitForJSONPath  = "jsonpath"
)

// WaitFor is the parsed form of wait.for / apply.waitFor:
// "delete", "condition=<Name>[=<value>]", or "jsonpath=<expr>[=<value>]".
type WaitFor struct {
	Mode string
	// Condition name, or the jsonpath template (normalized to "{...}" form).
	Expr string
	// Expected condition status (default "True") or jsonpath value ("" for
	// jsonpath means any non-empty result).
	Value string
}

func (w *WaitFor) String() string {
	switch w.Mode {
	case WaitForDelete:
		return WaitForDelete
	case WaitForJSONPath:
		if w.Value == "" {
			return "jsonpath=" + w.Expr
		}
		return fmt.Sprintf("jsonpath=%s=%s", w.Expr, w.Value)
	}
	return fmt.Sprintf("condition=%s=%s", w.Expr, w.Value)
}

// ParseWaitFor parses and validates a wait.for expression. Jsonpath
// expressions are compiled here so a bad path fails validation, not the run.
func ParseWaitFor(expr string) (*WaitFor, error) {
	switch {
	case expr == WaitForDelete:
		return &WaitFor{Mode: WaitForDelete}, nil
	case strings.HasPrefix(expr, "condition="):
		name, value, ok := strings.Cut(strings.TrimPrefix(expr, "condition="), "=")
		if name == "" {
			return nil, fmt.Errorf("condition name is empty")
		}
		if !ok {
			value = "True"
		}
		return &WaitFor{Mode: WaitForCondition, Expr: name, Value: value}, nil
	case strings.HasPrefix(expr, "jsonpath="):
		return parseJSONPathFor(strings.TrimPrefix(expr, "jsonpath="))
	}
	return nil, fmt.Errorf("for must be \"condition=<Name>[=<value>]\", \"jsonpath=<expr>[=<value>]\", or \"delete\", got %q", expr)
}

// parseJSONPathFor splits "<expr>[=<value>]" and normalizes the expression
// to a braced jsonpath template, kubectl's relaxed syntax: ".status.phase",
// "status.phase", and "{.status.phase}" are all accepted.
func parseJSONPathFor(s string) (*WaitFor, error) {
	expr, value := s, ""
	if strings.HasPrefix(s, "{") {
		// A braced expression may itself contain "=" (filters); the value
		// separator is the "=" after the final closing brace.
		end := strings.LastIndex(s, "}")
		if end < 0 {
			return nil, fmt.Errorf("jsonpath expression %q has no closing brace", s)
		}
		expr = s[:end+1]
		rest := s[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, "=") {
				return nil, fmt.Errorf("unexpected trailing %q after jsonpath expression", rest)
			}
			value = rest[1:]
		}
	} else if cut, v, ok := strings.Cut(s, "="); ok {
		expr, value = cut, v
	}
	if expr == "" {
		return nil, fmt.Errorf("jsonpath expression is empty")
	}
	tmpl, err := relaxedJSONPath(expr)
	if err != nil {
		return nil, err
	}
	if err := jsonpath.New("wait").Parse(tmpl); err != nil {
		return nil, fmt.Errorf("invalid jsonpath %q: %w", expr, err)
	}
	return &WaitFor{Mode: WaitForJSONPath, Expr: tmpl, Value: value}, nil
}

// relaxedJSONPath wraps a bare path into a jsonpath template the way kubectl
// does for wait/custom-columns.
func relaxedJSONPath(expr string) (string, error) {
	if strings.HasPrefix(expr, "{") {
		if !strings.HasSuffix(expr, "}") {
			return "", fmt.Errorf("jsonpath expression %q has no closing brace", expr)
		}
		return expr, nil
	}
	if strings.Contains(expr, "{") || strings.Contains(expr, "}") {
		return "", fmt.Errorf("jsonpath expression %q mixes braced and bare syntax", expr)
	}
	if !strings.HasPrefix(expr, ".") {
		expr = "." + expr
	}
	return "{" + expr + "}", nil
}
