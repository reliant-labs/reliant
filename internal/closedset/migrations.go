package closedset

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Resolver returns the values of the closed set a declaration names.
type Resolver func(name string) ([]string, error)

// Finding is one closed-set declaration whose CHECK does not hold its set, or
// that cannot be checked at all.
type Finding struct {
	// File and Line locate the finding: the migration that last defined the
	// CHECK when its list is wrong, the COMMENT ON CONSTRAINT otherwise.
	File string
	Line int
	// Table and Constraint name the CHECK as the migrations write them.
	Table, Constraint string
	// Set is the declared set's name, "" when the declaration names none.
	Set string
	// Problem is the sentence saying what is wrong.
	Problem string
	// Missing are the set's values the CHECK lacks; Extra the CHECK's values
	// the set lacks. Both empty unless the lists differ.
	Missing, Extra []string
	// Fix is the forward migration that repairs the finding, nil when there
	// is none forge can write (an unknown set, a CHECK it cannot read).
	Fix *Fix
}

// Analyze reads every *.up.sql migration under dir in apply order and checks
// each constraint whose catalog comment declares `forge:closed-set <name>`:
// the value list of the CHECK the migrations LAST defined for it must equal
// the named set. A missing directory has no declarations.
//
// It models what postgres does with the statements, as far as the
// declaration goes: a DROP CONSTRAINT drops the constraint's comment with
// it, so a CHECK replaced by DROP + ADD has lost its declaration unless the
// migration re-issues the COMMENT. That is reported too — silently losing
// the declaration would silently end the check.
func Analyze(dir string, resolve Resolver) ([]Finding, error) {
	files, err := migrationFiles(dir)
	if err != nil {
		return nil, err
	}
	m := &model{constraints: map[string]*constraint{}}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		m.apply(f, string(data))
	}
	var out []Finding
	for _, c := range m.ordered() {
		if f, ok := c.check(resolve); ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func migrationFiles(dir string) ([]string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".up.sql") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	// Version-prefixed names: lexical order is apply order.
	sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
	return files, nil
}

// location is a file:line in the migrations.
type location struct {
	File string
	Line int
}

func (l location) str() string { return fmt.Sprintf("%s:%d", filepath.Base(l.File), l.Line) }

// declaration is a COMMENT ON CONSTRAINT carrying forge:closed-set.
type declaration struct {
	at location
	// set is the named set, "" when the marker names none.
	set string
	// comment is the whole comment body as written (quotes still doubled),
	// re-issued verbatim by a fix.
	comment string
}

// constraint is the state the migrations leave one named constraint in.
type constraint struct {
	// table and name as the defining statement wrote them.
	table, name string
	// defined is false before any definition and after a DROP.
	defined bool
	// kind is "check" or the keyword of another constraint kind.
	kind  string
	expr  string // a CHECK's expression, the text inside CHECK ( … )
	defAt location
	// decl is the live declaration; lost is one a DROP took with it.
	decl, lost *declaration
	// orphanDecl is a declaration on a constraint no migration defines.
	orphanDecl *declaration
	order      int
}

type model struct {
	constraints map[string]*constraint
	next        int
}

func key(table, name string) string { return sqlident.Last(table) + "\x00" + sqlident.Last(name) }

func (m *model) get(table, name string) *constraint {
	k := key(table, name)
	c, ok := m.constraints[k]
	if !ok {
		c = &constraint{table: table, name: name, order: m.next}
		m.next++
		m.constraints[k] = c
	}
	return c
}

func (m *model) ordered() []*constraint {
	out := make([]*constraint, 0, len(m.constraints))
	for _, c := range m.constraints {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}

var (
	alterHeadRe  = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?(` + sqlident.Qualified + `)`)
	createHeadRe = regexp.MustCompile(`(?is)^\s*create\s+(?:(?:global|local)\s+)?(?:(?:temporary|temp|unlogged)\s+)?table\s+(?:if\s+not\s+exists\s+)?(` + sqlident.Qualified + `)`)
	dropTableRe  = regexp.MustCompile(`(?is)^\s*drop\s+table\s+(?:if\s+exists\s+)?((?:` + sqlident.Qualified + `)(?:\s*,\s*` + sqlident.Qualified + `)*)`)
	commentRe    = regexp.MustCompile(`(?is)^\s*comment\s+on\s+constraint\s+(` + sqlident.Part + `)\s+on\s+(` + sqlident.Qualified + `)\s+is\s+(?:'((?:[^']|'')*)'|null\b)`)
	defineRe     = regexp.MustCompile(`(?is)\bconstraint\s+(` + sqlident.Part + `)\s+(check|foreign|unique|primary|exclude|references)\b`)
	dropConRe    = regexp.MustCompile(`(?is)\bdrop\s+constraint\s+(?:if\s+exists\s+)?(` + sqlident.Part + `)`)
	renameConRe  = regexp.MustCompile(`(?is)\brename\s+constraint\s+(` + sqlident.Part + `)\s+to\s+(` + sqlident.Part + `)`)
	markerRe     = regexp.MustCompile(`(?:^|[^\w/.:-])` + regexp.QuoteMeta(schemadef.ColumnMarkerClosedSet) + `(?:[ \t]+([^\s']+))?`)
)

