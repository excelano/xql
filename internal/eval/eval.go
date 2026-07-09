// Package eval is the XQL expression evaluator and predicate matcher.
// Both run over typed cell.Rows; they are backend-agnostic and shared by
// the CSV and SharePoint executors. The aggregation machinery
// (AggSlot, ValidateAggregate) lives here too because aggregates are
// expressions in their own right.
package eval

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// EvalCell is the typed result of evaluating an parse.Expr against a row. The cell.Cell
// shape matches the column-cell representation so it can flow into the same
// cell.Compare path as raw column values; Type tells the caller which cell.Cell field
// is meaningful.
type EvalCell struct {
	Cell cell.Cell
	Type cell.ColumnType
}

// EvalExpr evaluates an expression tree against a single row. Slice 1 handles
// columns, literals, and arithmetic. Aggregates are recognized at parse time
// but rejected here until slice 4 wires the accumulator path.
func EvalExpr(e parse.Expr, row cell.Row, ctx *EvalContext) (EvalCell, error) {
	switch n := e.(type) {
	case *parse.ColumnExpr:
		idx, ok := ctx.ColIdx[n.Name]
		if !ok {
			return EvalCell{}, fmt.Errorf("unknown column %q", n.Name)
		}
		return EvalCell{Cell: row[idx], Type: ctx.Schema[n.Name].Type}, nil
	case *parse.LiteralExpr:
		return evalLiteralExpr(n)
	case *parse.BinaryExpr:
		return evalBinary(n, row, ctx)
	case *parse.AggregateExpr:
		if ctx != nil && ctx.AggResults != nil {
			if v, ok := ctx.AggResults[n]; ok {
				return v, nil
			}
		}
		return EvalCell{}, fmt.Errorf("aggregate %s evaluated outside an aggregation context", n.Func)
	case *parse.FuncCallExpr:
		return evalFuncCall(n, row, ctx)
	}
	return EvalCell{}, fmt.Errorf("internal: unhandled expression type %T", e)
}

// evalLiteralExpr converts a parser parse.Value into a typed EvalCell. Numbers with
// a decimal point become floats; integer-shaped numbers become ints, falling
// back to float on int64 overflow. NULL literals carry cell.TypeString as a
// placeholder; the Null flag is what callers actually check.
func evalLiteralExpr(l *parse.LiteralExpr) (EvalCell, error) {
	v := l.Value
	switch v.Kind {
	case parse.ValNull:
		return EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeString}, nil
	case parse.ValBool:
		return EvalCell{Cell: cell.Cell{Bool: v.Bool}, Type: cell.TypeBool}, nil
	case parse.ValString:
		return EvalCell{Cell: cell.Cell{Str: v.Str}, Type: cell.TypeString}, nil
	case parse.ValNumber:
		if strings.ContainsRune(v.Num, '.') {
			f, err := strconv.ParseFloat(v.Num, 64)
			if err != nil {
				return EvalCell{}, fmt.Errorf("invalid number literal %q", v.Num)
			}
			return EvalCell{Cell: cell.Cell{Float: f}, Type: cell.TypeFloat}, nil
		}
		if n, err := strconv.ParseInt(v.Num, 10, 64); err == nil {
			return EvalCell{Cell: cell.Cell{Int: n}, Type: cell.TypeInt}, nil
		}
		f, err := strconv.ParseFloat(v.Num, 64)
		if err != nil {
			return EvalCell{}, fmt.Errorf("invalid number literal %q", v.Num)
		}
		return EvalCell{Cell: cell.Cell{Float: f}, Type: cell.TypeFloat}, nil
	}
	return EvalCell{}, fmt.Errorf("internal: unknown literal kind %d", v.Kind)
}

