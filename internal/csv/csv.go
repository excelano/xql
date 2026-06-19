// Package csv is the CSV backend for xql. It loads delimited text files into
// the shared cell.Table substrate (with inferred per-column types) and writes
// modified tables back. The executor lives alongside the loader because both
// share the CSV-specific quirks: BOM handling, delimiter override, sample-
// based type inference, and round-trip formatting.
package csv

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/excelano/xql/internal/cell"
)

// utf8BOM is the byte sequence Excel and other Windows tools prepend to
// UTF-8 CSV exports. It's not part of the data and not stripped by
// encoding/csv, so we peek-and-skip it before the reader sees the file.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// LoadOptions controls CSV parsing and type inference. Zero values mean
// "use defaults": comma delimiter, header row present, type inference
// enabled.
type LoadOptions struct {
	Delim     rune
	NoHeader  bool
	TypeHints map[string]cell.ColumnType
	SampleN   int
}

// validateHeaders rejects header rows that would silently corrupt schema
// lookups: empty / whitespace-only column names become unreachable keys,
// and duplicates collide in the Schema map while leaving both entries in
// Columns — so SELECT and the write path would target different cells.
// Both failure modes are quiet enough to debug for hours; better to fail
// fast at load time.
func validateHeaders(cols []string) error {
	seen := make(map[string]int, len(cols))
	for i, name := range cols {
		if name == "" {
			return fmt.Errorf("header column %d is empty (whitespace-only or missing)", i+1)
		}
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("header column %d duplicates column %d (both named %q)", i+1, prev+1, name)
		}
		seen[name] = i
	}
	return nil
}

// LoadCSV reads the file at path and returns a fully populated cell.Table.
// Type inference runs over the first SampleN rows (default 1024); a column
// gets the most specific type where every sampled non-empty cell parses.
func LoadCSV(path string, opts LoadOptions) (*cell.Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	return LoadCSVReader(path, f, opts)
}

// LoadCSVReader is the io.Reader form of LoadCSV. The label is purely
// cosmetic -- it populates Table.Path (used in REPL banners and Refresh
// output) and appears in load-time error messages. Non-file backends pass
// something user-recognizable like "xinglet://<uuid>".
func LoadCSVReader(label string, src io.Reader, opts LoadOptions) (*cell.Table, error) {
	delim := opts.Delim
	if delim == 0 {
		delim = ','
	}

	// Peek for a UTF-8 BOM before the csv.Reader sees the stream. Excel's
	// "Save as CSV UTF-8" prepends one, and encoding/csv would otherwise
	// fold it into the first column header.
	br := bufio.NewReader(src)
	if peek, _ := br.Peek(len(utf8BOM)); len(peek) == len(utf8BOM) && string(peek) == string(utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}

	r := csv.NewReader(br)
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", label, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s is empty", label)
	}

	var columns []string
	var dataStart int
	if opts.NoHeader {
		ncol := len(records[0])
		columns = make([]string, ncol)
		for i := range columns {
			columns[i] = fmt.Sprintf("col%d", i+1)
		}
		dataStart = 0
	} else {
		columns = make([]string, len(records[0]))
		for i, name := range records[0] {
			columns[i] = strings.TrimSpace(name)
		}
		if err := validateHeaders(columns); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		dataStart = 1
	}
	ncol := len(columns)

	rawRows := make([][]string, 0, len(records)-dataStart)
	for _, rec := range records[dataStart:] {
		row := make([]string, ncol)
		for i := 0; i < ncol && i < len(rec); i++ {
			row[i] = rec[i]
		}
		rawRows = append(rawRows, row)
	}

	sampleN := opts.SampleN
	if sampleN <= 0 {
		sampleN = 1024
	}
	if sampleN > len(rawRows) {
		sampleN = len(rawRows)
	}

	schema := make(map[string]cell.ColumnInfo, ncol)
	for i, name := range columns {
		var t cell.ColumnType
		if hint, ok := opts.TypeHints[name]; ok {
			t = hint
		} else {
			t = inferColumn(rawRows[:sampleN], i)
		}
		schema[name] = cell.ColumnInfo{Name: name, Type: t}
	}

	rows := make([]cell.Row, len(rawRows))
	for ri, rec := range rawRows {
		row := make(cell.Row, ncol)
		for ci, raw := range rec {
			row[ci] = cell.ParseCell(raw, schema[columns[ci]].Type)
		}
		rows[ri] = row
	}

	return &cell.Table{
		Path:      label,
		Columns:   columns,
		Schema:    schema,
		Rows:      rows,
		Delim:     delim,
		HasHeader: !opts.NoHeader,
	}, nil
}

