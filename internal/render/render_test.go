package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderCSV_HeadersOn(t *testing.T) {
	var buf bytes.Buffer
	r := Result{
		Columns: []string{"Id", "Title", "Done"},
		Rows: []map[string]any{
			{"Id": int64(1), "Title": "First", "Done": false},
			{"Id": int64(2), "Title": "Second", "Done": true},
		},
	}
	if err := Render(&buf, r, FormatCSV, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	want := "Id,Title,Done\r\n1,First,false\r\n2,Second,true\r\n"
	if got != want {
		t.Fatalf("output mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderCSV_HeadersOff(t *testing.T) {
	var buf bytes.Buffer
	r := Result{
		Columns: []string{"Id", "Title"},
		Rows:    []map[string]any{{"Id": int64(1), "Title": "x"}},
	}
	if err := Render(&buf, r, FormatCSV, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	want := "1,x\r\n"
	if got != want {
		t.Fatalf("output mismatch\ngot: %q\nwant: %q", got, want)
	}
}

func TestRenderCSV_QuotingPerRFC4180(t *testing.T) {
	var buf bytes.Buffer
	r := Result{
		Columns: []string{"a", "b", "c"},
		Rows: []map[string]any{
			{
				"a": "comma, inside",
				"b": `quote " inside`,
				"c": "newline\ninside",
			},
		},
	}
	if err := Render(&buf, r, FormatCSV, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	// stdlib encoding/csv quotes fields containing comma, quote, or newline.
	// Embedded quotes double.
	if !strings.Contains(got, `"comma, inside"`) {
		t.Errorf("comma not quoted: %q", got)
	}
	if !strings.Contains(got, `"quote "" inside"`) {
		t.Errorf("quote not escaped: %q", got)
	}
	if !strings.Contains(got, "\"newline\r\ninside\"") {
		t.Errorf("newline not quoted: %q", got)
	}
}

func TestRenderCSV_NilAndTypes(t *testing.T) {
	var buf bytes.Buffer
	r := Result{
		Columns: []string{"i", "f", "s", "b", "n"},
		Rows: []map[string]any{
			{"i": int64(42), "f": 3.14, "s": "hi", "b": true, "n": nil},
		},
	}
	if err := Render(&buf, r, FormatCSV, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	want := "42,3.14,hi,true,\r\n"
	if got != want {
		t.Fatalf("output mismatch\ngot: %q\nwant: %q", got, want)
	}
}

func TestRender_UnknownMode(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, Result{}, "yaml", true)
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("error should mention 'unknown mode', got: %v", err)
	}
}

func TestRenderTSV_HeadersOff(t *testing.T) {
	var buf bytes.Buffer
	r := Result{
		Columns: []string{"a", "b"},
		Rows:    []map[string]any{{"a": "1", "b": "2"}},
	}
	if err := Render(&buf, r, FormatTSV, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	want := "1\t2\n"
	if got != want {
		t.Fatalf("output mismatch\ngot: %q\nwant: %q", got, want)
	}
}

func TestRenderTable_HeadersOff(t *testing.T) {
	var buf bytes.Buffer
	r := Result{
		Columns: []string{"a", "b"},
		Rows:    []map[string]any{{"a": "1", "b": "2"}},
	}
	if err := Render(&buf, r, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	// No header, no separator — just the data row and footer.
	if strings.Contains(got, "---") {
		t.Errorf("table separator should be suppressed: %q", got)
	}
	if !strings.Contains(got, "| 1 | 2 |") {
		t.Errorf("data row missing: %q", got)
	}
	if !strings.Contains(got, "(1 row)") {
		t.Errorf("footer missing: %q", got)
	}
}

func TestWriteTableBody_AlwaysShowsHeader(t *testing.T) {
	var buf bytes.Buffer
	cols := []string{"a", "b"}
	rows := []map[string]any{{"a": "1", "b": "2"}}
	if err := WriteTableBody(&buf, cols, rows); err != nil {
		t.Fatalf("WriteTableBody: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "| a | b |") {
		t.Errorf("header row missing from preview: %q", got)
	}
	if !strings.Contains(got, "| - | - |") {
		t.Errorf("separator missing from preview: %q", got)
	}
}
