package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRedactorString(t *testing.T) {
	r := &redactor{}
	r.Add("s3cr3t", "", "hunter2")

	got := r.String(`password: s3cr3t token=hunter2 plain`)
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "hunter2") {
		t.Fatalf("secret survived redaction: %q", got)
	}
	if got != `password: *** token=*** plain` {
		t.Errorf("got %q", got)
	}
}

func TestRedactorOverlappingValues(t *testing.T) {
	// The longer secret must be masked as a whole, not broken by masking its
	// substring first.
	r := &redactor{}
	r.Add("abc")
	r.Add("abcdef")
	if got := r.String("x abcdef y abc z"); got != "x *** y *** z" {
		t.Errorf("got %q", got)
	}
}

func TestRedactorJSONEscapedForm(t *testing.T) {
	r := &redactor{}
	r.Add(`pa"ss`)
	// As rendered inside a JSON string the value appears escaped.
	if got := r.String(`{"error":"auth pa\"ss rejected"}`); strings.Contains(got, `pa\"ss`) {
		t.Errorf("JSON-escaped secret survived: %q", got)
	}
}

func TestRedactorWrapWriter(t *testing.T) {
	r := &redactor{}
	var buf bytes.Buffer
	w := r.Wrap(&buf)

	// Values registered after wrapping must still be masked.
	r.Add("s3cr3t")
	n, err := fmt.Fprintf(w, "value is s3cr3t\n")
	if err != nil {
		t.Fatal(err)
	}
	if n != len("value is s3cr3t\n") {
		t.Errorf("reported %d bytes, want caller length %d", n, len("value is s3cr3t\n"))
	}
	if got := buf.String(); got != "value is ***\n" {
		t.Errorf("got %q", got)
	}
}

func TestRedactorNilSafe(t *testing.T) {
	var r *redactor
	r.Add("x")
	if got := r.String("x"); got != "x" {
		t.Errorf("nil redactor changed input: %q", got)
	}
	var buf bytes.Buffer
	if w := r.Wrap(&buf); w != &buf {
		t.Error("nil redactor should return the writer unchanged")
	}
}

func TestSecretPrefixVariables(t *testing.T) {
	t.Setenv("KHOOK_VAR_PLAIN", "visible")
	t.Setenv("KHOOK_SECRET_TOKEN", "t0ps3cret")
	t.Setenv("KHOOK_VAR_TOKEN", "shadowed")

	r := &redactor{}
	f := &specFlags{
		varPrefix:    "KHOOK_VAR_",
		secretPrefix: "KHOOK_SECRET_",
		redact:       r,
	}
	vars, err := f.variables()
	if err != nil {
		t.Fatal(err)
	}
	if vars["PLAIN"] != "visible" {
		t.Errorf("PLAIN = %q", vars["PLAIN"])
	}
	if vars["TOKEN"] != "t0ps3cret" {
		t.Errorf("secret prefix should win over plain env for the same name, TOKEN = %q", vars["TOKEN"])
	}
	if got := r.String("x t0ps3cret y"); got != "x *** y" {
		t.Errorf("secret value not registered for redaction: %q", got)
	}
	if got := r.String("visible"); got != "visible" {
		t.Errorf("plain variable value must not be redacted: %q", got)
	}
}
