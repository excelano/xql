package eval

import (
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// fixtureTable builds a small in-memory cell.Table representative of a typical CSV:
// mixed types, NULL cells, enough rows to exercise filtering. Used as the
// shared fixture for the filter and exec tests.
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

func TestMatchesNilPredicate(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	ok, err := Matches(nil, tbl.Rows[0], ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("nil predicate should match every row")
	}
}

func TestMatchesComparisons(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	tests := []struct {
		name    string
		pred    parse.Predicate
		wantIDs []int64 // matching row IDs in order
	}{
		{"int equals", cmp("Priority", "=", vnum("1")), []int64{2}},
		{"int greater", cmp("Priority", ">", vnum("2")), []int64{1, 3}},
		{"int less or equal", cmp("Priority", "<=", vnum("3")), []int64{1, 2}},
		{"string equals", cmp("Status", "=", vstr("Open")), []int64{1, 3, 4}},
		{"string not equals", cmp("Status", "!=", vstr("Open")), []int64{2}},
		{"bool true", cmp("Archived", "=", vbool(true)), []int64{2}},
		{"date before", cmp("Modified", "<", vstr("2024-01-01")), []int64{2}},
		{"date on or after", cmp("Modified", ">=", vstr("2024-01-01")), []int64{1, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tt.pred)
			if !equalIDs(ids, tt.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

func TestMatchesNullTests(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	ids := matchingIDs(t, tbl, ctx, isnull("Modified", false))
	if !equalIDs(ids, []int64{3}) {
		t.Fatalf("IS NULL: got %v, want [3]", ids)
	}
	ids = matchingIDs(t, tbl, ctx, isnull("Modified", true))
	if !equalIDs(ids, []int64{1, 2, 4}) {
		t.Fatalf("IS NOT NULL: got %v, want [1 2 4]", ids)
	}
}

func TestMatchesNullExcludesFromComparison(t *testing.T) {
	// cell.Row 4 has Priority = NULL. Comparing NULL with anything is UNKNOWN,
	// which means the row is excluded from WHERE results regardless of op.
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	for _, op := range []string{"=", "!=", "<", "<=", ">", ">="} {
		ids := matchingIDs(t, tbl, ctx, cmp("Priority", op, vnum("99")))
		for _, id := range ids {
			if id == 4 {
				t.Fatalf("op %q: row 4 (Priority=NULL) leaked into result", op)
			}
		}
	}
}

func TestMatchesLogicalOps(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	tests := []struct {
		name    string
		pred    parse.Predicate
		wantIDs []int64
	}{
		{
			name:    "AND",
			pred:    and(cmp("Status", "=", vstr("Open")), cmp("Priority", ">", vnum("2"))),
			wantIDs: []int64{1, 3},
		},
		{
			name:    "OR",
			pred:    or(cmp("Status", "=", vstr("Done")), cmp("Priority", "=", vnum("5"))),
			wantIDs: []int64{2, 3},
		},
		{
			name:    "NOT",
			pred:    not(cmp("Status", "=", vstr("Open"))),
			wantIDs: []int64{2},
		},
		{
			name: "compound: open AND not archived AND priority >= 3",
			pred: and(
				and(cmp("Status", "=", vstr("Open")), not(cmp("Archived", "=", vbool(true)))),
				cmp("Priority", ">=", vnum("3")),
			),
			wantIDs: []int64{1, 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tt.pred)
			if !equalIDs(ids, tt.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

// Three-valued logic: NULL OR TRUE = TRUE; NULL AND TRUE = NULL (excludes).
func TestMatchesThreeValuedLogic(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)

	// cell.Row 4: Priority=NULL. The NULL-tainted comparison is UNKNOWN.
	// "Priority > 0 OR Status = 'Open'" should still match row 4 because
	// the second branch is TRUE: UNKNOWN OR TRUE = TRUE.
	pred := or(cmp("Priority", ">", vnum("0")), cmp("Status", "=", vstr("Open")))
	ids := matchingIDs(t, tbl, ctx, pred)
	found4 := false
	for _, id := range ids {
		if id == 4 {
			found4 = true
		}
	}
	if !found4 {
		t.Fatal("UNKNOWN OR TRUE should let row 4 through")
	}

	// "Priority > 0 AND Status = 'Open'" should NOT match row 4 because
	// UNKNOWN AND TRUE = UNKNOWN, which is excluded.
	pred2 := and(cmp("Priority", ">", vnum("0")), cmp("Status", "=", vstr("Open")))
	ids2 := matchingIDs(t, tbl, ctx, pred2)
	for _, id := range ids2 {
		if id == 4 {
			t.Fatal("UNKNOWN AND TRUE should exclude row 4")
		}
	}
}

func TestMatchesLike(t *testing.T) {
	// Fixture titles: Alpha, Beta, Gamma, Delta.
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	cases := []struct {
		name    string
		pred    parse.Predicate
		wantIDs []int64
	}{
		{"prefix wildcard", like("Title", "A%", false), []int64{1}},
		{"suffix wildcard", like("Title", "%a", false), []int64{1, 2, 3, 4}}, // all end with 'a'
		{"contains wildcard", like("Title", "%et%", false), []int64{2}},
		{"single-char wildcard", like("Title", "_eta", false), []int64{2}},
		{"literal escape", like("Title", `\%`, false), []int64{}},
		{"not like prefix", like("Title", "A%", true), []int64{2, 3, 4}},
		{"no match", like("Title", "Zzz%", false), []int64{}},
		{"exact equal via empty wildcards", like("Title", "Alpha", false), []int64{1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tc.pred)
			if !equalIDs(ids, tc.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestMatchesILike(t *testing.T) {
	// Fixture titles: Alpha, Beta, Gamma, Delta.
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	cases := []struct {
		name    string
		pred    parse.Predicate
		wantIDs []int64
	}{
		{"ilike prefix lowercase pattern matches mixed case", ilike("Title", "alpha", false), []int64{1}},
		{"ilike prefix uppercase pattern matches mixed case", ilike("Title", "ALPHA%", false), []int64{1}},
		{"ilike contains case insensitive", ilike("Title", "%ET%", false), []int64{2}},
		{"not ilike", ilike("Title", "alpha", true), []int64{2, 3, 4}},
		{"ilike no match", ilike("Title", "Zzz%", false), []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tc.pred)
			if !equalIDs(ids, tc.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestMatchesLikeRejectsNonString(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	_, err := Matches(like("Priority", "1%", false), tbl.Rows[0], ctx)
	if err == nil {
		t.Fatal("LIKE on int column should error")
	}
	if !strings.Contains(err.Error(), "string columns") {
		t.Fatalf("error should explain why: %v", err)
	}
}

func TestMatchesIn(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	cases := []struct {
		name    string
		pred    parse.Predicate
		wantIDs []int64
	}{
		{"int in", in("Priority", []parse.Value{vnum("1"), vnum("5")}, false), []int64{2, 3}},
		{"string in", in("Status", []parse.Value{vstr("Open"), vstr("Done")}, false), []int64{1, 2, 3, 4}},
		{"single value", in("Status", []parse.Value{vstr("Done")}, false), []int64{2}},
		{"no match", in("Status", []parse.Value{vstr("Archived")}, false), []int64{}},
		{"not in", in("Status", []parse.Value{vstr("Done")}, true), []int64{1, 3, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tc.pred)
			if !equalIDs(ids, tc.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestMatchesInExcludesNullCell(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	// cell.Row 4 has Priority=NULL. IN with any list should not pick it up.
	ids := matchingIDs(t, tbl, ctx, in("Priority", []parse.Value{vnum("3")}, false))
	for _, id := range ids {
		if id == 4 {
			t.Fatal("row 4 (Priority=NULL) leaked into IN result")
		}
	}
	// NOT IN also excludes (UNKNOWN stays UNKNOWN).
	ids = matchingIDs(t, tbl, ctx, in("Priority", []parse.Value{vnum("99")}, true))
	for _, id := range ids {
		if id == 4 {
			t.Fatal("row 4 (Priority=NULL) leaked into NOT IN result")
		}
	}
}

func TestMatchesBetween(t *testing.T) {
	tbl := fixtureTable()
	ctx := NewEvalContext(tbl)
	cases := []struct {
		name    string
		pred    parse.Predicate
		wantIDs []int64
	}{
		{"int between inclusive", between("Priority", vnum("1"), vnum("3"), false), []int64{1, 2}},
		{"int between single match", between("Priority", vnum("5"), vnum("5"), false), []int64{3}},
		{"int between no match", between("Priority", vnum("10"), vnum("20"), false), []int64{}},
		{"int not between", between("Priority", vnum("2"), vnum("4"), true), []int64{2, 3}},
		{"date between", between("Modified", vstr("2024-01-01"), vstr("2024-06-30"), false), []int64{1, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := matchingIDs(t, tbl, ctx, tc.pred)
			if !equalIDs(ids, tc.wantIDs) {
				t.Fatalf("got %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestLikeMatcher(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"abc", "abc", true},
		{"abc", "abcd", false},
		{"a%", "abc", true},
		{"a%", "a", true},
		{"%c", "abc", true},
		{"%b%", "abc", true},
		{"_b_", "abc", true},
		{"_b_", "ab", false},
		{`\%`, "%", true},
		{`\_`, "_", true},
		{`\%`, "x", false},
		{"a%c", "abbbbc", true},
		{"a%c", "ac", true},
		{"a%c", "ab", false},
		{"", "", true},
		{"%", "", true},
		{"%", "anything", true},
	}
	for _, tc := range cases {
		got := likeMatch(tc.pattern, tc.s)
		if got != tc.want {
			t.Errorf("likeMatch(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

func TestValidatePredicate(t *testing.T) {
	tbl := fixtureTable()
	err := ValidatePredicate(cmp("Nope", "=", vstr("x")), tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error should mention column name: %v", err)
	}

	err = ValidatePredicate(isnull("Nope", false), tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column in IS NULL")
	}

	err = ValidatePredicate(and(cmp("Status", "=", vstr("Open")), cmp("Nope", "=", vstr("x"))), tbl.Schema)
	if err == nil {
		t.Fatal("expected error for unknown column nested in AND")
	}

	// Valid predicate passes.
	if err := ValidatePredicate(cmp("Status", "=", vstr("Open")), tbl.Schema); err != nil {
		t.Fatalf("valid predicate should pass: %v", err)
	}
}

func TestTriLogic(t *testing.T) {
	// Spot-check the truth tables — these power three-valued WHERE semantics.
	tests := []struct {
		name string
		got  triVal
		want triVal
	}{
		{"T AND T", triAnd(triTrue, triTrue), triTrue},
		{"T AND F", triAnd(triTrue, triFalse), triFalse},
		{"T AND U", triAnd(triTrue, triUnknown), triUnknown},
		{"F AND U", triAnd(triFalse, triUnknown), triFalse},
		{"U AND U", triAnd(triUnknown, triUnknown), triUnknown},
		{"T OR F", triOr(triTrue, triFalse), triTrue},
		{"F OR U", triOr(triFalse, triUnknown), triUnknown},
		{"T OR U", triOr(triTrue, triUnknown), triTrue},
		{"NOT T", triNot(triTrue), triFalse},
		{"NOT F", triNot(triFalse), triTrue},
		{"NOT U", triNot(triUnknown), triUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func matchingIDs(t *testing.T, tbl *cell.Table, ctx *EvalContext, pred parse.Predicate) []int64 {
	t.Helper()
	var out []int64
	for _, row := range tbl.Rows {
		ok, err := Matches(pred, row, ctx)
		if err != nil {
			t.Fatalf("Matches: %v", err)
		}
		if ok {
			out = append(out, row[0].Int)
		}
	}
	return out
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
