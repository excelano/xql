package parse

import (
	"fmt"
	"strconv"
	"strings"
)

// AST root.

type Stmt interface{ stmt() }

func (*SelectStmt) stmt() {}
func (*UpdateStmt) stmt() {}
func (*DeleteStmt) stmt() {}
func (*InsertStmt) stmt() {}

// SelectStmt represents a SELECT. Star is true for `SELECT *`; in that case
// Columns is nil. Distinct is true for `SELECT DISTINCT ...`. GroupBy is the
// list of grouping expressions in user order — bare column names appear here
// as ColumnExpr; a scalar-function call like `LOWER(app_name)` appears as
// FuncCallExpr, wrapping the column reference. Having is the post-aggregation
// predicate.
type SelectStmt struct {
	Distinct bool
	Star     bool
	Columns  []Projection
	Where    Predicate
	GroupBy  []Expr
	Having   Predicate
	OrderBy  []OrderKey
	Limit    *int
	Offset   *int
}

// Projection is one entry in a SELECT list. Alias is "" when the user did
// not write an `AS <name>` clause; renderers may synthesize a display label
// in that case.
type Projection struct {
	Expr  Expr
	Alias string
}

// OrderKey is one entry in an ORDER BY list: a column and a direction.
// Desc=false is ASC. ORDER BY is column-only in v2; expression keys are
// deferred to v2.1.
type OrderKey struct {
	Column string
	Desc   bool
}

type UpdateStmt struct {
	Assignments []Assignment
	Where       Predicate
}

type DeleteStmt struct {
	Where Predicate
}

type InsertStmt struct {
	Columns []string
	Values  []Value
}

// Assignment is one `col = <expr>` in UPDATE SET. The RHS is an Expr so that
// v2 can support computed assignments like `SET counter = counter + 1`.
type Assignment struct {
	Column string
	Value  Expr
}

// Expr is the projection / assignment-RHS / comparison-LHS expression tree.

type Expr interface{ expr() }

func (*ColumnExpr) expr()    {}
func (*LiteralExpr) expr()   {}
func (*BinaryExpr) expr()    {}
func (*AggregateExpr) expr() {}
func (*FuncCallExpr) expr()  {}

type ColumnExpr struct {
	Name string
}

type LiteralExpr struct {
	Value Value
}

// BinaryExpr carries an arithmetic op: "+", "-", "*", "/".
type BinaryExpr struct {
	Op string
	L  Expr
	R  Expr
}

// AggregateExpr is one of COUNT, SUM, AVG, MIN, MAX. Star is true only for
// COUNT(*); Arg is meaningful otherwise. Nested aggregates and DISTINCT
// arguments are not enforced at parse time — the executor decides.
type AggregateExpr struct {
	Func string
	Star bool
	Arg  Expr
}

// FuncCallExpr is a scalar function call: NAME(arg1, arg2, ...). Name is the
// upper-case canonical form (LOWER, UPPER, TRIM); the whitelist of supported
// names and their arity is enforced by the evaluator, not the parser, so
// unknown-function errors surface with the same "unknown function" phrasing
// regardless of the call shape.
type FuncCallExpr struct {
	Name string
	Args []Expr
}

// Predicate is the WHERE / HAVING tree.

type Predicate interface{ predicate() }

func (*BinaryOp) predicate()   {}
func (*NotOp) predicate()      {}
func (*Comparison) predicate() {}
func (*NullTest) predicate()   {}
func (*LikeOp) predicate()     {}
func (*InOp) predicate()       {}
func (*BetweenOp) predicate()  {}

// BinaryOp is "AND" or "OR".
type BinaryOp struct {
	Op string
	L  Predicate
	R  Predicate
}

type NotOp struct {
	Inner Predicate
}

// Comparison: <expr> op literal. Op is one of "=", "!=", "<", "<=", ">", ">=".
// The LHS broadens to a full Expr in v2 so `WHERE price * qty > 100` and
// `HAVING COUNT(*) > 5` share one node shape.
type Comparison struct {
	LExpr Expr
	Op    string
	Value Value
}

// NullTest: column IS [NOT] NULL.
type NullTest struct {
	Column string
	Not    bool
}

