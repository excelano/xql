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

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/eval"
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
func (e *Executor) Execute(ctx context.Context, stmt parse.Stmt, commit bool) error {
	switch s := stmt.(type) {
	case *parse.SelectStmt:
		return e.executeSelect(ctx, s)
	case *parse.UpdateStmt:
		return e.executeUpdate(ctx, s, commit)
	case *parse.DeleteStmt:
		return e.executeDelete(ctx, s, commit)
	case *parse.InsertStmt:
		return e.executeInsert(ctx, s, commit)
	}
	return fmt.Errorf("internal: unknown statement type %T", stmt)
}

func (e *Executor) executeSelect(ctx context.Context, sel *parse.SelectStmt) error {
	plan, err := e.resolveProjection(sel)
	if err != nil {
		return err
	}
	grouped := len(sel.GroupBy) > 0
	aggregated := grouped || planHasAggregate(plan)
	if sel.Having != nil && !aggregated {
		return fmt.Errorf("HAVING requires GROUP BY or aggregate projections")
	}
	if aggregated {
		if len(sel.OrderBy) > 0 {
			return fmt.Errorf("ORDER BY on aggregated queries: SharePoint backend support lands in a later v1.1 slice")
		}
		if grouped {
			return e.executeGroupedAggregation(ctx, sel, plan)
		}
		return e.executeImplicitAggregation(ctx, sel, plan)
	}
	if err := e.validateOrderBy(sel.OrderBy); err != nil {
		return err
	}

	sourceCols := make([]string, len(plan))
	labelCols := make([]string, len(plan))
	renamed := false
	for i, p := range plan {
		sourceCols[i] = p.Source
		labelCols[i] = p.Label
		if p.Source != p.Label {
			renamed = true
		}
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
			key := distinctKey(item.Fields, sourceCols)
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

	if renamed {
		rows = relabelRows(rows, plan)
	}

	return render.Render(e.Out, render.Result{Columns: labelCols, Rows: rows}, e.Format)
}

// relabelRows builds new per-row maps keyed by the projection's output label,
// pulling each value from the source column. Used when AS aliases (or any
// future expression with a synthetic label) make the renderer's column keys
// differ from the field names returned by Graph.
func relabelRows(rows []map[string]any, plan []projEntry) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		m := make(map[string]any, len(plan))
		for _, p := range plan {
			m[p.Label] = row[p.Source]
		}
		out[i] = m
	}
	return out
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

// projEntry is one entry in the SELECT projection plan. Source is the
// SharePoint field name the value comes from for the bare-column fast path
// (empty when the projection is an aggregate or other expression that needs
// the evaluator). Label is the output column name shown in headers and used
// as the row-map key at render time. Expr is the parse-tree expression
// evaluated per row in the aggregated path; nil in the bare-column fast
// path. Type is the static result type of Expr, used to flatten the
// EvalCell back to a renderable Go value.
//
// Slice F will reuse this shape unchanged; slice G adds ORDER BY by Label.
type projEntry struct {
	Source string
	Label  string
	Expr   parse.Expr
	Type   cell.ColumnType
}

