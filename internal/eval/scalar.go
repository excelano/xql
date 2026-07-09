package eval

import (
	"fmt"
	"strings"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// scalarFunc describes one supported scalar function: its arity and the static
// output type it produces. Validation compares the actual call against this
// entry before eval runs. Kept in one place so adding a new function is a
// single-map edit.
type scalarFunc struct {
	arity int
	out   cell.ColumnType
}

var scalarFuncs = map[string]scalarFunc{
	"LOWER": {arity: 1, out: cell.TypeString},
	"UPPER": {arity: 1, out: cell.TypeString},
	"TRIM":  {arity: 1, out: cell.TypeString},
}

// scalarFuncOutputType returns the static result type of a scalar function
// call. Unknown names surface here as errors so ExprType callers (projection
// dedup keys, ORDER BY, GROUP BY) fail at plan time rather than mid-scan.
func scalarFuncOutputType(f *parse.FuncCallExpr, schema map[string]cell.ColumnInfo) (cell.ColumnType, error) {
	def, ok := scalarFuncs[f.Name]
	if !ok {
		return cell.TypeString, fmt.Errorf("unknown function %s", f.Name)
	}
	return def.out, nil
}

// validateScalarFunc enforces the known-function-and-arity rules and validates
// each argument's shape. Argument type-checks that depend on runtime values
// (e.g. LOWER on a numeric literal) surface at eval time via evalFuncCall's
// coercion path; validation here only rejects unknown names, wrong arg counts,
// and column references that don't resolve.
func validateScalarFunc(f *parse.FuncCallExpr, schema map[string]cell.ColumnInfo) error {
	def, ok := scalarFuncs[f.Name]
	if !ok {
		return fmt.Errorf("unknown function %s", f.Name)
	}
	if len(f.Args) != def.arity {
		return fmt.Errorf("%s expects %d argument%s, got %d", f.Name, def.arity, plural(def.arity), len(f.Args))
	}
	for _, a := range f.Args {
		if HasAggregate(a) {
			return fmt.Errorf("%s: aggregate arguments are not allowed", f.Name)
		}
		if err := ValidateExpr(a, schema); err != nil {
			return err
		}
	}
	return nil
}

// evalFuncCall dispatches a validated FuncCallExpr to its per-function
// implementation. NULL arguments propagate NULL — matching standard SQL
// semantics and the way the aggregate path handles NULL. Non-string inputs
// are coerced via each value's string representation, so `LOWER(1)` yields
// "1"; behavior an issue-reporter can rely on rather than surprise them.
func evalFuncCall(f *parse.FuncCallExpr, row cell.Row, ctx *EvalContext) (EvalCell, error) {
	def, ok := scalarFuncs[f.Name]
	if !ok {
		return EvalCell{}, fmt.Errorf("unknown function %s", f.Name)
	}
	if len(f.Args) != def.arity {
		return EvalCell{}, fmt.Errorf("%s expects %d argument%s, got %d", f.Name, def.arity, plural(def.arity), len(f.Args))
	}
	switch f.Name {
	case "LOWER":
		return applyStringUnary(f.Args[0], row, ctx, strings.ToLower)
	case "UPPER":
		return applyStringUnary(f.Args[0], row, ctx, strings.ToUpper)
	case "TRIM":
		return applyStringUnary(f.Args[0], row, ctx, strings.TrimSpace)
	}
	return EvalCell{}, fmt.Errorf("internal: scalar function %s has no evaluator", f.Name)
}

// applyStringUnary evaluates a single argument, propagates NULL, and applies
// fn to the argument's string form. Numeric and boolean inputs stringify via
// the same rules the renderer uses so `LOWER(price)` produces the same digits
// a bare projection would.
func applyStringUnary(arg parse.Expr, row cell.Row, ctx *EvalContext, fn func(string) string) (EvalCell, error) {
	v, err := EvalExpr(arg, row, ctx)
	if err != nil {
		return EvalCell{}, err
	}
	if v.Cell.Null {
		return EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeString}, nil
	}
	s := stringify(v)
	return EvalCell{Cell: cell.Cell{Str: fn(s)}, Type: cell.TypeString}, nil
}

// stringify renders an EvalCell as a Go string. Mirrors cell.AsAny's stringy
// output shapes so scalar-function results align with what the rest of the
// pipeline would display for the same value.
func stringify(e EvalCell) string {
	switch e.Type {
	case cell.TypeString:
		return e.Cell.Str
	case cell.TypeInt:
		return fmt.Sprintf("%d", e.Cell.Int)
	case cell.TypeFloat:
		return fmt.Sprintf("%g", e.Cell.Float)
	case cell.TypeBool:
		if e.Cell.Bool {
			return "true"
		}
		return "false"
	case cell.TypeDate:
		return e.Cell.Date.UTC().Format("2006-01-02T15:04:05Z")
	}
	return ""
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