// LikeOp: column [NOT] LIKE 'pattern' or column [NOT] ILIKE 'pattern'.
// Pattern is the raw string literal — wildcards (%, _) and the backslash
// escape are interpreted at evaluation time so each executor (sqlcsv
// in-process, spsql OData) can apply them however suits its target.
// Insensitive=true marks the ILIKE variant: pattern and column value are
// compared case-insensitively.
type LikeOp struct {
	Column      string
	Pattern     string
	Not         bool
	Insensitive bool
}

// InOp: column [NOT] IN (v1, v2, ...). Values is non-empty; the parser
// rejects "IN ()".
type InOp struct {
	Column string
	Values []Value
	Not    bool
}

// BetweenOp: column [NOT] BETWEEN low AND high. Bounds are inclusive,
// matching standard SQL.
type BetweenOp struct {
	Column string
	Low    Value
	High   Value
	Not    bool
}

// Value is a literal. Kind selects which field is meaningful.
type Value struct {
	Kind ValueKind
	Str  string
	Num  string
	Bool bool
}

type ValueKind int

const (
	ValString ValueKind = iota
	ValNumber
	ValBool
	ValNull
)

// ParseError carries a byte-offset position to enable caret-style error
// rendering in later phases.
type ParseError struct {
	Msg string
	Pos int
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at offset %d: %s", e.Pos, e.Msg)
}

func parseErrorAt(pos int, msg string) *ParseError {
	return &ParseError{Msg: msg, Pos: pos}
}

// PreProcess strips REPL conveniences (trailing ";" and "!") from input
// before it reaches the parser. The bool return is the "skip prompt / commit
// immediately" signal carried by a trailing "!". Both suffixes may appear in
// either order and are stripped iteratively.
func PreProcess(input string) (string, bool) {
	s := strings.TrimSpace(input)
	commit := false
	for {
		changed := false
		if strings.HasSuffix(s, ";") {
			s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
			changed = true
		}
		if strings.HasSuffix(s, "!") {
			commit = true
			s = strings.TrimSpace(strings.TrimSuffix(s, "!"))
			changed = true
		}
		if !changed {
			break
		}
	}
	return s, commit
}

// Parse turns a SQL statement string into its AST. Input must be pre-processed
// (trailing ";" / "!" stripped) — call PreProcess first when coming from the
// REPL or --exec.
func Parse(input string) (Stmt, error) {
	p, err := newParser(input)
	if err != nil {
		return nil, err
	}
	return p.parseStatement()
}

// Lexer.

type TokenType int

const (
	TokEOF TokenType = iota
	TokIdent
	TokQuotedIdent
	TokString
	TokNumber
	TokStar
	TokPlus
	TokMinus
	TokSlash
	TokLParen
	TokRParen
	TokComma
	TokEq
	TokNe
	TokLt
	TokLe
	TokGt
	TokGe
	TokSelect
	TokDistinct
	TokUpdate
	TokDelete
	TokInsert
	TokSet
	TokValues
	TokWhere
	TokAnd
	TokOr
	TokNot
	TokIs
	TokNull
	TokTrue
	TokFalse
	TokOrder
	TokGroup
	TokHaving
	TokBy
	TokAs
	TokAsc
	TokDesc
	TokLimit
	TokOffset
	TokLike
	TokILike
	TokIn
	TokBetween
)

type Token struct {
	Type TokenType
	Lit  string
	Pos  int
}

var keywords = map[string]TokenType{
	"SELECT":   TokSelect,
	"DISTINCT": TokDistinct,
	"UPDATE":   TokUpdate,
	"DELETE":   TokDelete,
	"INSERT":   TokInsert,
	"SET":      TokSet,
	"VALUES":   TokValues,
	"WHERE":    TokWhere,
	"AND":      TokAnd,
	"OR":       TokOr,
	"NOT":      TokNot,
	"IS":       TokIs,
	"NULL":     TokNull,
	"TRUE":     TokTrue,
	"FALSE":    TokFalse,
	"ORDER":    TokOrder,
	"GROUP":    TokGroup,
	"HAVING":   TokHaving,
	"BY":       TokBy,
	"AS":       TokAs,
	"ASC":      TokAsc,
	"DESC":     TokDesc,
	"LIMIT":    TokLimit,
	"OFFSET":   TokOffset,
	"LIKE":     TokLike,
	"ILIKE":    TokILike,
	"IN":       TokIn,
	"BETWEEN":  TokBetween,
}