// resolveProjection decides which columns to return. SELECT * uses every
// non-hidden column in schema order (or every column when AllFields is set).
// An explicit column list is validated against the schema; unknown columns
// produce a clear error. AS aliases rename the output header without
// affecting the underlying Graph fetch.
//
// When any projection contains an aggregate, every bare column reference
// must be wrapped in an aggregate (standard SQL implicit-aggregation rule);
// aggregates themselves are validated up front so SUM(Title) fails at plan
// time, before the row scan.
//
// v2 grammar shapes still unsupported (non-aggregate arithmetic
// projections) parse successfully but error here until the corresponding
// Pass 3 slice lands.
func (e *Executor) resolveProjection(sel *parse.SelectStmt) ([]projEntry, error) {
	cellSchema := buildCellSchemaFromFieldInfo(e.Bound.Schema)
	groupCols := make(map[string]bool, len(sel.GroupBy))
	for _, c := range sel.GroupBy {
		if _, ok := e.Bound.Schema[c]; !ok {
			return nil, fmt.Errorf("unknown column %q in GROUP BY", c)
		}
		if groupCols[c] {
			return nil, fmt.Errorf("duplicate column %q in GROUP BY", c)
		}
		groupCols[c] = true
	}
	grouped := len(sel.GroupBy) > 0
	if sel.Star {
		if grouped {
			return nil, fmt.Errorf("SELECT * with GROUP BY is not supported; list the GROUP BY columns explicitly")
		}
		out := make([]projEntry, 0, len(e.Bound.Columns))
		for _, name := range e.Bound.Columns {
			info := e.Bound.Schema[name]
			if !e.AllFields && info.Hidden {
				continue
			}
			out = append(out, projEntry{
				Source: name,
				Label:  name,
				Expr:   &parse.ColumnExpr{Name: name},
				Type:   FieldTypeToCellType(info.Type),
			})
		}
		return out, nil
	}
	anyAgg := false
	for _, pr := range sel.Columns {
		if eval.HasAggregate(pr.Expr) {
			anyAgg = true
			break
		}
	}
	plan := make([]projEntry, 0, len(sel.Columns))
	seen := make(map[string]struct{}, len(sel.Columns))
	for _, pr := range sel.Columns {
		if !anyAgg && !grouped {
			name, ok := columnExprName(pr.Expr)
			if !ok {
				return nil, fmt.Errorf("computed projection expressions: SharePoint backend support lands in a later v1.1 slice")
			}
			info, ok := e.Bound.Schema[name]
			if !ok {
				return nil, fmt.Errorf("unknown column %q (not in list schema)", name)
			}
			label := pr.Alias
			if label == "" {
				label = name
			}
			if _, dup := seen[label]; dup {
				return nil, fmt.Errorf("duplicate output column %q; use AS to give them distinct names", label)
			}
			seen[label] = struct{}{}
			plan = append(plan, projEntry{
				Source: name,
				Label:  label,
				Expr:   pr.Expr,
				Type:   FieldTypeToCellType(info.Type),
			})
			continue
		}
		// Aggregated / grouped projection: bare columns under GROUP BY must
		// name a GROUP BY column; without GROUP BY they must sit inside an
		// aggregate. Each aggregate node is validated up front so SUM(Title)
		// and similar fail at plan time, before any Graph fetch.
		if grouped {
			if bare := eval.BareColumnNotIn(pr.Expr, groupCols); bare != "" {
				return nil, fmt.Errorf("column %q must appear in GROUP BY or be wrapped in an aggregate", bare)
			}
		} else if bare := eval.BareColumn(pr.Expr); bare != "" {
			return nil, fmt.Errorf("column %q must appear inside an aggregate or in GROUP BY", bare)
		}
		if err := eval.ValidateExpr(pr.Expr, cellSchema); err != nil {
			return nil, err
		}
		for _, a := range eval.CollectAggregates(pr.Expr, nil) {
			if err := eval.ValidateAggregate(a, cellSchema); err != nil {
				return nil, err
			}
		}
		t, err := eval.ExprType(pr.Expr, cellSchema)
		if err != nil {
			return nil, err
		}
		label := pr.Alias
		if label == "" {
			label = renderExpr(pr.Expr)
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("duplicate output column %q; use AS to give them distinct names", label)
		}
		seen[label] = struct{}{}
		plan = append(plan, projEntry{Label: label, Expr: pr.Expr, Type: t})
	}
	return plan, nil
}

// planHasAggregate reports whether any projection plan entry contains an
// aggregate. Used by executeSelect to pick the implicit-aggregation path.
func planHasAggregate(plan []projEntry) bool {
	for _, p := range plan {
		if eval.HasAggregate(p.Expr) {
			return true
		}
	}
	return false
}

// executeImplicitAggregation handles SELECT with aggregates and no GROUP BY.
// WHERE still translates to OData and runs server-side; the filtered rows
// are converted to typed cells via the slice A adapter, fed through one
// AggSlot per unique parse.AggregateExpr pointer, then each projection
// expression is evaluated once with the slot results injected via
// ctx.AggResults. The output is always exactly one row, even when the
// filter matched nothing — COUNT(*) returns 0; the other aggregates return
// NULL.
func (e *Executor) executeImplicitAggregation(ctx context.Context, sel *parse.SelectStmt, plan []projEntry) error {
	items, err := e.fetchTargets(ctx, sel.Where)
	if err != nil {
		return err
	}
	tbl, err := BuildCellTable(e.Bound, items)
	if err != nil {
		return err
	}
	labelCols, rows, err := aggregateOneRow(tbl, plan, sel.Having, sel.Offset, sel.Limit)
	if err != nil {
		return err
	}
	return render.Render(e.Out, render.Result{Columns: labelCols, Rows: rows}, e.Format)
}

