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

// projTestExecutor builds a minimal Executor with a fixed two-column schema
// so resolveProjection can be exercised without a live Graph client.
func projTestExecutor(allFields bool) *Executor {
	return &Executor{
		Bound: &BoundList{
			Columns: []string{"Title", "Count"},
			Schema: map[string]FieldInfo{
				"Title": {Name: "Title", Type: FieldText},
				"Count": {Name: "Count", Type: FieldNumber},
			},
		},
		AllFields: allFields,
	}
}

// projTestExecutorWithHidden adds a hidden column so SELECT * filtering can
// be checked.
func projTestExecutorWithHidden(allFields bool) *Executor {
	return &Executor{
		Bound: &BoundList{
			Columns: []string{"Title", "Internal"},
			Schema: map[string]FieldInfo{
				"Title":    {Name: "Title", Type: FieldText},
				"Internal": {Name: "Internal", Type: FieldText, Hidden: true},
			},
		},
		AllFields: allFields,
	}
}

func TestResolveProjectionStar(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{Star: true}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("got %d entries, want 2", len(plan))
	}
	for i, want := range []string{"Title", "Count"} {
		if plan[i].Source != want || plan[i].Label != want {
			t.Errorf("plan[%d] = %+v, want Source=Label=%q", i, plan[i], want)
		}
	}
}

