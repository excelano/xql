package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/excelano/xql/internal/cell"
	"github.com/excelano/xql/internal/parse"
)

// CanonicalizeStmt rewrites every column-name reference in stmt to its
// canonical-case schema name (case-insensitive identifier resolution, like
// unquoted SQL identifiers in standard databases). After this pass runs, every
// downstream column lookup against schema is exact-case and the existing map
// indexing keeps working. Returns an unknown-column error on the first
// reference that does not resolve, and an ambiguous-column error when two
// schema columns differ only in case.
//
// aliases is an optional name-to-canonical map (typically SharePoint display
// name → internal name) so users can reference columns by either name. Pass
// nil when the backend has no aliases (CSV). Schema names take precedence:
// a user-typed name that matches a schema key resolves there even when the
// same string is also someone's display name. Aliases that collide with each
// other (case-insensitively) surface as ambiguous-column errors at query time.
func CanonicalizeStmt(stmt parse.Stmt, schema map[string]cell.ColumnInfo, aliases map[string][]string) error {
	r, err := newResolver(schema, aliases)
	if err != nil {
		return err
	}
	switch s := stmt.(type) {
	case *parse.SelectStmt:
		return canonicalizeSelect(s, r)
	case *parse.UpdateStmt:
		return canonicalizeUpdate(s, r)
	case *parse.DeleteStmt:
		return canonicalizePredicate(s.Where, r)
	case *parse.InsertStmt:
		return canonicalizeInsert(s, r)
	}
	return fmt.Errorf("internal: unknown statement type %T", stmt)
}

// resolver maps lowercased column names to the canonical schema name. The
// ambiguous set tracks lowercased names whose schema has more than one
// canonical form so the caller's error message can list both real names.
// aliasLower / aliasAmbiguous handle the same case-folding for the alias
// dimension (display names); schema lookups are tried first.
type resolver struct {
	byLower   map[string]string
	ambiguous map[string][]string

	aliasLower     map[string]string
	aliasAmbiguous map[string][]string
}

func newResolver(schema map[string]cell.ColumnInfo, aliases map[string][]string) (*resolver, error) {
	r := &resolver{byLower: make(map[string]string, len(schema))}
	for name := range schema {
		k := strings.ToLower(name)
		if existing, ok := r.byLower[k]; ok {
			if r.ambiguous == nil {
				r.ambiguous = make(map[string][]string)
			}
			r.ambiguous[k] = append(r.ambiguous[k], existing, name)
			continue
		}
		r.byLower[k] = name
	}

	for alias, targets := range aliases {
		ak := strings.ToLower(alias)
		// Schema match always wins; aliases that collide with a schema key
		// are silently shadowed. A display name that lowercase-equals an
		// internal name resolves to the internal name, which is what Graph
		// needs anyway.
		if _, inSchema := r.byLower[ak]; inSchema {
			continue
		}
		if len(targets) > 1 {
			// Two columns share this display name; surface that as
			// ambiguity at query time, listing the internal targets so
			// the user can disambiguate.
			if r.aliasAmbiguous == nil {
				r.aliasAmbiguous = make(map[string][]string)
			}
			r.aliasAmbiguous[ak] = append(r.aliasAmbiguous[ak], targets...)
			continue
		}
		if existing, ok := r.aliasLower[ak]; ok {
			// Two distinct aliases lowercase to the same key (e.g.
			// "Vendor" and "vendor" as display names of two different
			// columns).
			if r.aliasAmbiguous == nil {
				r.aliasAmbiguous = make(map[string][]string)
			}
			r.aliasAmbiguous[ak] = append(r.aliasAmbiguous[ak], existing, targets[0])
			continue
		}
		if r.aliasLower == nil {
			r.aliasLower = make(map[string]string)
		}
		r.aliasLower[ak] = targets[0]
	}

	return r, nil
}

// resolve returns the canonical schema name for user. Schema names take
// precedence over aliases. Reports ambiguity when both forms exist in the
// schema, even if user matches one exactly — silently picking the exact
// match would surprise the next user who happens to type the other case.
func (r *resolver) resolve(user string) (string, error) {
	k := strings.ToLower(user)
	if names, bad := r.ambiguous[k]; bad {
		sort.Strings(names)
		return "", fmt.Errorf("ambiguous column %q: matches %s", user, strings.Join(quote(dedupSorted(names)), " and "))
	}
	if canon, ok := r.byLower[k]; ok {
		return canon, nil
	}
	if names, bad := r.aliasAmbiguous[k]; bad {
		sort.Strings(names)
		return "", fmt.Errorf("ambiguous column %q: display name matches %s", user, strings.Join(quote(dedupSorted(names)), " and "))
	}
	if target, ok := r.aliasLower[k]; ok {
		return target, nil
	}
	return "", fmt.Errorf("unknown column %q", user)
}