// aggregateNames is checked in parseFactor when a TokIdent is followed by '('.
// Names that match (case-insensitive) become AggregateExpr nodes; everything
// else stays a column reference. Listing them outside `keywords` keeps `MIN`,
// `MAX`, etc. usable as bare column names.
var aggregateNames = map[string]string{
	"COUNT": "COUNT",
	"SUM":   "SUM",
	"AVG":   "AVG",
	"MIN":   "MIN",
	"MAX":   "MAX",
}

type lexer struct {
	src string
	pos int
}

// skipWhitespace also discards SQL comments. Line comments start with `--`
// and run to end-of-line (or end-of-input); block comments start with `/*`
// and run to `*/`. Block comments do not nest, matching ANSI SQL.
func (l *lexer) skipWhitespace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			l.pos++
			continue
		}
		if c == '-' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '-' {
			l.pos += 2
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		if c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
			l.pos += 2
			for l.pos < len(l.src) {
				if l.src[l.pos] == '*' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/' {
					l.pos += 2
					break
				}
				l.pos++
			}
			continue
		}
		break
	}
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func (l *lexer) next() (Token, error) {
	l.skipWhitespace()
	if l.pos >= len(l.src) {
		return Token{Type: TokEOF, Pos: l.pos}, nil
	}
	start := l.pos
	c := l.src[l.pos]
	switch {
	case c == '\'':
		return l.lexString(start)
	case c == '"':
		return l.lexQuotedIdent(start)
	case isDigit(c):
		return l.lexNumber(start)
	case isLetter(c) || c == '_':
		return l.lexIdent(start)
	}

	switch c {
	case '*':
		l.pos++
		return Token{Type: TokStar, Lit: "*", Pos: start}, nil
	case '+':
		l.pos++
		return Token{Type: TokPlus, Lit: "+", Pos: start}, nil
	case '-':
		l.pos++
		return Token{Type: TokMinus, Lit: "-", Pos: start}, nil
	case '/':
		l.pos++
		return Token{Type: TokSlash, Lit: "/", Pos: start}, nil
	case '(':
		l.pos++
		return Token{Type: TokLParen, Lit: "(", Pos: start}, nil
	case ')':
		l.pos++
		return Token{Type: TokRParen, Lit: ")", Pos: start}, nil
	case ',':
		l.pos++
		return Token{Type: TokComma, Lit: ",", Pos: start}, nil
	case '=':
		l.pos++
		return Token{Type: TokEq, Lit: "=", Pos: start}, nil
	case '!':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: TokNe, Lit: "!=", Pos: start}, nil
		}
		return Token{}, parseErrorAt(start, "expected '=' after '!'")
	case '<':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: TokLe, Lit: "<=", Pos: start}, nil
		}
		l.pos++
		return Token{Type: TokLt, Lit: "<", Pos: start}, nil
	case '>':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: TokGe, Lit: ">=", Pos: start}, nil
		}
		l.pos++
		return Token{Type: TokGt, Lit: ">", Pos: start}, nil
	}

	return Token{}, parseErrorAt(start, fmt.Sprintf("unexpected character %q", c))
}

func (l *lexer) lexString(start int) (Token, error) {
	l.pos++ // consume opening '
	var sb strings.Builder
	for {
		if l.pos >= len(l.src) {
			return Token{}, parseErrorAt(start, "unterminated string literal")
		}
		c := l.src[l.pos]
		if c == '\'' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '\'' {
				sb.WriteByte('\'')
				l.pos += 2
				continue
			}
			l.pos++
			return Token{Type: TokString, Lit: sb.String(), Pos: start}, nil
		}
		sb.WriteByte(c)
		l.pos++
	}
}

func (l *lexer) lexQuotedIdent(start int) (Token, error) {
	l.pos++ // consume opening "
	var sb strings.Builder
	for {
		if l.pos >= len(l.src) {
			return Token{}, parseErrorAt(start, "unterminated quoted identifier")
		}
		c := l.src[l.pos]
		if c == '"' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '"' {
				sb.WriteByte('"')
				l.pos += 2
				continue
			}
			l.pos++
			if sb.Len() == 0 {
				return Token{}, parseErrorAt(start, "empty quoted identifier")
			}
			return Token{Type: TokQuotedIdent, Lit: sb.String(), Pos: start}, nil
		}
		sb.WriteByte(c)
		l.pos++
	}
}

