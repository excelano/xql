package cell

import (
	"strings"
	"testing"
	"time"

	"github.com/excelano/xql/internal/parse"
)

func vstr(s string) parse.Value { return parse.Value{Kind: parse.ValString, Str: s} }
func vnum(n string) parse.Value { return parse.Value{Kind: parse.ValNumber, Num: n} }
func vbool(b bool) parse.Value  { return parse.Value{Kind: parse.ValBool, Bool: b} }

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func signOf(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestParseColumnType(t *testing.T) {
	tests := []struct {
		in   string
		want ColumnType
		err  bool
	}{
		{"string", TypeString, false},
		{"str", TypeString, false},
		{"int", TypeInt, false},
		{"integer", TypeInt, false},
		{"float", TypeFloat, false},
		{"bool", TypeBool, false},
		{"date", TypeDate, false},
		{"datetime", TypeDate, false},
		{"INT", TypeInt, false},
		{"  bool  ", TypeBool, false},
		{"junk", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseColumnType(tt.in)
			if tt.err {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseCell(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		typ  ColumnType
		want Cell
	}{
		{"empty is null", "", TypeInt, Cell{Null: true}},
		{"whitespace is null", "   ", TypeInt, Cell{Null: true}},
		{"int parses", "42", TypeInt, Cell{Int: 42}},
		{"int negative", "-7", TypeInt, Cell{Int: -7}},
		{"int unparseable becomes null", "abc", TypeInt, Cell{Null: true}},
		{"float parses", "3.14", TypeFloat, Cell{Float: 3.14}},
		{"bool true word", "true", TypeBool, Cell{Bool: true}},
		{"bool yes", "yes", TypeBool, Cell{Bool: true}},
		{"bool false word", "false", TypeBool, Cell{Bool: false}},
		{"bool no", "no", TypeBool, Cell{Bool: false}},
		{"bool unparseable becomes null", "maybe", TypeBool, Cell{Null: true}},
		{"date iso", "2024-01-15", TypeDate, Cell{Date: mustDate("2024-01-15")}},
		{"date with time", "2024-01-15T12:00:00Z", TypeDate, Cell{Date: mustTime("2024-01-15T12:00:00Z")}},
		{"date unparseable becomes null", "yesterday", TypeDate, Cell{Null: true}},
		{"string keeps raw", "hello world", TypeString, Cell{Str: "hello world"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCell(tt.raw, tt.typ)
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFormatCell(t *testing.T) {
	tests := []struct {
		name string
		cell Cell
		typ  ColumnType
		want string
	}{
		{"null", Cell{Null: true}, TypeInt, ""},
		{"int", Cell{Int: 42}, TypeInt, "42"},
		{"float whole", Cell{Float: 3.0}, TypeFloat, "3"},
		{"float fractional", Cell{Float: 3.14}, TypeFloat, "3.14"},
		{"bool true", Cell{Bool: true}, TypeBool, "true"},
		{"bool false", Cell{Bool: false}, TypeBool, "false"},
		{"date only", Cell{Date: mustDate("2024-01-15")}, TypeDate, "2024-01-15"},
		{"datetime", Cell{Date: mustTime("2024-01-15T12:00:00Z")}, TypeDate, "2024-01-15T12:00:00Z"},
		{"string", Cell{Str: "hello"}, TypeString, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCell(tt.cell, tt.typ)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCoerceLiteral(t *testing.T) {
	tests := []struct {
		name    string
		lit     parse.Value
		typ     ColumnType
		want    Cell
		wantErr string
	}{
		{"null is universal", parse.Value{Kind: parse.ValNull}, TypeInt, Cell{Null: true}, ""},
		{"int literal to int", vnum("42"), TypeInt, Cell{Int: 42}, ""},
		{"string of int to int", vstr("42"), TypeInt, Cell{Int: 42}, ""},
		{"string of non-int to int errors", vstr("abc"), TypeInt, Cell{}, "cannot coerce"},
		{"float literal to float", vnum("3.14"), TypeFloat, Cell{Float: 3.14}, ""},
		{"int literal to float promotes", vnum("3"), TypeFloat, Cell{Float: 3.0}, ""},
		{"bool literal to bool", vbool(true), TypeBool, Cell{Bool: true}, ""},
		{"string true to bool", vstr("true"), TypeBool, Cell{Bool: true}, ""},
		{"string 1 to bool", vstr("1"), TypeBool, Cell{Bool: true}, ""},
		{"date string to date", vstr("2024-01-15"), TypeDate, Cell{Date: mustDate("2024-01-15")}, ""},
		{"number to date errors", vnum("42"), TypeDate, Cell{}, "cannot coerce"},
		{"int literal to string", vnum("42"), TypeString, Cell{Str: "42"}, ""},
		{"bool literal to string", vbool(true), TypeString, Cell{Str: "true"}, ""},
		{"string to string", vstr("hello"), TypeString, Cell{Str: "hello"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CoerceLiteral(tt.lit, tt.typ)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
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
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b Cell
		typ  ColumnType
		want int
	}{
		{"null < non-null", Cell{Null: true}, Cell{Int: 1}, TypeInt, -1},
		{"non-null > null", Cell{Int: 1}, Cell{Null: true}, TypeInt, 1},
		{"null == null", Cell{Null: true}, Cell{Null: true}, TypeInt, 0},
		{"int less", Cell{Int: 1}, Cell{Int: 2}, TypeInt, -1},
		{"int equal", Cell{Int: 2}, Cell{Int: 2}, TypeInt, 0},
		{"int greater", Cell{Int: 3}, Cell{Int: 2}, TypeInt, 1},
		{"float less", Cell{Float: 1.5}, Cell{Float: 2.5}, TypeFloat, -1},
		{"bool false < true", Cell{Bool: false}, Cell{Bool: true}, TypeBool, -1},
		{"date before", Cell{Date: mustDate("2024-01-01")}, Cell{Date: mustDate("2024-02-01")}, TypeDate, -1},
		{"string lexical", Cell{Str: "apple"}, Cell{Str: "banana"}, TypeString, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compare(tt.a, tt.b, tt.typ)
			if signOf(got) != signOf(tt.want) {
				t.Fatalf("got %d, want sign %d", got, tt.want)
			}
		})
	}
}
