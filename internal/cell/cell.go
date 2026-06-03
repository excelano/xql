// Package cell defines the typed-cell substrate shared by all XQL backends.
// A Table is a rectangular grid of Cells under a fixed schema; backends
// (CSV today, SharePoint tomorrow) hydrate into this shape so the
// expression evaluator and predicate matcher can run uniformly.
package cell

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/excelano/xql/internal/parse"
)

type ColumnType int

const (
	TypeString ColumnType = iota
	TypeInt
	TypeFloat
	TypeBool
	TypeDate
)

func (t ColumnType) String() string {
	switch t {
	case TypeInt:
		return "int"
	case TypeFloat:
		return "float"
	case TypeBool:
		return "bool"
	case TypeDate:
		return "date"
	default:
		return "string"
	}
}

// ParseColumnType maps a --type=... flag value back to a ColumnType.
func ParseColumnType(s string) (ColumnType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "string", "str", "text":
		return TypeString, nil
	case "int", "integer":
		return TypeInt, nil
	case "float", "number", "num":
		return TypeFloat, nil
	case "bool", "boolean":
		return TypeBool, nil
	case "date", "datetime":
		return TypeDate, nil
	}
	return 0, fmt.Errorf("unknown type %q (expected string, int, float, bool, or date)", s)
}

// ColumnInfo describes one column in a Table.
type ColumnInfo struct {
	Name string
	Type ColumnType
}

// Cell is a typed value. Exactly one of the Str/Int/Float/Bool/Date fields
// is meaningful, picked by the column's ColumnType. An empty source cell
// becomes a Cell with Null = true regardless of the column type.
type Cell struct {
	Null  bool
	Str   string
	Int   int64
	Float float64
	Bool  bool
	Date  time.Time
}

// Row is one record in the same column order as Table.Columns.
type Row []Cell

// Table is the in-memory representation of one bound dataset (CSV, SP list,
// etc.). Columns preserves header order; Schema maps name to type info;
// Rows holds the typed records. Backend-specific dialect fields (CSV's
// Delim/HasHeader, SP's site/list URL) hang off the Table — fields not
// understood by a given backend are zero-valued and ignored.
type Table struct {
	Path      string
	Columns   []string
	Schema    map[string]ColumnInfo
	Rows      []Row
	Delim     rune
	HasHeader bool
}

// ParseDateString tries the ISO 8601 forms XQL supports. Anything outside
// these formats falls through to string, intentionally; predictable beats
// aggressive guessing.
func ParseDateString(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not an ISO 8601 date: %q", s)
}

// ParseCell converts a raw text cell to a typed Cell using the column's
// inferred type. An unparseable cell becomes NULL rather than failing the
// load; pin the column to string via --type if you prefer the raw text.
func ParseCell(raw string, t ColumnType) Cell {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Cell{Null: true}
	}
	switch t {
	case TypeInt:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Cell{Int: n}
		}
		return Cell{Null: true}
	case TypeFloat:
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return Cell{Float: n}
		}
		return Cell{Null: true}
	case TypeBool:
		switch strings.ToLower(s) {
		case "true", "yes":
			return Cell{Bool: true}
		case "false", "no":
			return Cell{Bool: false}
		}
		return Cell{Null: true}
	case TypeDate:
		if dt, err := ParseDateString(s); err == nil {
			return Cell{Date: dt}
		}
		return Cell{Null: true}
	default:
		return Cell{Str: raw}
	}
}

// FormatCell renders a typed Cell back to a text field string. NULL becomes
// the empty string. Dates that have no time component render as date-only;
// the rest use RFC 3339. Floats use the shortest round-trippable form.
func FormatCell(v Cell, t ColumnType) string {
	if v.Null {
		return ""
	}
	switch t {
	case TypeInt:
		return strconv.FormatInt(v.Int, 10)
	case TypeFloat:
		return strconv.FormatFloat(v.Float, 'g', -1, 64)
	case TypeBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case TypeDate:
		if v.Date.Hour() == 0 && v.Date.Minute() == 0 && v.Date.Second() == 0 {
			return v.Date.Format("2006-01-02")
		}
		return v.Date.Format(time.RFC3339)
	default:
		return v.Str
	}
}

