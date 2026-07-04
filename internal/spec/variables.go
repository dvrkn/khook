package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DefaultVarPrefix is the environment-variable prefix consumed by khook
// (KHOOK_VAR_FOO=x -> ${FOO}); configurable via --var-prefix.
const DefaultVarPrefix = "KHOOK_VAR_"

// varPattern matches ${NAME} and ${NAME:-default}. NAME is an identifier;
// the default may be any text without a closing brace. Strings like the
// bcrypt "$2a$10$..." deliberately do not match.
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// MissingVariablesError reports every unresolved ${NAME} at once.
type MissingVariablesError struct {
	Names []string
}

func (e *MissingVariablesError) Error() string {
	return fmt.Sprintf("unresolved variables (no value and no default): %s", strings.Join(e.Names, ", "))
}

// Substitute replaces ${NAME} / ${NAME:-default} textually across the raw
// spec before YAML parsing. vars is the already-merged variable map (CLI
// --set > --var-file > prefixed env). Any reference with no value and no
// default is collected and returned as a single MissingVariablesError.
func Substitute(raw []byte, vars map[string]string) ([]byte, error) {
	missing := map[string]bool{}
	out := varPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		groups := varPattern.FindSubmatch(match)
		name := string(groups[1])
		if val, ok := vars[name]; ok {
			return []byte(val)
		}
		if len(groups[2]) > 0 { // ":-default" present (possibly empty default)
			return groups[3]
		}
		missing[name] = true
		return match
	})
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, &MissingVariablesError{Names: names}
	}
	return out, nil
}

// VarsFromEnviron extracts prefixed variables from an os.Environ()-style
// list: prefix "KHOOK_VAR_" turns KHOOK_VAR_FOO=x into {"FOO": "x"}.
func VarsFromEnviron(environ []string, prefix string) map[string]string {
	vars := map[string]string{}
	for _, kv := range environ {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, prefix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		if name != "" {
			vars[name] = val
		}
	}
	return vars
}

// MergeVars layers variable maps left to right, later maps taking precedence
// (pass env, then --var-file, then --set).
func MergeVars(layers ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, layer := range layers {
		for k, v := range layer {
			merged[k] = v
		}
	}
	return merged
}