// aggregateOneRow runs the aggregate slots over every row in tbl, then
// evaluates each projection once and packages the result as the single
// output row the renderer expects. When HAVING is present, every column
// reference inside it must be wrapped in an aggregate (no GROUP BY here);
// if HAVING evaluates false against the finalized aggregates, the row is
// dropped. OFFSET >= 1 and LIMIT 0 both clip the row out, matching csv's
// behavior — DISTINCT on a single row is a no-op and isn't honored here.
// Pulled out so tests can drive it with a hand-built cell.Table instead
// of a live Graph fetch.
func aggregateOneRow(tbl *cell.Table, plan []projEntry, having parse.Predicate, offset, limit *int) ([]string, []map[string]any, error) {
	if having != nil {
		if err := validateAggregatedHaving(having, nil, tbl.Schema); err != nil {
			return nil, nil, err
		}
	}
	evalCtx := eval.NewEvalContext(tbl)
	slotByExpr := make(map[*parse.AggregateExpr]*eval.AggSlot)
	var slots []*eval.AggSlot
	for _, a := range collectAllAggregates(plan, having) {
		if _, ok := slotByExpr[a]; ok {
			continue
		}
		s, err := eval.NewAggSlot(a, tbl.Schema)
		if err != nil {
			return nil, nil, err
		}
		slotByExpr[a] = s
		slots = append(slots, s)
	}
	for _, row := range tbl.Rows {
		for _, s := range slots {
			if err := s.Advance(row, evalCtx); err != nil {
				return nil, nil, err
			}
		}
	}
	evalCtx.AggResults = make(map[*parse.AggregateExpr]eval.EvalCell, len(slots))
	for a, s := range slotByExpr {
		evalCtx.AggResults[a] = s.Finalize()
	}

	labelCols := make([]string, len(plan))
	out := make(map[string]any, len(plan))
	for i, p := range plan {
		labelCols[i] = p.Label
		// The row arg is unused: the plan rejected bare columns when aggregates
		// are present, so the only column refs left are inside aggregates that
		// resolve through ctx.AggResults rather than the row.
		res, err := eval.EvalExpr(p.Expr, nil, evalCtx)
		if err != nil {
			return nil, nil, err
		}
		out[p.Label] = res.Cell.AsAny(p.Type)
	}

	rows := []map[string]any{out}
	if having != nil {
		// HAVING refs resolve through AggResults; the row arg is a length-
		// correct placeholder so length-indexed predicates don't panic, even
		// though they shouldn't reach the row at all.
		syntheticRow := make(cell.Row, len(tbl.Columns))
		ok, err := eval.Matches(having, syntheticRow, evalCtx)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			rows = rows[:0]
		}
	}
	if offset != nil && *offset >= 1 {
		rows = rows[:0]
	}
	if limit != nil && *limit == 0 {
		rows = rows[:0]
	}
	return labelCols, rows, nil
}

// executeGroupedAggregation handles SELECT ... GROUP BY [HAVING ...]. WHERE
// still translates to OData server-side; the filtered rows are converted to
// typed cells, grouped by the GROUP BY column tuple, and each group's slot
// table advances once per row. After the scan, HAVING (if present) filters
// groups against the finalized aggregates and the projection renders one
// output row per surviving group in insertion order.
func (e *Executor) executeGroupedAggregation(ctx context.Context, sel *parse.SelectStmt, plan []projEntry) error {
	items, err := e.fetchTargets(ctx, sel.Where)
	if err != nil {
		return err
	}
	tbl, err := BuildCellTable(e.Bound, items)
	if err != nil {
		return err
	}
	labelCols, rows, err := aggregateGrouped(tbl, plan, sel)
	if err != nil {
		return err
	}
	return render.Render(e.Out, render.Result{Columns: labelCols, Rows: rows}, e.Format)
}

// aggregateGrouped is the pure, testable core of the grouped-aggregation
// path. It builds one slot table per GROUP BY key, advances each row into
// its group, finalizes, then runs HAVING (if present) and the projection
// against a synthetic row that carries the group's key values. Groups
// emerge in insertion order — the first row to introduce a key wins.
// DISTINCT, OFFSET, and LIMIT apply to the projected output rows.
func aggregateGrouped(tbl *cell.Table, plan []projEntry, sel *parse.SelectStmt) ([]string, []map[string]any, error) {
	evalCtx := eval.NewEvalContext(tbl)
	groupCols := make(map[string]bool, len(sel.GroupBy))
	groupColIdx := make([]int, len(sel.GroupBy))
	groupColTypes := make([]cell.ColumnType, len(sel.GroupBy))
	for i, c := range sel.GroupBy {
		groupCols[c] = true
		groupColIdx[i] = evalCtx.ColIdx[c]
		groupColTypes[i] = tbl.Schema[c].Type
	}
	if sel.Having != nil {
		if err := validateAggregatedHaving(sel.Having, groupCols, tbl.Schema); err != nil {
			return nil, nil, err
		}
	}
	templateAggs := collectAllAggregates(plan, sel.Having)

	type group struct {
		keyCells   []cell.Cell
		slots      []*eval.AggSlot
		slotByExpr map[*parse.AggregateExpr]*eval.AggSlot
	}
	var groupOrder []string
	byKey := make(map[string]*group)

	for _, row := range tbl.Rows {
		key, keyCells := groupKey(row, groupColIdx, groupColTypes)
		g, ok := byKey[key]
		if !ok {
			g = &group{keyCells: keyCells, slotByExpr: make(map[*parse.AggregateExpr]*eval.AggSlot, len(templateAggs))}
			for _, a := range templateAggs {
				s, err := eval.NewAggSlot(a, tbl.Schema)
				if err != nil {
					return nil, nil, err
				}
				g.slots = append(g.slots, s)
				g.slotByExpr[a] = s
			}
			byKey[key] = g
			groupOrder = append(groupOrder, key)
		}
		for _, s := range g.slots {
			if err := s.Advance(row, evalCtx); err != nil {
				return nil, nil, err
			}
		}
	}

	labelCols := make([]string, len(plan))
	for i, p := range plan {
		labelCols[i] = p.Label
	}

	rowsOut := make([]map[string]any, 0, len(groupOrder))
	syntheticRow := make(cell.Row, len(tbl.Columns))
	for _, key := range groupOrder {
		g := byKey[key]
		for i := range syntheticRow {
			syntheticRow[i] = cell.Cell{}
		}
		for i, col := range sel.GroupBy {
			syntheticRow[evalCtx.ColIdx[col]] = g.keyCells[i]
		}
		evalCtx.AggResults = make(map[*parse.AggregateExpr]eval.EvalCell, len(g.slots))
		for a, s := range g.slotByExpr {
			evalCtx.AggResults[a] = s.Finalize()
		}
		if sel.Having != nil {
			ok, err := eval.Matches(sel.Having, syntheticRow, evalCtx)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				continue
			}
		}
		m := make(map[string]any, len(plan))
		for _, p := range plan {
			res, err := eval.EvalExpr(p.Expr, syntheticRow, evalCtx)
			if err != nil {
				return nil, nil, err
			}
			m[p.Label] = res.Cell.AsAny(p.Type)
		}
		rowsOut = append(rowsOut, m)
	}

	if sel.Distinct {
		seen := make(map[string]struct{}, len(rowsOut))
		out := rowsOut[:0]
		for _, r := range rowsOut {
			key := distinctKey(r, labelCols)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, r)
		}
		rowsOut = out
	}

	rowsOut = applyOffsetLimit(rowsOut, sel.Offset, sel.Limit)
	return labelCols, rowsOut, nil
}

