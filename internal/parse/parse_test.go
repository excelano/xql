package parse

import (
	"reflect"
	"strings"
	"testing"
)

func vstr(s string) Value { return Value{Kind: ValString, Str: s} }
func vnum(n string) Value { return Value{Kind: ValNumber, Num: n} }
func vbool(b bool) Value  { return Value{Kind: ValBool, Bool: b} }
func vnull() Value        { return Value{Kind: ValNull} }
func cmp(c, op string, v Value) *Comparison {
	return &Comparison{LExpr: &ColumnExpr{Name: c}, Op: op, Value: v}
}
func cmpE(lhs Expr, op string, v Value) *Comparison {
	return &Comparison{LExpr: lhs, Op: op, Value: v}
}
func isnull(c string, not bool) *NullTest   { return &NullTest{Column: c, Not: not} }
func and(l, r Predicate) *BinaryOp          { return &BinaryOp{Op: "AND", L: l, R: r} }
func or(l, r Predicate) *BinaryOp           { return &BinaryOp{Op: "OR", L: l, R: r} }
func not(p Predicate) *NotOp                { return &NotOp{Inner: p} }
func iptr(n int) *int                       { return &n }
func asc(c string) OrderKey                 { return OrderKey{Column: c} }
func desc(c string) OrderKey                { return OrderKey{Column: c, Desc: true} }
func like(c, p string, n bool) *LikeOp      { return &LikeOp{Column: c, Pattern: p, Not: n} }
func ilike(c, p string, n bool) *LikeOp     { return &LikeOp{Column: c, Pattern: p, Not: n, Insensitive: true} }
func in(c string, vs []Value, n bool) *InOp { return &InOp{Column: c, Values: vs, Not: n} }
func between(c string, lo, hi Value, n bool) *BetweenOp {
	return &BetweenOp{Column: c, Low: lo, High: hi, Not: n}
}

