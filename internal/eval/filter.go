package eval

import (
	"fmt"
	"strings"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// triVal encodes SQL three-valued logic. WHERE keeps only rows whose
// predicate evaluates to triTrue; triUnknown (NULL-tainted comparisons)
// and triFalse drop the row.
type triVal int

const (
	triFalse   triVal = -1
	triUnknown triVal = 0
	triTrue    triVal = 1
)

// EvalContext bundles the per-table state evalPredicate needs. colIdx maps
// column name to row position; schema carries inferred types. Built once per
// statement and reused across rows.
//
// AggResults is populated only during the projection-eval phase of an
// aggregated SELECT. Keyed by parse.AggregateExpr pointer identity, it carries each
// slot's finalized value so EvalExpr can substitute aggregates in arithmetic
// like COUNT(*) + 1 or SUM(Price) / COUNT(*). Nil during the row-accumulation
// scan and in non-aggregated queries.
type EvalContext struct {
	ColIdx     map[string]int
	Schema     map[string]cell.ColumnInfo
	AggResults map[*parse.AggregateExpr]EvalCell
}

// NewEvalContext constructs an EvalContext from a cell.Table.
func NewEvalContext(t *cell.Table) *EvalContext {
	idx := make(map[string]int, len(t.Columns))
	for i, name := range t.Columns {
		idx[name] = i
	}
	return &EvalContext{ColIdx: idx, Schema: t.Schema}
}

// Matches returns true if the predicate evaluates to TRUE for the row.
// A nil predicate matches every row. Unknown (NULL-tainted) results return
// false, matching standard SQL WHERE semantics.
func Matches(p parse.Predicate, row cell.Row, ctx *EvalContext) (bool, error) {
	if p == nil {
		return true, nil
	}
	v, err := evalPredicate(p, row, ctx)
	if err != nil {
		return false, err
	}
	return v == triTrue, nil
}

// ValidatePredicate walks a predicate and returns the first column reference
// that does not exist in the schema. Lets callers reject bad queries before
// scanning the whole table.
func ValidatePredicate(p parse.Predicate, schema map[string]cell.ColumnInfo) error {
	if p == nil {
		return nil
	}
	switch n := p.(type) {
	case *parse.BinaryOp:
		if err := ValidatePredicate(n.L, schema); err != nil {
			return err
		}
		return ValidatePredicate(n.R, schema)
	case *parse.NotOp:
		return ValidatePredicate(n.Inner, schema)
	case *parse.Comparison:
		return ValidateExpr(n.LExpr, schema)
	case *parse.NullTest:
		if _, ok := schema[n.Column]; !ok {
			return fmt.Errorf("unknown column %q", n.Column)
		}
		return nil
	case *parse.LikeOp:
		if _, ok := schema[n.Column]; !ok {
			return fmt.Errorf("unknown column %q", n.Column)
		}
		return nil
	case *parse.InOp:
		if _, ok := schema[n.Column]; !ok {
			return fmt.Errorf("unknown column %q", n.Column)
		}
		return nil
	case *parse.BetweenOp:
		if _, ok := schema[n.Column]; !ok {
			return fmt.Errorf("unknown column %q", n.Column)
		}
		return nil
	}
	return fmt.Errorf("internal: unhandled predicate type %T", p)
}

func evalPredicate(p parse.Predicate, row cell.Row, ctx *EvalContext) (triVal, error) {
	switch n := p.(type) {
	case *parse.BinaryOp:
		l, err := evalPredicate(n.L, row, ctx)
		if err != nil {
			return triFalse, err
		}
		r, err := evalPredicate(n.R, row, ctx)
		if err != nil {
			return triFalse, err
		}
		switch n.Op {
		case "AND":
			return triAnd(l, r), nil
		case "OR":
			return triOr(l, r), nil
		}
		return triFalse, fmt.Errorf("internal: unsupported binary op %q", n.Op)
	case *parse.NotOp:
		inner, err := evalPredicate(n.Inner, row, ctx)
		if err != nil {
			return triFalse, err
		}
		return triNot(inner), nil
	case *parse.Comparison:
		return evalComparison(n, row, ctx)
	case *parse.NullTest:
		return evalNullTest(n, row, ctx)
	case *parse.LikeOp:
		return evalLike(n, row, ctx)
	case *parse.InOp:
		return evalIn(n, row, ctx)
	case *parse.BetweenOp:
		return evalBetween(n, row, ctx)
	}
	return triFalse, fmt.Errorf("internal: unhandled predicate type %T", p)
}

// evalLike applies a SQL LIKE (case-sensitive) or ILIKE (case-insensitive)
// pattern. Only string-typed columns are supported (matches Postgres
// semantics; numeric/date LIKE would force a string cast that hides type
// bugs more than it helps). NULL cells produce UNKNOWN, matching standard
// SQL three-valued logic.
func evalLike(n *parse.LikeOp, row cell.Row, ctx *EvalContext) (triVal, error) {
	idx, ok := ctx.ColIdx[n.Column]
	if !ok {
		return triFalse, fmt.Errorf("unknown column %q", n.Column)
	}
	info := ctx.Schema[n.Column]
	if info.Type != cell.TypeString {
		op := "LIKE"
		if n.Insensitive {
			op = "ILIKE"
		}
		return triFalse, fmt.Errorf("WHERE %s %s: column has type %s; %s only works on string columns", n.Column, op, info.Type, op)
	}
	c := row[idx]
	if c.Null {
		return triUnknown, nil
	}
	pattern, target := n.Pattern, c.Str
	if n.Insensitive {
		pattern = strings.ToLower(pattern)
		target = strings.ToLower(target)
	}
	match := likeMatch(pattern, target)
	if n.Not {
		match = !match
	}
	return boolTri(match), nil
}

// likeMatch implements SQL LIKE pattern matching. % matches zero or more
// characters; _ matches exactly one. A backslash escapes the next char so
// `\%` matches a literal %. A dangling backslash at end of pattern is
// rejected by the caller (it represents an incomplete escape and should
// not match anything).
func likeMatch(pattern, s string) bool {
	p := []rune(pattern)
	r := []rune(s)
	return likeRecurse(p, 0, r, 0)
}

func likeRecurse(p []rune, pi int, s []rune, si int) bool {
	for pi < len(p) {
		switch p[pi] {
		case '%':
			pi++
			// Try matching the rest from every remaining position.
			for k := si; k <= len(s); k++ {
				if likeRecurse(p, pi, s, k) {
					return true
				}
			}
			return false
		case '_':
			if si >= len(s) {
				return false
			}
			pi++
			si++
		case '\\':
			if pi+1 >= len(p) {
				// Dangling backslash; treat as literal backslash. Most SQL
				// engines reject this, but treating it as a literal makes
				// the function total and the parser remains clean.
				if si >= len(s) || s[si] != '\\' {
					return false
				}
				pi++
				si++
				continue
			}
			pi++
			if si >= len(s) || p[pi] != s[si] {
				return false
			}
			pi++
			si++
		default:
			if si >= len(s) || p[pi] != s[si] {
				return false
			}
			pi++
			si++
		}
	}
	return si == len(s)
}

// evalIn returns TRUE if the column value equals any of the listed literals.
// NULL on the column side produces UNKNOWN; a NULL inside the value list is
// rejected at parse time. NOT IN negates the TRUE/FALSE result but leaves
// UNKNOWN as UNKNOWN, matching SQL three-valued logic.
func evalIn(n *parse.InOp, row cell.Row, ctx *EvalContext) (triVal, error) {
	idx, ok := ctx.ColIdx[n.Column]
	if !ok {
		return triFalse, fmt.Errorf("unknown column %q", n.Column)
	}
	info := ctx.Schema[n.Column]
	c := row[idx]
	if c.Null {
		return triUnknown, nil
	}
	found := false
	for _, v := range n.Values {
		if v.Kind == parse.ValNull {
			// NULL in the list contaminates with UNKNOWN if we haven't matched
			// yet. Match-first short-circuits before hitting this.
			continue
		}
		lit, err := cell.CoerceLiteral(v, info.Type)
		if err != nil {
			return triFalse, fmt.Errorf("WHERE %s IN: %w", n.Column, err)
		}
		if cell.Compare(c, lit, info.Type) == 0 {
			found = true
			break
		}
	}
	if n.Not {
		return boolTri(!found), nil
	}
	return boolTri(found), nil
}

// evalBetween is exactly `col >= low AND col <= high` (inclusive). NULL on
// the column produces UNKNOWN. NULL bounds were rejected at parse time.
func evalBetween(n *parse.BetweenOp, row cell.Row, ctx *EvalContext) (triVal, error) {
	idx, ok := ctx.ColIdx[n.Column]
	if !ok {
		return triFalse, fmt.Errorf("unknown column %q", n.Column)
	}
	info := ctx.Schema[n.Column]
	c := row[idx]
	if c.Null {
		return triUnknown, nil
	}
	lo, err := cell.CoerceLiteral(n.Low, info.Type)
	if err != nil {
		return triFalse, fmt.Errorf("WHERE %s BETWEEN: %w", n.Column, err)
	}
	hi, err := cell.CoerceLiteral(n.High, info.Type)
	if err != nil {
		return triFalse, fmt.Errorf("WHERE %s BETWEEN: %w", n.Column, err)
	}
	in := cell.Compare(c, lo, info.Type) >= 0 && cell.Compare(c, hi, info.Type) <= 0
	if n.Not {
		in = !in
	}
	return boolTri(in), nil
}

func evalComparison(c *parse.Comparison, row cell.Row, ctx *EvalContext) (triVal, error) {
	lhs, err := EvalExpr(c.LExpr, row, ctx)
	if err != nil {
		return triFalse, err
	}
	if lhs.Cell.Null {
		return triUnknown, nil
	}
	lit, err := cell.CoerceLiteral(c.Value, lhs.Type)
	if err != nil {
		return triFalse, fmt.Errorf("WHERE %s: %w", c.Op, err)
	}
	cmp := cell.Compare(lhs.Cell, lit, lhs.Type)
	switch c.Op {
	case "=":
		return boolTri(cmp == 0), nil
	case "!=":
		return boolTri(cmp != 0), nil
	case "<":
		return boolTri(cmp < 0), nil
	case "<=":
		return boolTri(cmp <= 0), nil
	case ">":
		return boolTri(cmp > 0), nil
	case ">=":
		return boolTri(cmp >= 0), nil
	}
	return triFalse, fmt.Errorf("internal: unsupported comparison op %q", c.Op)
}

func evalNullTest(n *parse.NullTest, row cell.Row, ctx *EvalContext) (triVal, error) {
	idx, ok := ctx.ColIdx[n.Column]
	if !ok {
		return triFalse, fmt.Errorf("unknown column %q", n.Column)
	}
	isNull := row[idx].Null
	if n.Not {
		return boolTri(!isNull), nil
	}
	return boolTri(isNull), nil
}

func boolTri(b bool) triVal {
	if b {
		return triTrue
	}
	return triFalse
}

// triAnd, triOr, triNot implement Kleene's three-valued logic, matching
// standard SQL.
func triAnd(a, b triVal) triVal {
	if a == triFalse || b == triFalse {
		return triFalse
	}
	if a == triUnknown || b == triUnknown {
		return triUnknown
	}
	return triTrue
}

func triOr(a, b triVal) triVal {
	if a == triTrue || b == triTrue {
		return triTrue
	}
	if a == triUnknown || b == triUnknown {
		return triUnknown
	}
	return triFalse
}

func triNot(a triVal) triVal {
	switch a {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	}
	return triUnknown
}
