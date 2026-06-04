package sp

import (
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/eval"
	"github.com/excelano/xql/internal/parse"
)

// execTestSchema covers every FieldType (writable + non-writable) and a
// read-only DateTime for the read-only path.
func execTestSchema() map[string]FieldInfo {
	return map[string]FieldInfo{
		"Title":    {Name: "Title", Type: FieldText},
		"Body":     {Name: "Body", Type: FieldNote},
		"Count":    {Name: "Count", Type: FieldNumber},
		"Active":   {Name: "Active", Type: FieldBoolean},
		"Due":      {Name: "Due", Type: FieldDateTime},
		"Status":   {Name: "Status", Type: FieldChoice},
		"Owner":    {Name: "Owner", Type: FieldPerson},
		"Parent":   {Name: "Parent", Type: FieldLookup},
		"Link":     {Name: "Link", Type: FieldHyperlink},
		"Computed": {Name: "Computed", Type: FieldCalculated},
		"Modified": {Name: "Modified", Type: FieldDateTime, ReadOnly: true},
	}
}

func TestValidateAssignment(t *testing.T) {
	schema := execTestSchema()
	tests := []struct {
		name    string
		col     string
		val     parse.Value
		wantErr string // substring; empty = expect success
	}{
		// happy paths
		{"text accepts string", "Title", vstr("hello"), ""},
		{"note accepts string", "Body", vstr("multi\nline"), ""},
		{"number accepts int", "Count", vnum("42"), ""},
		{"number accepts negative", "Count", vnum("-7"), ""},
		{"number accepts decimal", "Count", vnum("1.5"), ""},
		{"boolean accepts true", "Active", vbool(true), ""},
		{"boolean accepts false", "Active", vbool(false), ""},
		{"datetime accepts date-only", "Due", vstr("2024-01-15"), ""},
		{"datetime accepts full ISO", "Due", vstr("2024-01-15T12:00:00Z"), ""},
		{"choice accepts string", "Status", vstr("Open"), ""},
		{"null universal on text", "Title", vnull(), ""},
		{"null universal on number", "Count", vnull(), ""},
		{"null universal on datetime", "Due", vnull(), ""},

		// type mismatches
		{"text rejects number", "Title", vnum("1"), "expected string"},
		{"text rejects bool", "Title", vbool(true), "expected string"},
		{"number rejects string", "Count", vstr("1"), "expected number"},
		{"boolean rejects string", "Active", vstr("true"), "expected true or false"},
		{"datetime rejects number", "Due", vnum("1"), "expected ISO 8601"},
		{"datetime rejects unparseable string", "Due", vstr("yesterday"), "invalid datetime"},
		{"choice rejects bool", "Status", vbool(true), "expected string"},

		// unsupported column types
		{"person column rejected", "Owner", vstr("u@x"), "not supported in v1"},
		{"lookup column rejected", "Parent", vnum("1"), "not supported in v1"},
		{"hyperlink column rejected", "Link", vstr("https://x"), "not supported in v1"},
		{"calculated column rejected", "Computed", vstr("x"), "not supported in v1"},

		// unknown / read-only
		{"unknown column", "DoesNotExist", vstr("x"), `unknown column "DoesNotExist"`},
		{"read-only column", "Modified", vstr("2024-01-01"), "read-only"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateAssignment(tt.col, tt.val, schema)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValueToFieldJSON(t *testing.T) {
	tests := []struct {
		name string
		val  parse.Value
		typ  FieldType
		want any
	}{
		{"text passthrough", vstr("hi"), FieldText, "hi"},
		{"note preserves newlines", vstr("a\nb"), FieldNote, "a\nb"},
		{"choice passthrough", vstr("Open"), FieldChoice, "Open"},
		{"number int", vnum("42"), FieldNumber, int64(42)},
		{"number negative int", vnum("-7"), FieldNumber, int64(-7)},
		{"number decimal", vnum("1.5"), FieldNumber, 1.5},
		{"boolean true", vbool(true), FieldBoolean, true},
		{"boolean false", vbool(false), FieldBoolean, false},
		{"datetime full ISO preserved", vstr("2024-01-15T12:30:00Z"), FieldDateTime, "2024-01-15T12:30:00Z"},
		{"datetime date-only normalized", vstr("2024-01-01"), FieldDateTime, "2024-01-01T00:00:00Z"},
		{"null becomes nil on text", vnull(), FieldText, nil},
		{"null becomes nil on number", vnull(), FieldNumber, nil},
		{"null becomes nil on datetime", vnull(), FieldDateTime, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := valueToFieldJSON(tt.val, tt.typ)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %#v (%T), want %#v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestIsWritableType(t *testing.T) {
	writable := []FieldType{FieldText, FieldNote, FieldNumber, FieldBoolean, FieldDateTime, FieldChoice}
	notWritable := []FieldType{FieldPerson, FieldLookup, FieldHyperlink, FieldCalculated, FieldUnknown}

	for _, ft := range writable {
		if !isWritableType(ft) {
			t.Errorf("expected %s to be writable", ft)
		}
	}
	for _, ft := range notWritable {
		if isWritableType(ft) {
			t.Errorf("expected %s to NOT be writable", ft)
		}
	}
}

// TestBuildRowBodyLiterals covers the literal-only path through the
// executor's per-assignment validation and JSON shaping, including order
// independence (map output) and mixed-type batches.
func TestBuildRowBodyLiterals(t *testing.T) {
	e := &Executor{Bound: &BoundList{Schema: execTestSchema()}}
	assigns := []parse.Assignment{
		{Column: "Title", Value: litE(vstr("hello"))},
		{Column: "Count", Value: litE(vnum("3"))},
		{Column: "Active", Value: litE(vbool(true))},
		{Column: "Due", Value: litE(vstr("2024-01-01"))},
		{Column: "Status", Value: litE(vnull())},
	}
	if err := e.validateAssignments(assigns); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	body, err := e.buildRowBody(assigns, nil, nil)
	if err != nil {
		t.Fatalf("unexpected body error: %v", err)
	}
	want := map[string]any{
		"Title":  "hello",
		"Count":  int64(3),
		"Active": true,
		"Due":    "2024-01-01T00:00:00Z",
		"Status": nil,
	}
	if len(body) != len(want) {
		t.Fatalf("got %d fields, want %d (%v)", len(body), len(want), body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %#v, want %#v", k, body[k], v)
		}
	}
}

func TestValidateAssignmentsRejectsLiteralOnUnsupportedType(t *testing.T) {
	e := &Executor{Bound: &BoundList{Schema: execTestSchema()}}
	err := e.validateAssignments([]parse.Assignment{
		{Column: "Title", Value: litE(vstr("ok"))},
		{Column: "Owner", Value: litE(vstr("u@x"))},
	})
	if err == nil {
		t.Fatalf("expected error for Person column, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error %q does not mention support", err.Error())
	}
}

func TestCompareFieldValue(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		t    FieldType
		want int // -1, 0, +1; sign-checked
	}{
		{"number asc", 1.0, 2.0, FieldNumber, -1},
		{"number eq", 3.0, 3.0, FieldNumber, 0},
		{"number gt", 5.0, 2.0, FieldNumber, +1},
		{"bool false-true", false, true, FieldBoolean, -1},
		{"bool same", true, true, FieldBoolean, 0},
		{"text", "alpha", "beta", FieldText, -1},
		{"datetime iso lex compare", "2024-01-15", "2024-03-01", FieldDateTime, -1},
		{"both nil eq", nil, nil, FieldText, 0},
		{"nil sorts high vs string", nil, "z", FieldText, +1},
		{"string vs nil", "z", nil, FieldText, -1},
		{"nil sorts high vs number", nil, 1.0, FieldNumber, +1},
		{"number stored as string", "10", "2", FieldNumber, +1}, // numeric compare via toFloat
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compareFieldValue(tc.a, tc.b, tc.t)
			if (got < 0) != (tc.want < 0) || (got > 0) != (tc.want > 0) || (got == 0) != (tc.want == 0) {
				t.Errorf("compareFieldValue(%v, %v, %s) = %d, want sign %d", tc.a, tc.b, tc.t, got, tc.want)
			}
		})
	}
}

func TestApplyOffsetLimit(t *testing.T) {
	row := func(n int) map[string]any { return map[string]any{"n": float64(n)} }
	in := []map[string]any{row(1), row(2), row(3), row(4), row(5)}
	one := 1
	two := 2
	five := 5
	hundred := 100
	zero := 0
	cases := []struct {
		name               string
		offset, limit      *int
		wantLen            int
		wantFirst, wantLast int
	}{
		{"no clauses", nil, nil, 5, 1, 5},
		{"limit only", nil, &two, 2, 1, 2},
		{"offset only", &two, nil, 3, 3, 5},
		{"both", &one, &two, 2, 2, 3},
		{"limit zero", nil, &zero, 0, 0, 0},
		{"offset past end", &hundred, nil, 0, 0, 0},
		{"limit larger than input", nil, &five, 5, 1, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := append([]map[string]any(nil), in...)
			got := applyOffsetLimit(input, tc.offset, tc.limit)
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
			if tc.wantLen == 0 {
				return
			}
			if int(got[0]["n"].(float64)) != tc.wantFirst {
				t.Errorf("first = %v, want %d", got[0]["n"], tc.wantFirst)
			}
			if int(got[len(got)-1]["n"].(float64)) != tc.wantLast {
				t.Errorf("last = %v, want %d", got[len(got)-1]["n"], tc.wantLast)
			}
		})
	}
}

func TestSortRowsByKeys(t *testing.T) {
	schema := map[string]FieldInfo{
		"Status":   {Name: "Status", Type: FieldText},
		"Priority": {Name: "Priority", Type: FieldNumber},
	}
	rows := []map[string]any{
		{"Status": "Open", "Priority": 3.0},
		{"Status": "Done", "Priority": 1.0},
		{"Status": "Open", "Priority": 5.0},
		{"Status": "Open", "Priority": nil},
	}
	sortRowsByKeys(rows, []parse.OrderKey{{Column: "Status"}, {Column: "Priority", Desc: true}}, schema)
	// ASC Status: Done first. Within Open, DESC Priority with NULLs FIRST in
	// DESC: NULL, 5, 3.
	wantStatus := []string{"Done", "Open", "Open", "Open"}
	for i, w := range wantStatus {
		if rows[i]["Status"] != w {
			t.Errorf("row %d status = %v, want %s", i, rows[i]["Status"], w)
		}
	}
	if rows[1]["Priority"] != nil {
		t.Errorf("expected NULL Priority first under Open, got %v", rows[1]["Priority"])
	}
	if rows[2]["Priority"].(float64) != 5.0 {
		t.Errorf("expected Priority 5 second, got %v", rows[2]["Priority"])
	}
	if rows[3]["Priority"].(float64) != 3.0 {
		t.Errorf("expected Priority 3 third, got %v", rows[3]["Priority"])
	}
}

func TestDistinctKey(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]any
		cols []string
		same bool
	}{
		{
			name: "identical strings collapse",
			a:    map[string]any{"Status": "Open"},
			b:    map[string]any{"Status": "Open"},
			cols: []string{"Status"},
			same: true,
		},
		{
			name: "different strings separate",
			a:    map[string]any{"Status": "Open"},
			b:    map[string]any{"Status": "Done"},
			cols: []string{"Status"},
			same: false,
		},
		{
			name: "missing field equals explicit nil (NULL = NULL under DISTINCT)",
			a:    map[string]any{},
			b:    map[string]any{"Status": nil},
			cols: []string{"Status"},
			same: true,
		},
		{
			name: "NULL distinct from non-NULL",
			a:    map[string]any{"Status": nil},
			b:    map[string]any{"Status": "Open"},
			cols: []string{"Status"},
			same: false,
		},
		{
			name: "int 1 distinct from string \"1\" (type-aware)",
			a:    map[string]any{"X": float64(1)},
			b:    map[string]any{"X": "1"},
			cols: []string{"X"},
			same: false,
		},
		{
			name: "length-prefix prevents string-boundary collision",
			a:    map[string]any{"A": "ab", "B": "cd"},
			b:    map[string]any{"A": "abc", "B": "d"},
			cols: []string{"A", "B"},
			same: false,
		},
		{
			name: "multi-column collapse",
			a:    map[string]any{"A": "x", "B": float64(1)},
			b:    map[string]any{"A": "x", "B": float64(1)},
			cols: []string{"A", "B"},
			same: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ka := distinctKey(tc.a, tc.cols)
			kb := distinctKey(tc.b, tc.cols)
			if tc.same && ka != kb {
				t.Errorf("expected same key:\n  a=%q\n  b=%q", ka, kb)
			}
			if !tc.same && ka == kb {
				t.Errorf("expected different keys, both = %q", ka)
			}
		})
	}
}

