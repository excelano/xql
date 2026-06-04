package sp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/render"
)

// Executor binds a parsed statement to the live SharePoint list and runs it.
// One Executor per session; the bound list and graph client are immutable for
// the session.
//
// Confirm is the interactive "Apply? [y/N]" callback used by the REPL (lands
// in slice 4). When non-nil, write executors will call it after the dry-run
// preview to decide whether to commit (unless commit is already true via the
// trailing '!'). --exec mode leaves Confirm nil so writes either dry-run or
// commit explicitly based on --commit.
type Executor struct {
	Graph              *GraphClient
	Bound              *BoundList
	Format             string
	AllFields          bool
	ConfirmDestructive bool
	Confirm            func() bool
	Out                io.Writer
}

// Execute dispatches to the per-statement handler. The commit flag distinguishes
// dry-run (commit=false: preview only) from a real write (commit=true: preview
// + apply). It is ignored for SELECT.
//
// Slice 2 wires SELECT only; UPDATE/DELETE/INSERT land in slice 3.
func (e *Executor) Execute(ctx context.Context, stmt parse.Stmt, commit bool) error {
	switch s := stmt.(type) {
	case *parse.SelectStmt:
		return e.executeSelect(ctx, s)
	case *parse.UpdateStmt:
		return fmt.Errorf("UPDATE: SharePoint backend support lands in slice 3")
	case *parse.DeleteStmt:
		return fmt.Errorf("DELETE: SharePoint backend support lands in slice 3")
	case *parse.InsertStmt:
		return fmt.Errorf("INSERT: SharePoint backend support lands in slice 3")
	}
	return fmt.Errorf("internal: unknown statement type %T", stmt)
}