// validateAggregatedHaving applies the projection's column-reference rules
// to a HAVING predicate: bare columns must appear in the allowed (GROUP BY)
// set, or be wrapped in an aggregate. For implicit aggregation (no GROUP
// BY), allowed is empty and any bare column produces an error. parse.NullTest,
// LIKE, IN, and BETWEEN bind to bare column names by shape, so they require
// their column to live in the allowed set.
//
// Copied from internal/csv/exec.go per the copy-and-diff convention.
func validateAggregatedHaving(p parse.Predicate, allowed map[string]bool, schema map[string]cell.ColumnInfo) error {
	switch n := p.(type) {
	case *parse.BinaryOp:
		if err := validateAggregatedHaving(n.L, allowed, schema); err != nil {
			return err
		}
		return validateAggregatedHaving(n.R, allowed, schema)
	case *parse.NotOp:
		return validateAggregatedHaving(n.Inner, allowed, schema)
	case *parse.Comparison:
		if err := eval.ValidateExpr(n.LExpr, schema); err != nil {
			return err
		}
		if bare := eval.BareColumnNotIn(n.LExpr, allowed); bare != "" {
			if len(allowed) == 0 {
				return fmt.Errorf("HAVING: column %q must appear inside an aggregate (no GROUP BY)", bare)
			}
			return fmt.Errorf("HAVING: column %q must appear in GROUP BY or be wrapped in an aggregate", bare)
		}
		for _, a := range eval.CollectAggregates(n.LExpr, nil) {
			if err := eval.ValidateAggregate(a, schema); err != nil {
				return err
			}
		}
		return nil
	case *parse.NullTest:
		return havingRequiresGroupCol(n.Column, allowed)
	case *parse.LikeOp:
		return havingRequiresGroupCol(n.Column, allowed)
	case *parse.InOp:
		return havingRequiresGroupCol(n.Column, allowed)
	case *parse.BetweenOp:
		return havingRequiresGroupCol(n.Column, allowed)
	}
	return fmt.Errorf("internal: unhandled HAVING predicate type %T", p)
}

func havingRequiresGroupCol(col string, allowed map[string]bool) error {
	if !allowed[col] {
		if len(allowed) == 0 {
			return fmt.Errorf("HAVING: column %q can only appear under GROUP BY (no GROUP BY here)", col)
		}
		return fmt.Errorf("HAVING: column %q must appear in GROUP BY", col)
	}
	return nil
}

// collectAllAggregates pulls aggregate nodes from every plan expression and
// the HAVING predicate into one ordered, deduplicated list. Slot allocation
// keys on parse.AggregateExpr pointer identity, so sharing across projection
// and HAVING avoids accumulating the same aggregate twice.
//
// Copied from internal/csv/exec.go per the copy-and-diff convention.
func collectAllAggregates(plan []projEntry, having parse.Predicate) []*parse.AggregateExpr {
	var out []*parse.AggregateExpr
	seen := make(map[*parse.AggregateExpr]bool)
	add := func(a *parse.AggregateExpr) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, p := range plan {
		for _, a := range eval.CollectAggregates(p.Expr, nil) {
			add(a)
		}
	}
	if having != nil {
		for _, a := range eval.CollectAggregatesFromPredicate(having, nil) {
			add(a)
		}
	}
	return out
}