// apply runs one migration file's statements against the model.
func (m *model) apply(file, content string) {
	masked := maskComments(content)
	cursor := 0
	for _, stmt := range schemadef.SplitStatements(masked) {
		at := strings.Index(masked[cursor:], stmt)
		if at < 0 {
			continue
		}
		start := cursor + at
		cursor = start + len(stmt)
		orig := content[start : start+len(stmt)]
		loc := func(off int) location {
			return location{File: file, Line: strings.Count(content[:start+off], "\n") + 1}
		}
		m.statement(stmt, orig, loc)
	}
}

// statement applies one statement. masked is it with comments blanked out
// (same offsets), so a keyword inside a comment is never read; orig is the
// text as written, which literals and expressions are read from.
func (m *model) statement(masked, orig string, loc func(int) location) {
	if g := commentRe.FindStringSubmatchIndex(masked); g != nil {
		name, table := masked[g[2]:g[3]], masked[g[4]:g[5]]
		c := m.get(table, name)
		var body string
		if g[6] >= 0 {
			body = orig[g[6]:g[7]]
		}
		mk := markerRe.FindStringSubmatch(strings.ReplaceAll(body, "''", "'"))
		if mk == nil {
			// A comment without the marker replaces the declaration:
			// dropping it is deliberate.
			if c.defined {
				c.decl, c.lost = nil, nil
			}
			return
		}
		d := &declaration{at: loc(g[0]), set: strings.TrimRight(mk[1], ".,;:"), comment: body}
		if !c.defined {
			c.orphanDecl = d
			return
		}
		c.decl, c.lost = d, nil
		return
	}
	if g := dropTableRe.FindStringSubmatchIndex(masked); g != nil {
		for _, t := range strings.Split(masked[g[2]:g[3]], ",") {
			for _, c := range m.constraints {
				if sqlident.Last(c.table) == sqlident.Last(t) {
					c.drop()
				}
			}
		}
		return
	}
	table := ""
	if g := alterHeadRe.FindStringSubmatchIndex(masked); g != nil {
		table = masked[g[2]:g[3]]
	} else if g := createHeadRe.FindStringSubmatchIndex(masked); g != nil {
		table = masked[g[2]:g[3]]
	} else {
		return
	}

	type event struct {
		at   int
		kind string
		g    []int
	}
	var events []event
	for _, g := range defineRe.FindAllStringSubmatchIndex(masked, -1) {
		events = append(events, event{g[0], "define", g})
	}
	for _, g := range dropConRe.FindAllStringSubmatchIndex(masked, -1) {
		events = append(events, event{g[0], "drop", g})
	}
	for _, g := range renameConRe.FindAllStringSubmatchIndex(masked, -1) {
		events = append(events, event{g[0], "rename", g})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at < events[j].at })
	for _, e := range events {
		g := e.g
		switch e.kind {
		case "define":
			c := m.get(table, masked[g[2]:g[3]])
			c.drop()
			c.table, c.name = table, masked[g[2]:g[3]]
			c.defined, c.defAt = true, loc(g[0])
			c.kind = strings.ToLower(masked[g[4]:g[5]])
			c.expr = ""
			if c.kind == "check" {
				c.expr = parenBody(orig, g[5])
			}
		case "drop":
			if c, ok := m.constraints[key(table, masked[g[2]:g[3]])]; ok {
				c.drop()
			}
		case "rename":
			from, to := masked[g[2]:g[3]], masked[g[4]:g[5]]
			c, ok := m.constraints[key(table, from)]
			if !ok {
				continue
			}
			delete(m.constraints, key(table, from))
			c.name = to
			m.constraints[key(table, to)] = c
		}
	}
}

// drop is what DROP CONSTRAINT does to the declaration: the comment goes
// with the constraint.
func (c *constraint) drop() {
	if !c.defined {
		return
	}
	if c.decl != nil {
		c.lost = c.decl
	}
	c.defined, c.decl = false, nil
}

// parenBody returns the text inside the parenthesized group that opens at or
// after s[from], honoring string constants. "" when there is none.
func parenBody(s string, from int) string {
	open := strings.IndexByte(s[from:], '(')
	if open < 0 {
		return ""
	}
	open += from
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			q := s[i]
			for i++; i < len(s); i++ {
				if s[i] == q {
					if i+1 < len(s) && s[i+1] == q {
						i++
						continue
					}
					break
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i]
			}
		}
	}
	return ""
}

// maskComments blanks out every comment in SQL text, keeping newlines and
// byte offsets, so a statement's structure is read without reading prose.
// String constants, quoted identifiers and dollar-quoted bodies are kept.
func maskComments(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '\'' || b[i] == '"':
			q := b[i]
			for i++; i < len(b); i++ {
				if b[i] == q {
					if i+1 < len(b) && b[i+1] == q {
						i++
						continue
					}
					break
				}
			}
		case b[i] == '$':
			if j := strings.IndexByte(s[i+1:], '$'); j >= 0 {
				tag := s[i : i+j+2]
				if isDollarTag(tag) {
					if k := strings.Index(s[i+len(tag):], tag); k >= 0 {
						i += len(tag) + k + len(tag) - 1
					}
				}
			}
		case b[i] == '-' && i+1 < len(b) && b[i+1] == '-':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			stop := len(b)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			for ; i < stop; i++ {
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
			i--
		}
	}
	return string(b)
}

