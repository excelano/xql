package sp

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/excelano/xql/internal/render"
)

// Describe renders the bound list's columns and Graph-side types to w using
// the executor's current format. The primary "name" column shows the user-
// facing display name (what the SharePoint UI labels the column); a second
// "internal" column shows the Graph internal name when it differs, since
// that name is what Graph $filter expressions and PATCH bodies actually
// use. Hidden and read-only flags shape SELECT * (hidden) and INSERT/UPDATE
// (read-only); users hitting "why won't this column write?" find the answer
// here.
//
// arg is the REPL meta-command argument: "" omits hidden columns (matching
// the SELECT * default), "all" shows every column including SharePoint's
// system fields (LinkTitle, _ColorTag, ContentType, ...). Anything else is
// a usage error.
func (e *Executor) Describe(w io.Writer, arg string) error {
	includeHidden, err := parseDescribeArg(arg)
	if err != nil {
		return err
	}

	type row struct {
		name     string
		internal string
		typ      string
		hidden   bool
		readonly bool
	}
	rows := make([]row, 0, len(e.Bound.Columns))
	anyDifferent := false
	for _, name := range e.Bound.Columns {
		info := e.Bound.Schema[name]
		if info.Hidden && !includeHidden {
			continue
		}
		display := info.DisplayName
		if display == "" {
			display = info.Name
		}
		rows = append(rows, row{
			name:     display,
			internal: info.Name,
			typ:      string(info.Type),
			hidden:   info.Hidden,
			readonly: info.ReadOnly,
		})
		if display != info.Name {
			anyDifferent = true
		}
	}

	cols := []string{"name", "type", "hidden", "readonly"}
	if anyDifferent {
		cols = []string{"name", "internal", "type", "hidden", "readonly"}
	}
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		m := map[string]any{
			"name":     r.name,
			"type":     r.typ,
			"hidden":   r.hidden,
			"readonly": r.readonly,
		}
		if anyDifferent {
			m["internal"] = r.internal
		}
		out[i] = m
	}
	return render.Render(w, render.Result{Columns: cols, Rows: out}, e.Mode, true)
}

func parseDescribeArg(arg string) (includeHidden bool, err error) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
		return false, nil
	case "all":
		return true, nil
	}
	return false, fmt.Errorf("describe: unknown argument %q (want bare describe or 'describe all')", arg)
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