func (l *lexer) lexNumber(start int) (Token, error) {
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		if l.pos >= len(l.src) || !isDigit(l.src[l.pos]) {
			return Token{}, parseErrorAt(start, "expected digit after '.'")
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	return Token{Type: TokNumber, Lit: l.src[start:l.pos], Pos: start}, nil
}

func (l *lexer) lexIdent(start int) (Token, error) {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isLetter(c) || isDigit(c) || c == '_' {
			l.pos++
			continue
		}
		break
	}
	lit := l.src[start:l.pos]
	if kw, ok := keywords[strings.ToUpper(lit)]; ok {
		return Token{Type: kw, Lit: lit, Pos: start}, nil
	}
	return Token{Type: TokIdent, Lit: lit, Pos: start}, nil
}

// Parser.

type parser struct {
	tokens []Token
	pos    int
}

func newParser(input string) (*parser, error) {
	l := &lexer{src: input}
	var toks []Token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.Type == TokEOF {
			break
		}
	}
	return &parser{tokens: toks}, nil
}

func (p *parser) peek() Token { return p.tokens[p.pos] }

func (p *parser) peekAt(offset int) Token {
	i := p.pos + offset
	if i >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[i]
}

func (p *parser) advance() Token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

func (p *parser) accept(tt TokenType) (Token, bool) {
	if p.peek().Type == tt {
		return p.advance(), true
	}
	return Token{}, false
}

func (p *parser) expect(tt TokenType, what string) (Token, error) {
	if t, ok := p.accept(tt); ok {
		return t, nil
	}
	got := p.peek()
	return Token{}, parseErrorAt(got.Pos, fmt.Sprintf("expected %s, got %s", what, describeToken(got)))
}

func (p *parser) expectEOF() error {
	if p.peek().Type != TokEOF {
		t := p.peek()
		return parseErrorAt(t.Pos, fmt.Sprintf("unexpected %s after end of statement", describeToken(t)))
	}
	return nil
}

func (p *parser) parseStatement() (Stmt, error) {
	switch p.peek().Type {
	case TokSelect:
		p.advance()
		return p.parseSelectBody()
	case TokUpdate:
		p.advance()
		return p.parseUpdateBody()
	case TokDelete:
		p.advance()
		return p.parseDeleteBody()
	case TokInsert:
		p.advance()
		return p.parseInsertBody()
	case TokEOF:
		return nil, parseErrorAt(p.peek().Pos, "empty input")
	default:
		t := p.peek()
		return nil, parseErrorAt(t.Pos, fmt.Sprintf("expected SELECT, UPDATE, DELETE, or INSERT, got %s", describeToken(t)))
	}
}

func (p *parser) parseSelectBody() (Stmt, error) {
	sel := &SelectStmt{}
	if _, ok := p.accept(TokDistinct); ok {
		sel.Distinct = true
	}
	if _, ok := p.accept(TokStar); ok {
		sel.Star = true
	} else {
		projs, err := p.parseProjectionList()
		if err != nil {
			return nil, err
		}
		sel.Columns = projs
	}
	if _, ok := p.accept(TokWhere); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		sel.Where = pred
	}
	if _, ok := p.accept(TokGroup); ok {
		cols, err := p.parseGroupBy()
		if err != nil {
			return nil, err
		}
		sel.GroupBy = cols
	}
	if _, ok := p.accept(TokHaving); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		sel.Having = pred
	}
	if _, ok := p.accept(TokOrder); ok {
		keys, err := p.parseOrderBy()
		if err != nil {
			return nil, err
		}
		sel.OrderBy = keys
	}
	if _, ok := p.accept(TokLimit); ok {
		n, err := p.parseNonNegativeInt("LIMIT")
		if err != nil {
			return nil, err
		}
		sel.Limit = &n
	}
	if _, ok := p.accept(TokOffset); ok {
		n, err := p.parseNonNegativeInt("OFFSET")
		if err != nil {
			return nil, err
		}
		sel.Offset = &n
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return sel, nil
}

