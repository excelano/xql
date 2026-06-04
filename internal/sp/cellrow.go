package sp

import (
	"encoding/json"
	"fmt"

	"github.com/excelano/xql/internal/cell"
)

// FieldTypeToCellType maps a SharePoint FieldType to the cell.ColumnType used
// by the shared evaluator. Number is mapped to TypeFloat (not TypeInt)
// because Graph returns JSON numbers and json.Unmarshal into any always
// produces float64; even integer-valued list-item numbers arrive as floats.
// Object-shaped types (person, lookup, hyperlink) and the read-only
// calculated/unknown types fall back to TypeString — they round-trip as
// rendered text for read paths but cannot be written.
func FieldTypeToCellType(t FieldType) cell.ColumnType {
	switch t {
	case FieldNumber:
		return cell.TypeFloat
	case FieldBoolean:
		return cell.TypeBool
	case FieldDateTime:
		return cell.TypeDate
	default:
		return cell.TypeString
	}
}

// BuildCellSchema converts a bound list's SharePoint schema into the cell-
// based schema the shared evaluator expects. Column order matches
// Bound.Columns so row position is stable across calls.
func BuildCellSchema(bound *BoundList) ([]string, map[string]cell.ColumnInfo) {
	cols := make([]string, len(bound.Columns))
	copy(cols, bound.Columns)
	info := make(map[string]cell.ColumnInfo, len(cols))
	for _, name := range cols {
		fi := bound.Schema[name]
		info[name] = cell.ColumnInfo{Name: name, Type: FieldTypeToCellType(fi.Type)}
	}
	return cols, info
}

// FieldsToRow converts one Graph row (the unmarshaled "fields" subobject)
// into a typed cell.Row in the column order returned by BuildCellSchema.
// Missing keys, explicit nulls, and empty strings on non-string columns all
// produce Null cells; the evaluator treats those as NULL under SQL
// three-valued logic. Per-column conversion errors are surfaced rather than
// silently dropped so a malformed list row isn't mistaken for a NULL one.
func FieldsToRow(fields map[string]any, cols []string, info map[string]cell.ColumnInfo) (cell.Row, error) {
	row := make(cell.Row, len(cols))
	for i, name := range cols {
		raw, present := fields[name]
		if !present || raw == nil {
			row[i] = cell.Cell{Null: true}
			continue
		}
		c, err := convertField(raw, info[name].Type)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}
		row[i] = c
	}
	return row, nil
}

// BuildCellTable assembles a cell.Table from a bound list and a fetched item
// set. Row indices align with the items slice: rows[i] corresponds to
// items[i].ID, so callers that need to PATCH or DELETE the underlying Graph
// item can index back without a separate lookup.
//
// Path carries the bound list's display name as a label for table-rendering
// callers; SharePoint lists have no filesystem path.
func BuildCellTable(bound *BoundList, items []listItem) (*cell.Table, error) {
	cols, info := BuildCellSchema(bound)
	rows := make([]cell.Row, len(items))
	for i, it := range items {
		r, err := FieldsToRow(it.Fields, cols, info)
		if err != nil {
			return nil, err
		}
		rows[i] = r
	}
	return &cell.Table{
		Path:    bound.DisplayName,
		Columns: cols,
		Schema:  info,
		Rows:    rows,
	}, nil
}

// convertField coerces one Graph JSON value into a typed cell. Numbers
// arrive as float64 (json.Unmarshal default) or json.Number depending on
// decoder settings; both shapes are handled. Strings on non-string columns
// trigger a parse attempt — SP returns datetimes as ISO 8601 strings, and a
// few choice/text columns occasionally contain stringified numbers.
func convertField(raw any, t cell.ColumnType) (cell.Cell, error) {
	switch t {
	case cell.TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return cell.Cell{}, fmt.Errorf("expected bool, got %T", raw)
		}
		return cell.Cell{Bool: b}, nil
	case cell.TypeFloat:
		f, ok := toFloat64(raw)
		if !ok {
			return cell.Cell{}, fmt.Errorf("expected number, got %T", raw)
		}
		return cell.Cell{Float: f}, nil
	case cell.TypeInt:
		f, ok := toFloat64(raw)
		if !ok {
			return cell.Cell{}, fmt.Errorf("expected number, got %T", raw)
		}
		return cell.Cell{Int: int64(f)}, nil
	case cell.TypeDate:
		s, ok := raw.(string)
		if !ok {
			return cell.Cell{}, fmt.Errorf("expected ISO 8601 datetime string, got %T", raw)
		}
		if s == "" {
			return cell.Cell{Null: true}, nil
		}
		ts, err := cell.ParseDateString(s)
		if err != nil {
			return cell.Cell{}, fmt.Errorf("invalid datetime %q: %w", s, err)
		}
		return cell.Cell{Date: ts}, nil
	default:
		// TypeString: stringify whatever shape SP returned. Person/lookup/
		// hyperlink come back as objects; render them as JSON so downstream
		// LIKE/=/etc. comparisons see a deterministic string form.
		switch v := raw.(type) {
		case string:
			return cell.Cell{Str: v}, nil
		case bool:
			if v {
				return cell.Cell{Str: "true"}, nil
			}
			return cell.Cell{Str: "false"}, nil
		default:
			if f, ok := toFloat64(raw); ok {
				return cell.Cell{Str: formatNumberAsString(f)}, nil
			}
			b, err := json.Marshal(raw)
			if err != nil {
				return cell.Cell{}, fmt.Errorf("cannot stringify %T", raw)
			}
			return cell.Cell{Str: string(b)}, nil
		}
	}
}

// toFloat64 reads a number out of an any. Handles the two shapes
// json.Unmarshal can produce (float64 by default, json.Number when the
// decoder is configured with UseNumber) plus the int family in case a
// caller hands us a hand-built map.
func toFloat64(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// formatNumberAsString renders a JSON number back to a stable string form.
// Integers and integer-valued floats render without a decimal point so the
// resulting string compares lexically the same way a user-typed literal
// would; non-integer floats use %g for the shortest round-trippable form.
func formatNumberAsString(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
