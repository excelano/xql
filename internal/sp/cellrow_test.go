package sp

import (
	"strings"
	"testing"
	"time"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/eval"
	"github.com/excelano/xql/internal/parse"
)

func TestFieldTypeToCellType(t *testing.T) {
	cases := []struct {
		in   FieldType
		want cell.ColumnType
	}{
		{FieldText, cell.TypeString},
		{FieldNote, cell.TypeString},
		{FieldChoice, cell.TypeString},
		{FieldNumber, cell.TypeFloat},
		{FieldBoolean, cell.TypeBool},
		{FieldDateTime, cell.TypeDate},
		{FieldPerson, cell.TypeString},
		{FieldLookup, cell.TypeString},
		{FieldHyperlink, cell.TypeString},
		{FieldCalculated, cell.TypeString},
		{FieldUnknown, cell.TypeString},
	}
	for _, tc := range cases {
		if got := FieldTypeToCellType(tc.in); got != tc.want {
			t.Errorf("FieldTypeToCellType(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestBuildCellSchema(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Title", "Priority", "Active", "Due"},
		Schema: map[string]FieldInfo{
			"Title":    {Name: "Title", Type: FieldText},
			"Priority": {Name: "Priority", Type: FieldNumber},
			"Active":   {Name: "Active", Type: FieldBoolean},
			"Due":      {Name: "Due", Type: FieldDateTime},
		},
	}
	cols, info := BuildCellSchema(bound)
	if len(cols) != 4 || cols[0] != "Title" || cols[3] != "Due" {
		t.Fatalf("columns out of order: %v", cols)
	}
	if info["Priority"].Type != cell.TypeFloat {
		t.Errorf("Priority type = %s, want float", info["Priority"].Type)
	}
	if info["Due"].Type != cell.TypeDate {
		t.Errorf("Due type = %s, want date", info["Due"].Type)
	}
}

func TestFieldsToRowMissingAndNull(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Title", "Priority"},
		Schema: map[string]FieldInfo{
			"Title":    {Name: "Title", Type: FieldText},
			"Priority": {Name: "Priority", Type: FieldNumber},
		},
	}
	cols, info := BuildCellSchema(bound)

	// Missing key on Title, explicit nil on Priority.
	row, err := FieldsToRow(map[string]any{"Priority": nil}, cols, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !row[0].Null {
		t.Errorf("missing key should produce Null cell, got %+v", row[0])
	}
	if !row[1].Null {
		t.Errorf("explicit nil should produce Null cell, got %+v", row[1])
	}
}

func TestFieldsToRowTypeConversions(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Title", "Priority", "Active", "Due"},
		Schema: map[string]FieldInfo{
			"Title":    {Name: "Title", Type: FieldText},
			"Priority": {Name: "Priority", Type: FieldNumber},
			"Active":   {Name: "Active", Type: FieldBoolean},
			"Due":      {Name: "Due", Type: FieldDateTime},
		},
	}
	cols, info := BuildCellSchema(bound)

	row, err := FieldsToRow(map[string]any{
		"Title":    "Fix login",
		"Priority": float64(3),
		"Active":   true,
		"Due":      "2024-01-15T12:00:00Z",
	}, cols, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if row[0].Str != "Fix login" {
		t.Errorf("Title = %q, want %q", row[0].Str, "Fix login")
	}
	if row[1].Float != 3.0 {
		t.Errorf("Priority = %v, want 3.0", row[1].Float)
	}
	if !row[2].Bool {
		t.Errorf("Active = %v, want true", row[2].Bool)
	}
	if !row[3].Date.Equal(time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Due = %v, want 2024-01-15T12:00:00Z", row[3].Date)
	}
}

func TestFieldsToRowDateOnly(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Due"},
		Schema:  map[string]FieldInfo{"Due": {Name: "Due", Type: FieldDateTime}},
	}
	cols, info := BuildCellSchema(bound)
	row, err := FieldsToRow(map[string]any{"Due": "2024-01-15"}, cols, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !row[0].Date.Equal(time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Due = %v, want 2024-01-15 midnight UTC", row[0].Date)
	}
}

func TestFieldsToRowEmptyDateIsNull(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Due"},
		Schema:  map[string]FieldInfo{"Due": {Name: "Due", Type: FieldDateTime}},
	}
	cols, info := BuildCellSchema(bound)
	row, err := FieldsToRow(map[string]any{"Due": ""}, cols, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !row[0].Null {
		t.Errorf("empty date string should be Null, got %+v", row[0])
	}
}

func TestFieldsToRowTypeMismatch(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Priority"},
		Schema:  map[string]FieldInfo{"Priority": {Name: "Priority", Type: FieldNumber}},
	}
	cols, info := BuildCellSchema(bound)
	_, err := FieldsToRow(map[string]any{"Priority": "not-a-number"}, cols, info)
	if err == nil {
		t.Fatal("expected error for string in numeric column")
	}
	if !strings.Contains(err.Error(), "Priority") {
		t.Errorf("error %q should mention column name", err.Error())
	}
}