// evalBinary handles +, -, *, /. Any NULL operand propagates NULL. `+`, `-`,
// and `*` stay int when both operands are int; otherwise the result is float.
// `/` always returns float (SQLite-style — int division would silently
// truncate column-arithmetic results in ways that surprise spreadsheet
// users). Divide-by-zero yields NULL rather than an error so a single bad
// row does not abort the whole scan.
func evalBinary(b *parse.BinaryExpr, row cell.Row, ctx *EvalContext) (EvalCell, error) {
	l, err := EvalExpr(b.L, row, ctx)
	if err != nil {
		return EvalCell{}, err
	}
	r, err := EvalExpr(b.R, row, ctx)
	if err != nil {
		return EvalCell{}, err
	}
	if l.Cell.Null || r.Cell.Null {
		return EvalCell{Cell: cell.Cell{Null: true}, Type: arithResultType(l.Type, r.Type, b.Op)}, nil
	}
	if !isNumericType(l.Type) {
		return EvalCell{}, fmt.Errorf("arithmetic %q not supported on %s value", b.Op, l.Type)
	}
	if !isNumericType(r.Type) {
		return EvalCell{}, fmt.Errorf("arithmetic %q not supported on %s value", b.Op, r.Type)
	}
	if b.Op == "/" {
		lf := numericFloat(l)
		rf := numericFloat(r)
		if rf == 0 {
			return EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeFloat}, nil
		}
		return EvalCell{Cell: cell.Cell{Float: lf / rf}, Type: cell.TypeFloat}, nil
	}
	if l.Type == cell.TypeInt && r.Type == cell.TypeInt {
		li, ri := l.Cell.Int, r.Cell.Int
		var out int64
		switch b.Op {
		case "+":
			out = li + ri
		case "-":
			out = li - ri
		case "*":
			out = li * ri
		default:
			return EvalCell{}, fmt.Errorf("internal: unsupported op %q", b.Op)
		}
		return EvalCell{Cell: cell.Cell{Int: out}, Type: cell.TypeInt}, nil
	}
	lf := numericFloat(l)
	rf := numericFloat(r)
	var out float64
	switch b.Op {
	case "+":
		out = lf + rf
	case "-":
		out = lf - rf
	case "*":
		out = lf * rf
	default:
		return EvalCell{}, fmt.Errorf("internal: unsupported op %q", b.Op)
	}
	return EvalCell{Cell: cell.Cell{Float: out}, Type: cell.TypeFloat}, nil
}

func isNumericType(t cell.ColumnType) bool {
	return t == cell.TypeInt || t == cell.TypeFloat
}

// numericFloat reads the float value out of a numeric EvalCell, promoting an
// int operand to float64. The caller has already checked Null and that the
// type is numeric.
func numericFloat(e EvalCell) float64 {
	if e.Type == cell.TypeInt {
		return float64(e.Cell.Int)
	}
	return e.Cell.Float
}

// arithResultType picks the type that a NULL arithmetic result should carry.
// Division is always float; otherwise int-int stays int and any float operand
// promotes to float. Mirrors evalBinary's branching for non-NULL inputs.
func arithResultType(l, r cell.ColumnType, op string) cell.ColumnType {
	if op == "/" {
		return cell.TypeFloat
	}
	if l == cell.TypeInt && r == cell.TypeInt {
		return cell.TypeInt
	}
	return cell.TypeFloat
}

// CoerceEvalCell converts an expression result to a cell.Cell suitable for storage
// in a column of the given target type. NULL passes through. Same-type results
// copy directly. Cross-type results route through cell.CoerceLiteral so the rules
// match literal coercion in INSERT/UPDATE — string↔number, bool↔string, etc.
// behave identically. Float→int succeeds only when the float value is exactly
// representable as an integer; partial values surface as coercion errors
// rather than silently truncating.
func CoerceEvalCell(e EvalCell, target cell.ColumnType, colName string) (cell.Cell, error) {
	if e.Cell.Null {
		return cell.Cell{Null: true}, nil
	}
	if e.Type == target {
		return e.Cell, nil
	}
	c, err := cell.CoerceLiteral(evalCellAsValue(e), target)
	if err != nil {
		return cell.Cell{}, fmt.Errorf("column %q: %w", colName, err)
	}
	return c, nil
}