// v2 expression helpers.
func cols(names ...string) []Projection {
	ps := make([]Projection, len(names))
	for i, n := range names {
		ps[i] = Projection{Expr: &ColumnExpr{Name: n}}
	}
	return ps
}
func colE(name string) Expr                  { return &ColumnExpr{Name: name} }
func litE(v Value) Expr                      { return &LiteralExpr{Value: v} }
func binE(op string, l, r Expr) Expr         { return &BinaryExpr{Op: op, L: l, R: r} }
func aggE(fn string, arg Expr) Expr          { return &AggregateExpr{Func: fn, Arg: arg} }
func aggStar() Expr                          { return &AggregateExpr{Func: "COUNT", Star: true} }
func proj(e Expr) Projection                 { return Projection{Expr: e} }
func projAs(e Expr, alias string) Projection { return Projection{Expr: e, Alias: alias} }

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Stmt
		wantErr string
	}{
		// SELECT projections
		{
			name:  "select star",
			input: "SELECT *",
			want:  &SelectStmt{Star: true},
		},
		{
			name:  "select star lowercase",
			input: "select *",
			want:  &SelectStmt{Star: true},
		},
		{
			name:  "select single column",
			input: "SELECT Title",
			want:  &SelectStmt{Columns: cols("Title")},
		},
		{
			name:  "select multiple columns",
			input: "SELECT Title, Status, Priority",
			want:  &SelectStmt{Columns: cols("Title", "Status", "Priority")},
		},
		{
			name:  "select with quoted identifier",
			input: `SELECT Title, "Created Date"`,
			want:  &SelectStmt{Columns: cols("Title", "Created Date")},
		},
		{
			name:  "select with escaped quote in identifier",
			input: `SELECT "He said ""hi"""`,
			want:  &SelectStmt{Columns: cols(`He said "hi"`)},
		},

		// DISTINCT
		{
			name:  "select distinct star",
			input: "SELECT DISTINCT *",
			want:  &SelectStmt{Distinct: true, Star: true},
		},
		{
			name:  "select distinct single column",
			input: "SELECT DISTINCT Status",
			want:  &SelectStmt{Distinct: true, Columns: cols("Status")},
		},
		{
			name:  "select distinct multiple columns",
			input: "SELECT DISTINCT Status, Priority",
			want:  &SelectStmt{Distinct: true, Columns: cols("Status", "Priority")},
		},
		{
			name:  "select distinct lowercase",
			input: "select distinct status",
			want:  &SelectStmt{Distinct: true, Columns: cols("status")},
		},
		{
			name:  "select distinct with where",
			input: "SELECT DISTINCT Status WHERE Priority > 2",
			want: &SelectStmt{
				Distinct: true,
				Columns: cols("Status"),
				Where:    cmp("Priority", ">", vnum("2")),
			},
		},

		// ORDER BY / LIMIT / OFFSET
		{
			name:  "order by single column default asc",
			input: "SELECT * ORDER BY Title",
			want:  &SelectStmt{Star: true, OrderBy: []OrderKey{asc("Title")}},
		},
		{
			name:  "order by explicit asc",
			input: "SELECT * ORDER BY Title ASC",
			want:  &SelectStmt{Star: true, OrderBy: []OrderKey{asc("Title")}},
		},
		{
			name:  "order by desc",
			input: "SELECT * ORDER BY Priority DESC",
			want:  &SelectStmt{Star: true, OrderBy: []OrderKey{desc("Priority")}},
		},
		{
			name:  "order by multiple keys mixed direction",
			input: "SELECT * ORDER BY Status ASC, Priority DESC",
			want:  &SelectStmt{Star: true, OrderBy: []OrderKey{asc("Status"), desc("Priority")}},
		},
		{
			name:  "order by lowercase keywords",
			input: "select * order by title desc",
			want:  &SelectStmt{Star: true, OrderBy: []OrderKey{desc("title")}},
		},
		{
			name:  "limit only",
			input: "SELECT * LIMIT 10",
			want:  &SelectStmt{Star: true, Limit: iptr(10)},
		},
		{
			name:  "offset only",
			input: "SELECT * OFFSET 5",
			want:  &SelectStmt{Star: true, Offset: iptr(5)},
		},
		{
			name:  "limit and offset",
			input: "SELECT * LIMIT 10 OFFSET 5",
			want:  &SelectStmt{Star: true, Limit: iptr(10), Offset: iptr(5)},
		},
		{
			name:  "all clauses combined",
			input: "SELECT DISTINCT Status WHERE Priority > 2 ORDER BY Status DESC LIMIT 3 OFFSET 1",
			want: &SelectStmt{
				Distinct: true,
				Columns: cols("Status"),
				Where:    cmp("Priority", ">", vnum("2")),
				OrderBy:  []OrderKey{desc("Status")},
				Limit:    iptr(3),
				Offset:   iptr(1),
			},
		},
		{
			name:    "order missing by",
			input:   "SELECT * ORDER Title",
			wantErr: "BY",
		},
		{
			name:    "limit rejects negative",
			input:   "SELECT * LIMIT -1",
			wantErr: "non-negative",
		},
		{
			name:    "limit rejects float",
			input:   "SELECT * LIMIT 1.5",
			wantErr: "integer",
		},
		{
			name:    "offset rejects float",
			input:   "SELECT * OFFSET 0.5",
			wantErr: "integer",
		},

		// LIKE
		{
			name:  "like prefix",
			input: "SELECT * WHERE Title LIKE 'foo%'",
			want:  &SelectStmt{Star: true, Where: like("Title", "foo%", false)},
		},
		{
			name:  "like with underscore wildcard",
			input: "SELECT * WHERE Title LIKE 'a_b'",
			want:  &SelectStmt{Star: true, Where: like("Title", "a_b", false)},
		},
		{
			name:  "not like",
			input: "SELECT * WHERE Title NOT LIKE '%spam%'",
			want:  &SelectStmt{Star: true, Where: like("Title", "%spam%", true)},
		},
		{
			name:    "like rejects bare number",
			input:   "SELECT * WHERE Title LIKE 42",
			wantErr: "string pattern",
		},

		// ILIKE
		{
			name:  "ilike prefix",
			input: "SELECT * WHERE Title ILIKE 'Foo%'",
			want:  &SelectStmt{Star: true, Where: ilike("Title", "Foo%", false)},
		},
		{
			name:  "not ilike",
			input: "SELECT * WHERE Title NOT ILIKE '%draft%'",
			want:  &SelectStmt{Star: true, Where: ilike("Title", "%draft%", true)},
		},

		// SQL comments
		{
			name:  "line comment at end",
			input: "SELECT * WHERE Status = 'Open' -- old draft",
			want:  &SelectStmt{Star: true, Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "line comment in middle",
			input: "SELECT *\n-- the filter follows\nWHERE Status = 'Open'",
			want:  &SelectStmt{Star: true, Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "block comment in middle",
			input: "SELECT * /* historical: was status_v2 */ WHERE Status = 'Open'",
			want:  &SelectStmt{Star: true, Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "block comment spanning lines",
			input: "SELECT *\n/*\n  this query exists\n  because of ticket #42\n*/\nWHERE Status = 'Open'",
			want:  &SelectStmt{Star: true, Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "comment before SELECT",
			input: "-- some preamble\nSELECT * WHERE Status = 'Open'",
			want:  &SelectStmt{Star: true, Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "string literal containing comment-like text",
			input: "SELECT * WHERE Title = '-- not a comment'",
			want:  &SelectStmt{Star: true, Where: cmp("Title", "=", vstr("-- not a comment"))},
		},

		// IN
		{
			name: "in single value",
			input: "SELECT * WHERE Status IN ('Open')",
			want: &SelectStmt{Star: true, Where: in("Status", []Value{vstr("Open")}, false)},
		},
		{
			name: "in multiple values",
			input: "SELECT * WHERE Status IN ('Open', 'In Progress', 'Done')",
			want: &SelectStmt{Star: true, Where: in("Status",
				[]Value{vstr("Open"), vstr("In Progress"), vstr("Done")}, false)},
		},
		{
			name: "in numbers",
			input: "SELECT * WHERE Priority IN (1, 2, 3)",
			want: &SelectStmt{Star: true, Where: in("Priority",
				[]Value{vnum("1"), vnum("2"), vnum("3")}, false)},
		},
		{
			name: "not in",
			input: "SELECT * WHERE Status NOT IN ('Archived', 'Cancelled')",
			want: &SelectStmt{Star: true, Where: in("Status",
				[]Value{vstr("Archived"), vstr("Cancelled")}, true)},
		},
		{
			name:    "in rejects empty list",
			input:   "SELECT * WHERE Status IN ()",
			wantErr: "at least one value",
		},

		// BETWEEN
		{
			name:  "between numbers",
			input: "SELECT * WHERE Priority BETWEEN 1 AND 5",
			want:  &SelectStmt{Star: true, Where: between("Priority", vnum("1"), vnum("5"), false)},
		},
		{
			name:  "not between",
			input: "SELECT * WHERE Priority NOT BETWEEN 1 AND 3",
			want:  &SelectStmt{Star: true, Where: between("Priority", vnum("1"), vnum("3"), true)},
		},
		{
			name:  "between dates",
			input: "SELECT * WHERE Modified BETWEEN '2024-01-01' AND '2024-12-31'",
			want:  &SelectStmt{Star: true, Where: between("Modified", vstr("2024-01-01"), vstr("2024-12-31"), false)},
		},
		{
			name:    "between requires AND",
			input:   "SELECT * WHERE Priority BETWEEN 1, 5",
			wantErr: "AND",
		},
		{
			name:    "between rejects null bound",
			input:   "SELECT * WHERE Priority BETWEEN NULL AND 5",
			wantErr: "NULL",
		},
		{
			name:    "postfix NOT requires LIKE/IN/BETWEEN",
			input:   "SELECT * WHERE Title NOT = 'foo'",
			wantErr: "NOT",
		},

		// Combinations with AND/OR
		{
			name: "between inside and",
			input: "SELECT * WHERE Priority BETWEEN 1 AND 5 AND Status = 'Open'",
			want: &SelectStmt{Star: true, Where: and(
				between("Priority", vnum("1"), vnum("5"), false),
				cmp("Status", "=", vstr("Open")),
			)},
		},
		{
			name: "like and in",
			input: "SELECT * WHERE Title LIKE 'Fix%' AND Status IN ('Open', 'Done')",
			want: &SelectStmt{Star: true, Where: and(
				like("Title", "Fix%", false),
				in("Status", []Value{vstr("Open"), vstr("Done")}, false),
			)},
		},

		// SELECT with WHERE: comparison operators
		{
			name:  "select where equals",
			input: "SELECT Title WHERE Status = 'Open'",
			want:  &SelectStmt{Columns: cols("Title"), Where: cmp("Status", "=", vstr("Open"))},
		},
		{
			name:  "select where not equals",
			input: "SELECT Title WHERE Status != 'Open'",
			want:  &SelectStmt{Columns: cols("Title"), Where: cmp("Status", "!=", vstr("Open"))},
		},
		{
			name:  "select where less than",
			input: "SELECT Title WHERE Priority < 3",
			want:  &SelectStmt{Columns: cols("Title"), Where: cmp("Priority", "<", vnum("3"))},
		},
		{
			name:  "select where less or equal",
			input: "SELECT Title WHERE Priority <= 3",
			want:  &SelectStmt{Columns: cols("Title"), Where: cmp("Priority", "<=", vnum("3"))},
		},
		{
			name:  "select where greater than",
			input: "SELECT Title WHERE Priority > 3",
			want:  &SelectStmt{Columns: cols("Title"), Where: cmp("Priority", ">", vnum("3"))},
		},
		{
			name:  "select where greater or equal",
			input: "SELECT Title WHERE Priority >= 3",
			want:  &SelectStmt{Columns: cols("Title"), Where: cmp("Priority", ">=", vnum("3"))},
		},

		// Value kinds
		{
			name:  "negative number",
			input: "SELECT * WHERE Balance = -1.5",
			want:  &SelectStmt{Star: true, Where: cmp("Balance", "=", vnum("-1.5"))},
		},
		{
			name:  "bool true",
			input: "SELECT * WHERE Archived = TRUE",
			want:  &SelectStmt{Star: true, Where: cmp("Archived", "=", vbool(true))},
		},
		{
			name:  "bool false lowercase",
			input: "SELECT * WHERE Archived = false",
			want:  &SelectStmt{Star: true, Where: cmp("Archived", "=", vbool(false))},
		},
		{
			name:  "string with escaped quote",
			input: "SELECT * WHERE Name = 'O''Brien'",
			want:  &SelectStmt{Star: true, Where: cmp("Name", "=", vstr("O'Brien"))},
		},

		// IS NULL / IS NOT NULL
		{
			name:  "is null",
			input: "SELECT * WHERE DueDate IS NULL",
			want:  &SelectStmt{Star: true, Where: isnull("DueDate", false)},
		},
		{
			name:  "is not null",
			input: "SELECT * WHERE DueDate IS NOT NULL",
			want:  &SelectStmt{Star: true, Where: isnull("DueDate", true)},
		},

		// AND / OR / NOT / parens / precedence
		{
			name:  "and",
			input: "SELECT * WHERE A = 1 AND B = 2",
			want:  &SelectStmt{Star: true, Where: and(cmp("A", "=", vnum("1")), cmp("B", "=", vnum("2")))},
		},
		{
			name:  "or",
			input: "SELECT * WHERE A = 1 OR B = 2",
			want:  &SelectStmt{Star: true, Where: or(cmp("A", "=", vnum("1")), cmp("B", "=", vnum("2")))},
		},
		{
			name:  "not",
			input: "SELECT * WHERE NOT Archived = TRUE",
			want:  &SelectStmt{Star: true, Where: not(cmp("Archived", "=", vbool(true)))},
		},
		{
			name:  "and binds tighter than or",
			input: "SELECT * WHERE A = 1 OR B = 2 AND C = 3",
			want: &SelectStmt{Star: true, Where: or(
				cmp("A", "=", vnum("1")),
				and(cmp("B", "=", vnum("2")), cmp("C", "=", vnum("3"))),
			)},
		},
		{
			name:  "parens override precedence",
			input: "SELECT * WHERE (A = 1 OR B = 2) AND C = 3",
			want: &SelectStmt{Star: true, Where: and(
				or(cmp("A", "=", vnum("1")), cmp("B", "=", vnum("2"))),
				cmp("C", "=", vnum("3")),
			)},
		},
		{
			name:  "double not",
			input: "SELECT * WHERE NOT NOT A = 1",
			want:  &SelectStmt{Star: true, Where: not(not(cmp("A", "=", vnum("1"))))},
		},

		// UPDATE
		{
			name:  "update single assignment",
			input: "UPDATE SET Status = 'Done'",
			want: &UpdateStmt{
				Assignments: []Assignment{{Column: "Status", Value: litE(vstr("Done"))}},
			},
		},
		{
			name:  "update multiple assignments with where",
			input: "UPDATE SET Status = 'Done', Priority = 1 WHERE ID = 42",
			want: &UpdateStmt{
				Assignments: []Assignment{
					{Column: "Status", Value: litE(vstr("Done"))},
					{Column: "Priority", Value: litE(vnum("1"))},
				},
				Where: cmp("ID", "=", vnum("42")),
			},
		},

		// DELETE
		{
			name:  "delete bare",
			input: "DELETE",
			want:  &DeleteStmt{},
		},
		{
			name:  "delete with where",
			input: "DELETE WHERE Status = 'Archived'",
			want:  &DeleteStmt{Where: cmp("Status", "=", vstr("Archived"))},
		},

		// INSERT
		{
			name:  "insert single column",
			input: "INSERT (Title) VALUES ('New')",
			want: &InsertStmt{
				Columns: []string{"Title"},
				Values:  []Value{vstr("New")},
			},
		},
		{
			name:  "insert multiple columns",
			input: "INSERT (Title, Status, Priority) VALUES ('Migration', 'Open', 3)",
			want: &InsertStmt{
				Columns: []string{"Title", "Status", "Priority"},
				Values:  []Value{vstr("Migration"), vstr("Open"), vnum("3")},
			},
		},
		{
			name:  "insert with null value",
			input: "INSERT (Title, DueDate) VALUES ('Task', NULL)",
			want: &InsertStmt{
				Columns: []string{"Title", "DueDate"},
				Values:  []Value{vstr("Task"), vnull()},
			},
		},

		// Mixed case keywords
		{
			name:  "mixed case keywords",
			input: "Select Title Where Status = 'Open' And Priority > 2",
			want: &SelectStmt{
				Columns: cols("Title"),
				Where:   and(cmp("Status", "=", vstr("Open")), cmp("Priority", ">", vnum("2"))),
			},
		},

		// Whitespace tolerance
		{
			name:  "whitespace tolerant",
			input: "  SELECT   Title   WHERE   Status='Open'  ",
			want: &SelectStmt{
				Columns: cols("Title"),
				Where:   cmp("Status", "=", vstr("Open")),
			},
		},

		// Negative cases
		{
			name:    "empty input",
			input:   "",
			wantErr: "empty input",
		},
		{
			name:    "select with no projection",
			input:   "SELECT",
			wantErr: "expected expression",
		},
		{
			name:    "select with from",
			input:   "SELECT * FROM Foo",
			wantErr: "unexpected",
		},
		{
			name:    "select with trailing comma",
			input:   "SELECT Title,",
			wantErr: "expected expression",
		},
		{
			name:    "where with no predicate",
			input:   "SELECT * WHERE",
			wantErr: "expected expression",
		},
		{
			name:    "comparison missing value",
			input:   "SELECT * WHERE A =",
			wantErr: "expected literal value",
		},
		{
			name:    "RHS column not allowed",
			input:   "SELECT * WHERE A = B",
			wantErr: "expected literal value",
		},
		{
			name:    "equals null is rejected",
			input:   "SELECT * WHERE A = NULL",
			wantErr: "use IS NULL",
		},
		{
			name:    "not equals null is rejected",
			input:   "SELECT * WHERE A != NULL",
			wantErr: "use IS NULL",
		},
		{
			name:    "trailing junk after statement",
			input:   "SELECT Title WHERE A = 1 EXTRA",
			wantErr: "unexpected",
		},
		{
			name:    "is followed by non-null",
			input:   "SELECT * WHERE A IS 1",
			wantErr: "expected NULL",
		},
		{
			name:    "is not followed by non-null",
			input:   "SELECT * WHERE A IS NOT 1",
			wantErr: "expected NULL",
		},
		{
			name:    "update without set",
			input:   "UPDATE Status = 'Done'",
			wantErr: "expected SET",
		},
		{
			name:    "insert without column list",
			input:   "INSERT VALUES ('A')",
			wantErr: "expected '('",
		},
		{
			name:    "insert missing values keyword",
			input:   "INSERT (A) ('B')",
			wantErr: "expected VALUES",
		},
		{
			name:    "insert missing values paren",
			input:   "INSERT (A) VALUES",
			wantErr: "expected '('",
		},
		{
			name:    "unterminated string",
			input:   "SELECT * WHERE A = 'unfinished",
			wantErr: "unterminated string",
		},
		{
			name:    "unterminated quoted ident",
			input:   `SELECT "unfinished`,
			wantErr: "unterminated quoted identifier",
		},
		{
			name:    "bang without equals",
			input:   "SELECT * WHERE A ! 1",
			wantErr: "expected '=' after '!'",
		},
		{
			name:    "negative without digit",
			input:   "SELECT * WHERE A = -",
			wantErr: "expected number after '-'",
		},
		{
			name:    "decimal without digit",
			input:   "SELECT * WHERE A = 1.",
			wantErr: "expected digit after '.'",
		},

		// v2: AS alias
		{
			name:  "select column with alias",
			input: "SELECT Title AS t",
			want:  &SelectStmt{Columns: []Projection{projAs(colE("Title"), "t")}},
		},
		{
			name:  "select multiple aliased columns",
			input: "SELECT Title AS t, Status AS s",
			want: &SelectStmt{Columns: []Projection{
				projAs(colE("Title"), "t"),
				projAs(colE("Status"), "s"),
			}},
		},
		{
			name:  "select mixed aliased and bare",
			input: "SELECT Title AS t, Status",
			want: &SelectStmt{Columns: []Projection{
				projAs(colE("Title"), "t"),
				proj(colE("Status")),
			}},
		},
		{
			name:    "alias requires identifier",
			input:   "SELECT Title AS 42",
			wantErr: "alias name",
		},

		// v2: arithmetic in projections
		{
			name:  "select arithmetic add",
			input: "SELECT price + tax",
			want: &SelectStmt{Columns: []Projection{
				proj(binE("+", colE("price"), colE("tax"))),
			}},
		},
		{
			name:  "select arithmetic multiply",
			input: "SELECT price * qty",
			want: &SelectStmt{Columns: []Projection{
				proj(binE("*", colE("price"), colE("qty"))),
			}},
		},
		{
			name:  "select arithmetic precedence",
			input: "SELECT a + b * c",
			want: &SelectStmt{Columns: []Projection{
				proj(binE("+", colE("a"), binE("*", colE("b"), colE("c")))),
			}},
		},
		{
			name:  "select arithmetic parens override precedence",
			input: "SELECT (a + b) * c",
			want: &SelectStmt{Columns: []Projection{
				proj(binE("*", binE("+", colE("a"), colE("b")), colE("c"))),
			}},
		},
		{
			name:  "select arithmetic with literal",
			input: "SELECT price * 2 AS doubled",
			want: &SelectStmt{Columns: []Projection{
				projAs(binE("*", colE("price"), litE(vnum("2"))), "doubled"),
			}},
		},

		// v2: aggregates
		{
			name:  "select count star",
			input: "SELECT COUNT(*)",
			want:  &SelectStmt{Columns: []Projection{proj(aggStar())}},
		},
		{
			name:  "select count star with alias",
			input: "SELECT COUNT(*) AS n",
			want:  &SelectStmt{Columns: []Projection{projAs(aggStar(), "n")}},
		},
		{
			name:  "select sum column",
			input: "SELECT SUM(price)",
			want:  &SelectStmt{Columns: []Projection{proj(aggE("SUM", colE("price")))}},
		},
		{
			name:  "select avg expression",
			input: "SELECT AVG(price * qty)",
			want: &SelectStmt{Columns: []Projection{
				proj(aggE("AVG", binE("*", colE("price"), colE("qty")))),
			}},
		},
		{
			name:  "select multiple aggregates",
			input: "SELECT COUNT(*), MIN(price), MAX(price)",
			want: &SelectStmt{Columns: []Projection{
				proj(aggStar()),
				proj(aggE("MIN", colE("price"))),
				proj(aggE("MAX", colE("price"))),
			}},
		},
		{
			name:  "aggregate names are case insensitive",
			input: "SELECT count(*), sum(price)",
			want: &SelectStmt{Columns: []Projection{
				proj(aggStar()),
				proj(aggE("SUM", colE("price"))),
			}},
		},
		{
			name:    "non-count aggregate rejects star",
			input:   "SELECT SUM(*)",
			wantErr: "only COUNT accepts '*'",
		},

		// v2: GROUP BY
		{
			name:  "group by single column",
			input: "SELECT Status, COUNT(*) GROUP BY Status",
			want: &SelectStmt{
				Columns: []Projection{
					proj(colE("Status")),
					proj(aggStar()),
				},
				GroupBy: []Expr{&ColumnExpr{Name: "Status"}},
			},
		},
		{
			name:  "group by multiple columns",
			input: "SELECT Status, Priority, COUNT(*) GROUP BY Status, Priority",
			want: &SelectStmt{
				Columns: []Projection{
					proj(colE("Status")),
					proj(colE("Priority")),
					proj(aggStar()),
				},
				GroupBy: []Expr{&ColumnExpr{Name: "Status"}, &ColumnExpr{Name: "Priority"}},
			},
		},
		{
			name:    "group missing by",
			input:   "SELECT Status, COUNT(*) GROUP Status",
			wantErr: "BY",
		},

		// v2: HAVING
		{
			name:  "having on aggregate",
			input: "SELECT Status, COUNT(*) AS n GROUP BY Status HAVING COUNT(*) > 5",
			want: &SelectStmt{
				Columns: []Projection{
					proj(colE("Status")),
					projAs(aggStar(), "n"),
				},
				GroupBy: []Expr{&ColumnExpr{Name: "Status"}},
				Having:  cmpE(aggStar(), ">", vnum("5")),
			},
		},
		{
			name:  "having on sum",
			input: "SELECT Status GROUP BY Status HAVING SUM(price) > 100",
			want: &SelectStmt{
				Columns: []Projection{proj(colE("Status"))},
				GroupBy: []Expr{&ColumnExpr{Name: "Status"}},
				Having:  cmpE(aggE("SUM", colE("price")), ">", vnum("100")),
			},
		},

		// v2: expression on WHERE LHS (path B unification)
		{
			name:  "where arithmetic on lhs",
			input: "SELECT * WHERE price * qty > 100",
			want: &SelectStmt{
				Star:  true,
				Where: cmpE(binE("*", colE("price"), colE("qty")), ">", vnum("100")),
			},
		},

		// v2: UPDATE with computed RHS
		{
			name:  "update with arithmetic rhs",
			input: "UPDATE SET counter = counter + 1",
			want: &UpdateStmt{
				Assignments: []Assignment{
					{Column: "counter", Value: binE("+", colE("counter"), litE(vnum("1")))},
				},
			},
		},

		// v2: clause ordering
		{
			name:  "all v2 clauses combined",
			input: "SELECT Status, COUNT(*) AS n WHERE Priority > 1 GROUP BY Status HAVING COUNT(*) > 2 ORDER BY n DESC LIMIT 10",
			want: &SelectStmt{
				Columns: []Projection{
					proj(colE("Status")),
					projAs(aggStar(), "n"),
				},
				Where:   cmp("Priority", ">", vnum("1")),
				GroupBy: []Expr{&ColumnExpr{Name: "Status"}},
				Having:  cmpE(aggStar(), ">", vnum("2")),
				OrderBy: []OrderKey{desc("n")},
				Limit:   iptr(10),
			},
		},

		// v2: MIN / MAX usable as column names
		{
			name:  "min as column name (no parens)",
			input: "SELECT min, max",
			want: &SelectStmt{Columns: []Projection{
				proj(colE("min")),
				proj(colE("max")),
			}},
		},

		// scalar functions: LOWER / UPPER / TRIM in projections
		{
			name:  "select lower of column",
			input: "SELECT LOWER(name)",
			want: &SelectStmt{Columns: []Projection{
				proj(&FuncCallExpr{Name: "LOWER", Args: []Expr{colE("name")}}),
			}},
		},
		{
			name:  "select upper of column",
			input: "SELECT UPPER(name) AS shout",
			want: &SelectStmt{Columns: []Projection{
				projAs(&FuncCallExpr{Name: "UPPER", Args: []Expr{colE("name")}}, "shout"),
			}},
		},
		{
			name:  "select trim of column",
			input: "SELECT TRIM(name)",
			want: &SelectStmt{Columns: []Projection{
				proj(&FuncCallExpr{Name: "TRIM", Args: []Expr{colE("name")}}),
			}},
		},
		{
			name:  "scalar function names are case insensitive",
			input: "SELECT lower(x), Upper(y), tRiM(z)",
			want: &SelectStmt{Columns: []Projection{
				proj(&FuncCallExpr{Name: "LOWER", Args: []Expr{colE("x")}}),
				proj(&FuncCallExpr{Name: "UPPER", Args: []Expr{colE("y")}}),
				proj(&FuncCallExpr{Name: "TRIM", Args: []Expr{colE("z")}}),
			}},
		},
		{
			name:  "unknown-named function still parses",
			input: "SELECT foo(x)",
			want: &SelectStmt{Columns: []Projection{
				proj(&FuncCallExpr{Name: "FOO", Args: []Expr{colE("x")}}),
			}},
		},
		{
			name:    "unterminated function call",
			input:   "SELECT LOWER(x",
			wantErr: "')'",
		},

		// scalar functions in GROUP BY
		{
			name:  "group by lower(column)",
			input: "SELECT LOWER(app_name) AS k, COUNT(*) AS n GROUP BY LOWER(app_name)",
			want: &SelectStmt{
				Columns: []Projection{
					projAs(&FuncCallExpr{Name: "LOWER", Args: []Expr{colE("app_name")}}, "k"),
					projAs(aggStar(), "n"),
				},
				GroupBy: []Expr{&FuncCallExpr{Name: "LOWER", Args: []Expr{colE("app_name")}}},
			},
		},
		{
			name:  "group by mixed column and expression",
			input: "SELECT Status, LOWER(app_name) GROUP BY Status, LOWER(app_name)",
			want: &SelectStmt{
				Columns: []Projection{
					proj(colE("Status")),
					proj(&FuncCallExpr{Name: "LOWER", Args: []Expr{colE("app_name")}}),
				},
				GroupBy: []Expr{
					&ColumnExpr{Name: "Status"},
					&FuncCallExpr{Name: "LOWER", Args: []Expr{colE("app_name")}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (parsed: %#v)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AST mismatch.\n got:  %#v\n want: %#v", got, tt.want)
			}
		})
	}
}

func TestPreProcess(t *testing.T) {
	tests := []struct {
		input      string
		wantClean  string
		wantCommit bool
	}{
		{"SELECT *", "SELECT *", false},
		{"SELECT *;", "SELECT *", false},
		{"SELECT *  ;  ", "SELECT *", false},
		{"DELETE WHERE A = 1 !", "DELETE WHERE A = 1", true},
		{"DELETE WHERE A = 1!", "DELETE WHERE A = 1", true},
		{"DELETE WHERE A = 1 !;", "DELETE WHERE A = 1", true},
		{"DELETE WHERE A = 1 ;!", "DELETE WHERE A = 1", true},
		{"   ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, commit := PreProcess(tt.input)
			if got != tt.wantClean {
				t.Errorf("cleaned: got %q, want %q", got, tt.wantClean)
			}
			if commit != tt.wantCommit {
				t.Errorf("commit: got %v, want %v", commit, tt.wantCommit)
			}
		})
	}
}
