package policy

import (
	"fmt"
	"sync"

	"cel.dev/cel-go/cel"
)

var (
	celOnce sync.Once
	celEnv  *cel.Env
	celErr  error
	progs   sync.Map // expression -> cel.Program
)

// Guard evaluates a CEL guard over structured facts and the item. Free text is never in facts
// (ADR-006), so a ticket body cannot steer a guard.
func Guard(expr string, facts, item map[string]any) (bool, error) {
	if expr == "" {
		return true, nil
	}
	celOnce.Do(func() {
		celEnv, celErr = cel.NewEnv(
			cel.Variable("facts", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("item", cel.MapType(cel.StringType, cel.DynType)),
		)
	})
	if celErr != nil {
		return false, celErr
	}
	p, ok := progs.Load(expr)
	if !ok {
		ast, iss := celEnv.Compile(expr)
		if iss.Err() != nil {
			return false, fmt.Errorf("guard %q: %w", expr, iss.Err())
		}
		if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
			return false, fmt.Errorf("guard %q returns %s, want bool", expr, ast.OutputType())
		}
		prg, err := celEnv.Program(ast)
		if err != nil {
			return false, err
		}
		p, _ = progs.LoadOrStore(expr, prg)
	}
	out, _, err := p.(cel.Program).Eval(map[string]any{"facts": facts, "item": item})
	if err != nil {
		return false, fmt.Errorf("guard %q: %w", expr, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("guard %q returned %v, want bool", expr, out.Value())
	}
	return b, nil
}
