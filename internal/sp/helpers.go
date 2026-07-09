package sp

import (
	"strings"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

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

// renderExpr formats an expression as readable SQL text for write previews.
// Binary children are parenthesized when their op has lower precedence than
// the parent, so the preview reflects user intent even after the parser
// flattens precedence into the tree shape.
//
// Copied from internal/csv/exec.go per the copy-and-diff convention for
// shared helpers across backends; if a third backend grows the same need,
// promote to a shared package.
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
	case *parse.FuncCallExpr:
		parts := make([]string, len(n.Args))
		for i, a := range n.Args {
			parts[i] = renderExpr(a)
		}
		return n.Name + "(" + strings.Join(parts, ", ") + ")"
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