func (p *parser) parseProjectionList() ([]Projection, error) {
	first, err := p.parseProjection()
	if err != nil {
		return nil, err
	}
	projs := []Projection{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		pr, err := p.parseProjection()
		if err != nil {
			return nil, err
		}
		projs = append(projs, pr)
	}
	return projs, nil
}

func (p *parser) parseProjection() (Projection, error) {
	e, err := p.parseExpr()
	if err != nil {
		return Projection{}, err
	}
	pr := Projection{Expr: e}
	if _, ok := p.accept(TokAs); ok {
		alias, err := p.parseIdent("alias name after AS")
		if err != nil {
			return Projection{}, err
		}
		pr.Alias = alias
	}
	return pr, nil
}

func (p *parser) parseGroupBy() ([]Expr, error) {
	if _, err := p.expect(TokBy, "BY after GROUP"); err != nil {
		return nil, err
	}
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	exprs := []Expr{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, e)
	}
	return exprs, nil
}

func (p *parser) parseOrderBy() ([]OrderKey, error) {
	if _, err := p.expect(TokBy, "BY after ORDER"); err != nil {
		return nil, err
	}
	first, err := p.parseOrderKey()
	if err != nil {
		return nil, err
	}
	keys := []OrderKey{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		k, err := p.parseOrderKey()
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (p *parser) parseOrderKey() (OrderKey, error) {
	col, err := p.parseColumn()
	if err != nil {
		return OrderKey{}, err
	}
	k := OrderKey{Column: col}
	if _, ok := p.accept(TokAsc); ok {
		k.Desc = false
	} else if _, ok := p.accept(TokDesc); ok {
		k.Desc = true
	}
	return k, nil
}

// parseNonNegativeInt accepts a bare positive number token after LIMIT / OFFSET.
// Floats and negatives are rejected, since both would be nonsensical for the
// caller. The clause name appears in the error so the user can see which one
// objected.
func (p *parser) parseNonNegativeInt(clause string) (int, error) {
	t := p.peek()
	if t.Type == TokMinus {
		p.advance()
		next := p.peek()
		return 0, parseErrorAt(t.Pos, fmt.Sprintf("%s requires a non-negative integer, got -%s", clause, next.Lit))
	}
	if t.Type != TokNumber {
		return 0, parseErrorAt(t.Pos, fmt.Sprintf("expected non-negative integer after %s, got %s", clause, describeToken(t)))
	}
	if strings.ContainsRune(t.Lit, '.') {
		return 0, parseErrorAt(t.Pos, fmt.Sprintf("%s requires an integer, got %s", clause, t.Lit))
	}
	n, err := strconv.Atoi(t.Lit)
	if err != nil {
		return 0, parseErrorAt(t.Pos, fmt.Sprintf("%s: invalid number %q", clause, t.Lit))
	}
	p.advance()
	return n, nil
}

func (p *parser) parseUpdateBody() (Stmt, error) {
	if _, err := p.expect(TokSet, "SET"); err != nil {
		return nil, err
	}
	first, err := p.parseAssignment()
	if err != nil {
		return nil, err
	}
	assigns := []Assignment{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		a, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		assigns = append(assigns, a)
	}
	upd := &UpdateStmt{Assignments: assigns}
	if _, ok := p.accept(TokWhere); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		upd.Where = pred
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return upd, nil
}

func (p *parser) parseAssignment() (Assignment, error) {
	col, err := p.parseColumn()
	if err != nil {
		return Assignment{}, err
	}
	if _, err := p.expect(TokEq, "'='"); err != nil {
		return Assignment{}, err
	}
	e, err := p.parseExpr()
	if err != nil {
		return Assignment{}, err
	}
	return Assignment{Column: col, Value: e}, nil
}

func (p *parser) parseDeleteBody() (Stmt, error) {
	del := &DeleteStmt{}
	if _, ok := p.accept(TokWhere); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		del.Where = pred
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return del, nil
}

func (p *parser) parseInsertBody() (Stmt, error) {
	if _, err := p.expect(TokLParen, "'('"); err != nil {
		return nil, err
	}
	cols, err := p.parseColumnList()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	if _, err := p.expect(TokValues, "VALUES"); err != nil {
		return nil, err
	}
	if _, err := p.expect(TokLParen, "'('"); err != nil {
		return nil, err
	}
	first, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	values := []Value{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return &InsertStmt{Columns: cols, Values: values}, nil
}

func (p *parser) parseColumnList() ([]string, error) {
	first, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	cols := []string{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		c, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, nil
}

func (p *parser) parseColumn() (string, error) {
	t := p.peek()
	if t.Type == TokIdent || t.Type == TokQuotedIdent {
		p.advance()
		return t.Lit, nil
	}
	return "", parseErrorAt(t.Pos, fmt.Sprintf("expected column name, got %s", describeToken(t)))
}

func (p *parser) parseIdent(what string) (string, error) {
	t := p.peek()
	if t.Type == TokIdent || t.Type == TokQuotedIdent {
		p.advance()
		return t.Lit, nil
	}
	return "", parseErrorAt(t.Pos, fmt.Sprintf("expected %s, got %s", what, describeToken(t)))
}

// parseValue accepts a literal in WHERE-RHS / IN-list / BETWEEN-bound /
// INSERT-VALUES position. `- <number>` is folded into a single negative
// numeric literal so the AST keeps Value semantics. The parser does not
// accept compound expressions here in v2; if literals turn into expressions
// it would be in a later version.
func (p *parser) parseValue() (Value, error) {
	t := p.peek()
	switch t.Type {
	case TokString:
		p.advance()
		return Value{Kind: ValString, Str: t.Lit}, nil
	case TokNumber:
		p.advance()
		return Value{Kind: ValNumber, Num: t.Lit}, nil
	case TokMinus:
		minusTok := p.advance()
		next := p.peek()
		if next.Type != TokNumber {
			return Value{}, parseErrorAt(minusTok.Pos, fmt.Sprintf("expected number after '-', got %s", describeToken(next)))
		}
		p.advance()
		return Value{Kind: ValNumber, Num: "-" + next.Lit}, nil
	case TokTrue:
		p.advance()
		return Value{Kind: ValBool, Bool: true}, nil
	case TokFalse:
		p.advance()
		return Value{Kind: ValBool, Bool: false}, nil
	case TokNull:
		p.advance()
		return Value{Kind: ValNull}, nil
	}
	return Value{}, parseErrorAt(t.Pos, fmt.Sprintf("expected literal value, got %s", describeToken(t)))
}

// Expression grammar:
//
//   expr   := term (('+' | '-') term)*
//   term   := factor (('*' | '/') factor)*
//   factor := <column> | <literal> | <aggregate> | '(' expr ')'
//
// '(' inside an expression always starts a sub-expression; predicate
// grouping is handled at the WHERE / HAVING atom level instead, so the two
// rules never overlap.

func (p *parser) parseExpr() (Expr, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		var op string
		switch p.peek().Type {
		case TokPlus:
			op = "+"
		case TokMinus:
			op = "-"
		default:
			return left, nil
		}
		p.advance()
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, L: left, R: right}
	}
}

func (p *parser) parseTerm() (Expr, error) {
	left, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	for {
		var op string
		switch p.peek().Type {
		case TokStar:
			op = "*"
		case TokSlash:
			op = "/"
		default:
			return left, nil
		}
		p.advance()
		right, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, L: left, R: right}
	}
}

func (p *parser) parseFactor() (Expr, error) {
	t := p.peek()
	switch t.Type {
	case TokLParen:
		p.advance()
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRParen, "')'"); err != nil {
			return nil, err
		}
		return e, nil
	case TokString, TokNumber, TokTrue, TokFalse, TokNull, TokMinus:
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &LiteralExpr{Value: v}, nil
	case TokQuotedIdent:
		p.advance()
		return &ColumnExpr{Name: t.Lit}, nil
	case TokIdent:
		// Any IDENT immediately followed by '(' is a function call. Aggregate
		// names dispatch to parseAggregateBody; anything else becomes a
		// FuncCallExpr, and the evaluator's whitelist decides whether the name
		// is a supported scalar (LOWER, UPPER, TRIM). The lookahead keeps
		// MIN / MAX / COUNT etc. usable as column names when NOT followed by
		// '(' — a bare `count` in ORDER BY still means the column, not the
		// aggregate.
		if p.peekAt(1).Type == TokLParen {
			if fn, ok := aggregateNames[strings.ToUpper(t.Lit)]; ok {
				p.advance() // consume name
				p.advance() // consume '('
				return p.parseAggregateBody(fn)
			}
			name := t.Lit
			p.advance() // consume name
			p.advance() // consume '('
			return p.parseFuncCallBody(name)
		}
		p.advance()
		return &ColumnExpr{Name: t.Lit}, nil
	}
	return nil, parseErrorAt(t.Pos, fmt.Sprintf("expected expression, got %s", describeToken(t)))
}