func dedupSorted(xs []string) []string {
	out := xs[:0]
	var prev string
	for i, x := range xs {
		if i == 0 || x != prev {
			out = append(out, x)
		}
		prev = x
	}
	return out
}

func quote(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = fmt.Sprintf("%q", x)
	}
	return out
}

func canonicalizeSelect(s *parse.SelectStmt, r *resolver) error {
	for i := range s.Columns {
		if err := canonicalizeExpr(s.Columns[i].Expr, r); err != nil {
			return err
		}
	}
	if err := canonicalizePredicate(s.Where, r); err != nil {
		return err
	}
	for _, e := range s.GroupBy {
		if err := canonicalizeExpr(e, r); err != nil {
			return err
		}
	}
	if err := canonicalizePredicate(s.Having, r); err != nil {
		return err
	}
	for i := range s.OrderBy {
		canon, err := r.resolve(s.OrderBy[i].Column)
		if err != nil {
			// ORDER BY against an aggregated SELECT may name a projection
			// alias rather than a schema column — leave unresolved names
			// alone here so resolveOrderByOutput can still match the alias.
			if isUnknownColumn(err) {
				continue
			}
			return err
		}
		s.OrderBy[i].Column = canon
	}
	return nil
}

func canonicalizeUpdate(u *parse.UpdateStmt, r *resolver) error {
	for i := range u.Assignments {
		canon, err := r.resolve(u.Assignments[i].Column)
		if err != nil {
			return err
		}
		u.Assignments[i].Column = canon
		if err := canonicalizeExpr(u.Assignments[i].Value, r); err != nil {
			return err
		}
	}
	return canonicalizePredicate(u.Where, r)
}

func canonicalizeInsert(i *parse.InsertStmt, r *resolver) error {
	for k, col := range i.Columns {
		canon, err := r.resolve(col)
		if err != nil {
			return err
		}
		i.Columns[k] = canon
	}
	return nil
}

func canonicalizeExpr(e parse.Expr, r *resolver) error {
	switch n := e.(type) {
	case *parse.ColumnExpr:
		canon, err := r.resolve(n.Name)
		if err != nil {
			return err
		}
		n.Name = canon
		return nil
	case *parse.LiteralExpr:
		return nil
	case *parse.BinaryExpr:
		if err := canonicalizeExpr(n.L, r); err != nil {
			return err
		}
		return canonicalizeExpr(n.R, r)
	case *parse.AggregateExpr:
		if n.Star {
			return nil
		}
		return canonicalizeExpr(n.Arg, r)
	case *parse.FuncCallExpr:
		for _, a := range n.Args {
			if err := canonicalizeExpr(a, r); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("internal: unhandled expression type %T", e)
}

func canonicalizePredicate(p parse.Predicate, r *resolver) error {
	if p == nil {
		return nil
	}
	switch n := p.(type) {
	case *parse.BinaryOp:
		if err := canonicalizePredicate(n.L, r); err != nil {
			return err
		}
		return canonicalizePredicate(n.R, r)
	case *parse.NotOp:
		return canonicalizePredicate(n.Inner, r)
	case *parse.Comparison:
		return canonicalizeExpr(n.LExpr, r)
	case *parse.NullTest:
		canon, err := r.resolve(n.Column)
		if err != nil {
			return err
		}
		n.Column = canon
		return nil
	case *parse.LikeOp:
		canon, err := r.resolve(n.Column)
		if err != nil {
			return err
		}
		n.Column = canon
		return nil
	case *parse.InOp:
		canon, err := r.resolve(n.Column)
		if err != nil {
			return err
		}
		n.Column = canon
		return nil
	case *parse.BetweenOp:
		canon, err := r.resolve(n.Column)
		if err != nil {
			return err
		}
		n.Column = canon
		return nil
	}
	return fmt.Errorf("internal: unhandled predicate type %T", p)
}

func isUnknownColumn(err error) bool {
	return strings.HasPrefix(err.Error(), "unknown column ")
}
