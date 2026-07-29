package eval

import (
	"strings"
	"testing"
	"time"

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

// datePartTable holds a real date column, a string column carrying ISO text and
// one value that is not a date at all, and an int column that YEAR must refuse.
func datePartTable() (*cell.Table, *EvalContext) {
	d := func(s string) time.Time {
		t, err := cell.ParseDateString(s)
		if err != nil {
			panic(err)
		}
		return t
	}
	tbl := &cell.Table{
		Columns: []string{"hired", "text", "cost"},
		Schema: map[string]cell.ColumnInfo{
			"hired": {Name: "hired", Type: cell.TypeDate},
			"text":  {Name: "text", Type: cell.TypeString},
			"cost":  {Name: "cost", Type: cell.TypeInt},
		},
		Rows: []cell.Row{
			{{Date: d("2024-03-04")}, {Str: "2023-07-15"}, {Int: 100}},
			{{Null: true}, {Str: "sometime"}, {Int: 200}},
		},
	}
	return tbl, NewEvalContext(tbl)
}

func TestDatePartsOnDateColumn(t *testing.T) {
	tbl, ctx := datePartTable()
	cases := []struct {
		fn   string
		want int64
	}{
		{"YEAR", 2024},
		{"MONTH", 3},
		{"DAY", 4},
	}
	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			expr := &parse.FuncCallExpr{Name: tc.fn, Args: []parse.Expr{&parse.ColumnExpr{Name: "hired"}}}
			got, err := EvalExpr(expr, tbl.Rows[0], ctx)
			if err != nil {
				t.Fatalf("EvalExpr: %v", err)
			}
			if got.Type != cell.TypeInt {
				t.Errorf("got type %s, want int", got.Type)
			}
			if got.Cell.Int != tc.want {
				t.Errorf("got %d, want %d", got.Cell.Int, tc.want)
			}
		})
	}
}

func TestDatePartsReadIsoStringsAndNullTheRest(t *testing.T) {
	tbl, ctx := datePartTable()
	expr := &parse.FuncCallExpr{Name: "YEAR", Args: []parse.Expr{&parse.ColumnExpr{Name: "text"}}}

	got, err := EvalExpr(expr, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("EvalExpr: %v", err)
	}
	if got.Cell.Int != 2023 {
		t.Errorf("ISO text: got %d, want 2023", got.Cell.Int)
	}

	// Not a date, and not fatal: one bad cell must not abort a scan over the
	// rest. NULL is SQL's own word for "no answer for this row".
	got, err = EvalExpr(expr, tbl.Rows[1], ctx)
	if err != nil {
		t.Fatalf("an unparseable cell must not error the query: %v", err)
	}
	if !got.Cell.Null {
		t.Errorf("got %+v, want NULL", got)
	}
}

func TestDatePartsPropagateNull(t *testing.T) {
	tbl, ctx := datePartTable()
	expr := &parse.FuncCallExpr{Name: "MONTH", Args: []parse.Expr{&parse.ColumnExpr{Name: "hired"}}}
	got, err := EvalExpr(expr, tbl.Rows[1], ctx)
	if err != nil {
		t.Fatalf("EvalExpr: %v", err)
	}
	if !got.Cell.Null {
		t.Errorf("got %+v, want NULL", got)
	}
}

func TestDatePartOnNumericColumnFailsAtPlanTime(t *testing.T) {
	// The point is *when* this fails: before the scan, not on row 900,000.
	// Excel would answer with a serial number here; xql has none to give.
	tbl, _ := datePartTable()
	expr := &parse.FuncCallExpr{Name: "YEAR", Args: []parse.Expr{&parse.ColumnExpr{Name: "cost"}}}
	err := ValidateExpr(expr, tbl.Schema)
	if err == nil || !strings.Contains(err.Error(), "expects a date") {
		t.Fatalf("got %v, want a date-type rejection", err)
	}
	if !strings.Contains(err.Error(), "--type cost=date") {
		t.Errorf("the error should name the override that fixes it: %v", err)
	}
}

func TestUnknownFunctionRoutesToTheToolThatHasIt(t *testing.T) {
	schema := map[string]cell.ColumnInfo{"x": {Name: "x", Type: cell.TypeString}}
	cases := []struct{ fn, want string }{
		{"REPLACE", "xled"},             // rewriting a cell's text
		{"UNPIVOT", "xshape"},           // changing the table's shape
		{"LENGTH", "not available yet"}, // a real absence in xql, with a way through
	}
	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			expr := &parse.FuncCallExpr{Name: tc.fn, Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
			err := ValidateExpr(expr, schema)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a pointer to %q", err, tc.want)
			}
		})
	}
	// A plain typo stays a plain typo — no invented destination.
	expr := &parse.FuncCallExpr{Name: "LOWERR", Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
	err := ValidateExpr(expr, schema)
	if err == nil || strings.Contains(err.Error(), "xled") || strings.Contains(err.Error(), "xshape") {
		t.Fatalf("got %v, want a bare unknown-function error", err)
	}
}