func isDollarTag(tag string) bool {
	inner := tag[1 : len(tag)-1]
	for i := 0; i < len(inner); i++ {
		if !isIdentByte(inner[i]) || (i == 0 && isDigit(inner[i])) {
			return false
		}
	}
	return true
}

// check judges one constraint's final state.
func (c *constraint) check(resolve Resolver) (Finding, bool) {
	f := Finding{Table: c.table, Constraint: c.name}
	switch {
	case c.orphanDecl != nil && c.decl == nil:
		f.File, f.Line, f.Set = c.orphanDecl.at.File, c.orphanDecl.at.Line, c.orphanDecl.set
		f.Problem = fmt.Sprintf("%s on constraint %s on %s, which no earlier migration defines — postgres refuses a COMMENT ON a constraint that does not exist yet; define the CHECK first",
			schemadef.ColumnMarkerClosedSet, c.name, c.table)
		return f, true
	case c.decl != nil:
		return c.checkDeclared(*c.decl, resolve)
	case c.lost != nil && c.defined:
		// Re-created without re-declaring: the check stopped running. Judge
		// the list against the lost declaration's set too, so the forgotten
		// COMMENT never hides drift.
		f, ok := c.checkDeclared(*c.lost, resolve)
		if !ok {
			f = Finding{Table: c.table, Constraint: c.name, Set: c.lost.set}
		}
		f.File, f.Line = c.defAt.File, c.defAt.Line
		lost := fmt.Sprintf("constraint %s on %s was re-created at %s after its %s comment (%s), and postgres drops a constraint's comment with the constraint — the declaration is gone and nothing checks the CHECK any more; re-issue the COMMENT ON CONSTRAINT after the ADD",
			c.name, c.table, c.defAt.str(), schemadef.ColumnMarkerClosedSet, c.lost.at.str())
		if f.Problem != "" {
			lost += "; and " + f.Problem
		}
		f.Problem = lost
		if f.Fix == nil {
			f.Fix = commentOnlyFix(c, *c.lost)
		}
		return f, true
	}
	return Finding{}, false
}

func (c *constraint) checkDeclared(d declaration, resolve Resolver) (Finding, bool) {
	f := Finding{Table: c.table, Constraint: c.name, Set: d.set, File: d.at.File, Line: d.at.Line}
	if d.set == "" {
		f.Problem = fmt.Sprintf("%s names no set — write `%s <name>`; forge exports %s, and a project set is named `<import path>.<Var>`",
			schemadef.ColumnMarkerClosedSet, schemadef.ColumnMarkerClosedSet, strings.Join(Names(), ", "))
		return f, true
	}
	if c.kind != "check" {
		f.Problem = fmt.Sprintf("%s declares a CHECK constraint's value list, and %s on %s is a %s constraint — it does nothing there; move it to the CHECK",
			schemadef.ColumnMarkerClosedSet, c.name, c.table, strings.ToUpper(c.kind))
		return f, true
	}
	want, err := resolve(d.set)
	if err != nil {
		f.Problem = err.Error()
		return f, true
	}
	list, err := ParseCheck(c.expr)
	if err != nil {
		f.File, f.Line = c.defAt.File, c.defAt.Line
		f.Problem = fmt.Sprintf("cannot read the value list of CHECK %s on %s (defined at %s): %v", c.name, c.table, c.defAt.str(), err)
		return f, true
	}
	f.Missing, f.Extra = diff(want, list.Values)
	if len(f.Missing) == 0 && len(f.Extra) == 0 {
		return Finding{}, false
	}
	f.File, f.Line = c.defAt.File, c.defAt.Line
	var parts []string
	if len(f.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("lacks %s", quoteAll(f.Missing)))
	}
	if len(f.Extra) > 0 {
		parts = append(parts, fmt.Sprintf("admits %s, which %s does not hold", quoteAll(f.Extra), d.set))
	}
	f.Problem = fmt.Sprintf("CHECK %s on %s (last defined at %s) is declared %s %s and %s",
		c.name, c.table, c.defAt.str(), schemadef.ColumnMarkerClosedSet, d.set, strings.Join(parts, " and "))
	f.Fix = buildFix(c, d, list, want, f.Missing, f.Extra)
	return f, true
}

// diff returns want's values got lacks, and got's values want lacks, each in
// its own list's order.
func diff(want, got []string) (missing, extra []string) {
	in := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	for _, v := range want {
		if !in(got, v) && !in(missing, v) {
			missing = append(missing, v)
		}
	}
	for _, v := range got {
		if !in(want, v) && !in(extra, v) {
			extra = append(extra, v)
		}
	}
	return missing, extra
}

func quoteAll(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = "'" + v + "'"
	}
	return strings.Join(q, ", ")
}
