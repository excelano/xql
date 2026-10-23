// Author: David M. Anderson
// Built with AI assistance (Claude, Anthropic)

package sp

import (
	"fmt"
	"strings"
	"time"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// ODataFieldPrefix is prepended to every column reference in generated $filter
// expressions. SharePoint list-item filtering requires the "fields/" prefix
// because user columns live under the fields subobject on item resources.
const ODataFieldPrefix = "fields/"

// ToOData converts a parsed Predicate to the Microsoft Graph $filter that
// narrows the fetch for it. The filter selects a superset of the matching
// items, never fewer, and the executor evaluates the full predicate on what
// comes back. Two facts about SharePoint force that split: its list-item
// filter rejects endswith, contains, tolower and not, and it compares text
// case-insensitively, so text = and a prefix LIKE narrow safely while text
// !=, < and > do not. Conditions on ID, on an expression, or under NOT or a
// partial OR contribute nothing to the filter.
//
// Column references are verified against schema (keyed by internal name) and
// formatted with the fields/ prefix. Values are emitted in OData v4 form:
// single-quoted strings (with ” escape), bare numbers, lowercase booleans,
// and ISO 8601 datetimes normalized to RFC3339 in UTC. A predicate with
// nothing to narrow on returns the empty string. The result is not
// URL-encoded; pass it through url.Values when building the request.
func ToOData(node parse.Predicate, schema map[string]FieldInfo) (string, error) {
	narrow := serverFilter(node, schema)
	if narrow == nil {
		return "", nil
	}
	return translate(narrow, schema)
}

// serverFilter returns the part of p that Graph can evaluate as a superset of
// p's matches, or nil when nothing narrows the fetch.
func serverFilter(p parse.Predicate, schema map[string]FieldInfo) parse.Predicate {
	switch n := p.(type) {
	case *parse.BinaryOp:
		l, r := serverFilter(n.L, schema), serverFilter(n.R, schema)
		if n.Op == "OR" && (l == nil || r == nil) {
			return nil
		}
		switch {
		case l == nil:
			return r
		case r == nil:
			return l
		}
		return &parse.BinaryOp{Op: n.Op, L: l, R: r}
	case *parse.Comparison:
		col, ok := columnExprName(n.LExpr)
		if ok && filterable(col, schema) && (n.Op == "=" || !isTextField(schema[col])) {
			return n
		}
	case *parse.NullTest:
		if filterable(n.Column, schema) {
			return n
		}
	case *parse.LikeOp:
		if !n.Not && filterable(n.Column, schema) && isTextField(schema[n.Column]) && narrowingPrefix(n.Pattern) {
			return n
		}
	case *parse.InOp:
		if !n.Not && filterable(n.Column, schema) {
			return n
		}
	case *parse.BetweenOp:
		if !n.Not && filterable(n.Column, schema) && !isTextField(schema[n.Column]) {
			return n
		}
	}
	return nil
}

// filterable reports whether a column can narrow the fetch. Graph cannot
// filter on the item ID, a calculated column holds text whatever its formula
// produces, a multi-value column reads as text the evaluator matches
// differently, and an unknown name is left for the evaluator to report.
func filterable(col string, schema map[string]FieldInfo) bool {
	f, ok := schema[col]
	return ok && col != "ID" && f.Type != FieldCalculated && !f.Multi
}

func isTextField(f FieldInfo) bool {
	return f.CellType() == cell.TypeString
}

// narrowingPrefix reports whether a LIKE pattern is a literal prefix, with at
// most one trailing %, which startswith (or eq, with no wildcard) narrows.
func narrowingPrefix(pattern string) bool {
	body := strings.TrimSuffix(pattern, "%")
	return !strings.ContainsAny(body, "%_\\")
}

// fieldRef is the $filter name for a column: a person or lookup column is
// filtered through its LookupId field.
func fieldRef(f FieldInfo) string {
	if f.isLookupID() {
		return ODataFieldPrefix + f.Name + "LookupId"
	}
	return ODataFieldPrefix + f.Name
}

func translate(node parse.Predicate, schema map[string]FieldInfo) (string, error) {
	switch n := node.(type) {
	case *parse.BinaryOp:
		l, err := translate(n.L, schema)
		if err != nil {
			return "", err
		}
		r, err := translate(n.R, schema)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s %s %s)", l, strings.ToLower(n.Op), r), nil
	case *parse.Comparison:
		col, _ := columnExprName(n.LExpr)
		field := schema[col]
		op, ok := odataCmpOps[n.Op]
		if !ok {
			return "", fmt.Errorf("internal: unsupported operator %q", n.Op)
		}
		val, err := formatValue(n.Value, field)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s %s", fieldRef(field), op, val), nil
	case *parse.NullTest:
		op := "eq"
		if n.Not {
			op = "ne"
		}
		return fmt.Sprintf("%s %s null", fieldRef(schema[n.Column]), op), nil
	case *parse.LikeOp:
		lit := strings.TrimSuffix(n.Pattern, "%")
		if lit == n.Pattern {
			return fmt.Sprintf("%s eq '%s'", fieldRef(schema[n.Column]), escapeODataString(lit)), nil
		}
		return fmt.Sprintf("startswith(%s, '%s')", fieldRef(schema[n.Column]), escapeODataString(lit)), nil
	case *parse.InOp:
		return translateIn(n, schema)
	case *parse.BetweenOp:
		return translateBetween(n, schema)
	}
	return "", fmt.Errorf("internal: unhandled predicate type %T", node)
}

