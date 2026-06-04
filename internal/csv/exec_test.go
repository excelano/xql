package csv

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// numericFixture exercises numeric/aggregate paths: two int columns, one
// float, one nullable int, one string. Duplicated from internal/eval for the
// same reason fixtureTable is.
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

// fixtureTable builds a small in-memory cell.Table representative of a typical
// CSV: mixed types, NULL cells, enough rows to exercise filtering. Duplicated
// from internal/eval because the executor tests need it in the csv package
// scope; the duplication is intentional and self-contained.
func fixtureTable() *cell.Table {
	cols := []string{"ID", "Title", "Status", "Priority", "Archived", "Modified"}
	schema := map[string]cell.ColumnInfo{
		"ID":       {Name: "ID", Type: cell.TypeInt},
		"Title":    {Name: "Title", Type: cell.TypeString},
		"Status":   {Name: "Status", Type: cell.TypeString},
		"Priority": {Name: "Priority", Type: cell.TypeInt},
		"Archived": {Name: "Archived", Type: cell.TypeBool},
		"Modified": {Name: "Modified", Type: cell.TypeDate},
	}
	rows := []cell.Row{
		{cell.Cell{Int: 1}, cell.Cell{Str: "Alpha"}, cell.Cell{Str: "Open"}, cell.Cell{Int: 3}, cell.Cell{Bool: false}, cell.Cell{Date: mustDate("2024-01-15")}},
		{cell.Cell{Int: 2}, cell.Cell{Str: "Beta"}, cell.Cell{Str: "Done"}, cell.Cell{Int: 1}, cell.Cell{Bool: true}, cell.Cell{Date: mustDate("2023-11-30")}},
		{cell.Cell{Int: 3}, cell.Cell{Str: "Gamma"}, cell.Cell{Str: "Open"}, cell.Cell{Int: 5}, cell.Cell{Bool: false}, cell.Cell{Null: true}},
		{cell.Cell{Int: 4}, cell.Cell{Str: "Delta"}, cell.Cell{Str: "Open"}, cell.Cell{Null: true}, cell.Cell{Bool: false}, cell.Cell{Date: mustDate("2024-03-01")}},
	}
	return &cell.Table{
		Path:      "fixture.csv",
		Columns:   cols,
		Schema:    schema,
		Rows:      rows,
		Delim:     ',',
		HasHeader: true,
	}
}

// newExec produces an Executor bound to a fresh fixture cell.Table, with output
// captured to an in-memory buffer for assertion. Each test starts from a clean
// fixture so mutations don't leak between cases.
func newExec(t *testing.T) (*Executor, *bytes.Buffer, string) {
	t.Helper()
	tbl := fixtureTable()
	path := filepath.Join(t.TempDir(), "fixture.csv")
	tbl.Path = path
	// Persist so write tests have a real file to rewrite over.
	if err := SaveCSV(tbl, ""); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	return &Executor{Table: tbl, Mode: "tsv", Headers: true, Out: buf}, buf, path
}

func TestExecSelectStar(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT *")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("SELECT * lines: %d, want 5 (header + 4 rows): %q", len(lines), out.String())
	}
	if lines[0] != "ID\tTitle\tStatus\tPriority\tArchived\tModified" {
		t.Errorf("header: %q", lines[0])
	}
}

func TestExecSelectProjection(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Title, Priority WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// header + 3 Open rows
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %q", len(lines), out.String())
	}
	if lines[0] != "Title\tPriority" {
		t.Errorf("header: %q", lines[0])
	}
}

func TestExecSelectArithmeticProjection(t *testing.T) {
	e, out, _ := newExec(t)
	// Priority * 10 evaluated per row. Label synthesizes to "Priority * 10".
	stmt, err := parse.Parse("SELECT ID, Priority * 10 WHERE Status = 'Open' AND Priority IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "ID\tPriority * 10" {
		t.Errorf("header: %q", lines[0])
	}
	// Open rows with non-null priority: ID 1 (P=3), ID 3 (P=5) → 30, 50.
	if !strings.Contains(out.String(), "1\t30") {
		t.Errorf("expected 1\\t30: %q", out.String())
	}
	if !strings.Contains(out.String(), "3\t50") {
		t.Errorf("expected 3\\t50: %q", out.String())
	}
}

func TestExecSelectAlias(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Priority * 10 AS scaled WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "scaled" {
		t.Errorf("header should use alias: %q", lines[0])
	}
	if lines[1] != "30" {
		t.Errorf("value: %q", lines[1])
	}
}

