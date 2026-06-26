package csv

import (
	"fmt"
	"io"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/render"
)

// Describe renders the bound table's columns and inferred types to w using
// the executor's current format. Wired into the REPL's "describe" meta-cmd.
// The arg is accepted for signature parity with the SP backend's hidden-
// column toggle; CSV columns have no hidden flag, so any non-empty arg is
// rejected.
func (e *Executor) Describe(w io.Writer, arg string) error {
	if arg != "" {
		return fmt.Errorf("describe: csv backend takes no arguments")
	}
	rows := make([]map[string]any, 0, len(e.Table.Columns))
	for _, name := range e.Table.Columns {
		info := e.Table.Schema[name]
		rows = append(rows, map[string]any{
			"name": info.Name,
			"type": info.Type.String(),
		})
	}
	return render.Render(w, render.Result{
		Columns: []string{"name", "type"},
		Rows:    rows,
	}, e.Mode, true)
}

// Refresh re-reads the bound CSV from disk. Dialect (delimiter, header
// presence) and the previously-inferred types are preserved as hints so the
// re-load doesn't accidentally drift the schema mid-session.
func (e *Executor) Refresh() error {
	hints := make(map[string]cell.ColumnType, len(e.Table.Schema))
	for name, info := range e.Table.Schema {
		hints[name] = info.Type
	}
	opts := LoadOptions{
		Delim:     e.Table.Delim,
		NoHeader:  !e.Table.HasHeader,
		TypeHints: hints,
	}
	t, err := LoadCSV(e.Table.Path, opts)
	if err != nil {
		return err
	}
	e.Table = t
	fmt.Fprintf(e.Out, "Refreshed %s: %d columns, %d rows.\n", t.Path, len(t.Columns), len(t.Rows))
	return nil
}

// SetConfirm wires the REPL's y/N callback into the executor's destructive-
// write confirmation hook. Called once by repl.Run via Session.SetConfirm.
func (e *Executor) SetConfirm(fn func() bool) {
	e.Confirm = fn
}
