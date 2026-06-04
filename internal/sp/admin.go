package sp

import (
	"context"
	"fmt"
	"io"

	"github.com/excelano/xql/internal/render"
)

// Describe renders the bound list's columns and Graph-side types to w using
// the executor's current format. Hidden and read-only flags are surfaced
// because they shape SELECT * (hidden) and INSERT/UPDATE (read-only) behavior;
// users hitting "why won't this column write?" find the answer here.
func (e *Executor) Describe(w io.Writer) error {
	rows := make([]map[string]any, 0, len(e.Bound.Columns))
	for _, name := range e.Bound.Columns {
		info := e.Bound.Schema[name]
		rows = append(rows, map[string]any{
			"name":     info.Name,
			"type":     string(info.Type),
			"hidden":   info.Hidden,
			"readonly": info.ReadOnly,
		})
	}
	return render.Render(w, render.Result{
		Columns: []string{"name", "type", "hidden", "readonly"},
		Rows:    rows,
	}, e.Format)
}

// Refresh re-resolves the bound list from its source URL, picking up any
// column schema changes made in SharePoint mid-session. The graph client and
// its cached token are reused. Uses context.Background() because this is a
// user-initiated REPL command with no outer deadline to honor.
func (e *Executor) Refresh() error {
	bound, err := ResolveListBinding(context.Background(), e.Graph, e.Bound.SourceURL)
	if err != nil {
		return err
	}
	e.Bound = bound
	fmt.Fprintf(e.Out, "Refreshed %s: %d columns.\n", bound.DisplayName, len(bound.Columns))
	return nil
}

// SetConfirm wires the REPL's y/N callback into the executor's destructive-
// write confirmation hook. Called once by repl.Run via Session.SetConfirm.
func (e *Executor) SetConfirm(fn func() bool) {
	e.Confirm = fn
}
