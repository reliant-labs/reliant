package dashspec

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxFormulaLen bounds a panel's Formula.
const MaxFormulaLen = 256

// maxFormulaDepth bounds how deeply a formula nests (parentheses and unary
// minus), so a hostile one cannot exhaust a reader's stack.
const maxFormulaDepth = 32

// SeriesRef is the name a Formula gives Series[i]: A for the first, B for the
// second, up to J for the tenth.
func SeriesRef(i int) string { return string(rune('A' + i)) }

// Formula is a parsed Panel.Formula: arithmetic over a panel's series values.
// Every reader evaluates it with Eval, so a formula means the same thing in
// each.
type Formula struct {
	root formulaNode
}

// ParseFormula parses expr against a panel with series series.
//
// The grammar is the four operators over series references and numbers:
//
//	expr    = term { ("+" | "-") term }
//	term    = unary { ("*" | "/") unary }
//	unary   = "-" unary | primary
//	primary = number | ref | "(" expr ")"
//
// A ref is one capital letter naming a series by position (SeriesRef); a
// number is decimal, with an optional fraction and exponent. A formula must
// reference at least one series, and at most MaxFormulaLen bytes long.
func ParseFormula(expr string, series int) (*Formula, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("the formula is empty")
	}
	if len(expr) > MaxFormulaLen {
		return nil, fmt.Errorf("the formula is longer than %d characters", MaxFormulaLen)
	}
	p := &formulaParser{src: expr, series: series}
	root, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos < len(p.src) {
		return nil, p.errorf("unexpected %q", p.src[p.pos])
	}
	if !p.sawRef {
		return nil, fmt.Errorf("the formula references no series (A is the first, B the second, ...)")
	}
	return &Formula{root: root}, nil
}

