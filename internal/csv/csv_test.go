package csv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
)

// TestParseColumnType, TestParseCell, TestFormatCell, TestCoerceLiteral,
// and TestCompare moved to internal/cell with the substrate types.

func TestInferColumn(t *testing.T) {
	tests := []struct {
		name   string
		sample [][]string
		want   cell.ColumnType
	}{
		{"all ints", [][]string{{"1"}, {"2"}, {"-3"}, {"0"}}, cell.TypeInt},
		{"int promotes to float when one decimal appears", [][]string{{"1"}, {"2.5"}, {"3"}}, cell.TypeFloat},
		{"all floats", [][]string{{"1.0"}, {"2.5"}, {"3.14"}}, cell.TypeFloat},
		{"all dates iso", [][]string{{"2024-01-01"}, {"2024-02-15"}}, cell.TypeDate},
		{"date+time mix", [][]string{{"2024-01-01"}, {"2024-02-15T12:00:00Z"}}, cell.TypeDate},
		{"bool words", [][]string{{"true"}, {"false"}, {"yes"}, {"no"}}, cell.TypeBool},
		{"falls back to string when mixed", [][]string{{"open"}, {"closed"}, {"in-progress"}}, cell.TypeString},
		{"empty cells skipped during inference", [][]string{{"1"}, {""}, {"2"}}, cell.TypeInt},
		{"all empty defaults to string", [][]string{{""}, {""}}, cell.TypeString},
		{"single non-int kills int inference", [][]string{{"1"}, {"2"}, {"abc"}}, cell.TypeString},
		{"0/1 stay as int, not bool", [][]string{{"0"}, {"1"}, {"0"}}, cell.TypeInt},
		{"leading-zero ZIP codes stay as string", [][]string{{"07030"}, {"10001"}, {"02101"}}, cell.TypeString},
		{"single leading-zero entry knocks column out of int", [][]string{{"1"}, {"2"}, {"007"}}, cell.TypeString},
		{"NaN keeps column out of float", [][]string{{"1.5"}, {"2.5"}, {"NaN"}}, cell.TypeString},
		{"Infinity keeps column out of float", [][]string{{"1.5"}, {"Inf"}}, cell.TypeString},
		{"signed leading zero is rejected", [][]string{{"-01"}, {"-02"}}, cell.TypeString},
		{"plain zero is fine", [][]string{{"0"}, {"5"}, {"-7"}}, cell.TypeInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferColumn(tt.sample, 0)
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}


func TestLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	dst := filepath.Join(dir, "out.csv")
	content := "ID,Title,Score,Active,When\n" +
		"1,Alpha,3.5,true,2024-01-15\n" +
		"2,Beta,4.0,false,2024-02-20\n" +
		"3,Gamma,,true,\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}

	wantTypes := map[string]cell.ColumnType{
		"ID": cell.TypeInt, "Title": cell.TypeString, "Score": cell.TypeFloat,
		"Active": cell.TypeBool, "When": cell.TypeDate,
	}
	for name, want := range wantTypes {
		got := tbl.Schema[name].Type
		if got != want {
			t.Errorf("column %s: inferred %v, want %v", name, got, want)
		}
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(tbl.Rows))
	}
	if !tbl.Rows[2][2].Null {
		t.Error("Gamma.Score should be null (empty cell)")
	}

	if err := SaveCSV(tbl, dst); err != nil {
		t.Fatalf("SaveCSV: %v", err)
	}
	saved, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Headers + row count match; cell representations may differ slightly
	// (3.5 stays 3.5, 4.0 stringifies as 4 via %g — that's expected).
	lines := strings.Split(strings.TrimSpace(string(saved)), "\n")
	if len(lines) != 4 {
		t.Fatalf("saved has %d lines, want 4 (header + 3 rows)", len(lines))
	}
	if lines[0] != "ID,Title,Score,Active,When" {
		t.Errorf("header: got %q", lines[0])
	}
}