func TestExecSelectMixedColumnAndExpression(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Title, Priority, Priority * 2 AS doubled WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "Title\tPriority\tdoubled" {
		t.Errorf("header: %q", lines[0])
	}
	if lines[1] != "Alpha\t3\t6" {
		t.Errorf("row: %q", lines[1])
	}
}

func TestExecSelectLiteralProjection(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID, 'fixed' AS label WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "1\tfixed") {
		t.Errorf("expected literal projection: %q", out.String())
	}
}

func TestExecSelectArithmeticNullRenders(t *testing.T) {
	// cell.Row 4 has Priority=NULL. Priority * 2 should render as empty trailing
	// field. TrimSpace would eat the trailing tab/empty field, so strip only
	// the final newline.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID, Priority * 2 WHERE ID = 4")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	parts := strings.Split(lines[1], "\t")
	if len(parts) != 2 || parts[0] != "4" || parts[1] != "" {
		t.Errorf("NULL arithmetic should render as 4\\t<empty>: parts=%q", parts)
	}
}

func TestExecSelectDuplicateLabelRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID, ID")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("duplicate output label should error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should explain: %v", err)
	}
}

func TestExecSelectDuplicateResolvedByAlias(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID, ID AS dup WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "ID\tdup") {
		t.Errorf("alias should resolve collision: %q", out.String())
	}
}

func TestExecSelectDistinctOverComputed(t *testing.T) {
	// Priority * 0 evaluates to 0 for every non-null row, NULL for row 4.
	// SELECT DISTINCT collapses to two output rows: one "0" and one empty.
	// Each Fprintln appends a newline, so 3 lines means 3 newlines in the
	// raw output.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT DISTINCT Priority * 0")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	if n := strings.Count(got, "\n"); n != 3 {
		t.Fatalf("expected 3 newlines (header + 2 rows), got %d: %q", n, got)
	}
	if got != "Priority * 0\n0\n\n" {
		t.Errorf("rendered: %q", got)
	}
}

func TestExecSelectCountStar(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "COUNT(*)\n4") {
		t.Errorf("expected COUNT(*) = 4, got: %q", out.String())
	}
}

func TestExecSelectCountColumnSkipsNull(t *testing.T) {
	// Priority has 1 NULL row (Delta); COUNT(Priority) = 3.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(Priority)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "COUNT(Priority)\n3") {
		t.Errorf("expected COUNT(Priority) = 3, got: %q", out.String())
	}
}

func TestExecSelectSumAvgMinMax(t *testing.T) {
	// Priority across rows: 3, 1, 5, NULL → sum=9, avg=3.0, min=1, max=5.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT SUM(Priority), AVG(Priority), MIN(Priority), MAX(Priority)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "SUM(Priority)\tAVG(Priority)\tMIN(Priority)\tMAX(Priority)\n9\t3\t1\t5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectAggregateWithWhere(t *testing.T) {
	// WHERE Status = 'Open' → rows 1, 3, 4. SUM(Priority) = 3+5 (NULL skipped) = 8.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*), SUM(Priority) WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "COUNT(*)\tSUM(Priority)\n3\t8"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectAggregateNoRows(t *testing.T) {
	// WHERE matches nothing: COUNT(*) = 0; SUM, AVG, MIN, MAX = NULL (empty cell).
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*), SUM(Priority), AVG(Priority), MIN(Priority), MAX(Priority) WHERE Status = 'Closed'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	// Four NULL fields render as four trailing empty tabs.
	want := "COUNT(*)\tSUM(Priority)\tAVG(Priority)\tMIN(Priority)\tMAX(Priority)\n0\t\t\t\t"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectAggregateInsideArithmetic(t *testing.T) {
	// SUM(Priority) / COUNT(*) — full table: 9 / 4 = 2.25 (division promotes to float).
	// COUNT(*) + 1 = 5.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*) + 1 AS plus_one, SUM(Priority) / COUNT(*) AS ratio")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "plus_one\tratio\n5\t2.25"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectAggregateOverComputedArg(t *testing.T) {
	// SUM(Priority * 10) over non-null priorities: 30 + 10 + 50 = 90.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT SUM(Priority * 10)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "90") {
		t.Errorf("expected SUM(Priority*10) = 90: %q", out.String())
	}
}