// groupKey builds a stable string key from a row's GROUP BY column values,
// using a typed encoding so mixed types, NULL groups, and string-boundary
// cases all distinguish cleanly. The returned cells are the per-key values
// in GROUP BY order; the caller stashes them on the group so the projection
// can re-emit them via the synthetic row.
//
// Copied from internal/csv/exec.go per the copy-and-diff convention.
func groupKey(row cell.Row, idx []int, types []cell.ColumnType) (string, []cell.Cell) {
	cells := make([]cell.Cell, len(idx))
	var b strings.Builder
	for i, ci := range idx {
		c := row[ci]
		cells[i] = c
		if c.Null {
			b.WriteString("N|")
			continue
		}
		switch types[i] {
		case cell.TypeInt:
			fmt.Fprintf(&b, "I:%d|", c.Int)
		case cell.TypeFloat:
			fmt.Fprintf(&b, "F:%g|", c.Float)
		case cell.TypeBool:
			fmt.Fprintf(&b, "B:%t|", c.Bool)
		case cell.TypeDate:
			fmt.Fprintf(&b, "D:%d|", c.Date.UnixNano())
		default:
			fmt.Fprintf(&b, "S:%d:%s|", len(c.Str), c.Str)
		}
	}
	return b.String(), cells
}

// executeUpdate runs UPDATE SET ... [WHERE ...]. Always validates assignments
// and previews the affected rows; only when commit=true does it issue PATCHes.
func (e *Executor) executeUpdate(ctx context.Context, upd *parse.UpdateStmt, commit bool) error {
	if err := e.validateAssignments(upd.Assignments); err != nil {
		return err
	}

	items, err := e.fetchTargets(ctx, upd.Where)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would update %d row%s in %s:\n", len(items), plural(len(items)), e.Bound.DisplayName)
	for _, a := range upd.Assignments {
		fmt.Fprintf(e.Out, "  SET %s = %s\n", a.Column, renderExpr(a.Value))
	}
	e.printSample(items)

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}
	if len(items) == 0 {
		return nil
	}

	tbl, err := BuildCellTable(e.Bound, items)
	if err != nil {
		return fmt.Errorf("preparing rows for evaluation: %w", err)
	}
	evalCtx := eval.NewEvalContext(tbl)

	succ := 0
	for i, it := range items {
		body, berr := e.buildRowBody(upd.Assignments, tbl.Rows[i], evalCtx)
		if berr != nil {
			fmt.Fprintf(e.Out, "  id=%s: %v\n", it.ID, berr)
			continue
		}
		path := fmt.Sprintf("/sites/%s/lists/%s/items/%s/fields", e.Bound.SiteID, e.Bound.ListID, it.ID)
		if _, err := e.Graph.patch(ctx, path, body); err != nil {
			fmt.Fprintf(e.Out, "  id=%s: %v\n", it.ID, err)
			continue
		}
		succ++
	}
	fmt.Fprintf(e.Out, "Updated %d of %d row%s.\n", succ, len(items), plural(len(items)))
	if succ < len(items) {
		return fmt.Errorf("%d row%s failed to update", len(items)-succ, plural(len(items)-succ))
	}
	return nil
}

// executeDelete runs DELETE [WHERE ...]. Bare DELETE (no WHERE) is the
// nuclear option and additionally requires ConfirmDestructive when commit=true.
func (e *Executor) executeDelete(ctx context.Context, del *parse.DeleteStmt, commit bool) error {
	// Bare DELETE in --exec mode requires --confirm-destructive; the y/N
	// prompt isn't available there to catch a mistake. In REPL mode, the
	// trailing '!' shortcut is downgraded so the user still sees the "Apply?
	// [y/N]" prompt — bare DELETE is destructive enough that a one-character
	// typo shouldn't be able to wipe the list.
	if del.Where == nil && commit && !e.ConfirmDestructive && e.Confirm == nil {
		return fmt.Errorf("bare DELETE (no WHERE) requires --confirm-destructive")
	}
	if del.Where == nil && e.Confirm != nil {
		commit = false
	}

	items, err := e.fetchTargets(ctx, del.Where)
	if err != nil {
		return err
	}

	if del.Where == nil {
		fmt.Fprintf(e.Out, "Would delete ALL %d row%s from %s:\n", len(items), plural(len(items)), e.Bound.DisplayName)
	} else {
		fmt.Fprintf(e.Out, "Would delete %d row%s from %s:\n", len(items), plural(len(items)), e.Bound.DisplayName)
	}
	e.printSample(items)

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}
	if len(items) == 0 {
		return nil
	}

	succ := 0
	for _, it := range items {
		path := fmt.Sprintf("/sites/%s/lists/%s/items/%s", e.Bound.SiteID, e.Bound.ListID, it.ID)
		if err := e.Graph.delete(ctx, path); err != nil {
			fmt.Fprintf(e.Out, "  id=%s: %v\n", it.ID, err)
			continue
		}
		succ++
	}
	fmt.Fprintf(e.Out, "Deleted %d of %d row%s.\n", succ, len(items), plural(len(items)))
	if succ < len(items) {
		return fmt.Errorf("%d row%s failed to delete", len(items)-succ, plural(len(items)-succ))
	}
	return nil
}

