package obsquery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Log query limits.
const (
	MaxQueryLen     = 1024
	maxQueryTerms   = 40
	maxQueryDepth   = 8
	DefaultLogLimit = 100
	MaxLogLimit     = 1000
)

// ── Lexer ───────────────────────────────────────────────────────────────

type tokKind int

const (
	tkEOF tokKind = iota
	tkLParen
	tkRParen
	tkOr
	tkAnd
	tkNot // the NOT keyword or a leading '-'
	tkText
	tkField
)

type token struct {
	kind  tokKind
	pos   int // byte offset
	field string
	val   string
}

func syntax(pos int, format string, a ...any) error {
	return invalid("query syntax error at position %d: %s", pos+1, sprintf(format, a...))
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func lex(s string) ([]token, error) {
	var toks []token
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case isSpace(c):
			i++
		case c == '(':
			toks = append(toks, token{kind: tkLParen, pos: i})
			i++
		case c == ')':
			toks = append(toks, token{kind: tkRParen, pos: i})
			i++
		case c == '-':
			if i+1 >= n || isSpace(s[i+1]) || s[i+1] == ')' {
				return nil, syntax(i, "'-' must be directly followed by a term (quote the text to search for a literal '-')")
			}
			toks = append(toks, token{kind: tkNot, pos: i})
			i++
		case c == '"':
			v, end, err := lexQuoted(s, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tkText, pos: i, val: v})
			i = end
		default:
			start := i
			colon := -1
			for i < n && !isSpace(s[i]) && s[i] != '(' && s[i] != ')' {
				if s[i] == '"' {
					if colon >= 0 && i == colon+1 {
						break
					}
					return nil, syntax(i, "unexpected '\"' inside a word (quote the whole phrase)")
				}
				if s[i] == ':' && colon < 0 {
					colon = i
				}
				i++
			}
			if i < n && s[i] == '"' { // field:"quoted value"
				v, end, err := lexQuoted(s, i)
				if err != nil {
					return nil, err
				}
				toks = append(toks, token{kind: tkField, pos: start, field: s[start:colon], val: v})
				i = end
				continue
			}
			w := s[start:i]
			switch {
			case w == "OR":
				toks = append(toks, token{kind: tkOr, pos: start})
			case w == "AND":
				toks = append(toks, token{kind: tkAnd, pos: start})
			case w == "NOT":
				toks = append(toks, token{kind: tkNot, pos: start})
			case colon >= 0:
				if colon == start {
					return nil, syntax(start, "missing field name before ':' (quote the text to search for a literal ':')")
				}
				if colon == i-1 {
					return nil, syntax(start, "missing value after %q", w)
				}
				toks = append(toks, token{kind: tkField, pos: start, field: s[start:colon], val: s[colon+1 : i]})
			default:
				toks = append(toks, token{kind: tkText, pos: start, val: w})
			}
		}
	}
	toks = append(toks, token{kind: tkEOF, pos: n})
	return toks, nil
}

func lexQuoted(s string, i int) (val string, end int, err error) {
	var b strings.Builder
	j := i + 1
	for j < len(s) {
		switch s[j] {
		case '\\':
			if j+1 >= len(s) {
				return "", 0, syntax(i, "unterminated quote")
			}
			b.WriteByte(s[j+1])
			j += 2
		case '"':
			if b.Len() == 0 {
				return "", 0, syntax(i, "empty quoted string")
			}
			return b.String(), j + 1, nil
		default:
			b.WriteByte(s[j])
			j++
		}
	}
	return "", 0, syntax(i, "unterminated quote")
}

// ── Parser ──────────────────────────────────────────────────────────────

// Node is a parsed log query expression.
type Node interface{}

type (
	textNode  struct{ val string }
	fieldNode struct {
		pos        int
		field, val string
	}
	notNode struct{ n Node }
	binNode struct {
		or   bool
		kids []Node
	}
)