func TestExecSelectAggregateLabelAndAlias(t *testing.T) {
	// Unaliased aggregate renders as source text; aliased uses the alias.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*), SUM(Priority) AS total")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "COUNT(*)\ttotal\n") {
		t.Errorf("unexpected header: %q", out.String())
	}
}

func TestExecSelectBareColumnWithAggregateRejected(t *testing.T) {
	// SELECT Title, COUNT(*) without GROUP BY violates strict aggregation.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Title, COUNT(*)")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected rejection of bare column mixed with aggregate")
	}
	if !strings.Contains(err.Error(), "Title") || !strings.Contains(err.Error(), "GROUP BY") {
		t.Errorf("error should mention column and GROUP BY: %v", err)
	}
}

func TestExecSelectSumOnStringRejected(t *testing.T) {
	// SUM(Title) — string argument should fail at plan time.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT SUM(Title)")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected SUM(Title) to be rejected")
	}
	if !strings.Contains(err.Error(), "numeric") {
		t.Errorf("error should mention numeric requirement: %v", err)
	}
}

func TestExecSelectMinMaxOverString(t *testing.T) {
	// MIN/MAX work on any comparable type; Title is "Alpha" .. "Gamma".
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT MIN(Title), MAX(Title)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "MIN(Title)\tMAX(Title)\nAlpha\tGamma"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectAggregateInWhereRejected(t *testing.T) {
	// WHERE COUNT(*) > 1 is meaningless; the executor surfaces an aggregate
	// error when EvalExpr encounters AggregateExpr without slot context.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT * WHERE COUNT(*) > 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected aggregate-in-WHERE to error")
	}
	if !strings.Contains(err.Error(), "aggregate") {
		t.Errorf("error should mention aggregate: %v", err)
	}
}

func TestExecSelectAggregateFloatColumn(t *testing.T) {
	// numericFixture has Discount as float with one NULL row: 0.1, 0.0, NULL, 0.5.
	// SUM = 0.6; AVG = 0.6/3 surfaces the IEEE 754 short-repr (~0.2). The
	// test pins the verbatim rendering so any future change to float
	// formatting is caught.
	tbl := numericFixture()
	path := filepath.Join(t.TempDir(), "numeric.csv")
	tbl.Path = path
	if err := SaveCSV(tbl, ""); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	e := &Executor{Table: tbl, Mode: "tsv", Headers: true, Out: buf}
	stmt, err := parse.Parse("SELECT SUM(Discount), AVG(Discount)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	want := "SUM(Discount)\tAVG(Discount)\n0.6\t0.19999999999999998"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByStatus(t *testing.T) {
	// Fixture: Open=3 (rows 1,3,4), Done=1 (row 2). Insertion order Open first.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) GROUP BY Status")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tCOUNT(*)\nOpen\t3\nDone\t1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupBySumPerGroup(t *testing.T) {
	// Open priorities: 3, 5, NULL → SUM=8. Done priorities: 1 → SUM=1.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, SUM(Priority) GROUP BY Status")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tSUM(Priority)\nOpen\t8\nDone\t1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByMultipleColumns(t *testing.T) {
	// (Status, Archived): (Open,false)=2, (Done,true)=1, (Open,false)dup, (Open,false)dup
	// → (Open,false)=3, (Done,true)=1.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, Archived, COUNT(*) GROUP BY Status, Archived")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tArchived\tCOUNT(*)\nOpen\tfalse\t3\nDone\ttrue\t1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByNullIsOwnGroup(t *testing.T) {
	// Group by Priority: 3, 1, 5, NULL — each unique, NULL gets its own group.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Priority, COUNT(*) GROUP BY Priority")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	// Four groups in insertion order: 3, 1, 5, NULL.
	if !strings.Contains(got, "3\t1\n") || !strings.Contains(got, "1\t1\n") ||
		!strings.Contains(got, "5\t1\n") {
		t.Errorf("expected per-priority groups: %q", got)
	}
	// NULL group renders as empty field + 1.
	if !strings.Contains(got, "\n\t1\n") {
		t.Errorf("expected NULL group line: %q", got)
	}
}

func TestExecSelectGroupByHaving(t *testing.T) {
	// HAVING COUNT(*) > 1 keeps only Open (3 rows), drops Done (1 row).
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) AS n GROUP BY Status HAVING COUNT(*) > 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tn\nOpen\t3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByHavingAggregateNotInProjection(t *testing.T) {
	// HAVING SUM(Priority) > 5 keeps Open (sum=8), drops Done (sum=1). The
	// HAVING aggregate is not in the SELECT list — its slot lives in the
	// shared template only.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status GROUP BY Status HAVING SUM(Priority) > 5")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\nOpen"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByWithWhere(t *testing.T) {
	// WHERE Archived = false drops row 2 (Done/true). Remaining: 3 Open rows.
	// GROUP BY Status → one group: Open=3.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) WHERE Archived = false GROUP BY Status")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tCOUNT(*)\nOpen\t3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByHavingArithmetic(t *testing.T) {
	// AVG(Priority) per group: Open=(3+5)/2=4, Done=1. HAVING ratio > 2
	// keeps Open only. Tests arithmetic over aggregates inside HAVING.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, AVG(Priority) AS avg_p GROUP BY Status HAVING SUM(Priority) / COUNT(*) > 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tavg_p\nOpen\t4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByBareColumnRejected(t *testing.T) {
	// Title is not in GROUP BY — plan-time rejection.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Title, COUNT(*) GROUP BY Status")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected error for Title not in GROUP BY")
	}
	if !strings.Contains(err.Error(), "Title") || !strings.Contains(err.Error(), "GROUP BY") {
		t.Errorf("error should mention Title and GROUP BY: %v", err)
	}
}

func TestExecSelectGroupByUnknownColumnRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*) GROUP BY Nope")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected unknown-column error")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Errorf("error should mention Nope: %v", err)
	}
}

func TestExecSelectGroupByDuplicateColumnRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) GROUP BY Status, Status")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected duplicate-column error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

func TestExecSelectGroupByStarRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT * GROUP BY Status")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected SELECT * + GROUP BY rejection")
	}
	if !strings.Contains(err.Error(), "SELECT *") {
		t.Errorf("error should mention SELECT *: %v", err)
	}
}

func TestExecSelectGroupByOrderByGroupColumn(t *testing.T) {
	// Group insertion order is Open then Done; ORDER BY Status ASC must
	// flip that to Done then Open. Sorts post-projection on the output
	// label, not the source rows.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) GROUP BY Status ORDER BY Status")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tCOUNT(*)\nDone\t1\nOpen\t3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByOrderByAlias(t *testing.T) {
	// Same as the aggregate-sort test but referenced through an AS alias.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) AS n GROUP BY Status ORDER BY n DESC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tn\nOpen\t3\nDone\t1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectGroupByOrderByUnknownLabelRejected(t *testing.T) {
	// Aggregated queries cannot reach source columns through ORDER BY; a
	// non-projected column must be rejected with a clear hint.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) GROUP BY Status ORDER BY Priority")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected unknown-label rejection")
	}
	if !strings.Contains(err.Error(), "Priority") || !strings.Contains(err.Error(), "SELECT list") {
		t.Errorf("error should mention column and SELECT list: %v", err)
	}
}

func TestExecSelectGroupByOrderByLimit(t *testing.T) {
	// ORDER BY then LIMIT 1 → top group only. Verifies the slice 6 sort
	// runs before applyOffsetLimitRows.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) AS n GROUP BY Status ORDER BY n DESC LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tn\nOpen\t3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectImplicitAggregateOrderBy(t *testing.T) {
	// Single-row implicit aggregation — ORDER BY parses, resolves, and
	// trivially sorts the one-row result. No panic, just one row out.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*) AS n ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "n\n4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectImplicitHavingKeeps(t *testing.T) {
	// COUNT(*) = 4 > 0 → keep the row.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*) HAVING COUNT(*) > 0")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "COUNT(*)\n4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectImplicitHavingDrops(t *testing.T) {
	// COUNT(*) = 4 < 100 → HAVING false → header only, no data row.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*) HAVING COUNT(*) > 100")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "COUNT(*)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectImplicitHavingBareColumnRejected(t *testing.T) {
	// No GROUP BY → no bare columns allowed in HAVING.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT COUNT(*) HAVING Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected HAVING bare column rejection")
	}
	if !strings.Contains(err.Error(), "Status") || !strings.Contains(err.Error(), "HAVING") {
		t.Errorf("error should mention HAVING and Status: %v", err)
	}
}