// executeInsert runs INSERT (cols) VALUES (vals). Validates column/value
// pairing and types; previews the row; only POSTs when commit=true.
func (e *Executor) executeInsert(ctx context.Context, ins *parse.InsertStmt, commit bool) error {
	if len(ins.Columns) != len(ins.Values) {
		return fmt.Errorf("INSERT has %d column%s but %d value%s", len(ins.Columns), plural(len(ins.Columns)), len(ins.Values), plural(len(ins.Values)))
	}
	seen := map[string]bool{}
	for _, c := range ins.Columns {
		if seen[c] {
			return fmt.Errorf("INSERT column %q appears twice", c)
		}
		seen[c] = true
	}
	assigns := make([]parse.Assignment, len(ins.Columns))
	for i, c := range ins.Columns {
		assigns[i] = parse.Assignment{Column: c, Value: &parse.LiteralExpr{Value: ins.Values[i]}}
	}
	if err := e.validateAssignments(assigns); err != nil {
		return err
	}
	body, err := e.buildRowBody(assigns, nil, nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would insert row into %s:\n", e.Bound.DisplayName)
	for _, c := range ins.Columns {
		fmt.Fprintf(e.Out, "  %s = %s\n", c, jsonInline(body[c]))
	}

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}

	path := fmt.Sprintf("/sites/%s/lists/%s/items", e.Bound.SiteID, e.Bound.ListID)
	resp, err := e.Graph.post(ctx, path, map[string]any{"fields": body})
	if err != nil {
		return err
	}
	var created struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal(resp, &created); jerr == nil && created.ID != "" {
		fmt.Fprintf(e.Out, "Inserted row id=%s.\n", created.ID)
	} else {
		fmt.Fprintln(e.Out, "Inserted row.")
	}
	return nil
}

// decideCommit resolves a write's commit/abort decision after the preview has
// been shown. Three outcomes:
//   - commit=true (trailing '!' in REPL, --commit in --exec): proceed silently.
//   - REPL (Confirm != nil): ask the user; on "y", proceed; otherwise "(aborted)".
//   - --exec without --commit (Confirm == nil): never commit; print the
//     "(dry run; pass --commit to apply)" hint.
//
// The returned message, when non-empty, should be printed before the function
// returns; "" means proceed without further output.
func (e *Executor) decideCommit(commit bool) (bool, string) {
	if commit {
		return true, ""
	}
	if e.Confirm == nil {
		return false, "(dry run; pass --commit to apply)"
	}
	if e.Confirm() {
		return true, ""
	}
	return false, "(aborted)"
}

// listItem is the minimal subset of a list item resource the write path needs:
// the numeric id and the user fields subobject (for previews and ID-based
// PATCH/DELETE URLs).
type listItem struct {
	ID     string
	Fields map[string]any
}

