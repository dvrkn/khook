package spec

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

// when: is a CEL expression (https://cel.dev) evaluated once at spec load
// time, after variable merging — it sees no cluster state. The environment
// exposes:
//
//   - vars                    the merged variable map (all values are strings)
//   - vars.get(name, def)     vars[name], or def when the variable is unset
//
// ${NAME} substitution runs textually before parsing, so inside when: the
// spelling is vars.NAME / vars.get("NAME", ...), never ${NAME} — a
// substituted value would be spliced into the expression as bare tokens.

func whenEnv() (*cel.Env, error) {
	varsType := cel.MapType(cel.StringType, cel.StringType)
	return cel.NewEnv(
		cel.Variable("vars", varsType),
		cel.Function("get",
			cel.MemberOverload("vars_get_default",
				[]*cel.Type{varsType, cel.StringType, cel.StringType}, cel.StringType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					m, ok := args[0].(traits.Mapper)
					if !ok {
						return types.NewErr("get(): receiver is not a map")
					}
					if v, found := m.Find(args[1]); found {
						return v
					}
					return args[2]
				}))),
	)
}

// compileWhen parses and type-checks a when: expression; the result must be
// a bool.
func compileWhen(env *cel.Env, expr string) (*cel.Ast, error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, fmt.Errorf("expression must evaluate to a bool, got %s", ast.OutputType())
	}
	return ast, nil
}

// CheckWhen compile-checks a when: expression without evaluating it.
func CheckWhen(expr string) error {
	env, err := whenEnv()
	if err != nil {
		return err
	}
	_, err = compileWhen(env, expr)
	return err
}

// EvalWhen evaluates a when: expression against the merged variable map.
// Referencing an unset variable as vars.NAME is an error (like a bare
// ${NAME}); use vars.get("NAME", "default") or has(vars.NAME) for optional
// ones.
func EvalWhen(expr string, vars map[string]string) (bool, error) {
	if vars == nil {
		vars = map[string]string{}
	}
	env, err := whenEnv()
	if err != nil {
		return false, err
	}
	ast, err := compileWhen(env, expr)
	if err != nil {
		return false, err
	}
	prog, err := env.Program(ast)
	if err != nil {
		return false, err
	}
	out, _, err := prog.Eval(map[string]any{"vars": vars})
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("expression must evaluate to a bool, got %v", out.Type())
	}
	return b, nil
}