// translateIn fans an IN list out to (col eq v1 or col eq v2 or ...), since
// SharePoint's list-item filter rejects the OData v4 `in` operator.
func translateIn(n *parse.InOp, schema map[string]FieldInfo) (string, error) {
	field := schema[n.Column]
	parts := make([]string, 0, len(n.Values))
	for _, v := range n.Values {
		if v.Kind == parse.ValNull {
			return "", fmt.Errorf("NULL is not allowed in an IN list; use IS NULL instead")
		}
		s, err := formatValue(v, field)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s eq %s", fieldRef(field), s))
	}
	return "(" + strings.Join(parts, " or ") + ")", nil
}

// translateBetween emits the inclusive form `(col ge low and col le high)`.
// OData has no native BETWEEN but ge/le compose the same way.
func translateBetween(n *parse.BetweenOp, schema map[string]FieldInfo) (string, error) {
	field := schema[n.Column]
	lo, err := formatValue(n.Low, field)
	if err != nil {
		return "", err
	}
	hi, err := formatValue(n.High, field)
	if err != nil {
		return "", err
	}
	ref := fieldRef(field)
	return fmt.Sprintf("(%s ge %s and %s le %s)", ref, lo, ref, hi), nil
}

var odataCmpOps = map[string]string{
	"=":  "eq",
	"!=": "ne",
	"<":  "lt",
	"<=": "le",
	">":  "gt",
	">=": "ge",
}

func formatValue(v parse.Value, field FieldInfo) (string, error) {
	switch v.Kind {
	case parse.ValString:
		if field.Type == FieldDateTime {
			normalized, err := normalizeDateTime(v.Str)
			if err != nil {
				return "", fmt.Errorf("column %s expects ISO 8601 datetime, got %q", field.Name, v.Str)
			}
			// SharePoint's filter pipeline expects datetime values quoted
			// like strings (not as OData Edm.DateTimeOffset literals);
			// unquoted forms return 400 invalidRequest.
			return "'" + normalized + "'", nil
		}
		return "'" + escapeODataString(v.Str) + "'", nil
	case parse.ValNumber:
		return v.Num, nil
	case parse.ValBool:
		// SharePoint answers `eq true` with the false rows; 1 and 0 work.
		if v.Bool {
			return "1", nil
		}
		return "0", nil
	case parse.ValNull:
		return "", fmt.Errorf("internal: NULL in comparison (parser should reject)")
	}
	return "", fmt.Errorf("internal: unknown value kind %d", v.Kind)
}

// normalizeDateTime accepts common ISO 8601 forms and returns a canonical
// RFC3339 string in UTC. Bare dates default to midnight UTC.
func normalizeDateTime(s string) (string, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	}
	return "", fmt.Errorf("not a recognized ISO 8601 datetime")
}