func TestLoadNoHeader(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nh.csv")
	content := "1,Alpha,3.5\n2,Beta,4.0\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{NoHeader: true})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	wantCols := []string{"col1", "col2", "col3"}
	for i, want := range wantCols {
		if tbl.Columns[i] != want {
			t.Errorf("col[%d] = %q, want %q", i, tbl.Columns[i], want)
		}
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(tbl.Rows))
	}
}

func TestLoadCustomDelim(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tab.tsv")
	content := "id\tname\n1\tAlpha\n2\tBeta\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{Delim: '\t'})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if len(tbl.Columns) != 2 || tbl.Columns[0] != "id" || tbl.Columns[1] != "name" {
		t.Fatalf("columns: %v", tbl.Columns)
	}
	if tbl.Rows[0][1].Str != "Alpha" {
		t.Errorf("row 0 name: got %q, want Alpha", tbl.Rows[0][1].Str)
	}
}

func TestLoadTypeHintOverride(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hint.csv")
	content := "ID,Code\n1,001\n2,002\n3,003\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{
		TypeHints: map[string]cell.ColumnType{"Code": cell.TypeString},
	})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if got := tbl.Schema["Code"].Type; got != cell.TypeString {
		t.Fatalf("Code type: got %v, want string (hint override)", got)
	}
	if got := tbl.Schema["ID"].Type; got != cell.TypeInt {
		t.Fatalf("ID type: got %v, want int (auto inferred)", got)
	}
	if tbl.Rows[0][1].Str != "001" {
		t.Errorf("Code should preserve leading zeros, got %q", tbl.Rows[0][1].Str)
	}
}

// TestLoadStripsUTF8BOM exercises the BOM peek-and-skip path. Excel's
// "Save as CSV UTF-8" writes one and would otherwise corrupt the first
// column header.
func TestLoadStripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bom.csv")
	content := "\xef\xbb\xbfID,Name\n1,Alpha\n2,Beta\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if tbl.Columns[0] != "ID" {
		t.Fatalf("first column = %q, want %q (BOM not stripped)", tbl.Columns[0], "ID")
	}
	if _, ok := tbl.Schema["ID"]; !ok {
		t.Fatalf("Schema lookup for ID failed; keys: %v", keysOf(tbl.Schema))
	}
}

// TestLoadAutoDetectsLeadingZeros verifies the inference rule that pulls
// columns with leading-zero values out of cell.TypeInt without needing a
// --type override.
func TestLoadAutoDetectsLeadingZeros(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "zip.csv")
	content := "Name,Zip\nAlice,07030\nBob,10001\nCarol,02101\n"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if got := tbl.Schema["Zip"].Type; got != cell.TypeString {
		t.Fatalf("Zip type: got %v, want string", got)
	}
	if tbl.Rows[0][1].Str != "07030" {
		t.Errorf("Zip cell: got %q, want %q (leading zero lost)", tbl.Rows[0][1].Str, "07030")
	}
}

func TestLoadRejectsDuplicateHeaders(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dup.csv")
	if err := os.WriteFile(src, []byte("ID,Amount,Amount\n1,10,20\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCSV(src, LoadOptions{})
	if err == nil {
		t.Fatal("expected error for duplicate header")
	}
	if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "Amount") {
		t.Fatalf("error should name the duplicate: %v", err)
	}
}

func TestLoadRejectsEmptyHeader(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(src, []byte("ID, ,Name\n1, ,Alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCSV(src, LoadOptions{})
	if err == nil {
		t.Fatal("expected error for empty/whitespace header")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error should mention empty: %v", err)
	}
}

// TestLoadTrimsHeaderWhitespace: a CSV with " Name " in the header should
// load successfully and the column should be addressable as "Name".
func TestLoadTrimsHeaderWhitespace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "trim.csv")
	if err := os.WriteFile(src, []byte(" ID , Name \n1,Alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadCSV(src, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if tbl.Columns[0] != "ID" || tbl.Columns[1] != "Name" {
		t.Fatalf("columns: %v, want [ID Name]", tbl.Columns)
	}
}

func keysOf(m map[string]cell.ColumnInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

