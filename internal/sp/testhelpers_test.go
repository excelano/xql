package sp

import "github.com/excelano/xql/internal/parse"

// Test fixture builders that wrap parse.* constructors. Mirrors the helpers
// in internal/parse/parse_test.go so each predicate is one short call.

func vstr(s string) parse.Value  { return parse.Value{Kind: parse.ValString, Str: s} }
func vnum(n string) parse.Value  { return parse.Value{Kind: parse.ValNumber, Num: n} }
func vbool(b bool) parse.Value   { return parse.Value{Kind: parse.ValBool, Bool: b} }
func vnull() parse.Value         { return parse.Value{Kind: parse.ValNull} }
func litE(v parse.Value) parse.Expr {
	return &parse.LiteralExpr{Value: v}
}

func cmp(c, op string, v parse.Value) *parse.Comparison {
	return &parse.Comparison{LExpr: &parse.ColumnExpr{Name: c}, Op: op, Value: v}
}
func cmpE(lhs parse.Expr, op string, v parse.Value) *parse.Comparison {
	return &parse.Comparison{LExpr: lhs, Op: op, Value: v}
}
func isnull(c string, not bool) *parse.NullTest {
	return &parse.NullTest{Column: c, Not: not}
}
func and(l, r parse.Predicate) *parse.BinaryOp {
	return &parse.BinaryOp{Op: "AND", L: l, R: r}
}
func or(l, r parse.Predicate) *parse.BinaryOp {
	return &parse.BinaryOp{Op: "OR", L: l, R: r}
}
func not(p parse.Predicate) *parse.NotOp {
	return &parse.NotOp{Inner: p}
}
func like(col, pat string, not bool) *parse.LikeOp {
	return &parse.LikeOp{Column: col, Pattern: pat, Not: not}
}
func ilike(col, pat string, not bool) *parse.LikeOp {
	return &parse.LikeOp{Column: col, Pattern: pat, Not: not, Insensitive: true}
}
func in(col string, vals []parse.Value, not bool) *parse.InOp {
	return &parse.InOp{Column: col, Values: vals, Not: not}
}
func between(col string, lo, hi parse.Value, not bool) *parse.BetweenOp {
	return &parse.BetweenOp{Column: col, Low: lo, High: hi, Not: not}
}