func TestExecSelectHavingWithoutAggregationRejected(t *testing.T) {
	// HAVING without GROUP BY and without any aggregate projection is a
	// shape error — should be WHERE, not HAVING.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status HAVING Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected HAVING-without-aggregation rejection")
	}
	if !strings.Contains(err.Error(), "HAVING") {
		t.Errorf("error should mention HAVING: %v", err)
	}
}

func TestExecSelectGroupByHavingBareGroupColumn(t *testing.T) {
	// A bare column that IS in GROUP BY is fine in HAVING.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Status, COUNT(*) GROUP BY Status HAVING Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Status\tCOUNT(*)\nOpen\t3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExecSelectDistinct(t *testing.T) {
	// Fixture statuses: Open, Done, Open, Open → distinct {Open, Done}.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT DISTINCT Status")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("DISTINCT Status: %d lines, want 3 (header + Open + Done): %q", len(lines), out.String())
	}
	if lines[0] != "Status" {
		t.Errorf("header: %q", lines[0])
	}
	// First-seen order: Open, then Done.
	if lines[1] != "Open" || lines[2] != "Done" {
		t.Errorf("expected first-seen order Open, Done; got %q, %q", lines[1], lines[2])
	}
}

func TestExecSelectDistinctStarNoCollapse(t *testing.T) {
	// All four fixture rows differ across the full row, so DISTINCT * is a no-op.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT DISTINCT *")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("DISTINCT *: %d lines, want 5 (header + 4 rows): %q", len(lines), out.String())
	}
}

func TestExecSelectDistinctWithWhere(t *testing.T) {
	// WHERE filters first, then DISTINCT. Priority > 2 matches rows 1 (Open,3),
	// 3 (Open,5), 4 (Open,NULL). DISTINCT Status collapses to just Open.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT DISTINCT Status WHERE Priority > 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("DISTINCT + WHERE: %d lines, want 2: %q", len(lines), out.String())
	}
	if lines[1] != "Open" {
		t.Errorf("got %q, want Open", lines[1])
	}
}

func TestExecSelectDistinctMultiColumn(t *testing.T) {
	// Status × Archived pairs: (Open,false), (Done,true), (Open,false), (Open,false).
	// Distinct: (Open,false), (Done,true) → 2 rows.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT DISTINCT Status, Archived")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("DISTINCT 2-col: %d lines, want 3 (header + 2): %q", len(lines), out.String())
	}
}

func TestExecSelectDistinctNullsCollapse(t *testing.T) {
	// Two rows with NULL Priority should collapse to one under DISTINCT —
	// matches SQL's NULL-equal-to-NULL semantics for DISTINCT.
	tbl := &cell.Table{
		Path:    "x.csv",
		Columns: []string{"Priority"},
		Schema:  map[string]cell.ColumnInfo{"Priority": {Name: "Priority", Type: cell.TypeInt}},
		Rows: []cell.Row{
			{cell.Cell{Null: true}},
			{cell.Cell{Int: 1}},
			{cell.Cell{Null: true}},
		},
		Delim:     ',',
		HasHeader: true,
	}
	buf := &bytes.Buffer{}
	e := &Executor{Table: tbl, Mode: "tsv", Headers: true, Out: buf}
	stmt, err := parse.Parse("SELECT DISTINCT Priority")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (header + NULL + 1): %q", len(lines), buf.String())
	}
}

func TestExecSelectOrderByAsc(t *testing.T) {
	// Fixture priorities (in row order): 3, 1, 5, NULL.
	// ASC, NULLs LAST: 1, 3, 5, NULL → IDs 2, 1, 3, 4.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID ORDER BY Priority ASC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "2", "1", "3", "4"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: got %q, want %q", i, lines[i], w)
		}
	}
}

func TestExecSelectOrderByDesc(t *testing.T) {
	// DESC, NULLs FIRST: NULL, 5, 3, 1 → IDs 4, 3, 1, 2.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID ORDER BY Priority DESC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "4", "3", "1", "2"}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: got %q, want %q", i, lines[i], w)
		}
	}
}

func TestExecSelectOrderByMultiKey(t *testing.T) {
	// ORDER BY Status ASC, Priority DESC.
	// Statuses: Open, Done, Open, Open. Priorities: 3, 1, 5, NULL.
	// ASC by Status: Done first (ID 2), then three Opens.
	// Within Opens, DESC by Priority with NULLs FIRST in DESC:
	//   NULL (ID 4), 5 (ID 3), 3 (ID 1).
	// Expected ID order: 2, 4, 3, 1.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID ORDER BY Status ASC, Priority DESC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "2", "4", "3", "1"}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: got %q, want %q", i, lines[i], w)
		}
	}
}

