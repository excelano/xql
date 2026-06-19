// Executor wraps the CSV backend's Executor with a read-only gate and an
// HTTP-backed Refresh. The xinglet export endpoint is GET-only, so any
// INSERT/UPDATE/DELETE is rejected before it reaches the CSV pipeline.
package xinglet

import (
	"fmt"
	"io"

	csvbackend "github.com/excelano/xql/internal/csv"
	"github.com/excelano/xql/internal/parse"
)

// Executor adapts a csv.Executor to the xinglet backend's surface.
// Inner.Table is the only mutable piece (Refresh swaps it); everything else
// is a thin pass-through.
type Executor struct {
	Inner *csvbackend.Executor
	Cfg   Config
	UUID  string
}

// Execute rejects any non-SELECT statement with a clear "read-only" error,
// then delegates SELECT to the CSV executor. commit is meaningless for
// SELECT but accepted to satisfy the repl.Session signature.
func (e *Executor) Execute(stmt parse.Stmt, commit bool) error {
	if _, ok := stmt.(*parse.SelectStmt); !ok {
		return fmt.Errorf("xinglet backend is read-only; only SELECT is supported (writes would require new server endpoints)")
	}
	return e.Inner.Execute(stmt, commit)
}

// Describe delegates verbatim -- column listing has no read/write angle.
func (e *Executor) Describe(w io.Writer) error {
	return e.Inner.Describe(w)
}

// Refresh re-fetches the xinglist from the remote and swaps the inner
// executor's table. Schema can drift between refreshes (the owner may have
// added/removed/renamed columns server-side), so we let LoadList build a
// fresh table from scratch rather than pinning the original type hints.
func (e *Executor) Refresh() error {
	t, err := LoadList(e.Cfg, e.UUID)
	if err != nil {
		return err
	}
	e.Inner.Table = t
	fmt.Fprintf(e.Inner.Out, "Refreshed %s: %d columns, %d rows.\n", t.Path, len(t.Columns), len(t.Rows))
	return nil
}

// SetConfirm is a no-op: the backend has no destructive paths, so there's
// nothing for the REPL's y/N prompt to gate. We satisfy the interface so
// repl.Run doesn't fail to wire up the session.
func (e *Executor) SetConfirm(fn func() bool) {}
