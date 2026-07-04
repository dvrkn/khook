package spec

import (
	"strings"
	"testing"
)

func TestEvalWhen(t *testing.T) {
	vars := map[string]string{"ENABLE_ARGOCD": "true", "ENV": "prod"}
	tests := []struct {
		name string
		expr string
		want bool
	}{
		{"var equality true", `vars.ENABLE_ARGOCD == "true"`, true},
		{"var equality false", `vars.ENV == "dev"`, false},
		{"boolean operators", `vars.ENV == "prod" && vars.ENABLE_ARGOCD != "false"`, true},
		{"has on set var", `has(vars.ENV)`, true},
		{"has on unset var", `has(vars.MISSING)`, false},
		{"in operator", `vars.ENV in ["prod", "staging"]`, true},
		{"string function", `vars.ENV.startsWith("pr")`, true},
		{"get helper set", `vars.get("ENV", "dev") == "prod"`, true},
		{"get helper default", `vars.get("MISSING", "fallback") == "fallback"`, true},
		{"ternary", `(has(vars.REPLICAS) ? vars.REPLICAS : "2") == "2"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvalWhen(tt.expr, vars)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("EvalWhen(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvalWhenErrors(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantSub string
	}{
		{"unset var access", `vars.MISSING == "x"`, "no such key"},
		{"syntax error", `vars.ENV ==`, "Syntax error"},
		{"non-bool result", `vars.ENV`, "must evaluate to a bool"},
		{"unknown identifier", `env == "prod"`, "undeclared reference"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EvalWhen(tt.expr, map[string]string{"ENV": "prod"})
			if err == nil {
				t.Fatalf("EvalWhen(%q): want error", tt.expr)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("EvalWhen(%q) error = %v, want substring %q", tt.expr, err, tt.wantSub)
			}
		})
	}
}

func TestEvalWhenNilVars(t *testing.T) {
	got, err := EvalWhen(`vars.get("X", "no") == "no" && !has(vars.X)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("want true against empty vars")
	}
}

func TestCheckWhen(t *testing.T) {
	if err := CheckWhen(`vars.ANY == "x"`); err != nil {
		t.Errorf("valid expression rejected: %v", err)
	}
	// Compile-checking must not evaluate: unset vars are fine here.
	if err := CheckWhen(`vars.get("MISSING", "d") == "d"`); err != nil {
		t.Errorf("valid expression rejected: %v", err)
	}
	if err := CheckWhen(`size(vars)`); err == nil || !strings.Contains(err.Error(), "bool") {
		t.Errorf("non-bool expression: got %v, want bool type error", err)
	}
}
