package render

import (
	encodingcsv "encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
)

// FormatTable, FormatTSV, FormatCSV, FormatJSON are the supported --mode values.
const (
	FormatTable = "table"
	FormatTSV   = "tsv"
	FormatCSV   = "csv"
	FormatJSON  = "json"
)

// Result is a column-ordered tabular result ready for rendering. Cells hold
// the raw values returned by Graph (string, float64, bool, nil, ...) so the
// JSON renderer can preserve types; the table, TSV, and CSV renderers
// stringify on the way out.
type Result struct {
	Columns []string
	Rows    []map[string]any
}

// Render writes the result to out in the named format. An empty format string
// auto-detects (table when out is a terminal, TSV otherwise). The headers
// argument suppresses the header row for the row-shaped formats (table, tsv,
// csv); JSON ignores it since object keys carry the column names regardless.
func Render(out io.Writer, r Result, format string, headers bool) error {
	if format == "" {
		format = autoFormat(out)
	}
	switch format {
	case FormatTable:
		return renderTable(out, r, headers)
	case FormatTSV:
		return renderTSV(out, r, headers)
	case FormatCSV:
		return renderCSV(out, r, headers)
	case FormatJSON:
		return renderJSON(out, r)
	}
	return fmt.Errorf("unknown mode %q (want table, tsv, csv, or json)", format)
}

// autoFormat picks table when out is a terminal stdout, TSV otherwise. The
// check is conservative: only the literal os.Stdout file passes; pipes and
// redirected files fall through to TSV.
func autoFormat(out io.Writer) string {
	f, ok := out.(*os.File)
	if !ok {
		return FormatTSV
	}
	fi, err := f.Stat()
	if err != nil {
		return FormatTSV
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		return FormatTable
	}
	return FormatTSV
}

func renderTable(out io.Writer, r Result, headers bool) error {
	if len(r.Columns) == 0 {
		return nil
	}
	if err := writeTableBodyHeader(out, r.Columns, r.Rows, headers); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "(%d row%s)\n", len(r.Rows), plural(len(r.Rows)))
	return err
}

// WriteTableBody renders the header + separator + data rows, but no footer.
// Used by write previews; always shows the header (previews are a labeled
// snapshot, not a result set the user has flagged headerless).
func WriteTableBody(out io.Writer, cols []string, rows []map[string]any) error {
	return writeTableBodyHeader(out, cols, rows, true)
}

func writeTableBodyHeader(out io.Writer, cols []string, rows []map[string]any, headers bool) error {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = runewidth.StringWidth(c)
	}
	cells := make([][]string, len(rows))
	for ri, row := range rows {
		cells[ri] = make([]string, len(cols))
		for ci, c := range cols {
			s := stringify(row[c])
			cells[ri][ci] = s
			if w := runewidth.StringWidth(s); w > widths[ci] {
				widths[ci] = w
			}
		}
	}
	if headers {
		if err := writeTableRow(out, cols, widths); err != nil {
			return err
		}
		sep := make([]string, len(cols))
		for i, w := range widths {
			sep[i] = strings.Repeat("-", w)
		}
		if err := writeTableRow(out, sep, widths); err != nil {
			return err
		}
	}
	for _, row := range cells {
		if err := writeTableRow(out, row, widths); err != nil {
			return err
		}
	}
	return nil
}

func writeTableRow(out io.Writer, cells []string, widths []int) error {
	parts := make([]string, len(cells))
	for i, c := range cells {
		parts[i] = padRight(c, widths[i])
	}
	_, err := fmt.Fprintf(out, "| %s |\n", strings.Join(parts, " | "))
	return err
}

func renderTSV(out io.Writer, r Result, headers bool) error {
	if headers {
		if _, err := fmt.Fprintln(out, strings.Join(r.Columns, "\t")); err != nil {
			return err
		}
	}
	for _, row := range r.Rows {
		cells := make([]string, len(r.Columns))
		for i, c := range r.Columns {
			cells[i] = stringify(row[c])
		}
		if _, err := fmt.Fprintln(out, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// renderCSV emits RFC 4180 CSV: comma-delimited, CRLF line endings, fields
// containing commas, quotes, or newlines are double-quoted (the stdlib writer
// handles the quoting rules). Headers off skips the column-name row.
func renderCSV(out io.Writer, r Result, headers bool) error {
	w := encodingcsv.NewWriter(out)
	w.UseCRLF = true
	if headers {
		if err := w.Write(r.Columns); err != nil {
			return err
		}
	}
	for _, row := range r.Rows {
		cells := make([]string, len(r.Columns))
		for i, c := range r.Columns {
			cells[i] = stringify(row[c])
		}
		if err := w.Write(cells); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func renderJSON(out io.Writer, r Result) error {
	projected := make([]map[string]any, len(r.Rows))
	for i, row := range r.Rows {
		m := make(map[string]any, len(r.Columns))
		for _, c := range r.Columns {
			m[c] = row[c]
		}
		projected[i] = m
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(projected)
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return fmt.Sprintf("%d", x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	}
	return fmt.Sprintf("%v", v)
}

// padRight pads s to a target display width w, using runewidth so that
// multi-byte and East Asian wide characters land in the column count they
// actually occupy on a terminal — not the number of bytes they take up.
func padRight(s string, w int) string {
	sw := runewidth.StringWidth(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
