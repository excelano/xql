package eval

import (
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// scalarTestTable builds a small in-memory table so scalar-function tests can
// exercise EvalExpr through the same path a live SELECT would take. One row
// each: normal string, padded, empty, null.
func scalarTestTable() (*cell.Table, *EvalContext) {
	tbl := &cell.Table{
		Columns: []string{"name"},
		Schema:  map[string]cell.ColumnInfo{"name": {Name: "name", Type: cell.TypeString}},
		Rows: []cell.Row{
			{{Str: "CoStar"}},
			{{Str: "  hello  "}},
			{{Str: ""}},
			{{Null: true}},
		},
	}
	return tbl, NewEvalContext(tbl)
}

func TestScalarLowerUpperTrimOnColumn(t *testing.T) {
	tbl, ctx := scalarTestTable()
	cases := []struct {
		fn      string
		row     int
		want    string
		wantNil bool
	}{
		{"LOWER", 0, "costar", false},
		{"UPPER", 0, "COSTAR", false},
		{"TRIM", 1, "hello", false},
		{"LOWER", 2, "", false},
		{"UPPER", 3, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			expr := &parse.FuncCallExpr{Name: tc.fn, Args: []parse.Expr{&parse.ColumnExpr{Name: "name"}}}
			got, err := EvalExpr(expr, tbl.Rows[tc.row], ctx)
			if err != nil {
				t.Fatalf("EvalExpr: %v", err)
			}
			if tc.wantNil {
				if !got.Cell.Null {
					t.Errorf("row %d: expected NULL, got %+v", tc.row, got)
				}
				return
			}
			if got.Cell.Null {
				t.Fatalf("row %d: expected non-null, got NULL", tc.row)
			}
			if got.Cell.Str != tc.want {
				t.Errorf("row %d: got %q, want %q", tc.row, got.Cell.Str, tc.want)
			}
		})
	}
}

func TestScalarUnknownFunction(t *testing.T) {
	schema := map[string]cell.ColumnInfo{"x": {Name: "x", Type: cell.TypeString}}
	expr := &parse.FuncCallExpr{Name: "REVERSE", Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
	err := ValidateExpr(expr, schema)
	if err == nil || !strings.Contains(err.Error(), "unknown function") {
		t.Fatalf("got %v, want unknown-function error", err)
	}
}

func TestScalarWrongArity(t *testing.T) {
	schema := map[string]cell.ColumnInfo{"x": {Name: "x", Type: cell.TypeString}}
	expr := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{
		&parse.ColumnExpr{Name: "x"},
		&parse.ColumnExpr{Name: "x"},
	}}
	err := ValidateExpr(expr, schema)
	if err == nil || !strings.Contains(err.Error(), "expects 1 argument") {
		t.Fatalf("got %v, want arity error", err)
	}
}

func TestScalarRejectsAggregateArg(t *testing.T) {
	schema := map[string]cell.ColumnInfo{"x": {Name: "x", Type: cell.TypeInt}}
	expr := &parse.FuncCallExpr{
		Name: "LOWER",
		Args: []parse.Expr{&parse.AggregateExpr{Func: "COUNT", Star: true}},
	}
	err := ValidateExpr(expr, schema)
	if err == nil || !strings.Contains(err.Error(), "aggregate arguments are not allowed") {
		t.Fatalf("got %v, want aggregate-arg rejection", err)
	}
}

func TestScalarLowerCoercesNumeric(t *testing.T) {
	// LOWER(price) on an integer column should stringify the value: 42 → "42".
	// That way a user can dedup by normalized text form without an explicit
	// cast, matching what CSV import tends to hand back.
	tbl := &cell.Table{
		Columns: []string{"price"},
		Schema:  map[string]cell.ColumnInfo{"price": {Name: "price", Type: cell.TypeInt}},
		Rows:    []cell.Row{{{Int: 42}}},
	}
	ctx := NewEvalContext(tbl)
	expr := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "price"}}}
	got, err := EvalExpr(expr, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("EvalExpr: %v", err)
	}
	if got.Cell.Str != "42" {
		t.Errorf("got %q, want %q", got.Cell.Str, "42")
	}
}

func TestExprEqual(t *testing.T) {
	a := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
	b := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
	c := &parse.FuncCallExpr{Name: "UPPER", Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
	d := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "y"}}}
	if !ExprEqual(a, b) {
		t.Error("LOWER(x) == LOWER(x) should hold")
	}
	if ExprEqual(a, c) {
		t.Error("LOWER(x) == UPPER(x) should not hold")
	}
	if ExprEqual(a, d) {
		t.Error("LOWER(x) == LOWER(y) should not hold")
	}
}
