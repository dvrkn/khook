package spec

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

// DefaultVarPrefix is the environment-variable prefix consumed by khook
// (KHOOK_VAR_FOO=x -> ${FOO}); configurable via --var-prefix.
const DefaultVarPrefix = "KHOOK_VAR_"

// DefaultSecretPrefix marks variables whose values must never appear in
// khook's own output (KHOOK_SECRET_FOO=x -> ${FOO}, redacted in logs, plan,
// and diff); configurable via --secret-prefix.
const DefaultSecretPrefix = "KHOOK_SECRET_"

// varPattern matches ${NAME}, ${NAME:-default}, ${NAME|pipeline} and
// ${NAME:-default|pipeline}. NAME is an identifier; the default may be any
// text without a closing brace or pipe; the pipeline is a sprig function
// chain (see pipeFuncs) applied to the resolved value. Strings like the
// bcrypt "$2a$10$..." deliberately do not match.
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}|]*))?[ \t]*(\|([^}]*))?\}`)

// pipeFuncs is the function set available in ${NAME|...} pipelines: sprig's
// hermetic map (no env/network/date/random — substitution must be
// deterministic and must not read ambient host state), minus the certificate
// and password generators, whose random output would also differ between
// plan and apply.
var pipeFuncs = hermeticPipeFuncs()

func hermeticPipeFuncs() template.FuncMap {
	funcs := sprig.HermeticTxtFuncMap()
	for _, name := range []string{
		"bcrypt", "htpasswd", "genPrivateKey", "genCA", "genCAWithKey",
		"genSelfSignedCert", "genSelfSignedCertWithKey", "genSignedCert",
		"genSignedCertWithKey",
	} {
		delete(funcs, name)
	}
	return funcs
}

// evalPipeline applies an author-written sprig pipeline to a resolved
// variable value: `${TOKEN|b64enc}` renders `{{ . | b64enc }}` with the value
// as dot. Only the pipeline text (spec-authored) is compiled; the value is
// data and is never parsed as a template.
func evalPipeline(name, value, pipeline string) (string, error) {
	tmpl, err := template.New(name).Funcs(pipeFuncs).Parse("{{ . | " + pipeline + " }}")
	if err != nil {
		return "", fmt.Errorf("${%s|%s}: %w", name, strings.TrimSpace(pipeline), err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, value); err != nil {
		return "", fmt.Errorf("${%s|%s}: %w", name, strings.TrimSpace(pipeline), err)
	}
	return buf.String(), nil
}

// MissingVariablesError reports every unresolved ${NAME} at once.
type MissingVariablesError struct {
	Names []string
}

func (e *MissingVariablesError) Error() string {
	return fmt.Sprintf("unresolved variables (no value and no default): %s", strings.Join(e.Names, ", "))
}

// Substitute replaces ${NAME} / ${NAME:-default} / ${NAME|pipeline} textually
// across the raw spec before YAML parsing. vars is the already-merged
// variable map (CLI --set > --var-file > prefixed env). Any reference with no
// value and no default is collected and returned as a single
// MissingVariablesError — a pipeline does not lift that requirement (a
// typo'd name must fail loud, not become an empty string).
func Substitute(raw []byte, vars map[string]string) ([]byte, error) {
	out, _, err := SubstituteTracking(raw, vars, nil)
	return out, err
}

// SubstituteTracking is Substitute plus derived-value tracking: the final
// output of every reference whose NAME is in track is returned in derived
// (deduplicated). The CLI uses this to redact pipeline transformations of
// secret variables (e.g. the b64enc of a secret) alongside the raw values.
func SubstituteTracking(raw []byte, vars map[string]string, track map[string]bool) ([]byte, []string, error) {
	missing := map[string]bool{}
	derivedSeen := map[string]bool{}
	var derived []string
	var pipeErrs []error
	out := varPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		groups := varPattern.FindSubmatch(match)
		name := string(groups[1])
		hasDefault := len(groups[2]) > 0 // ":-default" present (possibly empty default)
		pipeline := string(groups[5])
		hasPipe := len(groups[4]) > 0

		val, ok := vars[name]
		switch {
		case !ok && hasDefault:
			val = string(groups[3])
		case !ok:
			missing[name] = true
			return match
		}
		if hasPipe {
			res, err := evalPipeline(name, val, pipeline)
			if err != nil {
				pipeErrs = append(pipeErrs, err)
				return match
			}
			// The pipeline output of a tracked (secret) variable is as
			// sensitive as the input (b64enc is decodable); raw values are
			// already registered by the caller, only derivations are new.
			if ok && track[name] && res != "" && !derivedSeen[res] {
				derivedSeen[res] = true
				derived = append(derived, res)
			}
			val = res
		}
		return []byte(val)
	})
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, nil, &MissingVariablesError{Names: names}
	}
	if len(pipeErrs) > 0 {
		return nil, nil, errors.Join(pipeErrs...)
	}
	return out, derived, nil
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