// CoerceLiteral converts a parsed XQL literal (parse.Value) to a Cell
// compatible with the given ColumnType. Used by INSERT and UPDATE to coerce
// user-supplied literals at write time. Returns an error if the literal
// cannot meaningfully coerce (for example 'abc' into an int column).
func CoerceLiteral(lit parse.Value, t ColumnType) (Cell, error) {
	if lit.Kind == parse.ValNull {
		return Cell{Null: true}, nil
	}
	switch t {
	case TypeString:
		switch lit.Kind {
		case parse.ValString:
			return Cell{Str: lit.Str}, nil
		case parse.ValNumber:
			return Cell{Str: lit.Num}, nil
		case parse.ValBool:
			if lit.Bool {
				return Cell{Str: "true"}, nil
			}
			return Cell{Str: "false"}, nil
		}
	case TypeInt:
		switch lit.Kind {
		case parse.ValNumber:
			if n, err := strconv.ParseInt(lit.Num, 10, 64); err == nil {
				return Cell{Int: n}, nil
			}
		case parse.ValString:
			if n, err := strconv.ParseInt(lit.Str, 10, 64); err == nil {
				return Cell{Int: n}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to int", RenderLiteral(lit))
	case TypeFloat:
		switch lit.Kind {
		case parse.ValNumber:
			if n, err := strconv.ParseFloat(lit.Num, 64); err == nil {
				return Cell{Float: n}, nil
			}
		case parse.ValString:
			if n, err := strconv.ParseFloat(lit.Str, 64); err == nil {
				return Cell{Float: n}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to float", RenderLiteral(lit))
	case TypeBool:
		switch lit.Kind {
		case parse.ValBool:
			return Cell{Bool: lit.Bool}, nil
		case parse.ValString:
			switch strings.ToLower(lit.Str) {
			case "true", "yes", "1":
				return Cell{Bool: true}, nil
			case "false", "no", "0":
				return Cell{Bool: false}, nil
			}
		case parse.ValNumber:
			switch lit.Num {
			case "1":
				return Cell{Bool: true}, nil
			case "0":
				return Cell{Bool: false}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to bool", RenderLiteral(lit))
	case TypeDate:
		if lit.Kind == parse.ValString {
			if dt, err := ParseDateString(lit.Str); err == nil {
				return Cell{Date: dt}, nil
			}
		}
		return Cell{}, fmt.Errorf("cannot coerce %s to date", RenderLiteral(lit))
	}
	return Cell{}, fmt.Errorf("unknown column type for coercion")
}

// RenderLiteral returns the XQL surface form of a parsed literal, used in
// error messages so coercion failures echo the user's input back to them.
func RenderLiteral(lit parse.Value) string {
	switch lit.Kind {
	case parse.ValString:
		return "'" + strings.ReplaceAll(lit.Str, "'", "''") + "'"
	case parse.ValNumber:
		return lit.Num
	case parse.ValBool:
		if lit.Bool {
			return "TRUE"
		}
		return "FALSE"
	case parse.ValNull:
		return "NULL"
	}
	return "?"
}

// Compare returns -1, 0, or +1 for the natural ordering of two Cells under
// the given column type. NULL sorts low. Used by predicate evaluation and
// ORDER BY.
func Compare(a, b Cell, t ColumnType) int {
	if a.Null && b.Null {
		return 0
	}
	if a.Null {
		return -1
	}
	if b.Null {
		return 1
	}
	switch t {
	case TypeInt:
		switch {
		case a.Int < b.Int:
			return -1
		case a.Int > b.Int:
			return 1
		}
		return 0
	case TypeFloat:
		switch {
		case a.Float < b.Float:
			return -1
		case a.Float > b.Float:
			return 1
		}
		return 0
	case TypeBool:
		ai, bi := 0, 0
		if a.Bool {
			ai = 1
		}
		if b.Bool {
			bi = 1
		}
		return ai - bi
	case TypeDate:
		switch {
		case a.Date.Before(b.Date):
			return -1
		case a.Date.After(b.Date):
			return 1
		}
		return 0
	default:
		return strings.Compare(a.Str, b.Str)
	}
}

// Render returns the human-readable form of a Cell for table/TSV output.
// NULL renders as the empty string.
func (v Cell) Render(t ColumnType) string {
	if v.Null {
		return ""
	}
	return FormatCell(v, t)
}

// AsAny returns the Cell as an untyped Go value suitable for JSON encoding.
// NULL becomes nil; everything else returns the underlying typed value.
func (v Cell) AsAny(t ColumnType) any {
	if v.Null {
		return nil
	}
	switch t {
	case TypeInt:
		return v.Int
	case TypeFloat:
		return v.Float
	case TypeBool:
		return v.Bool
	case TypeDate:
		return FormatCell(v, t)
	default:
		return v.Str
	}
}
