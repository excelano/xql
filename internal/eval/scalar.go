package eval

import (
	"fmt"
	"strings"
	"time"

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
	"YEAR":  {arity: 1, out: cell.TypeInt},
	"MONTH": {arity: 1, out: cell.TypeInt},
	"DAY":   {arity: 1, out: cell.TypeInt},
}

// elsewhere maps a function name xql does not have to the answer for it. Two
// kinds live here: work that belongs to a sibling tool, and work that is a real
// absence in xql with a stated way through today. Both beat a bare "unknown
// function", which leaves the user unable to tell a typo from a boundary.
//
// xled's error taxonomy already routes people here by name when they ask a
// query question of an editor. This is the return path, so the redirect works
// in both directions instead of one.
var elsewhere = map[string]string{
	"REPLACE":        "rewriting a cell's text by pattern is xled's job: xled -i '[col] s/old/new/g' file.csv",
	"SUBSTITUTE":     "rewriting a cell's text by pattern is xled's job: xled -i '[col] s/old/new/g' file.csv",
	"REGEXP_REPLACE": "rewriting a cell's text by pattern is xled's job: xled -i '[col] s/old/new/g' file.csv",
	"PIVOT":          "changing the table's shape is xshape's job — see xshape --help",
	"UNPIVOT":        "changing the table's shape is xshape's job — see xshape --help",
	"TRANSPOSE":      "changing the table's shape is xshape's job — see xshape --help",
	"SPLIT_PART":     "splitting one cell into several columns is xshape's job — see xshape --help",
	"STRING_SPLIT":   "splitting one cell into several columns is xshape's job — see xshape --help",
	"LENGTH":         "not available yet. Compute it first with xled: xled '[len] = len([col])' file.csv > tmp.csv",
	"LEN":            "not available yet. Compute it first with xled: xled '[len] = len([col])' file.csv > tmp.csv",
	"SUBSTRING":      "not available yet. Compute it first with xled: xled '[part] = substr([col], 1, 3)' file.csv > tmp.csv",
	"LEFT":           "not available yet. Compute it first with xled: xled '[part] = left([col], 3)' file.csv > tmp.csv",
	"RIGHT":          "not available yet. Compute it first with xled: xled '[part] = right([col], 3)' file.csv > tmp.csv",
	"ROUND":          "not available yet. Compute it first with xled: xled '[r] = round(num([col]), 2)' file.csv > tmp.csv",
}

// unknownFuncError names the function and, where the intent is clear, says
// where the capability actually lives.
func unknownFuncError(name string) error {
	if to, ok := elsewhere[name]; ok {
		return fmt.Errorf("unknown function %s — %s", name, to)
	}
	return fmt.Errorf("unknown function %s", name)
}

// isDatePart reports whether name is one of the calendar-component accessors,
// which share an argument rule the string functions do not have.
func isDatePart(name string) bool {
	switch name {
	case "YEAR", "MONTH", "DAY":
		return true
	}
	return false
}

// scalarFuncOutputType returns the static result type of a scalar function
// call. Unknown names surface here as errors so ExprType callers (projection
// dedup keys, ORDER BY, GROUP BY) fail at plan time rather than mid-scan.
func scalarFuncOutputType(f *parse.FuncCallExpr, schema map[string]cell.ColumnInfo) (cell.ColumnType, error) {
	def, ok := scalarFuncs[f.Name]
	if !ok {
		return cell.TypeString, unknownFuncError(f.Name)
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
		return unknownFuncError(f.Name)
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
	// A date part on an int, float, or bool column can only ever be wrong, and
	// finding that out on row 900,000 is worse than finding out before the scan
	// starts. Excel would answer with a serial number here; xql has none to give,
	// and neither does xled — the family agrees that serials are the damage.
	if isDatePart(f.Name) {
		t, err := ExprType(f.Args[0], schema)
		if err != nil {
			return err
		}
		if t != cell.TypeDate && t != cell.TypeString {
			return fmt.Errorf("%s expects a date, got %s — if that column really holds dates, pin it with --type %s=date", f.Name, t, exprColumnName(f.Args[0]))
		}
	}
	return nil
}

// exprColumnName returns the column name an expression references, for use in
// an error's suggested fix. Falls back to a placeholder when the argument is
// not a bare column, since --type only applies to real columns anyway.
func exprColumnName(e parse.Expr) string {
	if c, ok := e.(*parse.ColumnExpr); ok {
		return c.Name
	}
	return "COLUMN"
}

// evalFuncCall dispatches a validated FuncCallExpr to its per-function
// implementation. NULL arguments propagate NULL — matching standard SQL
// semantics and the way the aggregate path handles NULL. Non-string inputs
// are coerced via each value's string representation, so `LOWER(1)` yields
// "1"; behavior an issue-reporter can rely on rather than surprise them.
func evalFuncCall(f *parse.FuncCallExpr, row cell.Row, ctx *EvalContext) (EvalCell, error) {
	def, ok := scalarFuncs[f.Name]
	if !ok {
		return EvalCell{}, unknownFuncError(f.Name)
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
	case "YEAR", "MONTH", "DAY":
		return applyDatePart(f.Name, f.Args[0], row, ctx)
	}
	return EvalCell{}, fmt.Errorf("internal: scalar function %s has no evaluator", f.Name)
}

// applyDatePart extracts a calendar component from a date. A date column reads
// directly; a string parses through the same ISO reader the loader uses, so a
// literal and a --type-pinned string column both work.
//
// A string that is not a date yields NULL rather than aborting the query. SQL
// already has a value for "no answer for this row", and one unparseable cell
// should not kill a scan over the other million — the same call xled makes when
// it skips a cell and tallies, expressed in the idiom this tool has.
func applyDatePart(name string, arg parse.Expr, row cell.Row, ctx *EvalContext) (EvalCell, error) {
	v, err := EvalExpr(arg, row, ctx)
	if err != nil {
		return EvalCell{}, err
	}
	null := EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeInt}
	if v.Cell.Null {
		return null, nil
	}
	var t time.Time
	switch v.Type {
	case cell.TypeDate:
		t = v.Cell.Date
	case cell.TypeString:
		parsed, perr := cell.ParseDateString(strings.TrimSpace(v.Cell.Str))
		if perr != nil {
			return null, nil
		}
		t = parsed
	default:
		return EvalCell{}, fmt.Errorf("%s expects a date, got %s", name, v.Type)
	}
	var n int64
	switch name {
	case "YEAR":
		n = int64(t.Year())
	case "MONTH":
		n = int64(t.Month())
	case "DAY":
		n = int64(t.Day())
	}
	return EvalCell{Cell: cell.Cell{Int: n}, Type: cell.TypeInt}, nil
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
