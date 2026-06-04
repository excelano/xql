package csv

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/eval"
	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/render"
)

// Executor binds a parsed statement to the loaded CSV cell.Table and runs it.
// One Executor per session.
//
// Confirm is the interactive "Apply? [y/N]" callback used by the REPL. When
// non-nil, write executors will call it after the dry-run preview to decide
// whether to commit (unless commit is already true via the trailing '!').
// --exec mode leaves Confirm nil so writes either dry-run or commit explicitly
// based on --commit.
//
// OutputPath, when non-empty, redirects committed writes to a different file
// than the bound CSV. Empty means "write back to cell.Table.Path".
type Executor struct {
	Table              *cell.Table
	Format             string
	ConfirmDestructive bool
	Confirm            func() bool
	OutputPath         string
	Out                io.Writer
}

// Execute dispatches to the per-statement handler. The commit flag distinguishes
// dry-run (commit=false: preview only) from a real write (commit=true: preview
// + apply). It is ignored for SELECT.
func (e *Executor) Execute(stmt parse.Stmt, commit bool) error {
	switch s := stmt.(type) {
	case *parse.SelectStmt:
		return e.executeSelect(s)
	case *parse.UpdateStmt:
		return e.executeUpdate(s, commit)
	case *parse.DeleteStmt:
		return e.executeDelete(s, commit)
	case *parse.InsertStmt:
		return e.executeInsert(s, commit)
	}
	return fmt.Errorf("internal: unknown statement type %T", stmt)
}

func (e *Executor) executeSelect(sel *parse.SelectStmt) error {
	plan, err := e.planProjection(sel)
	if err != nil {
		return err
	}
	if err := eval.ValidatePredicate(sel.Where, e.Table.Schema); err != nil {
		return err
	}
	ctx := eval.NewEvalContext(e.Table)

	matched := make([]int, 0, len(e.Table.Rows))
	for i, row := range e.Table.Rows {
		ok, err := eval.Matches(sel.Where, row, ctx)
		if err != nil {
			return err
		}
		if ok {
			matched = append(matched, i)
		}
	}

	grouped := len(sel.GroupBy) > 0
	aggregated := grouped
	if !grouped {
		for _, p := range plan {
			if eval.HasAggregate(p.Expr) {
				aggregated = true
				break
			}
		}
	}
	if sel.Having != nil && !aggregated {
		return fmt.Errorf("HAVING requires GROUP BY or aggregate projections")
	}

	// Resolve ORDER BY against the right symbol space. Non-aggregated queries
	// sort source rows before projection (existing path, schema-validated);
	// aggregated queries sort projected output rows by SELECT-list label.
	var orderPlan []orderEntry
	if len(sel.OrderBy) > 0 {
		if aggregated {
			orderPlan, err = resolveOrderByOutput(sel.OrderBy, plan)
			if err != nil {
				return err
			}
		} else {
			if err := e.validateOrderBy(sel.OrderBy); err != nil {
				return err
			}
		}
	}

	var projected [][]cell.Cell
	if grouped {
		projected, err = e.evalGroupedAggregation(sel, plan, matched, ctx)
		if err != nil {
			return err
		}
	} else if aggregated {
		projected, err = e.evalImplicitAggregation(plan, sel.Having, matched, ctx)
		if err != nil {
			return err
		}
		if sel.Having != nil {
			projected, err = e.applyImplicitHaving(sel.Having, projected, ctx)
			if err != nil {
				return err
			}
		}
	} else {
		if len(sel.OrderBy) > 0 {
			e.sortByKeys(matched, sel.OrderBy, ctx)
		}
		// Evaluate the projection per matched row before DISTINCT / LIMIT so
		// dedup operates on the user-visible output values rather than raw
		// source cells. SELECT DISTINCT price * 0 collapses across all rows.
		projected = make([][]cell.Cell, 0, len(matched))
		for _, idx := range matched {
			row := e.Table.Rows[idx]
			out := make([]cell.Cell, len(plan))
			for i, p := range plan {
				res, err := eval.EvalExpr(p.Expr, row, ctx)
				if err != nil {
					return err
				}
				out[i] = res.Cell
			}
			projected = append(projected, out)
		}
	}

	if sel.Distinct {
		seen := make(map[string]struct{}, len(projected))
		out := projected[:0]
		for _, pr := range projected {
			key := projectedKey(pr, plan)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, pr)
		}
		projected = out
	}

	// Aggregated ORDER BY runs after DISTINCT and before OFFSET/LIMIT, the
	// standard SQL pipeline position. Non-aggregated sort happened before
	// projection above.
	if aggregated && len(orderPlan) > 0 {
		sortOutputRows(projected, orderPlan)
	}

	projected = applyOffsetLimitRows(projected, sel.Offset, sel.Limit)

	labels := make([]string, len(plan))
	for i, p := range plan {
		labels[i] = p.Label
	}
	rows := make([]map[string]any, len(projected))
	for i, pr := range projected {
		m := make(map[string]any, len(plan))
		for j, p := range plan {
			m[p.Label] = pr[j].AsAny(p.Type)
		}
		rows[i] = m
	}
	return render.Render(e.Out, render.Result{Columns: labels, Rows: rows}, e.Format)
}

