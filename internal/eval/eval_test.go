package eval

import (
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// numericFixture exercises the evaluator: two int columns, one float, one
// nullable int, one string for type-error tests.
func numericFixture() *cell.Table {
	cols := []string{"ID", "Price", "Qty", "Discount", "Name"}
	schema := map[string]cell.ColumnInfo{
		"ID":       {Name: "ID", Type: cell.TypeInt},
		"Price":    {Name: "Price", Type: cell.TypeInt},
		"Qty":      {Name: "Qty", Type: cell.TypeInt},
		"Discount": {Name: "Discount", Type: cell.TypeFloat},
		"Name":     {Name: "Name", Type: cell.TypeString},
	}
	rows := []cell.Row{
		{cell.Cell{Int: 1}, cell.Cell{Int: 10}, cell.Cell{Int: 5}, cell.Cell{Float: 0.1}, cell.Cell{Str: "Widget"}},
		{cell.Cell{Int: 2}, cell.Cell{Int: 20}, cell.Cell{Int: 0}, cell.Cell{Float: 0.0}, cell.Cell{Str: "Gizmo"}},
		{cell.Cell{Int: 3}, cell.Cell{Int: 30}, cell.Cell{Int: 2}, cell.Cell{Null: true}, cell.Cell{Str: "Sprocket"}},
		{cell.Cell{Int: 4}, cell.Cell{Null: true}, cell.Cell{Int: 4}, cell.Cell{Float: 0.5}, cell.Cell{Str: "Cog"}},
	}
	return &cell.Table{
		Path: "numeric.csv", Columns: cols, Schema: schema, Rows: rows,
		Delim: ',', HasHeader: true,
	}
}

func TestEvalExprColumn(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)
	got, err := EvalExpr(colE("Price"), tbl.Rows[0], ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cell.Int != 10 || got.Type != cell.TypeInt {
		t.Fatalf("Price row 0: got %+v, want Int=10 Type=int", got)
	}
}

func TestEvalExprLiteralInt(t *testing.T) {
	got, err := EvalExpr(litE(vnum("42")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != cell.TypeInt || got.Cell.Int != 42 {
		t.Fatalf("got %+v, want Int=42", got)
	}
}

func TestEvalExprLiteralFloat(t *testing.T) {
	got, err := EvalExpr(litE(vnum("3.14")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != cell.TypeFloat || got.Cell.Float != 3.14 {
		t.Fatalf("got %+v, want Float=3.14", got)
	}
}

func TestEvalExprLiteralNull(t *testing.T) {
	got, err := EvalExpr(litE(vnull()), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Cell.Null {
		t.Fatalf("NULL literal should produce Null cell, got %+v", got)
	}
}

func TestEvalExprArithmetic(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)
	row := tbl.Rows[0] // Price=10, Qty=5, Discount=0.1

	cases := []struct {
		name     string
		e        parse.Expr
		wantType cell.ColumnType
		wantInt  int64
		wantFlt  float64
	}{
		{"int + int", binE("+", colE("Price"), colE("Qty")), cell.TypeInt, 15, 0},
		{"int - int", binE("-", colE("Price"), colE("Qty")), cell.TypeInt, 5, 0},
		{"int * int", binE("*", colE("Price"), colE("Qty")), cell.TypeInt, 50, 0},
		{"int / int promotes to float", binE("/", colE("Price"), colE("Qty")), cell.TypeFloat, 0, 2.0},
		{"int + float promotes to float", binE("+", colE("Price"), colE("Discount")), cell.TypeFloat, 0, 10.1},
		{"int * literal int", binE("*", colE("Qty"), litE(vnum("3"))), cell.TypeInt, 15, 0},
		{"precedence: a + b * c", binE("+", colE("Price"), binE("*", colE("Qty"), litE(vnum("2")))), cell.TypeInt, 20, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvalExpr(tc.e, row, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.Type != tc.wantType {
				t.Fatalf("type: got %s, want %s", got.Type, tc.wantType)
			}
			if tc.wantType == cell.TypeInt && got.Cell.Int != tc.wantInt {
				t.Fatalf("int value: got %d, want %d", got.Cell.Int, tc.wantInt)
			}
			if tc.wantType == cell.TypeFloat && got.Cell.Float != tc.wantFlt {
				t.Fatalf("float value: got %g, want %g", got.Cell.Float, tc.wantFlt)
			}
		})
	}
}

func TestEvalExprDivideByZero(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)
	row := tbl.Rows[1] // Qty = 0
	got, err := EvalExpr(binE("/", colE("Price"), colE("Qty")), row, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Cell.Null {
		t.Fatalf("Price / 0 should be NULL, got %+v", got)
	}
	if got.Type != cell.TypeFloat {
		t.Fatalf("divide-by-zero result type should be float, got %s", got.Type)
	}
}

func TestEvalExprNullPropagates(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)

	// cell.Row 2: Discount=NULL. Price * Discount should be NULL.
	row := tbl.Rows[2]
	got, err := EvalExpr(binE("*", colE("Price"), colE("Discount")), row, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Cell.Null {
		t.Fatalf("int * NULL should be NULL, got %+v", got)
	}

	// cell.Row 3: Price=NULL. NULL + 1 should be NULL.
	row = tbl.Rows[3]
	got, err = EvalExpr(binE("+", colE("Price"), litE(vnum("1"))), row, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Cell.Null {
		t.Fatalf("NULL + 1 should be NULL, got %+v", got)
	}
}

func TestEvalExprTypeErrors(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)
	row := tbl.Rows[0]

	cases := []struct {
		name string
		e    parse.Expr
	}{
		{"string + int", binE("+", colE("Name"), litE(vnum("1")))},
		{"int * string literal", binE("*", colE("Price"), litE(vstr("foo")))},
		{"bool LHS", binE("+", litE(vbool(true)), litE(vnum("1")))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EvalExpr(tc.e, row, ctx)
			if err == nil {
				t.Fatal("expected type error")
			}
			if !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("error should explain: %v", err)
			}
		})
	}
}

func TestEvalExprAggregateRejected(t *testing.T) {
	// Evaluating an aggregate without ctx.AggResults populated is the path
	// taken outside an aggregation context (e.g. inside WHERE). It must
	// surface a clear error, not silently produce zero.
	_, err := EvalExpr(aggStar(), nil, nil)
	if err == nil {
		t.Fatal("aggregate outside aggregation context should error")
	}
	if !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("error should mention aggregate: %v", err)
	}
}

// Integration: WHERE arithmetic LHS now evaluates in v2.0.
func TestMatchesArithmeticLHS(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)

	// WHERE Price * Qty > 25
	// row 0: 10*5 = 50 > 25 ✓
	// row 1: 20*0 = 0 not > 25
	// row 2: 30*2 = 60 > 25 ✓
	// row 3: NULL*4 = NULL (excluded)
	pred := cmpE(binE("*", colE("Price"), colE("Qty")), ">", vnum("25"))
	ids := matchingIDs(t, tbl, ctx, pred)
	if !equalIDs(ids, []int64{1, 3}) {
		t.Fatalf("Price*Qty > 25: got %v, want [1 3]", ids)
	}
}

func TestMatchesArithmeticNullExcludes(t *testing.T) {
	tbl := numericFixture()
	ctx := NewEvalContext(tbl)

	// cell.Row 1 has Qty=0, so Price/Qty = NULL → row excluded.
	// cell.Row 3 has Price=NULL → row excluded.
	// Rows 0 and 2: 10/5 = 2.0, 30/2 = 15.0 → both > 1.0.
	pred := cmpE(binE("/", colE("Price"), colE("Qty")), ">", vnum("1"))
	ids := matchingIDs(t, tbl, ctx, pred)
	if !equalIDs(ids, []int64{1, 3}) {
		t.Fatalf("Price/Qty > 1: got %v, want [1 3]", ids)
	}
}

func TestValidatePredicateArithmetic(t *testing.T) {
	tbl := numericFixture()
	// Unknown column inside an arithmetic LHS surfaces from ValidateExpr.
	pred := cmpE(binE("*", colE("Price"), colE("Nope")), ">", vnum("0"))
	err := ValidatePredicate(pred, tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column inside arithmetic")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error should mention column: %v", err)
	}
}