// parseAggregateBody is called after the function name and '(' have been
// consumed. Handles COUNT(*) specially; for the other aggregates a single
// argument expression is required.
func (p *parser) parseAggregateBody(fn string) (Expr, error) {
	if _, ok := p.accept(TokStar); ok {
		if fn != "COUNT" {
			return nil, parseErrorAt(p.peek().Pos, fmt.Sprintf("only COUNT accepts '*'; %s requires a column expression", fn))
		}
		if _, err := p.expect(TokRParen, "')'"); err != nil {
			return nil, err
		}
		return &AggregateExpr{Func: fn, Star: true}, nil
	}
	arg, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	return &AggregateExpr{Func: fn, Arg: arg}, nil
}

// parseFuncCallBody is called after a non-aggregate function name and its
// opening '(' have been consumed. Parses zero or more comma-separated argument
// expressions and the closing ')'. The name is normalized to upper case so
// downstream dispatch can compare against a canonical whitelist without
// re-folding at every call site.
func (p *parser) parseFuncCallBody(name string) (Expr, error) {
	upper := strings.ToUpper(name)
	if p.peek().Type == TokRParen {
		p.advance()
		return &FuncCallExpr{Name: upper}, nil
	}
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	args := []Expr{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		next, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, next)
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	return &FuncCallExpr{Name: upper, Args: args}, nil
}