// projEntry is one entry in the SELECT projection plan: the output column
// label, the expression to evaluate per row, and the result type used for
// dedup key formatting and rendering.
type projEntry struct {
	Label string
	Type  cell.ColumnType
	Expr  parse.Expr
}

// evalGroupedAggregation handles SELECT ... GROUP BY [HAVING ...]. Each
// matched row contributes to the group identified by its GROUP BY column
// tuple; the first time a key is seen the group is allocated with a fresh
// slot table that covers every aggregate found in projection AND HAVING.
// After the scan, each group's slots finalize and the per-group projection
// runs against a synthetic row that carries only the GROUP BY column
// values (every other projection reference is either inside an aggregate
// or rejected at plan time). Groups emerge in insertion order — first row
// to introduce a key wins.
func (e *Executor) evalGroupedAggregation(sel *parse.SelectStmt, plan []projEntry, matched []int, ctx *eval.EvalContext) ([][]cell.Cell, error) {
	groupCols := make(map[string]bool, len(sel.GroupBy))
	groupColIdx := make([]int, len(sel.GroupBy))
	groupColTypes := make([]cell.ColumnType, len(sel.GroupBy))
	for i, c := range sel.GroupBy {
		groupCols[c] = true
		groupColIdx[i] = ctx.ColIdx[c]
		groupColTypes[i] = e.Table.Schema[c].Type
	}
	if sel.Having != nil {
		if err := validateAggregatedHaving(sel.Having, groupCols, e.Table.Schema); err != nil {
			return nil, err
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

	for _, idx := range matched {
		row := e.Table.Rows[idx]
		key, keyCells := groupKey(row, groupColIdx, groupColTypes)
		g, ok := byKey[key]
		if !ok {
			g = &group{keyCells: keyCells, slotByExpr: make(map[*parse.AggregateExpr]*eval.AggSlot, len(templateAggs))}
			for _, a := range templateAggs {
				s, err := eval.NewAggSlot(a, e.Table.Schema)
				if err != nil {
					return nil, err
				}
				g.slots = append(g.slots, s)
				g.slotByExpr[a] = s
			}
			byKey[key] = g
			groupOrder = append(groupOrder, key)
		}
		for _, s := range g.slots {
			if err := s.Advance(row, ctx); err != nil {
				return nil, err
			}
		}
	}

	out := make([][]cell.Cell, 0, len(groupOrder))
	syntheticRow := make(cell.Row, len(e.Table.Columns))
	for _, key := range groupOrder {
		g := byKey[key]
		for i := range syntheticRow {
			syntheticRow[i] = cell.Cell{}
		}
		for i, col := range sel.GroupBy {
			syntheticRow[ctx.ColIdx[col]] = g.keyCells[i]
		}
		ctx.AggResults = make(map[*parse.AggregateExpr]eval.EvalCell, len(g.slots))
		for a, s := range g.slotByExpr {
			ctx.AggResults[a] = s.Finalize()
		}
		if sel.Having != nil {
			ok, err := eval.Matches(sel.Having, syntheticRow, ctx)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		rowOut := make([]cell.Cell, len(plan))
		for i, p := range plan {
			res, err := eval.EvalExpr(p.Expr, syntheticRow, ctx)
			if err != nil {
				return nil, err
			}
			rowOut[i] = res.Cell
		}
		out = append(out, rowOut)
	}
	return out, nil
}

// applyImplicitHaving filters the single row produced by implicit
// aggregation through a HAVING predicate. The aggregate ctx state is
// already populated by evalImplicitAggregation, so eval.Matches sees the
// finalized values via ctx.AggResults. The HAVING predicate is restricted
// to aggregate expressions (no bare columns); the synthetic row is a
// length-correct placeholder so length-indexed predicates don't panic
// even though they shouldn't reach the row at all.
func (e *Executor) applyImplicitHaving(having parse.Predicate, projected [][]cell.Cell, ctx *eval.EvalContext) ([][]cell.Cell, error) {
	if err := validateAggregatedHaving(having, nil, e.Table.Schema); err != nil {
		return nil, err
	}
	if len(projected) == 0 {
		return projected, nil
	}
	row := make(cell.Row, len(e.Table.Columns))
	ok, err := eval.Matches(having, row, ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return [][]cell.Cell{}, nil
	}
	return projected, nil
}

// validateAggregatedHaving applies the same column-reference rules to a
// HAVING predicate that the projection uses: bare columns must be in the
// allowed (GROUP BY) set; aggregates are validated for argument shape.
// For implicit aggregation, allowed is empty — any bare column produces an
// error. parse.NullTest, LIKE, IN, and BETWEEN bind to bare column names by
// shape, so they require their column to be in the allowed set.
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
// the HAVING predicate into one ordered, deduplicated list. The slot table
// allocates one slot per unique parse.AggregateExpr pointer; sharing across
// projection and HAVING avoids accumulating COUNT(*) twice when both
// reference it.
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
// using the same encoding scheme as projectedKey so mixed types, NULL
// groups, and string-boundary cases all distinguish cleanly. The returned
// cells are the per-key values in GROUP BY order; the caller stashes them
// on the group so the projection can re-emit them later.
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

// evalImplicitAggregation handles SELECT with aggregates and no GROUP BY:
// one slot per unique parse.AggregateExpr pointer, advance once per matched row,
// finalize, then evaluate each projection expression once with the slot
// values injected via ctx.AggResults. The result is always exactly one
// output row, even when no rows matched the WHERE — COUNT(*) returns 0 and
// the other aggregates return NULL. When HAVING is also present, its
// aggregates must share the slot table so the predicate evaluator can
// resolve them by pointer identity.
func (e *Executor) evalImplicitAggregation(plan []projEntry, having parse.Predicate, matched []int, ctx *eval.EvalContext) ([][]cell.Cell, error) {
	slotByExpr := make(map[*parse.AggregateExpr]*eval.AggSlot)
	var slots []*eval.AggSlot
	for _, a := range collectAllAggregates(plan, having) {
		if _, ok := slotByExpr[a]; ok {
			continue
		}
		s, err := eval.NewAggSlot(a, e.Table.Schema)
		if err != nil {
			return nil, err
		}
		slotByExpr[a] = s
		slots = append(slots, s)
	}
	for _, idx := range matched {
		row := e.Table.Rows[idx]
		for _, s := range slots {
			if err := s.Advance(row, ctx); err != nil {
				return nil, err
			}
		}
	}
	ctx.AggResults = make(map[*parse.AggregateExpr]eval.EvalCell, len(slots))
	for a, s := range slotByExpr {
		ctx.AggResults[a] = s.Finalize()
	}
	out := make([]cell.Cell, len(plan))
	for i, p := range plan {
		// The row arg is unused: aggregated projections cannot reach a bare
		// column (planProjection rejected them), so eval.EvalExpr never touches it.
		res, err := eval.EvalExpr(p.Expr, nil, ctx)
		if err != nil {
			return nil, err
		}
		out[i] = res.Cell
	}
	return [][]cell.Cell{out}, nil
}

// applyOffsetLimitRows mirrors applyOffsetLimit but operates on projected
// row slices instead of source-row indices. Slice 3 needs both shapes
// because DISTINCT now runs after projection.
func applyOffsetLimitRows(rows [][]cell.Cell, offset, limit *int) [][]cell.Cell {
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

// projectedKey builds a dedup key from a projected row's typed cells. Same
// scheme as distinctKey but reads from a positional cell.Cell slice instead of
// a cell.Row keyed by column index.
func projectedKey(pr []cell.Cell, plan []projEntry) string {
	var b strings.Builder
	for i, p := range plan {
		c := pr[i]
		if c.Null {
			b.WriteString("N|")
			continue
		}
		switch p.Type {
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
	return b.String()
}

// validateOrderBy rejects sort keys that don't name a column in the table.
// Catching this here avoids a runtime nil deref deep in the comparator.
// Used by non-aggregated queries; aggregated paths resolve against the
// projection plan via resolveOrderByOutput instead.
func (e *Executor) validateOrderBy(keys []parse.OrderKey) error {
	for _, k := range keys {
		if _, ok := e.Table.Schema[k.Column]; !ok {
			return fmt.Errorf("unknown column %q in ORDER BY", k.Column)
		}
	}
	return nil
}

// orderEntry is one resolved ORDER BY key, bound to a projected-row column
// index. Produced once at plan time and reused per comparison during the
// stable sort.
type orderEntry struct {
	Col  int
	Type cell.ColumnType
	Desc bool
}

// resolveOrderByOutput maps each ORDER BY key to a projection-plan slot by
// label match. Aggregated queries can't reach source columns after
// projection; every sort key must therefore live in the SELECT list, either
// under an explicit alias or under the rendered source text of an
// unaliased projection.
func resolveOrderByOutput(keys []parse.OrderKey, plan []projEntry) ([]orderEntry, error) {
	out := make([]orderEntry, len(keys))
	for i, k := range keys {
		idx := -1
		for j, p := range plan {
			if p.Label == k.Column {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("unknown column %q in ORDER BY; aggregated queries must sort on a column in the SELECT list", k.Column)
		}
		out[i] = orderEntry{Col: idx, Type: plan[idx].Type, Desc: k.Desc}
	}
	return out, nil
}

// sortOutputRows stable-sorts projected output rows by the resolved
// ORDER BY plan. NULLs sort high per the same compareForOrder rules the
// source-row sort uses, so the two paths report a consistent ordering when
// either is reachable.
func sortOutputRows(rows [][]cell.Cell, order []orderEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, k := range order {
			cmp := compareForOrder(rows[i][k.Col], rows[j][k.Col], k.Type)
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

// sortByKeys does an in-place stable sort of row indices by the ORDER BY keys.
// Stability matters: ties on key N preserve the original (input) order, which
// gives users a predictable result. NULLs sort to the high end: last in ASC,
// first in DESC — the Postgres convention.
func (e *Executor) sortByKeys(indices []int, keys []parse.OrderKey, ctx *eval.EvalContext) {
	sort.SliceStable(indices, func(i, j int) bool {
		ra, rb := e.Table.Rows[indices[i]], e.Table.Rows[indices[j]]
		for _, k := range keys {
			ci := ctx.ColIdx[k.Column]
			t := e.Table.Schema[k.Column].Type
			cmp := compareForOrder(ra[ci], rb[ci], t)
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

// compareForOrder is a NULLs-go-high variant of cell.Compare: a NULL cell is treated
// as the maximum value, so ASC puts NULLs at the bottom of the result and DESC
// puts them at the top. This matches Postgres's default; SQLite goes the other
// way, but Postgres semantics are the more common reference point.
func compareForOrder(a, b cell.Cell, t cell.ColumnType) int {
	if a.Null && b.Null {
		return 0
	}
	if a.Null {
		return 1
	}
	if b.Null {
		return -1
	}
	// Delegate to the existing typed comparator for the non-NULL case.
	return cell.Compare(a, b, t)
}

// planProjection builds the typed projection plan for a SELECT. SELECT *
// synthesizes one entry per table column. Otherwise each user projection
// becomes a plan entry whose label is the alias if present, else the
// expression's source-text rendering. Duplicate output labels are
// rejected — the caller must alias to disambiguate, since the render
// layer keys output rows by label.
//
// Under GROUP BY, every bare column reference must name a GROUP BY column.
// Without GROUP BY but with aggregates anywhere, bare columns are rejected
// outright (Postgres-strict). Each aggregate node is validated up front so
// SUM(Title) and similar fail at plan time, before the row scan.
func (e *Executor) planProjection(sel *parse.SelectStmt) ([]projEntry, error) {
	groupCols := make(map[string]bool, len(sel.GroupBy))
	for _, c := range sel.GroupBy {
		if _, ok := e.Table.Schema[c]; !ok {
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
		plan := make([]projEntry, len(e.Table.Columns))
		for i, name := range e.Table.Columns {
			plan[i] = projEntry{
				Label: name,
				Type:  e.Table.Schema[name].Type,
				Expr:  &parse.ColumnExpr{Name: name},
			}
		}
		return plan, nil
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
		if err := eval.ValidateExpr(pr.Expr, e.Table.Schema); err != nil {
			return nil, err
		}
		switch {
		case grouped:
			if bare := eval.BareColumnNotIn(pr.Expr, groupCols); bare != "" {
				return nil, fmt.Errorf("column %q must appear in GROUP BY or be wrapped in an aggregate", bare)
			}
		case anyAgg:
			if bare := eval.BareColumn(pr.Expr); bare != "" {
				return nil, fmt.Errorf("column %q must appear inside an aggregate or in GROUP BY", bare)
			}
		}
		if grouped || anyAgg {
			for _, a := range eval.CollectAggregates(pr.Expr, nil) {
				if err := eval.ValidateAggregate(a, e.Table.Schema); err != nil {
					return nil, err
				}
			}
		}
		t, err := eval.ExprType(pr.Expr, e.Table.Schema)
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
		plan = append(plan, projEntry{Label: label, Type: t, Expr: pr.Expr})
	}
	return plan, nil
}

func (e *Executor) executeUpdate(upd *parse.UpdateStmt, commit bool) error {
	if err := eval.ValidatePredicate(upd.Where, e.Table.Schema); err != nil {
		return err
	}
	if err := e.validateAssignments(upd.Assignments); err != nil {
		return err
	}

	matches, err := e.findMatches(upd.Where)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would update %d row%s in %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	for _, a := range upd.Assignments {
		fmt.Fprintf(e.Out, "  SET %s = %s\n", a.Column, renderExpr(a.Value))
	}
	e.printSample(matches)

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}
	if len(matches) == 0 {
		return e.flush()
	}

	ctx := eval.NewEvalContext(e.Table)
	for _, idx := range matches {
		row := e.Table.Rows[idx]
		// Standard SQL UPDATE semantics: every SET RHS evaluates against the
		// pre-update row, so SET a = b, b = a swaps without using the new
		// value of a. Stage the new cells first, then write them all at once.
		newCells := make(map[string]cell.Cell, len(upd.Assignments))
		for _, a := range upd.Assignments {
			info := e.Table.Schema[a.Column]
			result, err := eval.EvalExpr(a.Value, row, ctx)
			if err != nil {
				return fmt.Errorf("UPDATE column %q: %w", a.Column, err)
			}
			c, err := eval.CoerceEvalCell(result, info.Type, a.Column)
			if err != nil {
				return err
			}
			newCells[a.Column] = c
		}
		for col, c := range newCells {
			row[e.colIndex(col)] = c
		}
	}
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Updated %d of %d row%s. Wrote %s.\n", len(matches), len(matches), plural(len(matches)), e.targetPath())
	return nil
}

// validateAssignments checks each SET target column exists, each RHS
// expression references known columns only, and no aggregate slips into
// SET (aggregates are meaningful only in projection / HAVING contexts).
func (e *Executor) validateAssignments(assigns []parse.Assignment) error {
	for _, a := range assigns {
		if _, ok := e.Table.Schema[a.Column]; !ok {
			return fmt.Errorf("unknown column %q", a.Column)
		}
		if err := eval.ValidateExpr(a.Value, e.Table.Schema); err != nil {
			return err
		}
		if eval.HasAggregate(a.Value) {
			return fmt.Errorf("column %q: aggregates are not allowed in SET", a.Column)
		}
	}
	return nil
}

func (e *Executor) executeDelete(del *parse.DeleteStmt, commit bool) error {
	// Bare DELETE in --exec mode requires --confirm-destructive; the y/N
	// prompt isn't available there to catch a mistake. In REPL mode, the
	// trailing '!' shortcut is downgraded so the user still sees the "Apply?
	// [y/N]" prompt — bare DELETE is destructive enough that a one-character
	// typo shouldn't be able to wipe the table.
	if del.Where == nil && commit && !e.ConfirmDestructive && e.Confirm == nil {
		return fmt.Errorf("bare DELETE (no WHERE) requires --confirm-destructive")
	}
	if del.Where == nil && e.Confirm != nil {
		commit = false
	}
	if err := eval.ValidatePredicate(del.Where, e.Table.Schema); err != nil {
		return err
	}

	matches, err := e.findMatches(del.Where)
	if err != nil {
		return err
	}

	if del.Where == nil {
		fmt.Fprintf(e.Out, "Would delete ALL %d row%s from %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	} else {
		fmt.Fprintf(e.Out, "Would delete %d row%s from %s:\n", len(matches), plural(len(matches)), e.Table.Path)
	}
	e.printSample(matches)

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}
	if len(matches) == 0 {
		return e.flush()
	}
	e.Table.Rows = removeIndices(e.Table.Rows, matches)
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Deleted %d row%s. Wrote %s.\n", len(matches), plural(len(matches)), e.targetPath())
	return nil
}

func (e *Executor) executeInsert(ins *parse.InsertStmt, commit bool) error {
	if len(ins.Columns) != len(ins.Values) {
		return fmt.Errorf("INSERT has %d column%s but %d value%s",
			len(ins.Columns), plural(len(ins.Columns)),
			len(ins.Values), plural(len(ins.Values)))
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
	cells, err := e.buildAssignmentCells(assigns)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.Out, "Would insert row into %s:\n", e.Table.Path)
	for _, c := range ins.Columns {
		fmt.Fprintf(e.Out, "  %s = %s\n", c, cell.RenderLiteral(findValue(ins, c)))
	}

	proceed, msg := e.decideCommit(commit)
	if msg != "" {
		fmt.Fprintln(e.Out, msg)
	}
	if !proceed {
		return nil
	}

	newRow := make(cell.Row, len(e.Table.Columns))
	for i, name := range e.Table.Columns {
		if c, ok := cells[name]; ok {
			newRow[i] = c
		} else {
			newRow[i] = cell.Cell{Null: true}
		}
	}
	e.Table.Rows = append(e.Table.Rows, newRow)
	if err := e.flush(); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Inserted row. Wrote %s.\n", e.targetPath())
	return nil
}

// decideCommit resolves a write's commit/abort decision after the preview has
// been shown.
//   - commit=true (trailing '!' in REPL, --commit in --exec): proceed silently.
//   - REPL (Confirm != nil): ask the user; on "y", proceed; otherwise "(aborted)".
//   - --exec without --commit (Confirm == nil): never commit; print the
//     "(dry run; pass --commit to apply)" hint.
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

// findMatches returns the row indices that satisfy the predicate. A nil
// predicate matches every row.
func (e *Executor) findMatches(where parse.Predicate) ([]int, error) {
	ctx := eval.NewEvalContext(e.Table)
	out := make([]int, 0)
	for i, row := range e.Table.Rows {
		ok, err := eval.Matches(where, row, ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, i)
		}
	}
	return out, nil
}

// buildAssignmentCells is the INSERT-side helper for evaluating literal
// assignments once and reusing the result. UPDATE uses a per-row eval path
// in executeUpdate because computed RHSes depend on the source row. Only
// parse.LiteralExpr is reachable here because executeInsert wraps each input
// parse.Value in a parse.LiteralExpr before calling.
func (e *Executor) buildAssignmentCells(assigns []parse.Assignment) (map[string]cell.Cell, error) {
	cells := make(map[string]cell.Cell, len(assigns))
	for _, a := range assigns {
		info, ok := e.Table.Schema[a.Column]
		if !ok {
			return nil, fmt.Errorf("unknown column %q", a.Column)
		}
		lit, ok := a.Value.(*parse.LiteralExpr)
		if !ok {
			return nil, fmt.Errorf("internal: INSERT requires literal values")
		}
		c, err := cell.CoerceLiteral(lit.Value, info.Type)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", a.Column, err)
		}
		cells[a.Column] = c
	}
	return cells, nil
}

func (e *Executor) colIndex(name string) int {
	for i, n := range e.Table.Columns {
		if n == name {
			return i
		}
	}
	return -1
}

// printSample emits a small preview table: row# + a primary column (Title when
// present, else the first column). At most previewSampleMax rows; a trailing
// "... N more" line counts what was elided.
func (e *Executor) printSample(rowIndices []int) {
	if len(rowIndices) == 0 {
		return
	}
	previewCols := e.previewColumns()
	header := append([]string{"row"}, previewCols...)
	sample := rowIndices
	if len(sample) > previewSampleMax {
		sample = sample[:previewSampleMax]
	}
	rows := make([]map[string]any, len(sample))
	for i, ri := range sample {
		m := map[string]any{"row": int64(ri + 1)}
		for _, c := range previewCols {
			ci := e.colIndex(c)
			m[c] = e.Table.Rows[ri][ci].AsAny(e.Table.Schema[c].Type)
		}
		rows[i] = m
	}
	fmt.Fprintln(e.Out, "Sample:")
	_ = render.WriteTableBody(e.Out, header, rows)
	if len(rowIndices) > previewSampleMax {
		fmt.Fprintf(e.Out, "  ... %d more\n", len(rowIndices)-previewSampleMax)
	}
}

const previewSampleMax = 5

// previewColumns picks the column to show alongside the row number in write
// previews. Prefers Title (case-insensitive match), then Name, then the first
// column. The goal is to surface enough identifying detail for a human to
// recognize a row.
func (e *Executor) previewColumns() []string {
	for _, candidate := range []string{"Title", "Name", "name", "title"} {
		if _, ok := e.Table.Schema[candidate]; ok {
			return []string{candidate}
		}
	}
	for _, c := range e.Table.Columns {
		if strings.EqualFold(c, "title") || strings.EqualFold(c, "name") {
			return []string{c}
		}
	}
	if len(e.Table.Columns) > 0 {
		return []string{e.Table.Columns[0]}
	}
	return nil
}

// flush persists the in-memory cell.Table to disk, either at OutputPath or at the
// originally bound path.
func (e *Executor) flush() error {
	return SaveCSV(e.Table, e.OutputPath)
}

func (e *Executor) targetPath() string {
	if e.OutputPath != "" {
		return e.OutputPath
	}
	return e.Table.Path
}

// removeIndices returns rows with the listed indices removed. indices must
// be sorted ascending (findMatches produces them that way).
func removeIndices(rows []cell.Row, indices []int) []cell.Row {
	out := make([]cell.Row, 0, len(rows)-len(indices))
	j := 0
	for i, r := range rows {
		if j < len(indices) && indices[j] == i {
			j++
			continue
		}
		out = append(out, r)
	}
	return out
}

// findValue picks the literal parse.Value for column c out of an parse.InsertStmt's
// parallel Columns/Values lists. Used only by the preview path; the
// pre-validation has already checked length parity.
func findValue(ins *parse.InsertStmt, c string) parse.Value {
	for i, name := range ins.Columns {
		if name == c {
			return ins.Values[i]
		}
	}
	return parse.Value{Kind: parse.ValNull}
}

// renderExpr formats an expression as readable SQL text for write previews.
// Binary children are parenthesized when their op has lower precedence than
// the parent, so the preview reflects user intent even after the parser
// flattens precedence into the tree shape.
func renderExpr(e parse.Expr) string {
	return renderExprPrec(e, 0)
}

func renderExprPrec(e parse.Expr, parentPrec int) string {
	switch n := e.(type) {
	case *parse.LiteralExpr:
		return cell.RenderLiteral(n.Value)
	case *parse.ColumnExpr:
		return n.Name
	case *parse.BinaryExpr:
		prec := opPrec(n.Op)
		s := renderExprPrec(n.L, prec) + " " + n.Op + " " + renderExprPrec(n.R, prec)
		if prec < parentPrec {
			s = "(" + s + ")"
		}
		return s
	case *parse.AggregateExpr:
		if n.Star {
			return n.Func + "(*)"
		}
		return n.Func + "(" + renderExpr(n.Arg) + ")"
	}
	return "?"
}

func opPrec(op string) int {
	switch op {
	case "+", "-":
		return 1
	case "*", "/":
		return 2
	}
	return 0
}