func TestExecSelectOrderByUnknownColumn(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT * ORDER BY Nope")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Nope") || !strings.Contains(err.Error(), "ORDER BY") {
		t.Fatalf("error should name column and clause: %v", err)
	}
}

func TestExecSelectLimit(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID ORDER BY ID LIMIT 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "1", "2"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
}

func TestExecSelectOffset(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID ORDER BY ID OFFSET 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "3", "4"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
}

func TestExecSelectLimitOffset(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID ORDER BY ID LIMIT 1 OFFSET 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"ID", "2"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out.String())
	}
}

func TestExecSelectOffsetPastEnd(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID OFFSET 100")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Header only.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] != "ID" {
		t.Fatalf("expected header only, got %q", out.String())
	}
}

func TestExecSelectLimitZero(t *testing.T) {
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT ID LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] != "ID" {
		t.Fatalf("expected header only, got %q", out.String())
	}
}

func TestExecSelectDistinctOrderLimit(t *testing.T) {
	// Combine all the new clauses with DISTINCT.
	// Statuses: Open, Done, Open, Open. DISTINCT → {Open, Done}.
	// ORDER BY Status ASC → Done, Open.
	// LIMIT 1 → Done.
	e, out, _ := newExec(t)
	stmt, err := parse.Parse("SELECT DISTINCT Status ORDER BY Status ASC LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || lines[1] != "Done" {
		t.Fatalf("want header + Done, got %q", out.String())
	}
}

func TestExecSelectUnknownColumn(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("SELECT Nope")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, false)
	if err == nil {
		t.Fatal("expected error for unknown column")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error should name column: %v", err)
	}
}

func TestExecUpdateDryRun(t *testing.T) {
	e, out, path := newExec(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := parse.Parse("UPDATE SET Status = 'Done' WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "Would update 3 rows") {
		t.Errorf("expected preview header in output: %q", out.String())
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("expected dry-run hint: %q", out.String())
	}
	// File should be unchanged.
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Error("dry-run modified the file")
	}
	// In-memory rows should also be unchanged.
	if e.Table.Rows[0][2].Str != "Open" {
		t.Errorf("dry-run mutated in-memory row 0 Status: %q", e.Table.Rows[0][2].Str)
	}
}

func TestExecUpdateCommit(t *testing.T) {
	e, _, path := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Status = 'Done' WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// In-memory: all rows now have Status = "Done".
	for i, row := range e.Table.Rows {
		if row[2].Str != "Done" {
			t.Errorf("row %d: Status %q, want Done", i, row[2].Str)
		}
	}
	// On-disk: reload and verify.
	reloaded, err := LoadCSV(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range reloaded.Rows {
		if row[2].Str != "Done" {
			t.Errorf("reloaded row %d: Status %q, want Done", i, row[2].Str)
		}
	}
}

func TestExecUpdateComputed(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Priority = Priority + 10 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := e.Table.Rows[0][3].Int; got != 13 {
		t.Errorf("row 0 Priority = %d, want 13 (was 3 + 10)", got)
	}
	if got := e.Table.Rows[1][3].Int; got != 1 {
		t.Errorf("row 1 Priority = %d, want unchanged 1", got)
	}
}

func TestExecUpdateComputedAcrossRows(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Priority = Priority * 2 WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Rows 1, 3, 4 are Open. cell.Row 4 has Priority=NULL → stays NULL.
	// cell.Row 1: 3 → 6. cell.Row 3: 5 → 10. cell.Row 2 (Done): unchanged at 1.
	wants := []struct {
		idx  int
		want int64
		null bool
	}{
		{0, 6, false},
		{1, 1, false},
		{2, 10, false},
		{3, 0, true},
	}
	for _, w := range wants {
		cell := e.Table.Rows[w.idx][3]
		if cell.Null != w.null {
			t.Errorf("row %d: null=%v, want %v", w.idx, cell.Null, w.null)
		}
		if !w.null && cell.Int != w.want {
			t.Errorf("row %d: Priority=%d, want %d", w.idx, cell.Int, w.want)
		}
	}
}

func TestExecUpdateComputedNullPropagates(t *testing.T) {
	e, _, _ := newExec(t)
	// cell.Row 4 has Priority=NULL. Adding 1 keeps it NULL — does not crash, does
	// not coerce.
	stmt, err := parse.Parse("UPDATE SET Priority = Priority + 1 WHERE ID = 4")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !e.Table.Rows[3][3].Null {
		t.Errorf("row 3 (ID=4) Priority should remain NULL, got %+v", e.Table.Rows[3][3])
	}
}

func TestExecUpdateComputedPreviewShowsExpression(t *testing.T) {
	e, buf, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Priority = Priority + 1 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, false); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(buf.String(), "SET Priority = Priority + 1") {
		t.Errorf("preview should reflect source expression: %q", buf.String())
	}
}

