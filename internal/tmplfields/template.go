package tmplfields

import (
	"strings"
	"text/template/parse"
)

// template is one parsed template file: its main tree and its defines.
type template struct {
	main  *parse.Tree
	trees map[string]*parse.Tree
	// skip is why the template cannot be checked, "" when it can.
	skip string
}

func (s *scanner) loadTemplate(site callSite) (*template, error) {
	if !strings.HasSuffix(site.name, ".tmpl") {
		return &template{skip: "not a .tmpl (Render returns it verbatim)"}, nil
	}
	src, ok := s.cfg.Accessors[site.accessor].Get(site.arg, site.name)
	if !ok {
		return &template{skip: "template not found"}, nil
	}
	return parseTemplate(site.name, string(src)), nil
}

// parseTemplate parses src as Render does, without its FuncMap: function
// names are not checked here, only the data they are handed.
func parseTemplate(name, src string) *template {
	tr := parse.New(name)
	tr.Mode = parse.SkipFuncCheck
	trees := map[string]*parse.Tree{}
	main, err := tr.Parse(src, "", "", trees)
	if err != nil {
		return &template{skip: "template does not parse"}
	}
	return &template{main: main, trees: trees}
}

// miss is one reference the data cannot satisfy.
type miss struct {
	at, field string
	on        *typ
}

// check walks the template with root as dot and returns what root lacks:
// each missing field of each type once, at its first reference, in walk
// order.
func (tm *template) check(root *typ) []miss {
	w := &walker{tm: tm, seen: map[string]bool{}, done: map[defineKey]bool{}}
	w.tree(tm.main, root)
	return w.misses
}

type defineKey struct {
	name string
	dot  *typ
}

type walker struct {
	tm     *template
	cur    *parse.Tree
	misses []miss
	seen   map[string]bool
	done   map[defineKey]bool
}

// cell is a template variable. Cells are shared by every scope that can see
// the variable, so an `=` assignment anywhere opens it everywhere: a
// template variable is untyped, and `$x = .Other` changes what it holds.
type cell struct{ t *typ }

type vars map[string]*cell

func (v vars) copy() vars {
	out := make(vars, len(v))
	for k, c := range v {
		out[k] = c
	}
	return out
}

func (w *walker) tree(t *parse.Tree, dot *typ) {
	if t == nil || t.Root == nil {
		return
	}
	prev := w.cur
	w.cur = t
	w.list(t.Root, dot, vars{"$": {t: dot}})
	w.cur = prev
}

func (w *walker) list(l *parse.ListNode, dot *typ, v vars) {
	if l == nil {
		return
	}
	for _, n := range l.Nodes {
		w.node(n, dot, v)
	}
}

func (w *walker) node(n parse.Node, dot *typ, v vars) {
	switch n := n.(type) {
	case *parse.ActionNode:
		w.pipe(n.Pipe, dot, v, true)
	case *parse.IfNode:
		inner := v.copy()
		w.pipe(n.Pipe, dot, inner, true)
		w.list(n.List, dot, inner.copy())
		w.list(n.ElseList, dot, inner.copy())
	case *parse.WithNode:
		inner := v.copy()
		t := w.pipe(n.Pipe, dot, inner, true)
		w.list(n.List, t, inner.copy())
		w.list(n.ElseList, dot, inner.copy())
	case *parse.RangeNode:
		inner := v.copy()
		elem := w.pipe(n.Pipe, dot, inner, false).elem.get()
		switch len(n.Pipe.Decl) {
		case 1:
			inner[n.Pipe.Decl[0].Ident[0]] = &cell{t: elem}
		case 2:
			inner[n.Pipe.Decl[0].Ident[0]] = &cell{t: open}
			inner[n.Pipe.Decl[1].Ident[0]] = &cell{t: elem}
		}
		w.list(n.List, elem, inner.copy())
		w.list(n.ElseList, dot, inner.copy())
	case *parse.TemplateNode:
		t := open
		if n.Pipe != nil {
			t = w.pipe(n.Pipe, dot, v, true)
		}
		if t.members == nil {
			return
		}
		k := defineKey{n.Name, t}
		if w.done[k] {
			return
		}
		w.done[k] = true
		w.tree(w.tm.trees[n.Name], t)
	case *parse.ListNode:
		w.list(n, dot, v)
	}
}

// pipe checks every argument of p and returns its type: that of its one
// argument when it is a lone field, variable or dot, else OPEN. declare
// binds p's declared variables (range binds its own).
func (w *walker) pipe(p *parse.PipeNode, dot *typ, v vars, declare bool) *typ {
	if p == nil {
		return open
	}
	result := open
	for _, cmd := range p.Cmds {
		for _, arg := range cmd.Args {
			t := w.arg(arg, dot, v)
			if len(p.Cmds) == 1 && len(cmd.Args) == 1 {
				result = t
			}
		}
	}
	if p.IsAssign {
		for _, d := range p.Decl {
			if c, ok := v[d.Ident[0]]; ok {
				c.t = open
			}
		}
		return result
	}
	if declare {
		for i, d := range p.Decl {
			t := result
			if len(p.Decl) > 1 || i > 0 {
				t = open
			}
			v[d.Ident[0]] = &cell{t: t}
		}
	}
	return result
}

func (w *walker) arg(n parse.Node, dot *typ, v vars) *typ {
	switch n := n.(type) {
	case *parse.DotNode:
		return dot
	case *parse.FieldNode:
		return w.chain(dot, n.Ident, n)
	case *parse.VariableNode:
		base := open
		if c, ok := v[n.Ident[0]]; ok {
			base = c.t
		}
		return w.chain(base, n.Ident[1:], n)
	case *parse.ChainNode:
		return w.chain(w.arg(n.Node, dot, v), n.Field, n)
	case *parse.PipeNode:
		return w.pipe(n, dot, v, false)
	}
	return open
}

// chain follows .A.B.C from t, recording the first name t cannot supply.
func (w *walker) chain(t *typ, idents []string, n parse.Node) *typ {
	for _, id := range idents {
		if t.members == nil {
			return open
		}
		m, ok := t.members[id]
		if !ok {
			at, _ := w.cur.ErrorContext(n)
			if key := id + "\x00" + t.desc; !w.seen[key] {
				w.seen[key] = true
				w.misses = append(w.misses, miss{at: at, field: id, on: t})
			}
			return open
		}
		t = m.typ.get()
	}
	return t
}