func (e *Executor) executeSelect(ctx context.Context, sel *parse.SelectStmt) error {
	if len(sel.GroupBy) > 0 {
		return fmt.Errorf("GROUP BY: SharePoint backend support lands in a later v1.1 slice")
	}
	if sel.Having != nil {
		return fmt.Errorf("HAVING: SharePoint backend support lands in a later v1.1 slice")
	}
	cols, err := e.resolveProjection(sel)
	if err != nil {
		return err
	}
	if err := e.validateOrderBy(sel.OrderBy); err != nil {
		return err
	}

	q := url.Values{
		"$expand": {"fields"},
	}
	if sel.Where != nil {
		filter, err := ToOData(sel.Where, e.Bound.Schema)
		if err != nil {
			return err
		}
		q.Set("$filter", filter)
	}

	path := fmt.Sprintf("/sites/%s/lists/%s/items", e.Bound.SiteID, e.Bound.ListID)
	raws, err := e.Graph.getAll(ctx, path, q)
	if err != nil {
		return err
	}

	rows := make([]map[string]any, 0, len(raws))
	var seen map[string]struct{}
	if sel.Distinct {
		seen = make(map[string]struct{}, len(raws))
	}
	for _, raw := range raws {
		var item struct {
			Fields map[string]any `json:"fields"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("decoding list item: %w", err)
		}
		if item.Fields == nil {
			item.Fields = map[string]any{}
		}
		if sel.Distinct {
			key := distinctKey(item.Fields, cols)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}
		rows = append(rows, item.Fields)
	}

	if len(sel.OrderBy) > 0 {
		sortRowsByKeys(rows, sel.OrderBy, e.Bound.Schema)
	}
	rows = applyOffsetLimit(rows, sel.Offset, sel.Limit)

	return render.Render(e.Out, render.Result{Columns: cols, Rows: rows}, e.Format)
}

// validateOrderBy rejects sort keys that don't name a known schema column.
func (e *Executor) validateOrderBy(keys []parse.OrderKey) error {
	for _, k := range keys {
		if _, ok := e.Bound.Schema[k.Column]; !ok {
			return fmt.Errorf("unknown column %q in ORDER BY", k.Column)
		}
	}
	return nil
}

// sortRowsByKeys does a stable in-place sort by the ORDER BY keys, using the
// SharePoint field type to decide how to compare each pair of values. NULLs
// (nil or missing-from-map) sort to the high end: last in ASC, first in DESC —
// matching the Postgres convention and sqlcsv's behavior.
func sortRowsByKeys(rows []map[string]any, keys []parse.OrderKey, schema map[string]FieldInfo) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, k := range keys {
			cmp := compareFieldValue(rows[i][k.Column], rows[j][k.Column], schema[k.Column].Type)
			if k.Desc {
				cmp = -cmp
			}
			if cmp != 0 {
				return cmp < 0
			}
		}
		return false
	})
}

// compareFieldValue is the per-field comparator used by ORDER BY. It works on
// the raw JSON-decoded value (the same any that lands in the row map after
// json.Unmarshal). Type information from the schema picks the right comparison
// strategy. NULLs sort to the high end.
func compareFieldValue(a, b any, t FieldType) int {
	aNil := a == nil
	bNil := b == nil
	if aNil && bNil {
		return 0
	}
	if aNil {
		return 1
	}
	if bNil {
		return -1
	}
	switch t {
	case FieldNumber:
		af, aok := toFloat(a)
		bf, bok := toFloat(b)
		if aok && bok {
			switch {
			case af < bf:
				return -1
			case af > bf:
				return 1
			}
			return 0
		}
	case FieldBoolean:
		ab, aok := a.(bool)
		bb, bok := b.(bool)
		if aok && bok {
			ai, bi := 0, 0
			if ab {
				ai = 1
			}
			if bb {
				bi = 1
			}
			return ai - bi
		}
	}
	// Text / Note / Choice / DateTime / fallback: compare as strings. ISO 8601
	// datetime strings sort lexically the same as chronologically, so a plain
	// string compare gives the right answer for DateTime fields.
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

// toFloat extracts a numeric value from the JSON-decoded any. Graph returns
// numbers as float64 by default; the string and json.Number cases are defensive
// against future API quirks.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// applyOffsetLimit returns the slice after OFFSET m skips and LIMIT n caps.
// Either may be nil. OFFSET past the end yields empty; LIMIT 0 yields empty.
func applyOffsetLimit(rows []map[string]any, offset, limit *int) []map[string]any {
	if offset != nil {
		if *offset >= len(rows) {
			return rows[:0]
		}
		rows = rows[*offset:]
	}
	if limit != nil && *limit < len(rows) {
		rows = rows[:*limit]
	}
	return rows
}

// distinctKey builds a per-row dedupe key from the projected fields. Each
// field is serialized with its Go-typed JSON encoding behind a length prefix
// so embedded separators cannot collide. Missing fields and explicit nulls
// both encode as the same `N|` sentinel — matching SQL's NULL = NULL semantics
// under DISTINCT.
func distinctKey(fields map[string]any, cols []string) string {
	var b strings.Builder
	for _, name := range cols {
		v, ok := fields[name]
		if !ok || v == nil {
			b.WriteString("N|")
			continue
		}
		// json.Marshal gives a stable, unambiguous typed encoding: strings come
		// out quoted, numbers bare, bools as true/false. A type tag plus the
		// length prefix protects against collisions across types.
		enc, err := json.Marshal(v)
		if err != nil {
			fmt.Fprintf(&b, "X:%d:%v|", 0, v)
			continue
		}
		fmt.Fprintf(&b, "V:%d:%s|", len(enc), enc)
	}
	return b.String()
}

// resolveProjection decides which columns to return. SELECT * uses every
// non-hidden column in schema order (or every column when AllFields is set).
// An explicit column list is validated against the schema; unknown columns
// produce a clear error.
//
// v2 grammar shapes (AS aliases, computed expressions, aggregates) parse
// successfully but error here until the corresponding Pass 3 slice lands.
func (e *Executor) resolveProjection(sel *parse.SelectStmt) ([]string, error) {
	if sel.Star {
		out := make([]string, 0, len(e.Bound.Columns))
		for _, name := range e.Bound.Columns {
			info := e.Bound.Schema[name]
			if !e.AllFields && info.Hidden {
				continue
			}
			out = append(out, name)
		}
		return out, nil
	}
	cols := make([]string, 0, len(sel.Columns))
	for _, pr := range sel.Columns {
		if pr.Alias != "" {
			return nil, fmt.Errorf("AS aliases: SharePoint backend support lands in a later v1.1 slice")
		}
		if _, isAgg := pr.Expr.(*parse.AggregateExpr); isAgg {
			return nil, fmt.Errorf("aggregate expressions: SharePoint backend support lands in a later v1.1 slice")
		}
		name, ok := columnExprName(pr.Expr)
		if !ok {
			return nil, fmt.Errorf("computed projection expressions: SharePoint backend support lands in a later v1.1 slice")
		}
		if _, ok := e.Bound.Schema[name]; !ok {
			return nil, fmt.Errorf("unknown column %q (not in list schema)", name)
		}
		cols = append(cols, name)
	}
	return cols, nil
}