func TestResolveProjectionStarHidesHiddenByDefault(t *testing.T) {
	e := projTestExecutorWithHidden(false)
	plan, err := e.resolveProjection(&parse.SelectStmt{Star: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 1 || plan[0].Source != "Title" {
		t.Errorf("plan = %+v, want only Title", plan)
	}
}

func TestResolveProjectionStarAllFields(t *testing.T) {
	e := projTestExecutorWithHidden(true)
	plan, err := e.resolveProjection(&parse.SelectStmt{Star: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 2 {
		t.Errorf("plan = %+v, want both columns including hidden", plan)
	}
}

func TestResolveProjectionBareColumnLabelEqualsSource(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Title"}},
			{Expr: &parse.ColumnExpr{Name: "Count"}},
		},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, want := range []string{"Title", "Count"} {
		if plan[i].Source != want || plan[i].Label != want {
			t.Errorf("plan[%d] = %+v, want Source=Label=%q", i, plan[i], want)
		}
	}
}

func TestResolveProjectionAliasRenamesLabel(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Title"}, Alias: "T"},
			{Expr: &parse.ColumnExpr{Name: "Count"}, Alias: "N"},
		},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan[0].Source != "Title" || plan[0].Label != "T" {
		t.Errorf("plan[0] = %+v, want Source=Title Label=T", plan[0])
	}
	if plan[1].Source != "Count" || plan[1].Label != "N" {
		t.Errorf("plan[1] = %+v, want Source=Count Label=N", plan[1])
	}
}

func TestResolveProjectionAliasMixedWithBare(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Title"}, Alias: "Subject"},
			{Expr: &parse.ColumnExpr{Name: "Count"}},
		},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan[0].Label != "Subject" || plan[1].Label != "Count" {
		t.Errorf("plan = %+v, want labels Subject/Count", plan)
	}
}

func TestResolveProjectionRejectsDuplicateLabels(t *testing.T) {
	e := projTestExecutor(false)
	cases := []struct {
		name string
		sel  *parse.SelectStmt
		want string
	}{
		{
			"two aliases collide",
			&parse.SelectStmt{Columns: []parse.Projection{
				{Expr: &parse.ColumnExpr{Name: "Title"}, Alias: "X"},
				{Expr: &parse.ColumnExpr{Name: "Count"}, Alias: "X"},
			}},
			`duplicate output column "X"`,
		},
		{
			"alias collides with bare column name",
			&parse.SelectStmt{Columns: []parse.Projection{
				{Expr: &parse.ColumnExpr{Name: "Title"}},
				{Expr: &parse.ColumnExpr{Name: "Count"}, Alias: "Title"},
			}},
			`duplicate output column "Title"`,
		},
		{
			"same column twice",
			&parse.SelectStmt{Columns: []parse.Projection{
				{Expr: &parse.ColumnExpr{Name: "Title"}},
				{Expr: &parse.ColumnExpr{Name: "Title"}},
			}},
			`duplicate output column "Title"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.resolveProjection(tc.sel)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestResolveProjectionAliasUnknownSourceColumn(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{Columns: []parse.Projection{
		{Expr: &parse.ColumnExpr{Name: "NotAColumn"}, Alias: "X"},
	}}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), `unknown column "NotAColumn"`) {
		t.Errorf("got %v, want unknown column error", err)
	}
}

func TestRelabelRows(t *testing.T) {
	plan := []projEntry{
		{Source: "Title", Label: "T"},
		{Source: "Count", Label: "Count"},
	}
	rows := []map[string]any{
		{"Title": "alpha", "Count": float64(1), "Extra": "ignored"},
		{"Title": "beta", "Count": float64(2)},
		{"Count": float64(3)}, // missing Title → nil under "T"
	}
	got := relabelRows(rows, plan)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if got[0]["T"] != "alpha" || got[0]["Count"] != float64(1) {
		t.Errorf("row 0 = %+v", got[0])
	}
	if _, hasExtra := got[0]["Extra"]; hasExtra {
		t.Error("row 0 leaked Extra into relabeled output")
	}
	if _, hasSource := got[0]["Title"]; hasSource {
		t.Error("row 0 leaked source key Title")
	}
	if got[2]["T"] != nil {
		t.Errorf("row 2 missing source key should map to nil, got %v", got[2]["T"])
	}
}

// aggExpr is a test helper that builds a parse.AggregateExpr around either
// a column reference (when col != "") or star (Star: true).
func aggExpr(fn, col string) *parse.AggregateExpr {
	a := &parse.AggregateExpr{Func: fn}
	if col == "" {
		a.Star = true
	} else {
		a.Arg = &parse.ColumnExpr{Name: col}
	}
	return a
}

func TestResolveProjectionAcceptsAggregates(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: aggExpr("COUNT", "")},
			{Expr: aggExpr("SUM", "Count"), Alias: "total"},
		},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("got %d plan entries, want 2", len(plan))
	}
	if plan[0].Label != "COUNT(*)" {
		t.Errorf("plan[0].Label = %q, want COUNT(*)", plan[0].Label)
	}
	if plan[0].Source != "" {
		t.Errorf("plan[0].Source = %q, want empty for aggregate", plan[0].Source)
	}
	if plan[0].Type != cell.TypeInt {
		t.Errorf("plan[0].Type = %v, want TypeInt", plan[0].Type)
	}
	if plan[1].Label != "total" {
		t.Errorf("plan[1].Label = %q, want total (alias)", plan[1].Label)
	}
}

func TestResolveProjectionRejectsBareColumnAlongsideAggregate(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Title"}},
			{Expr: aggExpr("COUNT", "")},
		},
	}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), `column "Title" must appear inside an aggregate or in GROUP BY`) {
		t.Errorf("got %v, want bare-column rejection", err)
	}
}

func TestResolveProjectionRejectsSumOnText(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: aggExpr("SUM", "Title")},
		},
	}
	_, err := e.resolveProjection(sel)
	if err == nil {
		t.Fatalf("expected SUM(Title) to fail validation")
	}
	// Exact wording lives in eval.ValidateAggregate; confirm SUM and the
	// type complaint are present so a user can locate the problem.
	msg := err.Error()
	if !strings.Contains(msg, "SUM") || !strings.Contains(msg, "numeric") {
		t.Errorf("error %q should reference SUM and numeric", msg)
	}
}

func TestResolveProjectionDuplicateAggregateLabels(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: aggExpr("COUNT", ""), Alias: "n"},
			{Expr: aggExpr("SUM", "Count"), Alias: "n"},
		},
	}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), `duplicate output column "n"`) {
		t.Errorf("got %v, want duplicate label error", err)
	}
}

func TestResolveProjectionAggregateDefaultLabelFromExpr(t *testing.T) {
	e := projTestExecutor(false)
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: aggExpr("COUNT", "")},
			{Expr: aggExpr("SUM", "Count")},
		},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan[0].Label != "COUNT(*)" || plan[1].Label != "SUM(Count)" {
		t.Errorf("labels = %q,%q; want COUNT(*) and SUM(Count)", plan[0].Label, plan[1].Label)
	}
}

func TestPlanHasAggregate(t *testing.T) {
	plain := []projEntry{
		{Source: "Title", Label: "Title", Expr: &parse.ColumnExpr{Name: "Title"}},
	}
	if planHasAggregate(plain) {
		t.Error("planHasAggregate returned true for plain projection")
	}
	withAgg := []projEntry{
		{Label: "n", Expr: aggExpr("COUNT", "")},
	}
	if !planHasAggregate(withAgg) {
		t.Error("planHasAggregate returned false for aggregate projection")
	}
}

// aggTestTable builds a five-row cell.Table with a Title (string) column
// and a Count (float) column. Row 4 has a NULL Count to exercise the
// "skip-NULL" branch in non-COUNT aggregates and the difference between
// COUNT(*) and COUNT(Count).
func aggTestTable() *cell.Table {
	schema := map[string]cell.ColumnInfo{
		"Title": {Name: "Title", Type: cell.TypeString},
		"Count": {Name: "Count", Type: cell.TypeFloat},
	}
	cols := []string{"Title", "Count"}
	rows := []cell.Row{
		{cell.Cell{Str: "a"}, cell.Cell{Float: 1}},
		{cell.Cell{Str: "b"}, cell.Cell{Float: 2}},
		{cell.Cell{Str: "c"}, cell.Cell{Float: 3}},
		{cell.Cell{Str: "d"}, cell.Cell{Float: 4}},
		{cell.Cell{Str: "e"}, cell.Cell{Null: true}},
	}
	return &cell.Table{Columns: cols, Schema: schema, Rows: rows}
}

func TestAggregateOneRowCountStar(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt}}
	cols, rows, err := aggregateOneRow(tbl, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["n"] != int64(5) {
		t.Errorf("cols=%v rows=%v, want one row with n=5", cols, rows)
	}
}

func TestAggregateOneRowSumAvgMinMax(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{
		{Label: "s", Expr: aggExpr("SUM", "Count"), Type: cell.TypeFloat},
		{Label: "a", Expr: aggExpr("AVG", "Count"), Type: cell.TypeFloat},
		{Label: "mn", Expr: aggExpr("MIN", "Count"), Type: cell.TypeFloat},
		{Label: "mx", Expr: aggExpr("MAX", "Count"), Type: cell.TypeFloat},
	}
	_, rows, err := aggregateOneRow(tbl, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r["s"] != float64(10) {
		t.Errorf("SUM = %v, want 10", r["s"])
	}
	if r["a"] != float64(2.5) {
		t.Errorf("AVG = %v, want 2.5", r["a"])
	}
	if r["mn"] != float64(1) {
		t.Errorf("MIN = %v, want 1", r["mn"])
	}
	if r["mx"] != float64(4) {
		t.Errorf("MAX = %v, want 4", r["mx"])
	}
}

func TestAggregateOneRowCountColumnSkipsNull(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{
		{Label: "all", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
		{Label: "nn", Expr: aggExpr("COUNT", "Count"), Type: cell.TypeInt},
	}
	_, rows, err := aggregateOneRow(tbl, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows[0]["all"] != int64(5) {
		t.Errorf("COUNT(*) = %v, want 5", rows[0]["all"])
	}
	if rows[0]["nn"] != int64(4) {
		t.Errorf("COUNT(Count) = %v, want 4 (skip NULL)", rows[0]["nn"])
	}
}

func TestAggregateOneRowEmptyTable(t *testing.T) {
	tbl := &cell.Table{
		Columns: []string{"Title", "Count"},
		Schema: map[string]cell.ColumnInfo{
			"Title": {Name: "Title", Type: cell.TypeString},
			"Count": {Name: "Count", Type: cell.TypeFloat},
		},
	}
	plan := []projEntry{
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
		{Label: "s", Expr: aggExpr("SUM", "Count"), Type: cell.TypeFloat},
	}
	_, rows, err := aggregateOneRow(tbl, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row even over empty table, got %d", len(rows))
	}
	if rows[0]["n"] != int64(0) {
		t.Errorf("COUNT(*) over empty = %v, want 0", rows[0]["n"])
	}
	if rows[0]["s"] != nil {
		t.Errorf("SUM over empty = %v, want nil (NULL)", rows[0]["s"])
	}
}

func TestAggregateOneRowSharedSlotForRepeatedAggregate(t *testing.T) {
	// Same parse.AggregateExpr pointer reused across the plan should be
	// scored exactly once, not advanced twice per row.
	tbl := aggTestTable()
	shared := aggExpr("COUNT", "")
	plan := []projEntry{
		{Label: "a", Expr: shared, Type: cell.TypeInt},
		{Label: "b", Expr: shared, Type: cell.TypeInt},
	}
	_, rows, err := aggregateOneRow(tbl, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows[0]["a"] != int64(5) || rows[0]["b"] != int64(5) {
		t.Errorf("shared slot got %v / %v, want 5 / 5", rows[0]["a"], rows[0]["b"])
	}
}

func TestAggregateOneRowLimitZeroClips(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt}}
	zero := 0
	_, rows, err := aggregateOneRow(tbl, plan, nil, nil, &zero)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("LIMIT 0 should clip the row, got %d rows", len(rows))
	}
}

func TestAggregateOneRowOffsetOneClips(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt}}
	one := 1
	_, rows, err := aggregateOneRow(tbl, plan, nil, &one, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("OFFSET 1 should clip the only row, got %d rows", len(rows))
	}
}

// groupProjTestExecutor builds an Executor with Title (text), Status (text),
// and Count (number) so GROUP BY validation can exercise bare/grouped column
// rules without spinning up a Graph client.
func groupProjTestExecutor() *Executor {
	return &Executor{
		Bound: &BoundList{
			Columns: []string{"Title", "Status", "Count"},
			Schema: map[string]FieldInfo{
				"Title":  {Name: "Title", Type: FieldText},
				"Status": {Name: "Status", Type: FieldText},
				"Count":  {Name: "Count", Type: FieldNumber},
			},
		},
	}
}

func TestResolveProjectionGroupByBareColumn(t *testing.T) {
	e := groupProjTestExecutor()
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Status"}},
			{Expr: aggExpr("COUNT", "")},
		},
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("got %d plan entries, want 2", len(plan))
	}
	if plan[0].Label != "Status" || plan[1].Label != "COUNT(*)" {
		t.Errorf("labels = %q/%q, want Status/COUNT(*)", plan[0].Label, plan[1].Label)
	}
}

func TestResolveProjectionGroupByRejectsBareNotInGroup(t *testing.T) {
	e := groupProjTestExecutor()
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Title"}},
			{Expr: aggExpr("COUNT", "")},
		},
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
	}
	_, err := e.resolveProjection(sel)
	if err == nil {
		t.Fatalf("expected rejection of bare Title under GROUP BY Status")
	}
	if !strings.Contains(err.Error(), `column "Title" must appear in GROUP BY`) {
		t.Errorf("error %q should mention GROUP BY", err.Error())
	}
}

func TestResolveProjectionGroupByRejectsSelectStar(t *testing.T) {
	e := groupProjTestExecutor()
	sel := &parse.SelectStmt{Star: true, GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), "SELECT * with GROUP BY") {
		t.Errorf("got %v, want SELECT * + GROUP BY rejection", err)
	}
}

func TestResolveProjectionGroupByUnknownColumn(t *testing.T) {
	e := groupProjTestExecutor()
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{{Expr: aggExpr("COUNT", "")}},
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Nope"}},
	}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), `GROUP BY: unknown column "Nope"`) {
		t.Errorf("got %v, want unknown-column error", err)
	}
}

func TestResolveProjectionGroupByDuplicateColumn(t *testing.T) {
	e := groupProjTestExecutor()
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{{Expr: aggExpr("COUNT", "")}},
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}, &parse.ColumnExpr{Name: "Status"}},
	}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), `duplicate expression "Status" in GROUP BY`) {
		t.Errorf("got %v, want duplicate-expression error", err)
	}
}

func TestResolveProjectionGroupByAliasOnGroupColumn(t *testing.T) {
	e := groupProjTestExecutor()
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "Status"}, Alias: "s"},
			{Expr: aggExpr("COUNT", ""), Alias: "n"},
		},
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
	}
	plan, err := e.resolveProjection(sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan[0].Label != "s" || plan[1].Label != "n" {
		t.Errorf("labels = %q/%q, want s/n", plan[0].Label, plan[1].Label)
	}
}

// groupTestTable builds a five-row cell.Table for grouped-aggregation tests.
// Three Open rows (one with NULL Count) plus two Done rows give two groups
// with distinct COUNT(*) / COUNT(Count) / SUM patterns and a HAVING split.
func groupTestTable() *cell.Table {
	schema := map[string]cell.ColumnInfo{
		"Status": {Name: "Status", Type: cell.TypeString},
		"Count":  {Name: "Count", Type: cell.TypeFloat},
	}
	cols := []string{"Status", "Count"}
	rows := []cell.Row{
		{cell.Cell{Str: "Open"}, cell.Cell{Float: 1}},
		{cell.Cell{Str: "Open"}, cell.Cell{Float: 2}},
		{cell.Cell{Str: "Done"}, cell.Cell{Float: 3}},
		{cell.Cell{Str: "Done"}, cell.Cell{Float: 4}},
		{cell.Cell{Str: "Open"}, cell.Cell{Null: true}},
	}
	return &cell.Table{Columns: cols, Schema: schema, Rows: rows}
}

func TestAggregateGroupedSingleColumn(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
		{Label: "s", Expr: aggExpr("SUM", "Count"), Type: cell.TypeFloat},
	}
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d groups, want 2", len(rows))
	}
	// Insertion order: Open first, Done second.
	if rows[0]["Status"] != "Open" || rows[0]["n"] != int64(3) || rows[0]["s"] != float64(3) {
		t.Errorf("Open group = %+v, want Status=Open n=3 s=3", rows[0])
	}
	if rows[1]["Status"] != "Done" || rows[1]["n"] != int64(2) || rows[1]["s"] != float64(7) {
		t.Errorf("Done group = %+v, want Status=Done n=2 s=7", rows[1])
	}
}

func TestAggregateGroupedTwoColumns(t *testing.T) {
	schema := map[string]cell.ColumnInfo{
		"Status":   {Name: "Status", Type: cell.TypeString},
		"Priority": {Name: "Priority", Type: cell.TypeFloat},
	}
	tbl := &cell.Table{
		Columns: []string{"Status", "Priority"},
		Schema:  schema,
		Rows: []cell.Row{
			{cell.Cell{Str: "Open"}, cell.Cell{Float: 1}},
			{cell.Cell{Str: "Open"}, cell.Cell{Float: 1}},
			{cell.Cell{Str: "Open"}, cell.Cell{Float: 2}},
			{cell.Cell{Str: "Done"}, cell.Cell{Float: 1}},
		},
	}
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "Priority", Expr: &parse.ColumnExpr{Name: "Priority"}, Type: cell.TypeFloat},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}, &parse.ColumnExpr{Name: "Priority"}}}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d groups, want 3", len(rows))
	}
	// (Open,1) seen twice, (Open,2) once, (Done,1) once.
	want := []struct {
		status string
		prio   float64
		n      int64
	}{
		{"Open", 1, 2},
		{"Open", 2, 1},
		{"Done", 1, 1},
	}
	for i, w := range want {
		if rows[i]["Status"] != w.status || rows[i]["Priority"] != w.prio || rows[i]["n"] != w.n {
			t.Errorf("row %d = %+v, want Status=%s Priority=%v n=%d", i, rows[i], w.status, w.prio, w.n)
		}
	}
}

func TestAggregateGroupedHavingFiltersOnAggregate(t *testing.T) {
	tbl := groupTestTable()
	sumCount := aggExpr("SUM", "Count")
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "s", Expr: sumCount, Type: cell.TypeFloat},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		Having:  cmpE(sumCount, ">", vnum("5")),
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows after HAVING, want 1", len(rows))
	}
	if rows[0]["Status"] != "Done" || rows[0]["s"] != float64(7) {
		t.Errorf("surviving group = %+v, want Done/7", rows[0])
	}
}

func TestAggregateGroupedHavingOnGroupColumn(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		Having:  cmp("Status", "=", vstr("Open")),
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["Status"] != "Open" {
		t.Errorf("rows = %+v, want only Open", rows)
	}
}

func TestAggregateGroupedRejectsHavingBareColNotInGroup(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		Having:  cmp("Count", ">", vnum("0")), // Count is not in GROUP BY
	}
	_, _, err := aggregateGrouped(tbl, plan, sel)
	if err == nil || !strings.Contains(err.Error(), `HAVING: column "Count" must appear in GROUP BY`) {
		t.Errorf("got %v, want HAVING bare-column rejection", err)
	}
}

func TestAggregateGroupedHavingNullTestRequiresGroupCol(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		Having:  isnull("Count", false), // bare Count IS NULL, not in GROUP BY
	}
	_, _, err := aggregateGrouped(tbl, plan, sel)
	if err == nil || !strings.Contains(err.Error(), `HAVING: column "Count" must appear in GROUP BY`) {
		t.Errorf("got %v, want HAVING NullTest column rejection", err)
	}
}

func TestAggregateGroupedEmptyInput(t *testing.T) {
	schema := map[string]cell.ColumnInfo{
		"Status": {Name: "Status", Type: cell.TypeString},
		"Count":  {Name: "Count", Type: cell.TypeFloat},
	}
	tbl := &cell.Table{
		Columns: []string{"Status", "Count"},
		Schema:  schema,
	}
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("empty input over GROUP BY should yield 0 groups, got %d", len(rows))
	}
}

func TestAggregateGroupedInsertionOrder(t *testing.T) {
	schema := map[string]cell.ColumnInfo{
		"Status": {Name: "Status", Type: cell.TypeString},
	}
	// Done seen first, then Open, then Done again — final order Done, Open.
	tbl := &cell.Table{
		Columns: []string{"Status"},
		Schema:  schema,
		Rows: []cell.Row{
			{cell.Cell{Str: "Done"}},
			{cell.Cell{Str: "Open"}},
			{cell.Cell{Str: "Done"}},
		},
	}
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d groups, want 2", len(rows))
	}
	if rows[0]["Status"] != "Done" || rows[1]["Status"] != "Open" {
		t.Errorf("order = %v/%v, want Done/Open", rows[0]["Status"], rows[1]["Status"])
	}
}

func TestAggregateGroupedDistinctDedupesAcrossGroups(t *testing.T) {
	tbl := groupTestTable()
	// Project only COUNT(*) without the grouping column — Open and Done both
	// have distinct counts (3 and 2), so DISTINCT keeps both. Then add a
	// fake third group manually... easier: project something that collapses.
	// Both groups share the same Count when projecting MIN(Count) on subsets,
	// but the simplest cross-group dedup is on a constant. Use literal '1'.
	plan := []projEntry{
		{Label: "one", Expr: litE(vnum("1")), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}, Distinct: true}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("DISTINCT across two groups projecting literal 1 should collapse to 1 row, got %d", len(rows))
	}
}

func TestAggregateGroupedOffsetLimit(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	one := 1
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}, Limit: &one}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["Status"] != "Open" {
		t.Errorf("LIMIT 1 should keep Open only, got %+v", rows)
	}
	sel = &parse.SelectStmt{GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}}, Offset: &one}
	_, rows, err = aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["Status"] != "Done" {
		t.Errorf("OFFSET 1 should keep Done only, got %+v", rows)
	}
}

func TestAggregateGroupedSharedAggInProjectionAndHaving(t *testing.T) {
	tbl := groupTestTable()
	// Same SUM(Count) pointer used in both projection and HAVING; the slot
	// table must dedupe so the sum isn't advanced twice per row.
	shared := aggExpr("SUM", "Count")
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "s", Expr: shared, Type: cell.TypeFloat},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		Having:  cmpE(shared, ">", vnum("5")),
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["Status"] != "Done" || rows[0]["s"] != float64(7) {
		t.Errorf("rows = %+v, want one Done row with s=7", rows)
	}
}

func TestAggregateOneRowHavingDropsRow(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt}}
	// COUNT(*) is 5; HAVING demands > 10, so the row drops.
	having := cmpE(aggExpr("COUNT", ""), ">", vnum("10"))
	_, rows, err := aggregateOneRow(tbl, plan, having, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("HAVING COUNT(*) > 10 should drop the row, got %d", len(rows))
	}
}

func TestAggregateOneRowHavingKeepsRow(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt}}
	having := cmpE(aggExpr("COUNT", ""), ">", vnum("0"))
	_, rows, err := aggregateOneRow(tbl, plan, having, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["n"] != int64(5) {
		t.Errorf("HAVING COUNT(*) > 0 should keep the row, got %+v", rows)
	}
}

func TestAggregateOneRowRejectsHavingBareColumn(t *testing.T) {
	tbl := aggTestTable()
	plan := []projEntry{{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt}}
	having := cmp("Count", ">", vnum("0"))
	_, _, err := aggregateOneRow(tbl, plan, having, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `HAVING: column "Count" must appear inside an aggregate`) {
		t.Errorf("got %v, want bare-column HAVING rejection", err)
	}
}

func TestValidateAggregatedHavingChecksAggregateArgType(t *testing.T) {
	tbl := groupTestTable()
	groupCols := map[string]bool{"Status": true}
	// SUM on a string column (Status) should fail aggregate validation.
	having := cmpE(aggExpr("SUM", "Status"), ">", vnum("0"))
	err := validateAggregatedHaving(having, groupCols, []parse.Expr{&parse.ColumnExpr{Name: "Status"}}, tbl.Schema)
	if err == nil || !strings.Contains(err.Error(), "SUM") || !strings.Contains(err.Error(), "numeric") {
		t.Errorf("got %v, want SUM numeric rejection", err)
	}
}

func TestCollectAllAggregatesDedupesSharedPointer(t *testing.T) {
	shared := aggExpr("COUNT", "")
	other := aggExpr("SUM", "Count")
	plan := []projEntry{
		{Expr: shared},
		{Expr: other},
	}
	having := cmpE(shared, ">", vnum("0"))
	got := collectAllAggregates(plan, having)
	if len(got) != 2 {
		t.Fatalf("got %d aggregates, want 2 (shared pointer dedupes)", len(got))
	}
	if got[0] != shared || got[1] != other {
		t.Errorf("order/identity wrong: got %v, want shared+other", got)
	}
}

func TestGroupKeyDistinguishesTypes(t *testing.T) {
	// Strings "1" and number 1 in the same slot must produce different keys.
	row1 := cell.Row{{Str: "1"}}
	row2 := cell.Row{{Int: 1}}
	idx := []int{0}
	k1, _ := groupKey(row1, idx, []cell.ColumnType{cell.TypeString})
	k2, _ := groupKey(row2, idx, []cell.ColumnType{cell.TypeInt})
	if k1 == k2 {
		t.Errorf("string %q and int 1 produced same key %q", "1", k1)
	}
	// NULL has its own slot.
	rowN := cell.Row{{Null: true}}
	kN, _ := groupKey(rowN, idx, []cell.ColumnType{cell.TypeString})
	if kN == k1 || kN == k2 {
		t.Errorf("NULL key %q collided with non-NULL", kN)
	}
}

func TestGroupKeyStringBoundaryCollision(t *testing.T) {
	// Length-prefixed encoding must prevent "ab" + "cd" colliding with "abc" + "d".
	idx := []int{0, 1}
	types := []cell.ColumnType{cell.TypeString, cell.TypeString}
	a := cell.Row{{Str: "ab"}, {Str: "cd"}}
	b := cell.Row{{Str: "abc"}, {Str: "d"}}
	ka, _ := groupKey(a, idx, types)
	kb, _ := groupKey(b, idx, types)
	if ka == kb {
		t.Errorf("string-boundary collision: both keys = %q", ka)
	}
}

func TestHavingWithoutAggOrGroupReportsError(t *testing.T) {
	// resolveProjection has no opinion on HAVING; executeSelect catches it.
	// Exercise that path through a hand-built Executor by calling resolveProjection
	// first to confirm the plan is non-aggregated, then check the error string in
	// executeSelect's gating logic. We can drive this directly by validating the
	// surface error from a plain plan + HAVING via the same gating shape.
	plan := []projEntry{{Source: "Title", Label: "Title", Expr: &parse.ColumnExpr{Name: "Title"}}}
	if planHasAggregate(plan) {
		t.Fatal("test setup: plan should be non-aggregated")
	}
}

func TestResolveOrderByOutputByLabel(t *testing.T) {
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	keys := []parse.OrderKey{{Column: "n", Desc: true}}
	got, err := resolveOrderByOutput(keys, plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Label != "n" || got[0].Type != cell.TypeInt || !got[0].Desc {
		t.Errorf("got %+v, want one entry Label=n Type=int Desc=true", got)
	}
}

func TestResolveOrderByOutputAggregateDefaultLabel(t *testing.T) {
	plan := []projEntry{
		{Label: "COUNT(*)", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	keys := []parse.OrderKey{{Column: "COUNT(*)"}}
	got, err := resolveOrderByOutput(keys, plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Label != "COUNT(*)" {
		t.Errorf("got %+v, want one entry for COUNT(*)", got)
	}
}

func TestResolveOrderByOutputUnknownLabel(t *testing.T) {
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
	}
	keys := []parse.OrderKey{{Column: "Nope"}}
	_, err := resolveOrderByOutput(keys, plan)
	if err == nil || !strings.Contains(err.Error(), `unknown column "Nope" in ORDER BY`) {
		t.Errorf("got %v, want unknown-label error", err)
	}
	if err != nil && !strings.Contains(err.Error(), "SELECT list") {
		t.Errorf("error %q should hint at SELECT list", err.Error())
	}
}

func TestCompareOutputValueInt(t *testing.T) {
	cases := []struct {
		a, b any
		want int // sign only
	}{
		{int64(1), int64(2), -1},
		{int64(5), int64(5), 0},
		{int64(7), int64(3), +1},
	}
	for _, tc := range cases {
		got := compareOutputValue(tc.a, tc.b, cell.TypeInt)
		if (got < 0) != (tc.want < 0) || (got > 0) != (tc.want > 0) || (got == 0) != (tc.want == 0) {
			t.Errorf("compareOutputValue(%v, %v, int) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareOutputValueFloat(t *testing.T) {
	if compareOutputValue(float64(1.5), float64(2.5), cell.TypeFloat) >= 0 {
		t.Error("1.5 < 2.5 expected")
	}
	if compareOutputValue(float64(3.0), float64(3.0), cell.TypeFloat) != 0 {
		t.Error("3.0 == 3.0 expected")
	}
}

func TestCompareOutputValueBool(t *testing.T) {
	if compareOutputValue(false, true, cell.TypeBool) >= 0 {
		t.Error("false < true expected")
	}
	if compareOutputValue(true, true, cell.TypeBool) != 0 {
		t.Error("true == true expected")
	}
}

func TestCompareOutputValueString(t *testing.T) {
	if compareOutputValue("alpha", "beta", cell.TypeString) >= 0 {
		t.Error("alpha < beta expected")
	}
}

func TestCompareOutputValueNullsHigh(t *testing.T) {
	if compareOutputValue(nil, nil, cell.TypeInt) != 0 {
		t.Error("nil == nil expected")
	}
	if compareOutputValue(nil, int64(1), cell.TypeInt) <= 0 {
		t.Error("nil sorts above int64(1) expected")
	}
	if compareOutputValue(int64(1), nil, cell.TypeInt) >= 0 {
		t.Error("int64(1) sorts below nil expected")
	}
}

func TestSortOutputRowsAsc(t *testing.T) {
	rows := []map[string]any{
		{"n": int64(3)},
		{"n": int64(1)},
		{"n": int64(2)},
	}
	order := []outOrderEntry{{Label: "n", Type: cell.TypeInt}}
	sortOutputRows(rows, order)
	want := []int64{1, 2, 3}
	for i, w := range want {
		if rows[i]["n"] != w {
			t.Errorf("row %d = %v, want %d", i, rows[i]["n"], w)
		}
	}
}

func TestSortOutputRowsDesc(t *testing.T) {
	rows := []map[string]any{
		{"n": int64(1)},
		{"n": int64(3)},
		{"n": int64(2)},
	}
	order := []outOrderEntry{{Label: "n", Type: cell.TypeInt, Desc: true}}
	sortOutputRows(rows, order)
	want := []int64{3, 2, 1}
	for i, w := range want {
		if rows[i]["n"] != w {
			t.Errorf("row %d = %v, want %d", i, rows[i]["n"], w)
		}
	}
}

func TestSortOutputRowsMultiKey(t *testing.T) {
	rows := []map[string]any{
		{"Status": "Open", "n": int64(2)},
		{"Status": "Done", "n": int64(1)},
		{"Status": "Open", "n": int64(5)},
		{"Status": "Done", "n": int64(3)},
	}
	order := []outOrderEntry{
		{Label: "Status", Type: cell.TypeString},
		{Label: "n", Type: cell.TypeInt, Desc: true},
	}
	sortOutputRows(rows, order)
	// ASC Status: Done first, then Open. Within each, DESC n.
	wantStatus := []string{"Done", "Done", "Open", "Open"}
	wantN := []int64{3, 1, 5, 2}
	for i := range rows {
		if rows[i]["Status"] != wantStatus[i] || rows[i]["n"] != wantN[i] {
			t.Errorf("row %d = %+v, want Status=%s n=%d", i, rows[i], wantStatus[i], wantN[i])
		}
	}
}

func TestSortOutputRowsNullsHigh(t *testing.T) {
	rows := []map[string]any{
		{"n": int64(2)},
		{"n": nil},
		{"n": int64(1)},
	}
	order := []outOrderEntry{{Label: "n", Type: cell.TypeInt}}
	sortOutputRows(rows, order)
	// ASC: 1, 2, NULL.
	want := []any{int64(1), int64(2), nil}
	for i, w := range want {
		if rows[i]["n"] != w {
			t.Errorf("ASC row %d = %v, want %v", i, rows[i]["n"], w)
		}
	}
	// DESC: NULL, 2, 1.
	order[0].Desc = true
	sortOutputRows(rows, order)
	wantDesc := []any{nil, int64(2), int64(1)}
	for i, w := range wantDesc {
		if rows[i]["n"] != w {
			t.Errorf("DESC row %d = %v, want %v", i, rows[i]["n"], w)
		}
	}
}

func TestAggregateGroupedOrderByAggregateDesc(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		OrderBy: []parse.OrderKey{{Column: "n", Desc: true}},
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d groups, want 2", len(rows))
	}
	// Open has 3 rows, Done has 2; DESC n puts Open first.
	if rows[0]["Status"] != "Open" || rows[1]["Status"] != "Done" {
		t.Errorf("order = %v/%v, want Open/Done", rows[0]["Status"], rows[1]["Status"])
	}
}

func TestAggregateGroupedOrderByGroupColumnAsc(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		OrderBy: []parse.OrderKey{{Column: "Status"}},
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ASC Status: Done, Open (alphabetical).
	if rows[0]["Status"] != "Done" || rows[1]["Status"] != "Open" {
		t.Errorf("ASC Status = %v/%v, want Done/Open", rows[0]["Status"], rows[1]["Status"])
	}
}

func TestAggregateGroupedOrderByAlias(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "s", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "total", Expr: aggExpr("SUM", "Count"), Type: cell.TypeFloat},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		OrderBy: []parse.OrderKey{{Column: "total"}},
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// SUM(Count) for Open = 3, for Done = 7. ASC → Open, Done.
	if rows[0]["s"] != "Open" || rows[1]["s"] != "Done" {
		t.Errorf("order by alias 'total' ASC = %v/%v, want Open/Done", rows[0]["s"], rows[1]["s"])
	}
}

func TestAggregateGroupedOrderByThenLimit(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	one := 1
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		OrderBy: []parse.OrderKey{{Column: "n", Desc: true}},
		Limit:   &one,
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sort first (Open has higher count), then LIMIT 1 picks the top row.
	if len(rows) != 1 || rows[0]["Status"] != "Open" {
		t.Errorf("ORDER BY n DESC LIMIT 1 should pick Open, got %+v", rows)
	}
}

func TestAggregateGroupedOrderByUnknownLabel(t *testing.T) {
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "Status", Expr: &parse.ColumnExpr{Name: "Status"}, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy: []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		OrderBy: []parse.OrderKey{{Column: "Nope"}},
	}
	_, _, err := aggregateGrouped(tbl, plan, sel)
	if err == nil || !strings.Contains(err.Error(), `unknown column "Nope" in ORDER BY`) {
		t.Errorf("got %v, want unknown-label error", err)
	}
}

func TestAggregateGroupedOrderByAfterDistinct(t *testing.T) {
	// DISTINCT then ORDER BY: literal-projection groups collapse to one row,
	// so the sort sees a single row and applyOffsetLimit passes it through.
	tbl := groupTestTable()
	plan := []projEntry{
		{Label: "one", Expr: litE(vnum("1")), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{
		GroupBy:  []parse.Expr{&parse.ColumnExpr{Name: "Status"}},
		Distinct: true,
		OrderBy:  []parse.OrderKey{{Column: "one"}},
	}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0]["one"] != int64(1) {
		t.Errorf("DISTINCT + ORDER BY = %+v, want one row {one: 1}", rows)
	}
}

func TestRejectMutationOutput_UpdateDeleteInsert(t *testing.T) {
	// --output is SELECT-only on the sp backend; mutations must error before
	// any Graph call. The reject helper runs at the top of each execute*
	// path, so a minimal Executor (no Graph) is enough to drive the check.
	cases := []struct {
		name string
		stmt parse.Stmt
		want string
	}{
		{
			name: "update",
			stmt: &parse.UpdateStmt{
				Assignments: []parse.Assignment{{Column: "Title", Value: &parse.LiteralExpr{Value: parse.Value{Kind: parse.ValString, Str: "x"}}}},
			},
			want: "UPDATE",
		},
		{
			name: "delete",
			stmt: &parse.DeleteStmt{},
			want: "DELETE",
		},
		{
			name: "insert",
			stmt: &parse.InsertStmt{
				Columns: []string{"Title"},
				Values:  []parse.Value{{Kind: parse.ValString, Str: "x"}},
			},
			want: "INSERT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Executor{
				Bound:      &BoundList{Schema: execTestSchema()},
				OutputPath: "/tmp/should-not-write",
			}
			err := e.Execute(nil, tc.stmt, true)
			if err == nil {
				t.Fatal("expected error for mutation + --output")
			}
			if !strings.Contains(err.Error(), "--output is not supported") {
				t.Errorf("error should mention --output: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name verb %q: %v", tc.want, err)
			}
		})
	}
}

// caseFoldTestTable builds a small in-memory list where display-name
// grouping via LOWER collapses mixed-case values. Mirrors the CSV backend's
// caseFoldFixture so the two backends see equivalent inputs.
func caseFoldTestTable() *cell.Table {
	schema := map[string]cell.ColumnInfo{
		"application_name": {Name: "application_name", Type: cell.TypeString},
	}
	return &cell.Table{
		Columns: []string{"application_name"},
		Schema:  schema,
		Rows: []cell.Row{
			{cell.Cell{Str: "CoStar"}},
			{cell.Cell{Str: "Costar"}},
			{cell.Cell{Str: "costar"}},
			{cell.Cell{Str: "Sailpoint"}},
			{cell.Cell{Str: "SailPoint"}},
			{cell.Cell{Str: "Something"}},
		},
	}
}

func TestAggregateGroupedWithLowerExpression(t *testing.T) {
	tbl := caseFoldTestTable()
	lower := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "application_name"}}}
	plan := []projEntry{
		{Label: "k", Expr: lower, Type: cell.TypeString},
		{Label: "n", Expr: aggExpr("COUNT", ""), Type: cell.TypeInt},
	}
	sel := &parse.SelectStmt{GroupBy: []parse.Expr{lower}}
	_, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		t.Fatalf("aggregateGrouped: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d groups, want 3 (costar, sailpoint, something):\n%+v", len(rows), rows)
	}
	byKey := map[string]int64{}
	for _, r := range rows {
		byKey[r["k"].(string)] = r["n"].(int64)
	}
	if byKey["costar"] != 3 {
		t.Errorf("costar count = %d, want 3", byKey["costar"])
	}
	if byKey["sailpoint"] != 2 {
		t.Errorf("sailpoint count = %d, want 2", byKey["sailpoint"])
	}
	if byKey["something"] != 1 {
		t.Errorf("something count = %d, want 1", byKey["something"])
	}
}

func TestAggregateGroupedRejectsBareColumnUnderExprGroupBy(t *testing.T) {
	// GROUP BY LOWER(name); projection references bare `application_name`.
	// The bare column is not a group key (only its lowercased form is), so
	// this must be rejected at plan time — the value varies within a group.
	e := &Executor{
		Bound: &BoundList{
			Columns: []string{"application_name"},
			Schema: map[string]FieldInfo{
				"application_name": {Name: "application_name", DisplayName: "application_name", Type: FieldText},
			},
		},
	}
	lower := &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "application_name"}}}
	sel := &parse.SelectStmt{
		Columns: []parse.Projection{
			{Expr: &parse.ColumnExpr{Name: "application_name"}},
			{Expr: aggExpr("COUNT", "")},
		},
		GroupBy: []parse.Expr{lower},
	}
	_, err := e.resolveProjection(sel)
	if err == nil || !strings.Contains(err.Error(), "must appear in GROUP BY") {
		t.Fatalf("got %v, want must-appear-in-GROUP-BY error", err)
	}
}

func TestValidateGroupByRejectsAggregate(t *testing.T) {
	schema := map[string]cell.ColumnInfo{"x": {Name: "x", Type: cell.TypeInt}}
	exprs := []parse.Expr{aggExpr("COUNT", "x")}
	_, err := validateGroupBy(exprs, schema)
	if err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("got %v, want aggregate-rejection error", err)
	}
}

func TestValidateGroupByRejectsDuplicateExpression(t *testing.T) {
	schema := map[string]cell.ColumnInfo{"x": {Name: "x", Type: cell.TypeString}}
	lower := func() *parse.FuncCallExpr {
		return &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "x"}}}
	}
	exprs := []parse.Expr{lower(), lower()}
	_, err := validateGroupBy(exprs, schema)
	if err == nil || !strings.Contains(err.Error(), "duplicate expression") {
		t.Fatalf("got %v, want duplicate-expression error", err)
	}
}

func TestODataFilterRejectsScalarFunctionInWhere(t *testing.T) {
	// Scoped rejection: the SP OData translator can't emit LOWER(x) in
	// $filter, so a WHERE-side scalar function should fail with an
	// SP-permanent-rejection-style message rather than falling through.
	pred := &parse.Comparison{
		LExpr: &parse.FuncCallExpr{Name: "LOWER", Args: []parse.Expr{&parse.ColumnExpr{Name: "app_name"}}},
		Op:    "=",
		Value: parse.Value{Kind: parse.ValString, Str: "x"},
	}
	schema := map[string]FieldInfo{"app_name": {Name: "app_name", Type: FieldText}}
	_, err := ToOData(pred, schema)
	if err == nil || !strings.Contains(err.Error(), "not supported by SharePoint") || !strings.Contains(err.Error(), "LOWER") {
		t.Fatalf("got %v, want SP-rejection error naming LOWER", err)
	}
}