// Eval computes the formula with vals[i] as Series[i]'s value. Division by
// zero yields 0, as a ratio of a zero denominator does, and so does any other
// result that is not a finite number: a reader draws it as zero rather than
// as a gap or an error.
func (f *Formula) Eval(vals []float64) float64 {
	v := f.root.eval(vals)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// String is the formula's canonical spelling: single spaces around binary
// operators and only the parentheses its precedence needs.
func (f *Formula) String() string { return f.root.String() }

// Refs are the indexes of the series the formula reads, ascending.
func (f *Formula) Refs() []int {
	seen := map[int]bool{}
	f.root.refs(seen)
	out := make([]int, 0, len(seen))
	for i := 0; i < maxSeriesPerPanel; i++ {
		if seen[i] {
			out = append(out, i)
		}
	}
	return out
}

type formulaNode interface {
	eval(vals []float64) float64
	refs(seen map[int]bool)
	prec() int
	String() string
}

type numNode struct {
	v   float64
	src string
}

func (n numNode) eval([]float64) float64 { return n.v }
func (numNode) refs(map[int]bool)        {}
func (numNode) prec() int                { return 3 }
func (n numNode) String() string         { return n.src }

type refNode int

func (r refNode) eval(vals []float64) float64 {
	if int(r) < len(vals) {
		return vals[r]
	}
	return 0
}
func (r refNode) refs(seen map[int]bool) { seen[int(r)] = true }
func (refNode) prec() int                { return 3 }
func (r refNode) String() string         { return SeriesRef(int(r)) }

type negNode struct{ x formulaNode }

func (n negNode) eval(vals []float64) float64 { return -n.x.eval(vals) }
func (n negNode) refs(seen map[int]bool)      { n.x.refs(seen) }
func (negNode) prec() int                     { return 2 }
func (n negNode) String() string {
	if n.x.prec() < 2 {
		return "-(" + n.x.String() + ")"
	}
	return "-" + n.x.String()
}

type binNode struct {
	op   byte
	l, r formulaNode
}

func (b binNode) eval(vals []float64) float64 {
	l, r := b.l.eval(vals), b.r.eval(vals)
	switch b.op {
	case '+':
		return l + r
	case '-':
		return l - r
	case '*':
		return l * r
	default: // '/'
		if r == 0 {
			return 0
		}
		return l / r
	}
}

func (b binNode) refs(seen map[int]bool) { b.l.refs(seen); b.r.refs(seen) }

func (b binNode) prec() int {
	if b.op == '+' || b.op == '-' {
		return 0
	}
	return 1
}

func (b binNode) String() string {
	l, r := b.l.String(), b.r.String()
	if b.l.prec() < b.prec() {
		l = "(" + l + ")"
	}
	// The right operand of a left-associative operator needs parentheses at
	// equal precedence too: A - (B - C) is not A - B - C.
	if b.r.prec() < b.prec() || (b.r.prec() == b.prec() && (b.op == '-' || b.op == '/' || b.r.(binNode).op != b.op)) {
		r = "(" + r + ")"
	}
	return l + " " + string(b.op) + " " + r
}

type formulaParser struct {
	src    string
	pos    int
	series int
	sawRef bool
}

func (p *formulaParser) errorf(format string, args ...any) error {
	return fmt.Errorf("at character %d: %s", p.pos+1, fmt.Sprintf(format, args...))
}

func (p *formulaParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *formulaParser) peek() byte {
	p.skipSpace()
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *formulaParser) expr(depth int) (formulaNode, error) {
	l, err := p.term(depth)
	if err != nil {
		return nil, err
	}
	for {
		op := p.peek()
		if op != '+' && op != '-' {
			return l, nil
		}
		p.pos++
		r, err := p.term(depth)
		if err != nil {
			return nil, err
		}
		l = binNode{op: op, l: l, r: r}
	}
}

func (p *formulaParser) term(depth int) (formulaNode, error) {
	l, err := p.unary(depth)
	if err != nil {
		return nil, err
	}
	for {
		op := p.peek()
		if op != '*' && op != '/' {
			return l, nil
		}
		p.pos++
		r, err := p.unary(depth)
		if err != nil {
			return nil, err
		}
		l = binNode{op: op, l: l, r: r}
	}
}

func (p *formulaParser) unary(depth int) (formulaNode, error) {
	if depth >= maxFormulaDepth {
		return nil, p.errorf("the formula nests deeper than %d levels", maxFormulaDepth)
	}
	if p.peek() == '-' {
		p.pos++
		x, err := p.unary(depth + 1)
		if err != nil {
			return nil, err
		}
		return negNode{x: x}, nil
	}
	return p.primary(depth)
}

func (p *formulaParser) primary(depth int) (formulaNode, error) {
	c := p.peek()
	switch {
	case c == 0:
		return nil, p.errorf("the formula ends where a series, a number or \"(\" belongs")
	case c == '(':
		p.pos++
		x, err := p.expr(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, p.errorf("missing \")\"")
		}
		p.pos++
		return x, nil
	case c >= 'A' && c <= 'Z':
		i := int(c - 'A')
		if p.pos+1 < len(p.src) && isFormulaWord(p.src[p.pos+1]) {
			return nil, p.errorf("a series is one capital letter (A is the first series, B the second, ...)")
		}
		if i >= p.series {
			return nil, p.errorf("%c names series %d, but the panel has %d", c, i+1, p.series)
		}
		p.pos++
		p.sawRef = true
		return refNode(i), nil
	case (c >= '0' && c <= '9') || c == '.':
		start := p.pos
		for p.pos < len(p.src) && (isDigit(p.src[p.pos]) || p.src[p.pos] == '.') {
			p.pos++
		}
		if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
			p.pos++
			if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
				p.pos++
			}
			for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
				p.pos++
			}
		}
		lit := p.src[start:p.pos]
		v, err := strconv.ParseFloat(lit, 64)
		if err != nil || math.IsInf(v, 0) {
			p.pos = start
			return nil, p.errorf("%q is not a number", lit)
		}
		return numNode{v: v, src: lit}, nil
	}
	return nil, p.errorf("unexpected %q (a formula is series A-J, numbers, + - * / and parentheses)", c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isFormulaWord(c byte) bool {
	return c == '_' || isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
