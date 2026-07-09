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
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
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
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
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
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.OrderBy[0].Column != "Salary" {
		t.Errorf("ORDER BY: got %q, want %q", sel.OrderBy[0].Column, "Salary")
	}
}

func TestCanonicalizeGroupBy(t *testing.T) {
	stmt := mustParse(t, "SELECT lastname, COUNT(*) GROUP BY lastname")
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	col, ok := sel.GroupBy[0].(*parse.ColumnExpr)
	if !ok {
		t.Fatalf("GROUP BY: got %T, want *parse.ColumnExpr", sel.GroupBy[0])
	}
	if col.Name != "Lastname" {
		t.Errorf("GROUP BY: got %q, want %q", col.Name, "Lastname")
	}
}

func TestCanonicalizeUpdateAssignment(t *testing.T) {
	stmt := mustParse(t, "UPDATE SET salary = 90000 WHERE firstname = 'John'")
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	upd := stmt.(*parse.UpdateStmt)
	if upd.Assignments[0].Column != "Salary" {
		t.Errorf("UPDATE SET column: got %q, want %q", upd.Assignments[0].Column, "Salary")
	}
}

func TestCanonicalizeInsertColumns(t *testing.T) {
	stmt := mustParse(t, "INSERT (firstname, LASTNAME) VALUES ('Jane', 'Doe')")
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	ins := stmt.(*parse.InsertStmt)
	if ins.Columns[0] != "Firstname" || ins.Columns[1] != "Lastname" {
		t.Errorf("INSERT columns: got %v, want [Firstname Lastname]", ins.Columns)
	}
}

func TestCanonicalizeLikeAndBetweenAndNullTest(t *testing.T) {
	stmt := mustParse(t, "SELECT * WHERE firstname LIKE 'J%' AND salary BETWEEN 1 AND 9 AND lastname IS NOT NULL")
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
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
	err := CanonicalizeStmt(stmt, sampleSchema(), nil)
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
	err := CanonicalizeStmt(stmt, schema, nil)
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
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.Columns[0].Expr.(*parse.ColumnExpr).Name != "Firstname" {
		t.Error("exact-case column should be unchanged")
	}
}

func TestCanonicalizeDisplayNameAlias(t *testing.T) {
	// SP backend pattern: internal name is field_1 / field_2; user types the
	// display name (vendor, salary) and resolves to the internal name.
	schema := map[string]cell.ColumnInfo{
		"field_1": {Name: "field_1", Type: cell.TypeString},
		"field_2": {Name: "field_2", Type: cell.TypeInt},
	}
	aliases := map[string][]string{
		"vendor": {"field_1"},
		"salary": {"field_2"},
	}
	stmt := mustParse(t, "SELECT vendor WHERE Salary > 90000")
	if err := CanonicalizeStmt(stmt, schema, aliases); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if got := sel.Columns[0].Expr.(*parse.ColumnExpr).Name; got != "field_1" {
		t.Errorf("vendor → %q, want field_1", got)
	}
	cmp := sel.Where.(*parse.Comparison)
	if got := cmp.LExpr.(*parse.ColumnExpr).Name; got != "field_2" {
		t.Errorf("Salary → %q, want field_2", got)
	}
}

func TestCanonicalizeInternalNameStillWorks(t *testing.T) {
	// The internal name remains a valid handle even when aliases are present,
	// so existing scripts / power users referencing field_N keep working.
	schema := map[string]cell.ColumnInfo{
		"field_5": {Name: "field_5", Type: cell.TypeString},
	}
	aliases := map[string][]string{"contract_administrator": {"field_5"}}
	stmt := mustParse(t, "SELECT * WHERE field_5 = 'jane'")
	if err := CanonicalizeStmt(stmt, schema, aliases); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
}

func TestCanonicalizeSchemaShadowsAlias(t *testing.T) {
	// Internal name "Vendor" and a different column's display also being
	// "Vendor": schema match wins, alias is shadowed. Predictable for the
	// user who typed the name that exists internally.
	schema := map[string]cell.ColumnInfo{
		"Vendor":  {Name: "Vendor", Type: cell.TypeString},
		"field_9": {Name: "field_9", Type: cell.TypeString},
	}
	aliases := map[string][]string{"Vendor": {"field_9"}}
	stmt := mustParse(t, "SELECT vendor")
	if err := CanonicalizeStmt(stmt, schema, aliases); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if got := sel.Columns[0].Expr.(*parse.ColumnExpr).Name; got != "Vendor" {
		t.Errorf("vendor → %q, want Vendor (internal wins)", got)
	}
}

func TestCanonicalizeAmbiguousDisplayName(t *testing.T) {
	// Two columns sharing a display name → ambiguous; the error lists the
	// internal names so the user can pick.
	schema := map[string]cell.ColumnInfo{
		"field_3": {Name: "field_3", Type: cell.TypeString},
		"field_4": {Name: "field_4", Type: cell.TypeString},
	}
	aliases := map[string][]string{"state": {"field_3", "field_4"}}
	stmt := mustParse(t, "SELECT * WHERE state = 'TX'")
	err := CanonicalizeStmt(stmt, schema, aliases)
	if err == nil {
		t.Fatal("expected ambiguous-column error")
	}
	if !strings.Contains(err.Error(), "ambiguous column") {
		t.Errorf("want ambiguous error, got %v", err)
	}
	if !strings.Contains(err.Error(), `"field_3"`) || !strings.Contains(err.Error(), `"field_4"`) {
		t.Errorf("error should list internal names: %v", err)
	}
}

func TestCanonicalizeAliasCaseInsensitive(t *testing.T) {
	// Display name lookup case-folds the same way schema lookup does.
	schema := map[string]cell.ColumnInfo{"field_1": {Name: "field_1", Type: cell.TypeString}}
	aliases := map[string][]string{"Vendor": {"field_1"}}
	stmt := mustParse(t, "SELECT VENDOR")
	if err := CanonicalizeStmt(stmt, schema, aliases); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if got := sel.Columns[0].Expr.(*parse.ColumnExpr).Name; got != "field_1" {
		t.Errorf("VENDOR → %q, want field_1", got)
	}
}

func TestCanonicalizeOrderByAliasPassesThrough(t *testing.T) {
	// Aggregated ORDER BY may reference a projection alias, not a schema
	// column. The canonicalizer should not reject an unknown ORDER BY name —
	// downstream resolveOrderByOutput handles alias matching.
	stmt := mustParse(t, "SELECT COUNT(*) AS n ORDER BY n DESC")
	if err := CanonicalizeStmt(stmt, sampleSchema(), nil); err != nil {
		t.Fatalf("CanonicalizeStmt: %v", err)
	}
	sel := stmt.(*parse.SelectStmt)
	if sel.OrderBy[0].Column != "n" {
		t.Errorf("alias ORDER BY: got %q, want %q", sel.OrderBy[0].Column, "n")
	}
}