func (p *parser) parsePredicate() (Predicate, error) {
	return p.parseDisjunction()
}

func (p *parser) parseDisjunction() (Predicate, error) {
	left, err := p.parseConjunction()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.accept(TokOr); !ok {
			break
		}
		right, err := p.parseConjunction()
		if err != nil {
			return nil, err
		}
		left = &BinaryOp{Op: "OR", L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseConjunction() (Predicate, error) {
	left, err := p.parseNegation()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.accept(TokAnd); !ok {
			break
		}
		right, err := p.parseNegation()
		if err != nil {
			return nil, err
		}
		left = &BinaryOp{Op: "AND", L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseNegation() (Predicate, error) {
	if _, ok := p.accept(TokNot); ok {
		inner, err := p.parseNegation()
		if err != nil {
			return nil, err
		}
		return &NotOp{Inner: inner}, nil
	}
	return p.parseAtom()
}

// parseAtom is one predicate leaf. The grammar:
//
//   atom := '(' predicate ')'
//        | <expr> IS [NOT] NULL                  // <expr> must be a column
//        | <expr> [NOT] LIKE 'pattern'           // <expr> must be a column
//        | <expr> [NOT] ILIKE 'pattern'          // <expr> must be a column
//        | <expr> [NOT] IN (v, ...)              // <expr> must be a column
//        | <expr> [NOT] BETWEEN low AND high     // <expr> must be a column
//        | <expr> <cmp-op> <literal>             // <expr> is unconstrained
//
// The column-only constraint for IS / LIKE / IN / BETWEEN is enforced after
// the LHS is parsed so the error points at the bad expression, not the op.
func (p *parser) parseAtom() (Predicate, error) {
	if _, ok := p.accept(TokLParen); ok {
		inner, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRParen, "')'"); err != nil {
			return nil, err
		}
		return inner, nil
	}
	lhs, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, ok := p.accept(TokIs); ok {
		col, ok := columnName(lhs)
		if !ok {
			return nil, parseErrorAt(p.peek().Pos, "IS NULL requires a column on the left")
		}
		not := false
		if _, ok := p.accept(TokNot); ok {
			not = true
		}
		if _, err := p.expect(TokNull, "NULL"); err != nil {
			return nil, err
		}
		return &NullTest{Column: col, Not: not}, nil
	}
	notTok, hasNot := p.accept(TokNot)
	if _, ok := p.accept(TokLike); ok {
		col, ok := columnName(lhs)
		if !ok {
			return nil, parseErrorAt(notTok.Pos, "LIKE requires a column on the left")
		}
		return p.parseLikeBody(col, hasNot, false)
	}
	if _, ok := p.accept(TokILike); ok {
		col, ok := columnName(lhs)
		if !ok {
			return nil, parseErrorAt(notTok.Pos, "ILIKE requires a column on the left")
		}
		return p.parseLikeBody(col, hasNot, true)
	}
	if _, ok := p.accept(TokIn); ok {
		col, ok := columnName(lhs)
		if !ok {
			return nil, parseErrorAt(notTok.Pos, "IN requires a column on the left")
		}
		return p.parseInBody(col, hasNot)
	}
	if _, ok := p.accept(TokBetween); ok {
		col, ok := columnName(lhs)
		if !ok {
			return nil, parseErrorAt(notTok.Pos, "BETWEEN requires a column on the left")
		}
		return p.parseBetweenBody(col, hasNot)
	}
	if hasNot {
		return nil, parseErrorAt(notTok.Pos, fmt.Sprintf("expected LIKE, ILIKE, IN, or BETWEEN after NOT, got %s", describeToken(p.peek())))
	}
	opTok := p.peek()
	var op string
	switch opTok.Type {
	case TokEq:
		op = "="
	case TokNe:
		op = "!="
	case TokLt:
		op = "<"
	case TokLe:
		op = "<="
	case TokGt:
		op = ">"
	case TokGe:
		op = ">="
	default:
		return nil, parseErrorAt(opTok.Pos, fmt.Sprintf("expected comparison operator, IS, LIKE, ILIKE, IN, or BETWEEN, got %s", describeToken(opTok)))
	}
	p.advance()
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if v.Kind == ValNull {
		return nil, parseErrorAt(opTok.Pos, fmt.Sprintf("cannot use '%s' with NULL; use IS NULL or IS NOT NULL instead", op))
	}
	return &Comparison{LExpr: lhs, Op: op, Value: v}, nil
}

// columnName returns the column name when e is a bare ColumnExpr. Used by
// the IS / LIKE / IN / BETWEEN paths to enforce their column-only LHS.
func columnName(e Expr) (string, bool) {
	if c, ok := e.(*ColumnExpr); ok {
		return c.Name, true
	}
	return "", false
}

func (p *parser) parseLikeBody(col string, not, insensitive bool) (Predicate, error) {
	t := p.peek()
	if t.Type != TokString {
		name := "LIKE"
		if insensitive {
			name = "ILIKE"
		}
		return nil, parseErrorAt(t.Pos, fmt.Sprintf("%s requires a string pattern, got %s", name, describeToken(t)))
	}
	p.advance()
	return &LikeOp{Column: col, Pattern: t.Lit, Not: not, Insensitive: insensitive}, nil
}

func (p *parser) parseInBody(col string, not bool) (Predicate, error) {
	if _, err := p.expect(TokLParen, "'(' after IN"); err != nil {
		return nil, err
	}
	if p.peek().Type == TokRParen {
		return nil, parseErrorAt(p.peek().Pos, "IN requires at least one value")
	}
	first, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	values := []Value{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	return &InOp{Column: col, Values: values, Not: not}, nil
}

func (p *parser) parseBetweenBody(col string, not bool) (Predicate, error) {
	low, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if low.Kind == ValNull {
		return nil, parseErrorAt(p.peek().Pos, "BETWEEN bounds cannot be NULL")
	}
	if _, err := p.expect(TokAnd, "AND between BETWEEN bounds"); err != nil {
		return nil, err
	}
	high, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if high.Kind == ValNull {
		return nil, parseErrorAt(p.peek().Pos, "BETWEEN bounds cannot be NULL")
	}
	return &BetweenOp{Column: col, Low: low, High: high, Not: not}, nil
}

func describeToken(t Token) string {
	switch t.Type {
	case TokEOF:
		return "end of input"
	case TokIdent:
		return fmt.Sprintf("identifier %q", t.Lit)
	case TokQuotedIdent:
		return fmt.Sprintf("quoted identifier %q", t.Lit)
	case TokString:
		return fmt.Sprintf("string literal %q", t.Lit)
	case TokNumber:
		return fmt.Sprintf("number %s", t.Lit)
	default:
		return fmt.Sprintf("%q", t.Lit)
	}
}
