package spec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSubstitute(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		vars    map[string]string
		want    string
		missing []string
	}{
		{
			name: "simple",
			in:   "name: ${FOO}",
			vars: map[string]string{"FOO": "bar"},
			want: "name: bar",
		},
		{
			name: "default used when unset",
			in:   "replicas: ${REPLICAS:-2}",
			want: "replicas: 2",
		},
		{
			name: "value wins over default",
			in:   "replicas: ${REPLICAS:-2}",
			vars: map[string]string{"REPLICAS": "5"},
			want: "replicas: 5",
		},
		{
			name: "empty default",
			in:   "suffix: '${SUFFIX:-}'",
			want: "suffix: ''",
		},
		{
			name: "empty value is a value",
			in:   "suffix: '${SUFFIX:-fallback}'",
			vars: map[string]string{"SUFFIX": ""},
			want: "suffix: ''",
		},
		{
			name: "bcrypt-style dollar strings untouched",
			in:   `password: "$2a$10$9XihPJaud829"`,
			want: `password: "$2a$10$9XihPJaud829"`,
		},
		{
			name: "plain dollar untouched",
			in:   "cmd: echo $HOME ${}",
			want: "cmd: echo $HOME ${}",
		},
		{
			name:    "missing reported sorted and deduplicated",
			in:      "a: ${ZED}\nb: ${ALPHA}\nc: ${ZED}",
			missing: []string{"ALPHA", "ZED"},
		},
		{
			name: "multiple in one line",
			in:   "image: ${REPO}/${NAME}:${TAG:-latest}",
			vars: map[string]string{"REPO": "r", "NAME": "n"},
			want: "image: r/n:latest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Substitute([]byte(tt.in), tt.vars)
			if tt.missing != nil {
				var missingErr *MissingVariablesError
				if !errors.As(err, &missingErr) {
					t.Fatalf("want MissingVariablesError, got %v", err)
				}
				if !reflect.DeepEqual(missingErr.Names, tt.missing) {
					t.Fatalf("missing = %v, want %v", missingErr.Names, tt.missing)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVarsFromEnviron(t *testing.T) {
	environ := []string{
		"KHOOK_VAR_APP_NAME=my-app",
		"KHOOK_VAR_EMPTY=",
		"PATH=/usr/bin",
		"KHOOK_VAR_=nameless",
		"NOEQUALS",
	}
	got := VarsFromEnviron(environ, DefaultVarPrefix)
	want := map[string]string{"APP_NAME": "my-app", "EMPTY": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeVarsPrecedence(t *testing.T) {
	env := map[string]string{"A": "env", "B": "env", "C": "env"}
	file := map[string]string{"B": "file", "C": "file"}
	set := map[string]string{"C": "set"}
	got := MergeVars(env, file, set)
	want := map[string]string{"A": "env", "B": "file", "C": "set"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSubstitutePipelines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		vars map[string]string
		want string
	}{
		{
			name: "single function",
			in:   "name: ${APP|upper}",
			vars: map[string]string{"APP": "khook"},
			want: "name: KHOOK",
		},
		{
			name: "spaces around pipes",
			in:   "name: ${APP | lower | trunc 3}",
			vars: map[string]string{"APP": "KHOOK"},
			want: "name: kho",
		},
		{
			name: "function with string args",
			in:   "host: ${HOST | replace \".\" \"-\"}",
			vars: map[string]string{"HOST": "a.b.c"},
			want: "host: a-b-c",
		},
		{
			name: "b64enc",
			in:   "token: ${TOKEN|b64enc}",
			vars: map[string]string{"TOKEN": "hush"},
			want: "token: aHVzaA==",
		},
		{
			name: "default feeds the pipeline when unset",
			in:   "name: ${APP:-fallback|upper}",
			want: "name: FALLBACK",
		},
		{
			name: "value wins over default before the pipeline",
			in:   "name: ${APP:-fallback|upper}",
			vars: map[string]string{"APP": "set"},
			want: "name: SET",
		},
		{
			name: "sprig default sees empty value",
			in:   "name: ${APP | default \"none\"}",
			vars: map[string]string{"APP": ""},
			want: "name: none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Substitute([]byte(tt.in), tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubstitutePipelineErrors(t *testing.T) {
	t.Run("unknown function is a load error", func(t *testing.T) {
		_, err := Substitute([]byte("x: ${A|nosuchfn}"), map[string]string{"A": "v"})
		if err == nil || !strings.Contains(err.Error(), "nosuchfn") {
			t.Fatalf("want unknown-function error naming nosuchfn, got %v", err)
		}
	})
	t.Run("missing variable stays strict with a pipeline", func(t *testing.T) {
		_, err := Substitute([]byte("x: ${UNSET|upper}"), nil)
		var missingErr *MissingVariablesError
		if !errors.As(err, &missingErr) || !reflect.DeepEqual(missingErr.Names, []string{"UNSET"}) {
			t.Fatalf("want MissingVariablesError for UNSET, got %v", err)
		}
	})
	t.Run("hermetic map excludes env and random", func(t *testing.T) {
		for _, fn := range []string{"env \"PATH\"", "expandenv", "uuidv4", "randAlphaNum 8", "bcrypt", "now"} {
			_, err := Substitute([]byte("x: ${A|"+fn+"}"), map[string]string{"A": "v"})
			if err == nil {
				t.Errorf("pipeline %q should be rejected", fn)
			}
		}
	})
	t.Run("all pipeline errors reported at once", func(t *testing.T) {
		_, err := Substitute([]byte("x: ${A|bad1}\ny: ${A|bad2}"), map[string]string{"A": "v"})
		if err == nil || !strings.Contains(err.Error(), "bad1") || !strings.Contains(err.Error(), "bad2") {
			t.Fatalf("want both errors, got %v", err)
		}
	})
}

func TestSubstituteTrackingDerived(t *testing.T) {
	vars := map[string]string{"TOKEN": "hush", "PLAIN": "open"}
	track := map[string]bool{"TOKEN": true}
	in := "a: ${TOKEN|b64enc}\nb: ${TOKEN|b64enc}\nc: ${PLAIN|b64enc}\nd: ${TOKEN}"
	out, derived, err := SubstituteTracking([]byte(in), vars, track)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "aHVzaA==") {
		t.Fatalf("substitution missing: %s", out)
	}
	// Only the tracked variable's pipeline output, once; the raw value is the
	// caller's job, and PLAIN is not tracked.
	if !reflect.DeepEqual(derived, []string{"aHVzaA=="}) {
		t.Fatalf("derived = %v, want [aHVzaA==]", derived)
	}
}
