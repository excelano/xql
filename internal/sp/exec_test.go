package sp

import (
	"strings"
	"testing"

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

// TestBuildFieldsBody covers the path through the executor's per-assignment
// validation and JSON shaping, including order independence (map output) and
// mixed-type batches.
func TestBuildFieldsBody(t *testing.T) {
	e := &Executor{Bound: &BoundList{Schema: execTestSchema()}}
	body, err := e.buildFieldsBody([]parse.Assignment{
		{Column: "Title", Value: litE(vstr("hello"))},
		{Column: "Count", Value: litE(vnum("3"))},
		{Column: "Active", Value: litE(vbool(true))},
		{Column: "Due", Value: litE(vstr("2024-01-01"))},
		{Column: "Status", Value: litE(vnull())},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestBuildFieldsBodyRejects(t *testing.T) {
	e := &Executor{Bound: &BoundList{Schema: execTestSchema()}}
	_, err := e.buildFieldsBody([]parse.Assignment{
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
