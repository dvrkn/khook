package spec

import (
	"errors"
	"reflect"
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
