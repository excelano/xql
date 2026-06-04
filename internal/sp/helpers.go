package sp

import "github.com/excelano/xql/internal/parse"

// columnExprName returns the column name when e is a bare ColumnExpr (not
// nested inside an arithmetic or aggregate expression). Used by the OData
// translator and projection resolver to reject v2 grammar shapes that the
// SharePoint backend doesn't translate.
func columnExprName(e parse.Expr) (string, bool) {
	if c, ok := e.(*parse.ColumnExpr); ok {
		return c.Name, true
	}
	return "", false
}

// plural returns "" for n=1 and "s" otherwise. Used by row-count messages.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