// evalCellAsValue formats a non-NULL EvalCell as a parser-shaped parse.Value so it
// can be coerced via the shared cell.CoerceLiteral path. Float formatting uses
// the shortest round-trippable representation; date formatting uses RFC3339
// because cell.CoerceLiteral parses dates from ISO 8601 strings.
func evalCellAsValue(e EvalCell) parse.Value {
	if e.Cell.Null {
		return parse.Value{Kind: parse.ValNull}
	}
	switch e.Type {
	case cell.TypeInt:
		return parse.Value{Kind: parse.ValNumber, Num: strconv.FormatInt(e.Cell.Int, 10)}
	case cell.TypeFloat:
		return parse.Value{Kind: parse.ValNumber, Num: strconv.FormatFloat(e.Cell.Float, 'g', -1, 64)}
	case cell.TypeBool:
		return parse.Value{Kind: parse.ValBool, Bool: e.Cell.Bool}
	case cell.TypeString:
		return parse.Value{Kind: parse.ValString, Str: e.Cell.Str}
	case cell.TypeDate:
		return parse.Value{Kind: parse.ValString, Str: e.Cell.Date.Format(time.RFC3339)}
	}
	return parse.Value{Kind: parse.ValNull}
}

// ExprType derives the result type of an expression without evaluating it.
// Numeric literals with a decimal point are floats; integer-shaped numerics
// are ints. NULL literals default to cell.TypeString — the Null flag is what
// matters at evaluation time. Arithmetic mirrors evalBinary: / always yields
// float, + - * stay int when both operands are int and promote otherwise.
// Aggregates are rejected here; planProjection handles them in slice 4.
func ExprType(e parse.Expr, schema map[string]cell.ColumnInfo) (cell.ColumnType, error) {
	switch n := e.(type) {
	case *parse.ColumnExpr:
		info, ok := schema[n.Name]
		if !ok {
			return cell.TypeString, fmt.Errorf("unknown column %q", n.Name)
		}
		return info.Type, nil
	case *parse.LiteralExpr:
		switch n.Value.Kind {
		case parse.ValNumber:
			if strings.ContainsRune(n.Value.Num, '.') {
				return cell.TypeFloat, nil
			}
			return cell.TypeInt, nil
		case parse.ValString, parse.ValNull:
			return cell.TypeString, nil
		case parse.ValBool:
			return cell.TypeBool, nil
		}
		return cell.TypeString, fmt.Errorf("internal: unknown literal kind %d", n.Value.Kind)
	case *parse.BinaryExpr:
		lt, err := ExprType(n.L, schema)
		if err != nil {
			return cell.TypeString, err
		}
		rt, err := ExprType(n.R, schema)
		if err != nil {
			return cell.TypeString, err
		}
		return arithResultType(lt, rt, n.Op), nil
	case *parse.AggregateExpr:
		return aggregateOutputType(n, schema)
	case *parse.FuncCallExpr:
		return scalarFuncOutputType(n, schema)
	}
	return cell.TypeString, fmt.Errorf("internal: unhandled expression type %T", e)
}

// aggregateOutputType derives the static result type for an aggregate node.
// COUNT is always int; AVG is always float; SUM/MIN/MAX inherit the static
// type of the argument expression. The runtime path may promote SUM from int
// to float on the first float input, but the static type used for projection
// dedup keys and rendering tracks the argument's declared type.
func aggregateOutputType(a *parse.AggregateExpr, schema map[string]cell.ColumnInfo) (cell.ColumnType, error) {
	if a.Star {
		return cell.TypeInt, nil
	}
	switch a.Func {
	case "COUNT":
		return cell.TypeInt, nil
	case "AVG":
		return cell.TypeFloat, nil
	case "SUM", "MIN", "MAX":
		return ExprType(a.Arg, schema)
	}
	return cell.TypeString, fmt.Errorf("internal: unknown aggregate %q", a.Func)
}

// HasAggregate reports whether the expression tree contains an aggregate
// node. UPDATE SET and WHERE forbid aggregates; SELECT projection and
// HAVING permit them.
func HasAggregate(e parse.Expr) bool {
	switch n := e.(type) {
	case *parse.AggregateExpr:
		return true
	case *parse.BinaryExpr:
		return HasAggregate(n.L) || HasAggregate(n.R)
	case *parse.FuncCallExpr:
		for _, a := range n.Args {
			if HasAggregate(a) {
				return true
			}
		}
		return false
	}
	return false
}

