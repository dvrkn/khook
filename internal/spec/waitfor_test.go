package spec

import (
	"strings"
	"testing"
)

func TestParseWaitFor(t *testing.T) {
	tests := []struct {
		in   string
		want WaitFor
	}{
		{"delete", WaitFor{Mode: WaitForDelete}},
		{"condition=Ready", WaitFor{Mode: WaitForCondition, Expr: "Ready", Value: "True"}},
		{"condition=Ready=False", WaitFor{Mode: WaitForCondition, Expr: "Ready", Value: "False"}},
		{"jsonpath={.status.phase}=Running", WaitFor{Mode: WaitForJSONPath, Expr: "{.status.phase}", Value: "Running"}},
		{"jsonpath={.status.phase}", WaitFor{Mode: WaitForJSONPath, Expr: "{.status.phase}", Value: ""}},
		{"jsonpath=.status.phase=Running", WaitFor{Mode: WaitForJSONPath, Expr: "{.status.phase}", Value: "Running"}},
		{"jsonpath=status.phase", WaitFor{Mode: WaitForJSONPath, Expr: "{.status.phase}", Value: ""}},
		// A filter expression contains "=" inside the braces; the value
		// separator is the "=" after the closing brace.
		{
			`jsonpath={.status.conditions[?(@.type=="Ready")].status}=True`,
			WaitFor{Mode: WaitForJSONPath, Expr: `{.status.conditions[?(@.type=="Ready")].status}`, Value: "True"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseWaitFor(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if *got != tt.want {
				t.Fatalf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestParseWaitForErrors(t *testing.T) {
	tests := []struct {
		in      string
		wantSub string
	}{
		{"ready", "for must be"},
		{"", "for must be"},
		{"condition=", "condition name is empty"},
		{"jsonpath=", "expression is empty"},
		{"jsonpath={.status.phase", "closing brace"},
		{"jsonpath={.status.phase}Running", "unexpected trailing"},
		{"jsonpath=.status{.phase}", "mixes braced and bare"},
		{"jsonpath={.status[}", "invalid jsonpath"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := ParseWaitFor(tt.in)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tt.wantSub)
			}
		})
	}
}
