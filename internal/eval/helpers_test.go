package eval

import (
	"time"

	"github.com/excelano/xql/internal/parse"
)

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// Test helpers reproduced from sqlcsv's parse_test.go. The originals live in
// package parse and aren't exported, so internal/eval tests carry their own
// copy. Keeping them tiny and centralized matches the cell_test.go pattern.

func vstr(s string) parse.Value  { return parse.Value{Kind: parse.ValString, Str: s} }
func vnum(n string) parse.Value  { return parse.Value{Kind: parse.ValNumber, Num: n} }
func vbool(b bool) parse.Value   { return parse.Value{Kind: parse.ValBool, Bool: b} }
func vnull() parse.Value         { return parse.Value{Kind: parse.ValNull} }

func cmp(c, op string, v parse.Value) *parse.Comparison {
	return &parse.Comparison{LExpr: &parse.ColumnExpr{Name: c}, Op: op, Value: v}
}
func cmpE(lhs parse.Expr, op string, v parse.Value) *parse.Comparison {
	return &parse.Comparison{LExpr: lhs, Op: op, Value: v}
}
func isnull(c string, not bool) *parse.NullTest { return &parse.NullTest{Column: c, Not: not} }
func and(l, r parse.Predicate) *parse.BinaryOp  { return &parse.BinaryOp{Op: "AND", L: l, R: r} }
func or(l, r parse.Predicate) *parse.BinaryOp   { return &parse.BinaryOp{Op: "OR", L: l, R: r} }
func not(p parse.Predicate) *parse.NotOp        { return &parse.NotOp{Inner: p} }

func like(c, p string, n bool) *parse.LikeOp {
	return &parse.LikeOp{Column: c, Pattern: p, Not: n}
}
func ilike(c, p string, n bool) *parse.LikeOp {
	return &parse.LikeOp{Column: c, Pattern: p, Not: n, Insensitive: true}
}
func in(c string, vs []parse.Value, n bool) *parse.InOp {
	return &parse.InOp{Column: c, Values: vs, Not: n}
}
func between(c string, lo, hi parse.Value, n bool) *parse.BetweenOp {
	return &parse.BetweenOp{Column: c, Low: lo, High: hi, Not: n}
}

func colE(name string) parse.Expr          { return &parse.ColumnExpr{Name: name} }
func litE(v parse.Value) parse.Expr        { return &parse.LiteralExpr{Value: v} }
func binE(op string, l, r parse.Expr) parse.Expr {
	return &parse.BinaryExpr{Op: op, L: l, R: r}
}
func aggE(fn string, arg parse.Expr) parse.Expr {
	return &parse.AggregateExpr{Func: fn, Arg: arg}
}
func aggStar() parse.Expr { return &parse.AggregateExpr{Func: "COUNT", Star: true} }