// BareColumn returns the name of a parse.ColumnExpr that appears outside any
// parse.AggregateExpr in the tree, or "" if every column reference is wrapped in
// an aggregate. Used to reject `SELECT Title, COUNT(*)` when no GROUP BY is
// in play — Postgres-strict semantics per Pass 2 decisions.
func BareColumn(e parse.Expr) string {
	switch n := e.(type) {
	case *parse.ColumnExpr:
		return n.Name
	case *parse.BinaryExpr:
		if c := BareColumn(n.L); c != "" {
			return c
		}
		return BareColumn(n.R)
	case *parse.AggregateExpr:
		return ""
	case *parse.FuncCallExpr:
		for _, a := range n.Args {
			if c := BareColumn(a); c != "" {
				return c
			}
		}
		return ""
	}
	return ""
}

// BareColumnNotIn returns the name of a column reference that appears outside
// any parse.AggregateExpr and is not covered by GROUP BY, or "" if every such
// reference is permitted. Two forms of coverage:
//
//   - allowedCols names bare-column GROUP BY entries.
//   - allowedExprs is the full list of GROUP BY expressions. At every subtree
//     we first check whether the WHOLE subtree structurally matches one of
//     these expressions; if so it is allowed without descending further, so
//     `SELECT LOWER(x), COUNT(*) GROUP BY LOWER(x)` accepts the projection.
//
// allowedExprs may be nil; then only the map-based check applies.
func BareColumnNotIn(e parse.Expr, allowedCols map[string]bool, allowedExprs []parse.Expr) string {
	if exprMatchesAny(e, allowedExprs) {
		return ""
	}
	switch n := e.(type) {
	case *parse.ColumnExpr:
		if allowedCols[n.Name] {
			return ""
		}
		return n.Name
	case *parse.LiteralExpr:
		return ""
	case *parse.BinaryExpr:
		if c := BareColumnNotIn(n.L, allowedCols, allowedExprs); c != "" {
			return c
		}
		return BareColumnNotIn(n.R, allowedCols, allowedExprs)
	case *parse.AggregateExpr:
		return ""
	case *parse.FuncCallExpr:
		for _, a := range n.Args {
			if c := BareColumnNotIn(a, allowedCols, allowedExprs); c != "" {
				return c
			}
		}
		return ""
	}
	return ""
}