// fetchTargets runs the equivalent of SELECT * WHERE <pred> and returns the
// matched items as listItem records. nil WHERE returns every row in the list.
func (e *Executor) fetchTargets(ctx context.Context, where parse.Predicate) ([]listItem, error) {
	q := url.Values{"$expand": {"fields"}}
	if where != nil {
		filter, err := ToOData(where, e.Bound.Schema)
		if err != nil {
			return nil, err
		}
		q.Set("$filter", filter)
	}
	path := fmt.Sprintf("/sites/%s/lists/%s/items", e.Bound.SiteID, e.Bound.ListID)
	raws, err := e.Graph.getAll(ctx, path, q)
	if err != nil {
		return nil, err
	}
	items := make([]listItem, 0, len(raws))
	for _, raw := range raws {
		var it struct {
			ID     string         `json:"id"`
			Fields map[string]any `json:"fields"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, fmt.Errorf("decoding list item: %w", err)
		}
		if it.Fields == nil {
			it.Fields = map[string]any{}
		}
		items = append(items, listItem{ID: it.ID, Fields: it.Fields})
	}
	return items, nil
}

// validateAssignments enforces v1 write rules on each assignment before
// the executor fetches any data: each target column must exist, be writable,
// have a supported type, and the RHS must be either a literal whose kind
// matches the column type, or a computed expression that references only
// existing columns, contains no aggregates, and produces a result type
// compatible with the target column. Failing fast here means a bad UPDATE
// surfaces its error without burning a Graph round-trip.
func (e *Executor) validateAssignments(assigns []parse.Assignment) error {
	cellSchema := buildCellSchemaFromFieldInfo(e.Bound.Schema)
	for _, a := range assigns {
		if lit, ok := a.Value.(*parse.LiteralExpr); ok {
			if _, err := validateAssignment(a.Column, lit.Value, e.Bound.Schema); err != nil {
				return err
			}
			continue
		}
		if err := e.validateAssignmentExpr(a.Column, a.Value, cellSchema); err != nil {
			return err
		}
	}
	return nil
}

// validateAssignmentExpr handles the computed-RHS path: schema + aggregate
// check + result-type compatibility against the target SharePoint column.
func (e *Executor) validateAssignmentExpr(col string, expr parse.Expr, cellSchema map[string]cell.ColumnInfo) error {
	if eval.HasAggregate(expr) {
		return fmt.Errorf("column %q: aggregate functions in UPDATE SET — SharePoint backend support lands in a later v1.1 slice", col)
	}
	if err := eval.ValidateExpr(expr, cellSchema); err != nil {
		return fmt.Errorf("column %q: %w", col, err)
	}
	info, ok := e.Bound.Schema[col]
	if !ok {
		return fmt.Errorf("unknown column %q", col)
	}
	if info.ReadOnly {
		return fmt.Errorf("column %q is read-only", col)
	}
	if !isWritableType(info.Type) {
		return fmt.Errorf("column %q has type %s; writes to %s columns are not supported in v1", col, info.Type, info.Type)
	}
	resultType, err := eval.ExprType(expr, cellSchema)
	if err != nil {
		return fmt.Errorf("column %q: %w", col, err)
	}
	if !typesCompatibleForUpdate(resultType, info.Type) {
		return fmt.Errorf("column %q: expression produces %s, target SharePoint column is %s", col, resultType, info.Type)
	}
	return nil
}

// typesCompatibleForUpdate gates which expression-result types can flow
// into a write at a given SharePoint column. Text/Note/Choice accept any
// scalar (we stringify); Number requires numeric; Boolean requires bool;
// DateTime requires either a date result or a string we can parse.
func typesCompatibleForUpdate(src cell.ColumnType, target FieldType) bool {
	switch target {
	case FieldText, FieldNote, FieldChoice:
		return true
	case FieldNumber:
		return src == cell.TypeInt || src == cell.TypeFloat
	case FieldBoolean:
		return src == cell.TypeBool
	case FieldDateTime:
		return src == cell.TypeString || src == cell.TypeDate
	}
	return false
}

// buildRowBody produces the JSON-encodable map ready for a Graph PATCH or
// POST {"fields": ...} body. Literal assignments use the validated literal
// path; computed assignments evaluate against the supplied row via
// internal/eval and convert the typed result back to the JSON shape Graph
// expects. Validation should have run first via validateAssignments; this
// path skips re-validation in the hot per-row loop.
//
// INSERT calls this with (nil, nil) because INSERT values are parser-level
// literals only; the eval branch is never taken there.
func (e *Executor) buildRowBody(assigns []parse.Assignment, row cell.Row, ctx *eval.EvalContext) (map[string]any, error) {
	body := map[string]any{}
	for _, a := range assigns {
		if lit, ok := a.Value.(*parse.LiteralExpr); ok {
			info := e.Bound.Schema[a.Column]
			fj, err := valueToFieldJSON(lit.Value, info.Type)
			if err != nil {
				return nil, fmt.Errorf("column %q: %v", a.Column, err)
			}
			body[a.Column] = fj
			continue
		}
		info := e.Bound.Schema[a.Column]
		result, err := eval.EvalExpr(a.Value, row, ctx)
		if err != nil {
			return nil, fmt.Errorf("column %q: %v", a.Column, err)
		}
		fj, err := evalCellToFieldJSON(result, info.Type)
		if err != nil {
			return nil, fmt.Errorf("column %q: %v", a.Column, err)
		}
		body[a.Column] = fj
	}
	return body, nil
}

// evalCellToFieldJSON converts a typed expression result to the JSON shape
// Graph expects in a fields body. NULL universally maps to JSON null; for
// non-null values, we honor the target SharePoint column's type (e.g.
// integer-valued floats coerce to int64 so Graph stores them without a
// spurious decimal point, and string expression results bound for a
// DateTime column get parsed and normalized).
func evalCellToFieldJSON(c eval.EvalCell, target FieldType) (any, error) {
	if c.Cell.Null {
		return nil, nil
	}
	switch target {
	case FieldText, FieldNote, FieldChoice:
		switch c.Type {
		case cell.TypeString:
			return c.Cell.Str, nil
		case cell.TypeInt:
			return strconv.FormatInt(c.Cell.Int, 10), nil
		case cell.TypeFloat:
			return formatNumberAsString(c.Cell.Float), nil
		case cell.TypeBool:
			if c.Cell.Bool {
				return "true", nil
			}
			return "false", nil
		case cell.TypeDate:
			return c.Cell.Date.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	case FieldNumber:
		switch c.Type {
		case cell.TypeInt:
			return c.Cell.Int, nil
		case cell.TypeFloat:
			if c.Cell.Float == float64(int64(c.Cell.Float)) {
				return int64(c.Cell.Float), nil
			}
			return c.Cell.Float, nil
		}
	case FieldBoolean:
		if c.Type == cell.TypeBool {
			return c.Cell.Bool, nil
		}
	case FieldDateTime:
		switch c.Type {
		case cell.TypeDate:
			return c.Cell.Date.UTC().Format("2006-01-02T15:04:05Z"), nil
		case cell.TypeString:
			ts, err := cell.ParseDateString(c.Cell.Str)
			if err != nil {
				return nil, fmt.Errorf("invalid datetime %q: %w", c.Cell.Str, err)
			}
			return ts.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	}
	return nil, fmt.Errorf("cannot coerce %s expression result to %s target", c.Type, target)
}

// buildCellSchemaFromFieldInfo is the FieldInfo-to-cell.ColumnInfo map used
// by validation paths that don't need the full Bound.Columns ordering.
func buildCellSchemaFromFieldInfo(schema map[string]FieldInfo) map[string]cell.ColumnInfo {
	out := make(map[string]cell.ColumnInfo, len(schema))
	for name, fi := range schema {
		out[name] = cell.ColumnInfo{Name: name, Type: FieldTypeToCellType(fi.Type)}
	}
	return out
}

// printSample emits a small preview table: id + a primary column (Title when
// present, else the first user column). At most previewSampleMax rows; a
// trailing "... N more" line counts what was elided.
func (e *Executor) printSample(items []listItem) {
	if len(items) == 0 {
		return
	}
	previewCols := e.previewColumns()
	header := append([]string{"id"}, previewCols...)
	sample := items
	if len(sample) > previewSampleMax {
		sample = sample[:previewSampleMax]
	}
	rows := make([]map[string]any, len(sample))
	for i, it := range sample {
		row := map[string]any{"id": it.ID}
		for _, c := range previewCols {
			row[c] = it.Fields[c]
		}
		rows[i] = row
	}
	fmt.Fprintln(e.Out, "Sample:")
	_ = render.WriteTableBody(e.Out, header, rows)
	if len(items) > previewSampleMax {
		fmt.Fprintf(e.Out, "  ... %d more\n", len(items)-previewSampleMax)
	}
}

const previewSampleMax = 5

// previewColumns returns the column(s) to show alongside the id in write
// previews. Prefers Title; otherwise the first non-hidden non-readonly user
// column.
func (e *Executor) previewColumns() []string {
	if _, ok := e.Bound.Schema["Title"]; ok {
		return []string{"Title"}
	}
	for _, c := range e.Bound.Columns {
		info := e.Bound.Schema[c]
		if info.Hidden || info.ReadOnly {
			continue
		}
		return []string{c}
	}
	return nil
}

// jsonInline produces a compact JSON literal for display in previews. Strings
// come out quoted, numbers and booleans bare, NULL as `null` — matching what
// will actually be sent over the wire.
func jsonInline(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// validateAssignment enforces v1 write rules: the column must exist and be
// writable (not read-only, type in the supported set), and the literal value
// must match the column's type (NULL is universal for any writable type).
func validateAssignment(col string, v parse.Value, schema map[string]FieldInfo) (FieldInfo, error) {
	info, ok := schema[col]
	if !ok {
		return FieldInfo{}, fmt.Errorf("unknown column %q", col)
	}
	if info.ReadOnly {
		return FieldInfo{}, fmt.Errorf("column %q is read-only", col)
	}
	if !isWritableType(info.Type) {
		return FieldInfo{}, fmt.Errorf("column %q has type %s; writes to %s columns are not supported in v1", col, info.Type, info.Type)
	}
	if v.Kind == parse.ValNull {
		return info, nil
	}
	if err := valueMatchesType(v, info.Type); err != nil {
		return FieldInfo{}, fmt.Errorf("column %q: %v", col, err)
	}
	return info, nil
}

func isWritableType(t FieldType) bool {
	switch t {
	case FieldText, FieldNote, FieldNumber, FieldBoolean, FieldDateTime, FieldChoice:
		return true
	}
	return false
}

func valueMatchesType(v parse.Value, t FieldType) error {
	switch t {
	case FieldText, FieldNote, FieldChoice:
		if v.Kind != parse.ValString {
			return fmt.Errorf("expected string, got %s", valueKindName(v.Kind))
		}
	case FieldNumber:
		if v.Kind != parse.ValNumber {
			return fmt.Errorf("expected number, got %s", valueKindName(v.Kind))
		}
	case FieldBoolean:
		if v.Kind != parse.ValBool {
			return fmt.Errorf("expected true or false, got %s", valueKindName(v.Kind))
		}
	case FieldDateTime:
		if v.Kind != parse.ValString {
			return fmt.Errorf("expected ISO 8601 datetime string, got %s", valueKindName(v.Kind))
		}
		if _, err := normalizeDateTime(v.Str); err != nil {
			return fmt.Errorf("invalid datetime %q", v.Str)
		}
	}
	return nil
}

func valueKindName(k parse.ValueKind) string {
	switch k {
	case parse.ValString:
		return "string"
	case parse.ValNumber:
		return "number"
	case parse.ValBool:
		return "boolean"
	case parse.ValNull:
		return "null"
	}
	return "unknown"
}

// valueToFieldJSON returns the JSON-encodable Go value Graph expects in a
// PATCH body for the given field type. Integer-valued numbers come out as
// int64 so they marshal without a decimal point.
func valueToFieldJSON(v parse.Value, t FieldType) (any, error) {
	if v.Kind == parse.ValNull {
		return nil, nil
	}
	switch t {
	case FieldText, FieldNote, FieldChoice:
		return v.Str, nil
	case FieldNumber:
		if n, err := strconv.ParseInt(v.Num, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(v.Num, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing number %q", v.Num)
		}
		return f, nil
	case FieldBoolean:
		return v.Bool, nil
	case FieldDateTime:
		s, err := normalizeDateTime(v.Str)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	return nil, fmt.Errorf("internal: cannot serialize value to type %s", t)
}