func TestFieldsToRowObjectFieldStringified(t *testing.T) {
	bound := &BoundList{
		Columns: []string{"Owner"},
		Schema:  map[string]FieldInfo{"Owner": {Name: "Owner", Type: FieldPerson}},
	}
	cols, info := BuildCellSchema(bound)
	row, err := FieldsToRow(map[string]any{
		"Owner": map[string]any{"displayName": "David", "email": "d@x"},
	}, cols, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(row[0].Str, "David") {
		t.Errorf("person object should stringify to JSON containing the display name, got %q", row[0].Str)
	}
}

func TestBuildCellTableRoundTrip(t *testing.T) {
	bound := &BoundList{
		DisplayName: "Tasks",
		Columns:     []string{"Title", "Priority"},
		Schema: map[string]FieldInfo{
			"Title":    {Name: "Title", Type: FieldText},
			"Priority": {Name: "Priority", Type: FieldNumber},
		},
	}
	items := []listItem{
		{ID: "1", Fields: map[string]any{"Title": "Alpha", "Priority": float64(1)}},
		{ID: "2", Fields: map[string]any{"Title": "Beta", "Priority": float64(3)}},
		{ID: "3", Fields: map[string]any{"Title": "Gamma", "Priority": float64(5)}},
	}
	tbl, err := BuildCellTable(bound, items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tbl.Path != "Tasks" {
		t.Errorf("Path = %q, want %q", tbl.Path, "Tasks")
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(tbl.Rows))
	}

	// Row index aligns with items index — caller can correlate back to Graph IDs.
	if tbl.Rows[1][0].Str != "Beta" || tbl.Rows[1][1].Float != 3.0 {
		t.Errorf("row 1 = %+v, want Beta/3.0", tbl.Rows[1])
	}
}

// TestEvalAgainstAdaptedTable confirms the shared evaluator works against an
// SP-derived cell.Table without any backend-specific glue. This is the whole
// point of the adapter: Pass 3 features can use eval.Matches and eval.EvalExpr
// against SP rows the same way the CSV executor does.
func TestEvalAgainstAdaptedTable(t *testing.T) {
	bound := &BoundList{
		DisplayName: "Tasks",
		Columns:     []string{"Title", "Priority"},
		Schema: map[string]FieldInfo{
			"Title":    {Name: "Title", Type: FieldText},
			"Priority": {Name: "Priority", Type: FieldNumber},
		},
	}
	items := []listItem{
		{ID: "1", Fields: map[string]any{"Title": "Alpha", "Priority": float64(1)}},
		{ID: "2", Fields: map[string]any{"Title": "Beta", "Priority": float64(3)}},
		{ID: "3", Fields: map[string]any{"Title": "Gamma", "Priority": float64(5)}},
	}
	tbl, err := BuildCellTable(bound, items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := eval.NewEvalContext(tbl)

	// WHERE Priority > 2
	pred := cmp("Priority", ">", vnum("2"))
	matched := 0
	for _, row := range tbl.Rows {
		ok, err := eval.Matches(pred, row, ctx)
		if err != nil {
			t.Fatalf("eval error: %v", err)
		}
		if ok {
			matched++
		}
	}
	if matched != 2 {
		t.Errorf("WHERE Priority > 2: matched %d, want 2", matched)
	}

	// Arithmetic: Priority * 2 on the first row (1 * 2 = 2)
	expr := &parse.BinaryExpr{
		Op: "*",
		L:  &parse.ColumnExpr{Name: "Priority"},
		R:  &parse.LiteralExpr{Value: vnum("2")},
	}
	got, err := eval.EvalExpr(expr, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("EvalExpr error: %v", err)
	}
	if got.Cell.Float != 2.0 {
		t.Errorf("Priority * 2 on row 0 = %v, want 2.0", got.Cell.Float)
	}
}