// ExprEqual is a structural equality check across the Expr AST — same node
// type, same operator/function name, same operand shape all the way down.
// Used to recognize when a projection or HAVING subtree matches a GROUP BY
// expression exactly (SQL's "functional dependency" rule collapsed to
// syntactic equality; the canonicalizer has already normalized column names
// so equal names compare identically).
func ExprEqual(a, b parse.Expr) bool {
	switch x := a.(type) {
	case *parse.ColumnExpr:
		y, ok := b.(*parse.ColumnExpr)
		return ok && x.Name == y.Name
	case *parse.LiteralExpr:
		y, ok := b.(*parse.LiteralExpr)
		return ok && x.Value == y.Value
	case *parse.BinaryExpr:
		y, ok := b.(*parse.BinaryExpr)
		return ok && x.Op == y.Op && ExprEqual(x.L, y.L) && ExprEqual(x.R, y.R)
	case *parse.AggregateExpr:
		y, ok := b.(*parse.AggregateExpr)
		if !ok || x.Func != y.Func || x.Star != y.Star {
			return false
		}
		if x.Star {
			return true
		}
		return ExprEqual(x.Arg, y.Arg)
	case *parse.FuncCallExpr:
		y, ok := b.(*parse.FuncCallExpr)
		if !ok || x.Name != y.Name || len(x.Args) != len(y.Args) {
			return false
		}
		for i := range x.Args {
			if !ExprEqual(x.Args[i], y.Args[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func exprMatchesAny(e parse.Expr, allowed []parse.Expr) bool {
	for _, a := range allowed {
		if ExprEqual(e, a) {
			return true
		}
	}
	return false
}

// CollectAggregatesFromPredicate gathers aggregate nodes reachable from a
// HAVING predicate. parse.Comparison LHSes pass through CollectAggregates;
// parse.NullTest/LIKE/IN/BETWEEN bind to bare column names directly and contribute
// no aggregates.
func CollectAggregatesFromPredicate(p parse.Predicate, out []*parse.AggregateExpr) []*parse.AggregateExpr {
	switch n := p.(type) {
	case *parse.BinaryOp:
		out = CollectAggregatesFromPredicate(n.L, out)
		return CollectAggregatesFromPredicate(n.R, out)
	case *parse.NotOp:
		return CollectAggregatesFromPredicate(n.Inner, out)
	case *parse.Comparison:
		return CollectAggregates(n.LExpr, out)
	}
	return out
}

// CollectAggregates walks the tree and appends each parse.AggregateExpr to out.
// Order is left-to-right, depth-first. Pointer identity defines slot
// uniqueness — distinct AST nodes produce distinct slots even if they read
// the same column. Nested aggregates are rejected at validate time, so this
// walker never recurses through an parse.AggregateExpr's Arg.
func CollectAggregates(e parse.Expr, out []*parse.AggregateExpr) []*parse.AggregateExpr {
	switch n := e.(type) {
	case *parse.AggregateExpr:
		return append(out, n)
	case *parse.BinaryExpr:
		out = CollectAggregates(n.L, out)
		return CollectAggregates(n.R, out)
	case *parse.FuncCallExpr:
		for _, a := range n.Args {
			out = CollectAggregates(a, out)
		}
		return out
	}
	return out
}

// ValidateAggregate checks an parse.AggregateExpr is well-formed: the function is
// known, COUNT is the only one that accepts *, the argument validates
// against the schema, no nested aggregates, and SUM/AVG arguments are
// numeric. MIN/MAX accept any comparable type; runtime cell.Compare handles the
// type-specific path.
func ValidateAggregate(a *parse.AggregateExpr, schema map[string]cell.ColumnInfo) error {
	switch a.Func {
	case "COUNT", "SUM", "AVG", "MIN", "MAX":
	default:
		return fmt.Errorf("unknown aggregate function %q", a.Func)
	}
	if a.Star {
		if a.Func != "COUNT" {
			return fmt.Errorf("%s(*) is not valid; only COUNT(*) is supported", a.Func)
		}
		return nil
	}
	if err := ValidateExpr(a.Arg, schema); err != nil {
		return err
	}
	if HasAggregate(a.Arg) {
		return fmt.Errorf("%s: nested aggregates are not allowed", a.Func)
	}
	argT, err := ExprType(a.Arg, schema)
	if err != nil {
		return err
	}
	if a.Func == "SUM" || a.Func == "AVG" {
		if argT != cell.TypeInt && argT != cell.TypeFloat {
			return fmt.Errorf("%s requires a numeric argument, got %s", a.Func, argT)
		}
	}
	return nil
}

// AggSlot is the per-aggregate accumulator. One slot per unique parse.AggregateExpr
// in the projection plan; advance(row) consumes one input row, finalize()
// produces the aggregated result. The state union is wide enough to cover
// every function — each one reads only the fields its semantics require.
type AggSlot struct {
	Agg *parse.AggregateExpr
	ArgType cell.ColumnType

	count      int64
	sumInt     int64
	sumFloat   float64
	sumIsInt   bool
	minMaxCell cell.Cell
	hasValue   bool
}

// NewAggSlot builds a slot for an aggregate node. ArgType is the static type
// of the argument expression (used for MIN/MAX comparison and the static
// output type of SUM/MIN/MAX); COUNT(*) carries cell.TypeInt as a placeholder.
// sumIsInt starts true; the first float-typed value flips it and converts
// any int sum collected so far into the float accumulator.
func NewAggSlot(a *parse.AggregateExpr, schema map[string]cell.ColumnInfo) (*AggSlot, error) {
	s := &AggSlot{Agg: a, sumIsInt: true, ArgType: cell.TypeInt}
	if !a.Star {
		t, err := ExprType(a.Arg, schema)
		if err != nil {
			return nil, err
		}
		s.ArgType = t
	}
	return s, nil
}

// advance folds one row into the accumulator. COUNT(*) counts unconditionally;
// every other function evaluates the argument expression and skips NULL,
// matching standard SQL aggregate NULL semantics.
func (s *AggSlot) Advance(row cell.Row, ctx *EvalContext) error {
	if s.Agg.Star {
		s.count++
		return nil
	}
	v, err := EvalExpr(s.Agg.Arg, row, ctx)
	if err != nil {
		return err
	}
	if v.Cell.Null {
		return nil
	}
	switch s.Agg.Func {
	case "COUNT":
		s.count++
	case "SUM":
		s.hasValue = true
		s.count++
		if v.Type == cell.TypeFloat {
			if s.sumIsInt {
				s.sumFloat = float64(s.sumInt)
				s.sumIsInt = false
			}
			s.sumFloat += v.Cell.Float
		} else {
			if s.sumIsInt {
				s.sumInt += v.Cell.Int
			} else {
				s.sumFloat += float64(v.Cell.Int)
			}
		}
	case "AVG":
		s.hasValue = true
		s.count++
		if v.Type == cell.TypeFloat {
			s.sumFloat += v.Cell.Float
		} else {
			s.sumFloat += float64(v.Cell.Int)
		}
	case "MIN":
		if !s.hasValue || cell.Compare(v.Cell, s.minMaxCell, s.ArgType) < 0 {
			s.minMaxCell = v.Cell
			s.hasValue = true
		}
	case "MAX":
		if !s.hasValue || cell.Compare(v.Cell, s.minMaxCell, s.ArgType) > 0 {
			s.minMaxCell = v.Cell
			s.hasValue = true
		}
	}
	return nil
}

// finalize closes the accumulator and yields the EvalCell that represents
// this aggregate's value for the output row. COUNT always produces an
// integer (0 over an empty set). SUM/AVG/MIN/MAX produce NULL over an empty
// or all-NULL set; their static type is preserved so projection rendering
// stays consistent.
func (s *AggSlot) Finalize() EvalCell {
	switch s.Agg.Func {
	case "COUNT":
		return EvalCell{Cell: cell.Cell{Int: s.count}, Type: cell.TypeInt}
	case "SUM":
		if !s.hasValue {
			return EvalCell{Cell: cell.Cell{Null: true}, Type: s.ArgType}
		}
		if s.sumIsInt {
			return EvalCell{Cell: cell.Cell{Int: s.sumInt}, Type: cell.TypeInt}
		}
		return EvalCell{Cell: cell.Cell{Float: s.sumFloat}, Type: cell.TypeFloat}
	case "AVG":
		if !s.hasValue {
			return EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeFloat}
		}
		return EvalCell{Cell: cell.Cell{Float: s.sumFloat / float64(s.count)}, Type: cell.TypeFloat}
	case "MIN", "MAX":
		if !s.hasValue {
			return EvalCell{Cell: cell.Cell{Null: true}, Type: s.ArgType}
		}
		return EvalCell{Cell: s.minMaxCell, Type: s.ArgType}
	}
	return EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeString}
}

// ValidateExpr walks an expression tree and rejects column references that
// don't exist in the schema. Catches typos before the row scan begins.
// Aggregate nodes pass validation here; the executor decides whether they
// are allowed in the calling context.
func ValidateExpr(e parse.Expr, schema map[string]cell.ColumnInfo) error {
	switch n := e.(type) {
	case *parse.ColumnExpr:
		if _, ok := schema[n.Name]; !ok {
			return fmt.Errorf("unknown column %q", n.Name)
		}
		return nil
	case *parse.LiteralExpr:
		return nil
	case *parse.BinaryExpr:
		if err := ValidateExpr(n.L, schema); err != nil {
			return err
		}
		return ValidateExpr(n.R, schema)
	case *parse.AggregateExpr:
		if !n.Star {
			return ValidateExpr(n.Arg, schema)
		}
		return nil
	case *parse.FuncCallExpr:
		return validateScalarFunc(n, schema)
	}
	return fmt.Errorf("internal: unhandled expression type %T", e)
}