// SaveCSV writes the cell.Table back to its bound path (or to dst if non-empty).
// Cells emit in their canonical string form; NULL becomes an empty field.
func SaveCSV(t *cell.Table, dst string) error {
	if dst == "" {
		dst = t.Path
	}
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Comma = t.Delim
	defer w.Flush()

	if t.HasHeader {
		if err := w.Write(t.Columns); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
	}
	rec := make([]string, len(t.Columns))
	for _, row := range t.Rows {
		for i, name := range t.Columns {
			rec[i] = cell.FormatCell(row[i], t.Schema[name].Type)
		}
		if err := w.Write(rec); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

// inferColumn picks the most specific cell.ColumnType where every non-empty cell
// in the column index parses. Order of specificity: int, float, date, bool,
// then string. A column of all empty cells defaults to string.
func inferColumn(sample [][]string, idx int) cell.ColumnType {
	allInt, allFloat, allBool, allDate := true, true, true, true
	seenNonEmpty := false
	for _, row := range sample {
		if idx >= len(row) {
			continue
		}
		v := strings.TrimSpace(row[idx])
		if v == "" {
			continue
		}
		seenNonEmpty = true
		if allInt && !looksLikeInt(v) {
			allInt = false
		}
		if allFloat && !looksLikeFloat(v) {
			allFloat = false
		}
		if allBool && !looksLikeBool(v) {
			allBool = false
		}
		if allDate && !looksLikeDate(v) {
			allDate = false
		}
		if !allInt && !allFloat && !allBool && !allDate {
			return cell.TypeString
		}
	}
	if !seenNonEmpty {
		return cell.TypeString
	}
	switch {
	case allInt:
		return cell.TypeInt
	case allFloat:
		return cell.TypeFloat
	case allDate:
		return cell.TypeDate
	case allBool:
		return cell.TypeBool
	default:
		return cell.TypeString
	}
}

// looksLikeInt accepts a base-10 integer that does not have a leading zero
// (after an optional sign). Reject leading-zero forms because they are
// almost always identifiers — ZIP codes, employee numbers, phone extensions
// — and we would silently destroy them on round-trip if we inferred them
// as integers and then FormatInt'd them back without the zero.
func looksLikeInt(s string) bool {
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return false
	}
	body := s
	if len(body) > 1 && (body[0] == '-' || body[0] == '+') {
		body = body[1:]
	}
	if len(body) > 1 && body[0] == '0' {
		return false
	}
	return true
}

// looksLikeFloat accepts ordinary base-10 floats but rejects:
//   - strconv's special-case parses of "NaN", "Inf", "+Inf", "-Inf",
//     "Infinity", etc. Those values are valid IEEE 754 but pollute a
//     column: NaN ≠ NaN under SQL comparison and the round-trip writes
//     "NaN" back rather than the original Excel-style "#DIV/0!" cell.
//   - Leading-zero integer parts ("07030", "-01.5"). Same reasoning as
//     looksLikeInt: these are almost always identifiers, and inferring
//     them as numeric destroys the leading zero on round-trip. "0.5"
//     and "0" are still allowed.
func looksLikeFloat(s string) bool {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}
	body := s
	if len(body) > 1 && (body[0] == '-' || body[0] == '+') {
		body = body[1:]
	}
	if len(body) > 1 && body[0] == '0' && body[1] != '.' {
		return false
	}
	return true
}

func looksLikeBool(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no":
		return true
	}
	return false
}

func looksLikeDate(s string) bool {
	_, err := cell.ParseDateString(s)
	return err == nil
}
