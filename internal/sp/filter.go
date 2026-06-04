package sp

import (
	"fmt"
	"strings"
	"time"

	"github.com/excelano/xql/internal/parse"
)

// ODataFieldPrefix is prepended to every column reference in generated $filter
// expressions. SharePoint list-item filtering requires the "fields/" prefix
// because user columns live under the fields subobject on item resources.
const ODataFieldPrefix = "fields/"

// ToOData converts a parsed Predicate to a Microsoft Graph $filter expression.
// Column references are verified against schema (keyed by internal name) and
// formatted with the fields/ prefix. Values are emitted in OData v4 form:
// single-quoted strings (with '' escape), bare numbers, lowercase booleans,
// and bare ISO 8601 datetimes. Date-only strings on DateTime columns are
// normalized to full RFC3339 in UTC.
//
// A nil predicate returns the empty string. The result is not URL-encoded;
// pass it through url.Values when building the request.
func ToOData(node parse.Predicate, schema map[string]FieldInfo) (string, error) {
	if node == nil {
		return "", nil
	}
	return translate(node, schema)
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
	case *parse.NotOp:
		inner, err := translate(n.Inner, schema)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("not (%s)", inner), nil
	case *parse.Comparison:
		col, ok := columnExprName(n.LExpr)
		if !ok {
			return "", fmt.Errorf("arithmetic on a column reference in WHERE is not supported by SharePoint: OData $filter has no equivalent operator. Rewrite by computing the literal side yourself (e.g. WHERE Priority + 1 = 5 → WHERE Priority = 4)")
		}
		field, ok := schema[col]
		if !ok {
			return "", fmt.Errorf("unknown column %q", col)
		}
		op, ok := odataCmpOps[n.Op]
		if !ok {
			return "", fmt.Errorf("internal: unsupported operator %q", n.Op)
		}
		val, err := formatValue(n.Value, field)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s%s %s %s", ODataFieldPrefix, col, op, val), nil
	case *parse.NullTest:
		if _, ok := schema[n.Column]; !ok {
			return "", fmt.Errorf("unknown column %q", n.Column)
		}
		op := "eq"
		if n.Not {
			op = "ne"
		}
		return fmt.Sprintf("%s%s %s null", ODataFieldPrefix, n.Column, op), nil
	case *parse.LikeOp:
		return translateLike(n, schema)
	case *parse.InOp:
		return translateIn(n, schema)
	case *parse.BetweenOp:
		return translateBetween(n, schema)
	}
	return "", fmt.Errorf("internal: unhandled predicate type %T", node)
}

// translateLike maps a SQL LIKE (case-sensitive) or ILIKE (case-insensitive)
// pattern to one of OData's string functions. SharePoint $filter supports
// startswith, endswith, and contains; arbitrary LIKE patterns can't be
// expressed, so we accept only the three pattern shapes that have a clean
// equivalent: prefix (foo%), suffix (%foo), and substring (%foo%).
// Single-character wildcards (_) and mid-pattern % are rejected with a
// clear error.
//
// ILIKE wraps the field in tolower(...) and lowercases the literal before
// emission. Microsoft Graph supports tolower() in $filter expressions.
func translateLike(n *parse.LikeOp, schema map[string]FieldInfo) (string, error) {
	field, ok := schema[n.Column]
	if !ok {
		return "", fmt.Errorf("unknown column %q", n.Column)
	}
	op := "LIKE"
	if n.Insensitive {
		op = "ILIKE"
	}
	if field.Type != FieldText && field.Type != FieldNote && field.Type != FieldChoice {
		return "", fmt.Errorf("%s only works on text columns; %q is %s", op, n.Column, field.Type)
	}
	fn, lit, err := splitLikePattern(n.Pattern)
	if err != nil {
		return "", fmt.Errorf("%s on %q: %w", op, n.Column, err)
	}
	fieldRef := ODataFieldPrefix + n.Column
	if n.Insensitive {
		fieldRef = "tolower(" + fieldRef + ")"
		lit = strings.ToLower(lit)
	}
	expr := fmt.Sprintf("%s(%s, '%s')", fn, fieldRef, escapeODataString(lit))
	if n.Not {
		return "not (" + expr + ")", nil
	}
	return expr, nil
}

// splitLikePattern returns the OData function name (startswith / endswith /
// contains) and the literal substring to match. It rejects patterns that
// can't be translated cleanly: an underscore wildcard, a backslash escape,
// or a % anywhere other than the leading/trailing edge.
func splitLikePattern(pattern string) (string, string, error) {
	if strings.ContainsRune(pattern, '_') {
		return "", "", fmt.Errorf("single-character wildcard '_' is not supported against SharePoint; rephrase the query")
	}
	if strings.Contains(pattern, "\\") {
		return "", "", fmt.Errorf("backslash escapes in LIKE patterns are not supported against SharePoint")
	}
	prefix := strings.HasPrefix(pattern, "%")
	suffix := strings.HasSuffix(pattern, "%")
	body := pattern
	if prefix {
		body = body[1:]
	}
	if suffix && len(body) > 0 {
		body = body[:len(body)-1]
	}
	if strings.ContainsRune(body, '%') {
		return "", "", fmt.Errorf("mid-pattern '%%' is not supported against SharePoint; only prefix, suffix, and contains shapes translate")
	}
	switch {
	case prefix && suffix:
		return "contains", body, nil
	case suffix:
		return "startswith", body, nil
	case prefix:
		return "endswith", body, nil
	}
	// No wildcards at all: equivalent to equality on a string column.
	return "contains", body, nil
}

// translateIn fans an IN list out to (col eq v1 or col eq v2 or ...). Microsoft
// Graph technically supports OData v4 `in` but it has had bugs and uneven
// rollout; the OR form is universally supported and produces the same plan.
func translateIn(n *parse.InOp, schema map[string]FieldInfo) (string, error) {
	field, ok := schema[n.Column]
	if !ok {
		return "", fmt.Errorf("unknown column %q", n.Column)
	}
	parts := make([]string, 0, len(n.Values))
	for _, v := range n.Values {
		if v.Kind == parse.ValNull {
			return "", fmt.Errorf("NULL is not allowed in an IN list; use IS NULL instead")
		}
		s, err := formatValue(v, field)
		if err != nil {
			return "", err
		}
		op := "eq"
		parts = append(parts, fmt.Sprintf("%s%s %s %s", ODataFieldPrefix, n.Column, op, s))
	}
	joiner := " or "
	expr := "(" + strings.Join(parts, joiner) + ")"
	if n.Not {
		return "not " + expr, nil
	}
	return expr, nil
}

// translateBetween emits the inclusive form `(col ge low and col le high)`.
// OData has no native BETWEEN but ge/le compose exactly the same way.
func translateBetween(n *parse.BetweenOp, schema map[string]FieldInfo) (string, error) {
	field, ok := schema[n.Column]
	if !ok {
		return "", fmt.Errorf("unknown column %q", n.Column)
	}
	lo, err := formatValue(n.Low, field)
	if err != nil {
		return "", err
	}
	hi, err := formatValue(n.High, field)
	if err != nil {
		return "", err
	}
	expr := fmt.Sprintf("(%s%s ge %s and %s%s le %s)", ODataFieldPrefix, n.Column, lo, ODataFieldPrefix, n.Column, hi)
	if n.Not {
		return "not " + expr, nil
	}
	return expr, nil
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
		if v.Bool {
			return "true", nil
		}
		return "false", nil
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
