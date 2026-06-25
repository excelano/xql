package eval

import (
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

func sampleSchema() map[string]cell.ColumnInfo {
	return map[string]cell.ColumnInfo{
		"Firstname": {Name: "Firstname", Type: cell.TypeString},
		"Lastname":  {Name: "Lastname", Type: cell.TypeString},
		"Salary":    {Name: "Salary", Type: cell.TypeInt},
	}
}

func mustParse(t *testing.T, src string) parse.Stmt {
	t.Helper()
	stmt, err := parse.Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return stmt
}

func TestCanonicalizeWhereColumn(t *testing.T) {
	stmt := mustParse(t, "SELECT * WHERE firstname = 'John'")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	c := sel.Where.(*parse.Comparison)
	col := c.LExpr.(*parse.ColumnExpr)
	if col.Name != "Firstname" {
		t.Errorf("WHERE column: got %q, want %q", col.Name, "Firstname")
	}
}

func TestCanonicalizeProjection(t *testing.T) {
	stmt := mustParse(t, "SELECT firstname, LASTNAME, salary")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	want := []string{"Firstname", "Lastname", "Salary"}
	for i, w := range want {
		got := sel.Columns[i].Expr.(*parse.ColumnExpr).Name
		if got != w {
			t.Errorf("projection[%d]: got %q, want %q", i, got, w)
		}
	}
}

func TestCanonicalizeOrderBy(t *testing.T) {
	stmt := mustParse(t, "SELECT * ORDER BY salary DESC")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.OrderBy[0].Column != "Salary" {
		t.Errorf("ORDER BY: got %q, want %q", sel.OrderBy[0].Column, "Salary")
	}
}

func TestCanonicalizeGroupBy(t *testing.T) {
	stmt := mustParse(t, "SELECT lastname, COUNT(*) GROUP BY lastname")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.GroupBy[0] != "Lastname" {
		t.Errorf("GROUP BY: got %q, want %q", sel.GroupBy[0], "Lastname")
	}
}

func TestCanonicalizeUpdateAssignment(t *testing.T) {
	stmt := mustParse(t, "UPDATE SET salary = 90000 WHERE firstname = 'John'")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	upd := stmt.(*parse.UpdateStmt)
	if upd.Assignments[0].Column != "Salary" {
		t.Errorf("UPDATE SET column: got %q, want %q", upd.Assignments[0].Column, "Salary")
	}
}

func TestCanonicalizeInsertColumns(t *testing.T) {
	stmt := mustParse(t, "INSERT (firstname, LASTNAME) VALUES ('Jane', 'Doe')")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	ins := stmt.(*parse.InsertStmt)
	if ins.Columns[0] != "Firstname" || ins.Columns[1] != "Lastname" {
		t.Errorf("INSERT columns: got %v, want [Firstname Lastname]", ins.Columns)
	}
}

func TestCanonicalizeLikeAndBetweenAndNullTest(t *testing.T) {
	stmt := mustParse(t, "SELECT * WHERE firstname LIKE 'J%' AND salary BETWEEN 1 AND 9 AND lastname IS NOT NULL")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	root := sel.Where.(*parse.BinaryOp)
	// (firstname LIKE 'J%' AND salary BETWEEN 1 AND 9) AND lastname IS NOT NULL
	right := root.R.(*parse.NullTest)
	if right.Column != "Lastname" {
		t.Errorf("IS NOT NULL column: got %q, want %q", right.Column, "Lastname")
	}
	leftPair := root.L.(*parse.BinaryOp)
	if leftPair.L.(*parse.LikeOp).Column != "Firstname" {
		t.Errorf("LIKE column: %q", leftPair.L.(*parse.LikeOp).Column)
	}
	if leftPair.R.(*parse.BetweenOp).Column != "Salary" {
		t.Errorf("BETWEEN column: %q", leftPair.R.(*parse.BetweenOp).Column)
	}
}

func TestCanonicalizeUnknownColumnError(t *testing.T) {
	stmt := mustParse(t, "SELECT * WHERE nope = 'x'")
	err := CanonicalizeStmt(stmt, sampleSchema())
	if err == nil {
		t.Fatal("expected unknown-column error")
	}
	if !strings.Contains(err.Error(), `unknown column "nope"`) {
		t.Errorf("error: %v", err)
	}
}

func TestCanonicalizeAmbiguousColumnError(t *testing.T) {
	schema := map[string]cell.ColumnInfo{
		"ID": {Name: "ID", Type: cell.TypeInt},
		"id": {Name: "id", Type: cell.TypeString},
	}
	stmt := mustParse(t, "SELECT * WHERE id = 1")
	err := CanonicalizeStmt(stmt, schema)
	if err == nil {
		t.Fatal("expected ambiguous-column error")
	}
	if !strings.Contains(err.Error(), "ambiguous column") {
		t.Errorf("error should mention ambiguity: %v", err)
	}
	if !strings.Contains(err.Error(), `"ID"`) || !strings.Contains(err.Error(), `"id"`) {
		t.Errorf("error should list both names: %v", err)
	}
}

func TestCanonicalizeExactMatchUnaffected(t *testing.T) {
	// Already-canonical references should pass through unchanged.
	stmt := mustParse(t, "SELECT Firstname WHERE Salary > 0")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.Columns[0].Expr.(*parse.ColumnExpr).Name != "Firstname" {
		t.Error("exact-case column should be unchanged")
	}
}

func TestCanonicalizeOrderByAliasPassesThrough(t *testing.T) {
	// Aggregated ORDER BY may reference a projection alias, not a schema
	// column. The canonicalizer should not reject an unknown ORDER BY name —
	// downstream resolveOrderByOutput handles alias matching.
	stmt := mustParse(t, "SELECT COUNT(*) AS n ORDER BY n DESC")
	if err := CanonicalizeStmt(stmt, sampleSchema()); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.OrderBy[0].Column != "n" {
		t.Errorf("alias ORDER BY: got %q, want %q", sel.OrderBy[0].Column, "n")
	}
}