// computedUpdateBound returns a bound list shaped for the computed-RHS
// tests: Title is text, Counter is number, Active is bool, Due is dateTime,
// Modified is read-only.
func computedUpdateBound() *BoundList {
	return &BoundList{
		Columns: []string{"Title", "Counter", "Active", "Due", "Modified"},
		Schema: map[string]FieldInfo{
			"Title":    {Name: "Title", Type: FieldText},
			"Counter":  {Name: "Counter", Type: FieldNumber},
			"Active":   {Name: "Active", Type: FieldBoolean},
			"Due":      {Name: "Due", Type: FieldDateTime},
			"Modified": {Name: "Modified", Type: FieldDateTime, ReadOnly: true},
		},
	}
}

func TestValidateAssignmentsComputed(t *testing.T) {
	e := &Executor{Bound: computedUpdateBound()}

	cases := []struct {
		name    string
		assigns []parse.Assignment
		wantErr string // substring; empty = expect success
	}{
		{
			name: "counter + 1 on Number column",
			assigns: []parse.Assignment{
				{Column: "Counter", Value: &parse.BinaryExpr{
					Op: "+",
					L:  &parse.ColumnExpr{Name: "Counter"},
					R:  &parse.LiteralExpr{Value: vnum("1")},
				}},
			},
			wantErr: "",
		},
		{
			name: "counter * 2 on Number column",
			assigns: []parse.Assignment{
				{Column: "Counter", Value: &parse.BinaryExpr{
					Op: "*",
					L:  &parse.ColumnExpr{Name: "Counter"},
					R:  &parse.LiteralExpr{Value: vnum("2")},
				}},
			},
			wantErr: "",
		},
		{
			name: "computed RHS targeting read-only column",
			assigns: []parse.Assignment{
				{Column: "Modified", Value: &parse.BinaryExpr{
					Op: "+",
					L:  &parse.ColumnExpr{Name: "Counter"},
					R:  &parse.LiteralExpr{Value: vnum("1")},
				}},
			},
			wantErr: "read-only",
		},
		{
			name: "computed RHS referencing unknown column",
			assigns: []parse.Assignment{
				{Column: "Counter", Value: &parse.BinaryExpr{
					Op: "+",
					L:  &parse.ColumnExpr{Name: "Nope"},
					R:  &parse.LiteralExpr{Value: vnum("1")},
				}},
			},
			wantErr: `unknown column "Nope"`,
		},
		{
			name: "computed numeric RHS targeting Boolean column",
			assigns: []parse.Assignment{
				{Column: "Active", Value: &parse.BinaryExpr{
					Op: "+",
					L:  &parse.ColumnExpr{Name: "Counter"},
					R:  &parse.LiteralExpr{Value: vnum("1")},
				}},
			},
			wantErr: "target SharePoint column is boolean",
		},
		{
			name: "aggregate in UPDATE SET rejected with slice pointer",
			assigns: []parse.Assignment{
				{Column: "Counter", Value: &parse.AggregateExpr{
					Func: "SUM",
					Arg:  &parse.ColumnExpr{Name: "Counter"},
				}},
			},
			wantErr: "aggregate functions in UPDATE SET",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := e.validateAssignments(tc.assigns)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestBuildRowBodyComputed drives the per-row evaluation path: assignments
// with non-literal RHS evaluate against the supplied row and produce the
// correct integer-vs-float JSON coercion for Number columns.
func TestBuildRowBodyComputed(t *testing.T) {
	bound := computedUpdateBound()
	e := &Executor{Bound: bound}

	items := []listItem{
		{ID: "1", Fields: map[string]any{"Counter": float64(3)}},
	}
	tbl, err := BuildCellTable(bound, items)
	if err != nil {
		t.Fatalf("build table: %v", err)
	}
	ctx := eval.NewEvalContext(tbl)

	assigns := []parse.Assignment{
		{Column: "Counter", Value: &parse.BinaryExpr{
			Op: "+",
			L:  &parse.ColumnExpr{Name: "Counter"},
			R:  &parse.LiteralExpr{Value: vnum("1")},
		}},
	}
	if err := e.validateAssignments(assigns); err != nil {
		t.Fatalf("validate: %v", err)
	}
	body, err := e.buildRowBody(assigns, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("buildRowBody: %v", err)
	}

	// 3 + 1 = 4: integer-valued float must coerce to int64 so Graph stores
	// without a decimal point.
	got, ok := body["Counter"].(int64)
	if !ok {
		t.Fatalf("Counter = %#v (%T), want int64", body["Counter"], body["Counter"])
	}
	if got != 4 {
		t.Errorf("Counter = %d, want 4", got)
	}
}

// TestBuildRowBodyComputedNullProp confirms NULL propagation: if a row's
// referenced column is NULL, the arithmetic result is NULL, which writes
// JSON null to the Graph PATCH body.
func TestBuildRowBodyComputedNullProp(t *testing.T) {
	bound := computedUpdateBound()
	e := &Executor{Bound: bound}

	items := []listItem{
		{ID: "1", Fields: map[string]any{}}, // Counter absent → NULL cell
	}
	tbl, err := BuildCellTable(bound, items)
	if err != nil {
		t.Fatalf("build table: %v", err)
	}
	ctx := eval.NewEvalContext(tbl)

	assigns := []parse.Assignment{
		{Column: "Counter", Value: &parse.BinaryExpr{
			Op: "+",
			L:  &parse.ColumnExpr{Name: "Counter"},
			R:  &parse.LiteralExpr{Value: vnum("1")},
		}},
	}
	body, err := e.buildRowBody(assigns, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["Counter"] != nil {
		t.Errorf("Counter = %#v, want nil (NULL propagated)", body["Counter"])
	}
}

func TestTypesCompatibleForUpdate(t *testing.T) {
	cases := []struct {
		src    cell.ColumnType
		target FieldType
		want   bool
	}{
		// Text family accepts everything (we stringify).
		{cell.TypeString, FieldText, true},
		{cell.TypeInt, FieldText, true},
		{cell.TypeFloat, FieldNote, true},
		{cell.TypeBool, FieldChoice, true},
		// Number requires numeric.
		{cell.TypeInt, FieldNumber, true},
		{cell.TypeFloat, FieldNumber, true},
		{cell.TypeString, FieldNumber, false},
		{cell.TypeBool, FieldNumber, false},
		// Boolean is strict.
		{cell.TypeBool, FieldBoolean, true},
		{cell.TypeInt, FieldBoolean, false},
		{cell.TypeString, FieldBoolean, false},
		// DateTime accepts string (parsed) or date.
		{cell.TypeDate, FieldDateTime, true},
		{cell.TypeString, FieldDateTime, true},
		{cell.TypeInt, FieldDateTime, false},
	}
	for _, tc := range cases {
		got := typesCompatibleForUpdate(tc.src, tc.target)
		if got != tc.want {
			t.Errorf("typesCompatibleForUpdate(%s, %s) = %v, want %v", tc.src, tc.target, got, tc.want)
		}
	}
}

func TestEvalCellToFieldJSON(t *testing.T) {
	cases := []struct {
		name   string
		ec     eval.EvalCell
		target FieldType
		want   any
	}{
		{
			name:   "int to Number stays int64",
			ec:     eval.EvalCell{Cell: cell.Cell{Int: 7}, Type: cell.TypeInt},
			target: FieldNumber,
			want:   int64(7),
		},
		{
			name:   "integer-valued float to Number coerces to int64",
			ec:     eval.EvalCell{Cell: cell.Cell{Float: 4.0}, Type: cell.TypeFloat},
			target: FieldNumber,
			want:   int64(4),
		},
		{
			name:   "non-integer float to Number stays float",
			ec:     eval.EvalCell{Cell: cell.Cell{Float: 1.5}, Type: cell.TypeFloat},
			target: FieldNumber,
			want:   1.5,
		},
		{
			name:   "string to Text passes through",
			ec:     eval.EvalCell{Cell: cell.Cell{Str: "hello"}, Type: cell.TypeString},
			target: FieldText,
			want:   "hello",
		},
		{
			name:   "int to Text stringifies",
			ec:     eval.EvalCell{Cell: cell.Cell{Int: 42}, Type: cell.TypeInt},
			target: FieldText,
			want:   "42",
		},
		{
			name:   "bool to Boolean passes through",
			ec:     eval.EvalCell{Cell: cell.Cell{Bool: true}, Type: cell.TypeBool},
			target: FieldBoolean,
			want:   true,
		},
		{
			name:   "null cell produces nil regardless of target",
			ec:     eval.EvalCell{Cell: cell.Cell{Null: true}, Type: cell.TypeString},
			target: FieldText,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evalCellToFieldJSON(tc.ec, tc.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %#v (%T), want %#v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}