type parser struct {
	toks  []token
	i     int
	terms int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

func (p *parser) parseOr(depth int) (Node, error) {
	if depth > maxQueryDepth {
		return nil, syntax(p.peek().pos, "query is nested more than %d levels deep", maxQueryDepth)
	}
	first, err := p.parseAnd(depth)
	if err != nil {
		return nil, err
	}
	kids := []Node{first}
	for p.peek().kind == tkOr {
		p.next()
		k, err := p.parseAnd(depth)
		if err != nil {
			return nil, err
		}
		kids = append(kids, k)
	}
	if len(kids) == 1 {
		return first, nil
	}
	return binNode{or: true, kids: kids}, nil
}

func startsUnary(k tokKind) bool {
	return k == tkLParen || k == tkNot || k == tkText || k == tkField
}

func (p *parser) parseAnd(depth int) (Node, error) {
	first, err := p.parseUnary(depth)
	if err != nil {
		return nil, err
	}
	kids := []Node{first}
	for {
		if p.peek().kind == tkAnd {
			p.next()
		} else if !startsUnary(p.peek().kind) {
			break
		}
		k, err := p.parseUnary(depth)
		if err != nil {
			return nil, err
		}
		kids = append(kids, k)
	}
	if len(kids) == 1 {
		return first, nil
	}
	return binNode{kids: kids}, nil
}

func (p *parser) parseUnary(depth int) (Node, error) {
	t := p.next()
	switch t.kind {
	case tkNot:
		if depth+1 > maxQueryDepth {
			return nil, syntax(t.pos, "query is nested more than %d levels deep", maxQueryDepth)
		}
		n, err := p.parseUnary(depth + 1)
		if err != nil {
			return nil, err
		}
		return notNode{n}, nil
	case tkLParen:
		n, err := p.parseOr(depth + 1)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tkRParen {
			return nil, syntax(t.pos, "unclosed '('")
		}
		return n, nil
	case tkText:
		if p.terms++; p.terms > maxQueryTerms {
			return nil, syntax(t.pos, "more than %d terms", maxQueryTerms)
		}
		return textNode{t.val}, nil
	case tkField:
		if p.terms++; p.terms > maxQueryTerms {
			return nil, syntax(t.pos, "more than %d terms", maxQueryTerms)
		}
		return fieldNode{pos: t.pos, field: t.field, val: t.val}, nil
	case tkRParen:
		return nil, syntax(t.pos, "unmatched ')'")
	case tkOr, tkAnd:
		return nil, syntax(t.pos, "operator needs a term on both sides")
	}
	return nil, syntax(t.pos, "unexpected end of query")
}

// ParseLogQuery parses the log query language into an AST. An empty query
// matches every record (nil Node).
func ParseLogQuery(q string) (Node, error) {
	if len(q) > MaxQueryLen {
		return nil, invalid("query is longer than %d characters", MaxQueryLen)
	}
	if !utf8.ValidString(q) {
		return nil, invalid("query is not valid UTF-8")
	}
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	toks, err := lex(q)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseOr(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tkEOF {
		return nil, syntax(t.pos, "unexpected %s", describe(t))
	}
	return n, nil
}

func describe(t token) string {
	switch t.kind {
	case tkRParen:
		return "')'"
	case tkLParen:
		return "'('"
	}
	return "token"
}

// ── Severity ────────────────────────────────────────────────────────────

var severityNames = [][]string{
	{"TRACE"},
	{"DEBUG"},
	{"INFO", "NOTICE"},
	{"WARN", "WARNING"},
	{"ERROR", "ERR"},
	{"FATAL", "CRITICAL", "PANIC"},
}

func severityLevel(name string) (int, bool) {
	u := strings.ToUpper(name)
	for lvl, names := range severityNames {
		for _, n := range names {
			if n == u {
				return lvl, true
			}
		}
	}
	return 0, false
}

// SeverityNames returns every stored SeverityText spelling in the inclusive
// level range [lo, hi].
func severitySet(lo, hi int) []string {
	var out []string
	for l := lo; l <= hi; l++ {
		out = append(out, severityNames[l]...)
	}
	return out
}

func (b *builder) severityCond(val string, pos int) (string, error) {
	op := "="
	v := val
	for _, o := range []string{">=", "<=", ">", "<"} {
		if strings.HasPrefix(val, o) {
			op, v = o, strings.TrimPrefix(val, o)
			break
		}
	}
	lvl, ok := severityLevel(v)
	if !ok {
		return "", syntax(pos, "unknown severity %q (use trace, debug, info, warn, error or fatal)", v)
	}
	last := len(severityNames) - 1
	var set []string
	switch op {
	case "=":
		set = severitySet(lvl, lvl)
	case ">=":
		set = severitySet(lvl, last)
	case ">":
		set = severitySet(lvl+1, last)
	case "<=":
		set = severitySet(0, lvl)
	case "<":
		set = severitySet(0, lvl-1)
	}
	if len(set) == 0 {
		return "0", nil
	}
	ps := make([]string, len(set))
	for i, s := range set {
		ps[i] = b.str(s)
	}
	return "upper(SeverityText) IN (" + strings.Join(ps, ", ") + ")", nil
}

// ── Compiler ────────────────────────────────────────────────────────────

const hasBody = "positionCaseInsensitive(Body, %s) > 0"

func (b *builder) compileNode(n Node) (string, error) {
	switch v := n.(type) {
	case textNode:
		return strings.Replace(hasBody, "%s", b.str(v.val), 1), nil
	case notNode:
		c, err := b.compileNode(v.n)
		if err != nil {
			return "", err
		}
		return "NOT (" + c + ")", nil
	case binNode:
		parts := make([]string, len(v.kids))
		for i, k := range v.kids {
			c, err := b.compileNode(k)
			if err != nil {
				return "", err
			}
			parts[i] = c
		}
		sep := " AND "
		if v.or {
			sep = " OR "
		}
		return "(" + strings.Join(parts, sep) + ")", nil
	case fieldNode:
		return b.compileField(v)
	}
	return "", invalid("internal: unknown query Node")
}

func (b *builder) eqOrPrefix(col, val string) string {
	switch {
	case val == "*":
		return col + " != ''"
	case strings.HasSuffix(val, "*"):
		return "startsWith(" + col + ", " + b.str(strings.TrimSuffix(val, "*")) + ")"
	}
	return col + " = " + b.str(val)
}

func (b *builder) mapCond(col, key, val string) (string, error) {
	if key == "" || len(key) > 128 {
		return "", invalid("attribute key must be 1-128 characters")
	}
	k := b.str(key)
	switch {
	case val == "*":
		return "mapContains(" + col + ", " + k + ")", nil
	case strings.HasSuffix(val, "*"):
		return "startsWith(" + col + "[" + k + "], " + b.str(strings.TrimSuffix(val, "*")) + ")", nil
	}
	return col + "[" + k + "] = " + b.str(val), nil
}

func (b *builder) compileField(f fieldNode) (string, error) {
	lower := strings.ToLower(f.field)
	switch {
	case lower == "severity" || lower == "level":
		return b.severityCond(f.val, f.pos)
	case lower == "service":
		return b.eqOrPrefix("ServiceName", f.val), nil
	case lower == "body" || lower == "message" || lower == "msg":
		return strings.Replace(hasBody, "%s", b.str(f.val), 1), nil
	case lower == "trace_id" || lower == "traceid":
		return "TraceId = " + b.str(strings.ToLower(f.val)), nil
	case lower == "span_id" || lower == "spanid":
		return "SpanId = " + b.str(strings.ToLower(f.val)), nil
	case strings.HasPrefix(lower, "attr."):
		return b.mapCond("LogAttributes", f.field[len("attr."):], f.val)
	case strings.HasPrefix(lower, "res."):
		return b.mapCond("ResourceAttributes", f.field[len("res."):], f.val)
	}
	return "", syntax(f.pos, "unknown field %q (fields: severity, service, body, trace_id, span_id, attr.<key>, res.<key>; quote the text to search for a literal ':')", f.field)
}

// ── Statement ───────────────────────────────────────────────────────────

// LogCursor is the keyset position of the last row of a page.
type LogCursor struct {
	TsNano int64  `json:"t"`
	Hash   uint64 `json:"h,string"`
	Finger string `json:"f"`
}

// LogQuery is a SearchLogs request.
type LogQuery struct {
	Scope      Scope
	Query      string
	Severities []string
	Oldest     bool
	Limit      int // rows to return; the statement fetches one more
	Cursor     *LogCursor
}

// Fingerprint identifies the filters a cursor belongs to. The time range is
// deliberately not part of it: the keyset position anchors the page, so a
// client that slides a relative range between pages still pages correctly.
func (q LogQuery) Fingerprint() string {
	h := sha256.New()
	for _, s := range append(append(q.Scope.Fingerprint(), q.Query, strconv.FormatBool(q.Oldest), "sev"), q.Severities...) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LogColumns is the select list, in the order the handler scans it.
const logHash = "cityHash64(Body, ServiceName, SpanId, TraceId, toString(LogAttributes))"

// Compile builds the SearchLogs statement. Columns, in order: Timestamp,
// SeverityText, SeverityNumber, ServiceName, Body, TraceId, SpanId,
// LogAttributes, ResourceAttributes, row hash.
func (q LogQuery) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > MaxLogLimit {
		return Stmt{}, invalid("page size must be 1-%d", MaxLogLimit)
	}
	ast, err := ParseLogQuery(q.Query)
	if err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCond("Timestamp", q.Scope)}
	sc, err := b.scopeConds(SigLogs, q.Scope)
	if err != nil {
		return Stmt{}, err
	}
	conds = append(conds, sc...)
	if len(q.Severities) > 0 {
		ps := make([]string, 0, len(q.Severities)*2)
		for _, s := range q.Severities {
			lvl, ok := severityLevel(s)
			if !ok {
				return Stmt{}, invalid("unknown severity %q (use trace, debug, info, warn, error or fatal)", s)
			}
			for _, name := range severityNames[lvl] {
				ps = append(ps, b.str(name))
			}
		}
		conds = append(conds, "upper(SeverityText) IN ("+strings.Join(ps, ", ")+")")
	}
	if ast != nil {
		c, err := b.compileNode(ast)
		if err != nil {
			return Stmt{}, err
		}
		conds = append(conds, c)
	}
	dir, cmp := "DESC", "<"
	if q.Oldest {
		dir, cmp = "ASC", ">"
	}
	if c := q.Cursor; c != nil {
		if c.Finger != q.Fingerprint() {
			return Stmt{}, invalid("page_token does not belong to this query")
		}
		conds = append(conds, "(Timestamp, "+logHash+") "+cmp+" ("+
			"fromUnixTimestamp64Nano("+b.i64(c.TsNano)+"), "+b.add("UInt64", strconv.FormatUint(c.Hash, 10))+")")
	}
	sql := "SELECT Timestamp, SeverityText, SeverityNumber, ServiceName, Body, TraceId, SpanId, LogAttributes, ResourceAttributes,\n" +
		"       " + logHash + " AS row_hash\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"ORDER BY Timestamp " + dir + ", row_hash " + dir + "\n" +
		"LIMIT " + strconv.Itoa(q.Limit+1)
	return b.stmt(sql), nil
}

// NextCursor builds the page token from the last returned row.
func (q LogQuery) NextCursor(ts time.Time, hash uint64) string {
	raw, _ := json.Marshal(LogCursor{TsNano: ts.UnixNano(), Hash: hash, Finger: q.Fingerprint()})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeLogCursor parses a page token.
func DecodeLogCursor(tok string) (*LogCursor, error) {
	if tok == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return nil, invalid("page_token is malformed")
	}
	var c LogCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Finger == "" {
		return nil, invalid("page_token is malformed")
	}
	return &c, nil
}