func TestExecUpdateComputedSwapSemantics(t *testing.T) {
	// Standard SQL: every SET RHS evaluates against the pre-update row.
	// SET a = b, b = a should swap; using the new value of `a` for `b` would
	// leave both columns equal to the old `b`.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET ID = Priority, Priority = ID WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// cell.Row 0 originally: ID=1, Priority=3. After swap: ID=3, Priority=1.
	if e.Table.Rows[0][0].Int != 3 {
		t.Errorf("ID = %d, want 3 (swap)", e.Table.Rows[0][0].Int)
	}
	if e.Table.Rows[0][3].Int != 1 {
		t.Errorf("Priority = %d, want 1 (swap)", e.Table.Rows[0][3].Int)
	}
}

func TestExecUpdateUnknownColumnInExpression(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Priority = Nope + 1 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected error for unknown column in SET expression")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Errorf("error should mention column name: %v", err)
	}
}

func TestExecUpdateAggregateInSetRejected(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Priority = COUNT(*) WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("aggregates in SET should be rejected")
	}
	if !strings.Contains(err.Error(), "aggregate") {
		t.Errorf("error should mention aggregate: %v", err)
	}
}

func TestExecUpdateComputedFloatIntoIntRejected(t *testing.T) {
	// 3 / 2 = 1.5 in v2 (/ always promotes to float). Storing 1.5 into an
	// int column has no lossless representation, so coerceEvalCell should
	// reject. SET Priority = 3 / 2 WHERE ID = 1 exercises that path.
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Priority = 3 / 2 WHERE ID = 1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("float result into int column should error")
	}
}

func TestExecUpdateLiteralStillWorks(t *testing.T) {
	// Slice 2 should not regress the v1 literal-SET path.
	e, _, path := newExec(t)
	stmt, err := parse.Parse("UPDATE SET Status = 'Done' WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadCSV(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range reloaded.Rows {
		if row[2].Str != "Done" {
			t.Errorf("row %d: Status %q, want Done", i, row[2].Str)
		}
	}
}

func TestExecDeleteWithWhere(t *testing.T) {
	e, _, path := newExec(t)
	stmt, err := parse.Parse("DELETE WHERE Status = 'Done'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(e.Table.Rows) != 3 {
		t.Errorf("rows after delete: %d, want 3", len(e.Table.Rows))
	}
	for _, row := range e.Table.Rows {
		if row[2].Str == "Done" {
			t.Errorf("Done row survived delete")
		}
	}
	reloaded, _ := LoadCSV(path, LoadOptions{})
	if len(reloaded.Rows) != 3 {
		t.Errorf("on-disk rows: %d, want 3", len(reloaded.Rows))
	}
}

func TestExecBareDeleteRequiresConfirmDestructive(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("DELETE")
	if err != nil {
		t.Fatal(err)
	}
	// commit=true, no Confirm, no ConfirmDestructive → should reject.
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected error for bare DELETE")
	}
	if !strings.Contains(err.Error(), "confirm-destructive") {
		t.Fatalf("error should mention flag: %v", err)
	}
}

func TestExecBareDeleteWithConfirmDestructive(t *testing.T) {
	e, _, path := newExec(t)
	e.ConfirmDestructive = true
	stmt, err := parse.Parse("DELETE")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(e.Table.Rows) != 0 {
		t.Errorf("rows after bare delete: %d, want 0", len(e.Table.Rows))
	}
	reloaded, _ := LoadCSV(path, LoadOptions{})
	if len(reloaded.Rows) != 0 {
		t.Errorf("on-disk rows: %d, want 0", len(reloaded.Rows))
	}
}

