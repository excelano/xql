package sp

import (
	"strings"
	"testing"

	"github.com/excelano/xql/internal/parse"
)

func testSchema() map[string]FieldInfo {
	return map[string]FieldInfo{
		"Title":    {Name: "Title", Type: FieldText},
		"Status":   {Name: "Status", Type: FieldChoice},
		"Priority": {Name: "Priority", Type: FieldNumber},
		"Archived": {Name: "Archived", Type: FieldBoolean},
		"DueDate":  {Name: "DueDate", Type: FieldDateTime},
		"Modified": {Name: "Modified", Type: FieldDateTime},
	}
}

func TestToOData(t *testing.T) {
	tests := []struct {
		name    string
		pred    parse.Predicate
		want    string
		wantErr string
	}{
		// Operator mapping
		{
			name: "equals",
			pred: cmp("Status", "=", vstr("Open")),
			want: "fields/Status eq 'Open'",
		},
		{
			name: "not equals",
			pred: cmp("Status", "!=", vstr("Open")),
			want: "fields/Status ne 'Open'",
		},
		{
			name: "less than",
			pred: cmp("Priority", "<", vnum("3")),
			want: "fields/Priority lt 3",
		},
		{
			name: "less or equal",
			pred: cmp("Priority", "<=", vnum("3")),
			want: "fields/Priority le 3",
		},
		{
			name: "greater than",
			pred: cmp("Priority", ">", vnum("3")),
			want: "fields/Priority gt 3",
		},
		{
			name: "greater or equal",
			pred: cmp("Priority", ">=", vnum("3")),
			want: "fields/Priority ge 3",
		},

		// Value types
		{
			name: "string escapes single quotes",
			pred: cmp("Title", "=", vstr("O'Brien")),
			want: "fields/Title eq 'O''Brien'",
		},
		{
			name: "negative number",
			pred: cmp("Priority", "=", vnum("-1")),
			want: "fields/Priority eq -1",
		},
		{
			name: "decimal number",
			pred: cmp("Priority", "=", vnum("1.5")),
			want: "fields/Priority eq 1.5",
		},
		{
			name: "boolean true",
			pred: cmp("Archived", "=", vbool(true)),
			want: "fields/Archived eq true",
		},
		{
			name: "boolean false",
			pred: cmp("Archived", "=", vbool(false)),
			want: "fields/Archived eq false",
		},

		// DateTime normalization
		{
			name: "datetime full ISO preserved and quoted",
			pred: cmp("Modified", "<", vstr("2024-01-15T12:30:00Z")),
			want: "fields/Modified lt '2024-01-15T12:30:00Z'",
		},
		{
			name: "datetime date-only normalized to midnight UTC and quoted",
			pred: cmp("Modified", "<", vstr("2024-01-01")),
			want: "fields/Modified lt '2024-01-01T00:00:00Z'",
		},

		// IS NULL / IS NOT NULL
		{
			name: "is null",
			pred: isnull("DueDate", false),
			want: "fields/DueDate eq null",
		},
		{
			name: "is not null",
			pred: isnull("DueDate", true),
			want: "fields/DueDate ne null",
		},

		// Logical operators
		{
			name: "and",
			pred: and(cmp("Status", "=", vstr("Open")), cmp("Priority", ">", vnum("2"))),
			want: "(fields/Status eq 'Open' and fields/Priority gt 2)",
		},
		{
			name: "or",
			pred: or(cmp("Status", "=", vstr("Open")), cmp("Status", "=", vstr("Review"))),
			want: "(fields/Status eq 'Open' or fields/Status eq 'Review')",
		},
		{
			name: "not",
			pred: not(cmp("Archived", "=", vbool(true))),
			want: "not (fields/Archived eq true)",
		},
		{
			name: "combined with precedence parens",
			pred: and(
				or(cmp("Status", "=", vstr("Open")), cmp("Status", "=", vstr("Review"))),
				not(cmp("Archived", "=", vbool(true))),
			),
			want: "((fields/Status eq 'Open' or fields/Status eq 'Review') and not (fields/Archived eq true))",
		},
		{
			name: "and binds tighter than or per parser",
			pred: or(
				cmp("Status", "=", vstr("A")),
				and(cmp("Status", "=", vstr("B")), cmp("Priority", ">", vnum("1"))),
			),
			want: "(fields/Status eq 'A' or (fields/Status eq 'B' and fields/Priority gt 1))",
		},

		// LIKE
		{
			name: "like prefix translates to startswith",
			pred: like("Title", "Fix%", false),
			want: "startswith(fields/Title, 'Fix')",
		},
		{
			name: "like suffix translates to endswith",
			pred: like("Title", "%bug", false),
			want: "endswith(fields/Title, 'bug')",
		},
		{
			name: "like contains translates to contains",
			pred: like("Title", "%auth%", false),
			want: "contains(fields/Title, 'auth')",
		},
		{
			name: "not like wraps with not",
			pred: like("Title", "%spam%", true),
			want: "not (contains(fields/Title, 'spam'))",
		},
		{
			name: "like escapes single quotes in literal",
			pred: like("Title", "O'Brien%", false),
			want: "startswith(fields/Title, 'O''Brien')",
		},
		{
			name:    "like rejects mid-pattern percent",
			pred:    like("Title", "foo%bar", false),
			wantErr: "mid-pattern",
		},
		{
			name:    "like rejects underscore wildcard",
			pred:    like("Title", "a_b", false),
			wantErr: "_",
		},
		{
			name:    "like on number column",
			pred:    like("Priority", "1%", false),
			wantErr: "text columns",
		},

		// ILIKE: wraps field in tolower() and lowercases the literal
		{
			name: "ilike prefix",
			pred: ilike("Title", "Fix%", false),
			want: "startswith(tolower(fields/Title), 'fix')",
		},
		{
			name: "ilike suffix",
			pred: ilike("Title", "%BUG", false),
			want: "endswith(tolower(fields/Title), 'bug')",
		},
		{
			name: "ilike contains",
			pred: ilike("Title", "%Auth%", false),
			want: "contains(tolower(fields/Title), 'auth')",
		},
		{
			name: "not ilike",
			pred: ilike("Title", "%SPAM%", true),
			want: "not (contains(tolower(fields/Title), 'spam'))",
		},
		{
			name:    "ilike rejects underscore wildcard",
			pred:    ilike("Title", "a_b", false),
			wantErr: "ILIKE",
		},

		// IN
		{
			name: "in string list",
			pred: in("Status", []parse.Value{vstr("Open"), vstr("Done")}, false),
			want: "(fields/Status eq 'Open' or fields/Status eq 'Done')",
		},
		{
			name: "in numbers",
			pred: in("Priority", []parse.Value{vnum("1"), vnum("2"), vnum("3")}, false),
			want: "(fields/Priority eq 1 or fields/Priority eq 2 or fields/Priority eq 3)",
		},
		{
			name: "not in",
			pred: in("Status", []parse.Value{vstr("Archived")}, true),
			want: "not (fields/Status eq 'Archived')",
		},

		// BETWEEN
		{
			name: "between numbers",
			pred: between("Priority", vnum("1"), vnum("5"), false),
			want: "(fields/Priority ge 1 and fields/Priority le 5)",
		},
		{
			name: "not between",
			pred: between("Priority", vnum("2"), vnum("4"), true),
			want: "not (fields/Priority ge 2 and fields/Priority le 4)",
		},
		{
			name: "between dates normalized",
			pred: between("Modified", vstr("2024-01-01"), vstr("2024-12-31"), false),
			want: "(fields/Modified ge '2024-01-01T00:00:00Z' and fields/Modified le '2024-12-31T00:00:00Z')",
		},

		// Negative
		{
			name:    "unknown column in comparison",
			pred:    cmp("DoesNotExist", "=", vstr("x")),
			wantErr: `unknown column "DoesNotExist"`,
		},
		{
			name:    "unknown column in null test",
			pred:    isnull("DoesNotExist", false),
			wantErr: `unknown column "DoesNotExist"`,
		},
		{
			name:    "datetime malformed",
			pred:    cmp("Modified", "<", vstr("yesterday")),
			wantErr: "ISO 8601 datetime",
		},
		{
			name:    "arithmetic LHS rejected with slice pointer",
			pred:    cmpE(&parse.BinaryExpr{Op: "+", L: &parse.ColumnExpr{Name: "Priority"}, R: &parse.LiteralExpr{Value: vnum("1")}}, "=", vnum("3")),
			wantErr: "v1.1 slice",
		},
	}

	schema := testSchema()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToOData(tt.pred, schema)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (output: %q)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestToODataNil(t *testing.T) {
	got, err := ToOData(nil, testSchema())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("nil predicate should produce empty string, got %q", got)
	}
}

// Integration with parser: parsing then translating produces expected $filter.
func TestParseAndTranslate(t *testing.T) {
	schema := testSchema()
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "SELECT * WHERE Status = 'Open' AND Priority > 2",
			want:  "(fields/Status eq 'Open' and fields/Priority gt 2)",
		},
		{
			input: "SELECT * WHERE DueDate IS NULL",
			want:  "fields/DueDate eq null",
		},
		{
			input: "SELECT * WHERE Modified < '2024-01-01' AND NOT Archived = TRUE",
			want:  "(fields/Modified lt '2024-01-01T00:00:00Z' and not (fields/Archived eq true))",
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			stmt, err := parse.Parse(tt.input)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			sel, ok := stmt.(*parse.SelectStmt)
			if !ok {
				t.Fatalf("expected SelectStmt, got %T", stmt)
			}
			got, err := ToOData(sel.Where, schema)
			if err != nil {
				t.Fatalf("translate error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