func TestExecInsert(t *testing.T) {
	e, _, path := newExec(t)
	stmt, err := parse.Parse("INSERT (ID, Title, Status, Priority, Archived, Modified) VALUES (99, 'New cell.Row', 'Open', 4, FALSE, '2024-05-14')")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(e.Table.Rows) != 5 {
		t.Fatalf("rows after insert: %d, want 5", len(e.Table.Rows))
	}
	last := e.Table.Rows[4]
	if last[0].Int != 99 || last[1].Str != "New cell.Row" || last[2].Str != "Open" {
		t.Errorf("inserted row wrong: %+v", last)
	}
	reloaded, _ := LoadCSV(path, LoadOptions{})
	if len(reloaded.Rows) != 5 {
		t.Errorf("on-disk rows: %d, want 5", len(reloaded.Rows))
	}
}

func TestExecInsertPartialColumnsFillsNull(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("INSERT (ID, Title) VALUES (99, 'Partial')")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	last := e.Table.Rows[4]
	if last[0].Int != 99 || last[1].Str != "Partial" {
		t.Errorf("ID/Title wrong: %+v", last)
	}
	for i := 2; i < len(last); i++ {
		if !last[i].Null {
			t.Errorf("col %d should be NULL (unspecified), got %+v", i, last[i])
		}
	}
}

func TestExecInsertColumnValueMismatch(t *testing.T) {
	e, _, _ := newExec(t)
	stmt, err := parse.Parse("INSERT (ID, Title) VALUES (99)")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected error for column/value count mismatch")
	}
}

func TestExecCoercionFailureReported(t *testing.T) {
	e, _, _ := newExec(t)
	// Priority is int; 'abc' as a string cannot coerce.
	stmt, err := parse.Parse("UPDATE SET Priority = 'abc'")
	if err != nil {
		t.Fatal(err)
	}
	err = e.Execute(stmt, true)
	if err == nil {
		t.Fatal("expected coercion error")
	}
	if !strings.Contains(err.Error(), "Priority") {
		t.Fatalf("error should name column: %v", err)
	}
}

func TestExecOutputPath(t *testing.T) {
	e, _, originalPath := newExec(t)
	dst := filepath.Join(t.TempDir(), "out.csv")
	e.OutputPath = dst

	originalBytes, _ := os.ReadFile(originalPath)

	stmt, err := parse.Parse("UPDATE SET Priority = 9 WHERE Status = 'Open'")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(stmt, true); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	afterOriginal, _ := os.ReadFile(originalPath)
	if !bytes.Equal(originalBytes, afterOriginal) {
		t.Error("OutputPath set but original file was modified")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	reloaded, _ := LoadCSV(dst, LoadOptions{})
	if reloaded.Rows[0][3].Int != 9 {
		t.Errorf("output row 0 Priority: %d, want 9", reloaded.Rows[0][3].Int)
	}
}

func TestExecDecideCommit(t *testing.T) {
	// commit=true bypasses everything.
	e := &Executor{}
	ok, msg := e.decideCommit(true)
	if !ok || msg != "" {
		t.Errorf("commit=true: got (%v, %q), want (true, \"\")", ok, msg)
	}

	// Exec mode (no Confirm): never commits, prints dry-run hint.
	e = &Executor{}
	ok, msg = e.decideCommit(false)
	if ok || !strings.Contains(msg, "dry run") {
		t.Errorf("exec dry-run: got (%v, %q)", ok, msg)
	}

	// REPL mode with user saying y → proceed.
	e = &Executor{Confirm: func() bool { return true }}
	ok, msg = e.decideCommit(false)
	if !ok || msg != "" {
		t.Errorf("repl yes: got (%v, %q)", ok, msg)
	}

	// REPL mode with user saying n → aborted.
	e = &Executor{Confirm: func() bool { return false }}
	ok, msg = e.decideCommit(false)
	if ok || msg != "(aborted)" {
		t.Errorf("repl no: got (%v, %q)", ok, msg)
	}
}

func TestRemoveIndices(t *testing.T) {
	rows := []cell.Row{
		{cell.Cell{Int: 1}}, {cell.Cell{Int: 2}}, {cell.Cell{Int: 3}}, {cell.Cell{Int: 4}}, {cell.Cell{Int: 5}},
	}
	got := removeIndices(rows, []int{0, 2, 4})
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0][0].Int != 2 || got[1][0].Int != 4 {
		t.Errorf("got %+v, want [2, 4]", got)
	}
}
