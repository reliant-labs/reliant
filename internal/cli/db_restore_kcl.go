package cli

// The KCL edit behind `forge db restore <env> --to <time>`: declare a new
// forge.ManagedDatabase in the env's main.k that is bootstrapped from another
// one's backups, stopped at a point in time.
//
// A restore IS a declaration (pkg/deploy/v1alpha1 DatabaseRestore): the
// platform honours spec.restore once, when it creates the database, and never
// over a database that already holds data. So this edit never touches the
// source database's literal. It adds a second literal beside it, with the
// source's own runtime (and namespace), and lists it in the same
// `databases = [...]` the source is listed in. Pointing the app at the
// restored copy is left to the author, after the rows check out.
//
// It locates everything on KCL's own AST (parseKCLForEdit, collectKCLFacts:
// the machinery `forge vuln exempt` uses) and splices new text in at offsets
// the AST gave, so the rest of the file keeps its bytes, comments and layout.
// It FAILS CLOSED: whenever it cannot see the source's name, runtime or the
// list that names it, it writes nothing and prints the lines to add by hand.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// managedDatabaseNameRE is ManagedDatabase.name's rule in kcl/schema.k: an
// RFC-1123 label of at most 40 characters.
var managedDatabaseNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const maxManagedDatabaseName = 40

// errDBRestoreNoAnchor is the refusal when the edit cannot be made safely.
type errDBRestoreNoAnchor struct {
	why     string
	snippet string
}

func (e *errDBRestoreNoAnchor) Error() string {
	return e.why + "\n  Nothing was written. Declare it by hand beside the source database, and list it in the Bundle's databases:\n\n" + e.snippet
}

// dbRestoreEdit is one restore being declared.
type dbRestoreEdit struct {
	source, name, pointInTime string
	alias                     string // the forge import's alias in the file
	runtime, namespace        string // source text of the source's fields, "" when unset
}

// literal is the restored database's KCL, on one line (inline) or as a
// top-level block.
func (e *dbRestoreEdit) literal(multiline bool) string {
	restore := fmt.Sprintf("restore = {sourceDatabase = %s", kclQuote(e.source))
	if e.pointInTime != "" {
		restore += fmt.Sprintf(", pointInTime = %s", kclQuote(e.pointInTime))
	}
	restore += "}"
	if !multiline {
		fields := []string{"name = " + kclQuote(e.name)}
		if e.runtime != "" {
			fields = append(fields, "runtime = "+e.runtime)
		}
		if e.namespace != "" {
			fields = append(fields, "namespace = "+e.namespace)
		}
		fields = append(fields, "spec = {"+restore+"}")
		return fmt.Sprintf("%s.ManagedDatabase {%s}", e.alias, strings.Join(fields, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s.ManagedDatabase {\n", e.alias)
	fmt.Fprintf(&b, "    name = %s\n", kclQuote(e.name))
	if e.runtime != "" {
		fmt.Fprintf(&b, "    runtime = %s\n", e.runtime)
	}
	if e.namespace != "" {
		fmt.Fprintf(&b, "    namespace = %s\n", e.namespace)
	}
	b.WriteString("    spec = {\n        restore = {\n")
	fmt.Fprintf(&b, "            sourceDatabase = %s\n", kclQuote(e.source))
	if e.pointInTime != "" {
		fmt.Fprintf(&b, "            pointInTime = %s\n", kclQuote(e.pointInTime))
	}
	b.WriteString("        }\n    }\n}")
	return b.String()
}

// comment is the line above a top-level declaration.
func (e *dbRestoreEdit) comment() string {
	at := "as far as its backups go"
	if e.pointInTime != "" {
		at = "as it was at " + e.pointInTime
	}
	return fmt.Sprintf("# %s %s (forge db restore). The app still reads %s:\n# check the rows, then pass this database's name where the env passes %s's.\n", e.source, at, e.source, e.source)
}

// variable is the top-level name the new declaration is assigned to.
func (e *dbRestoreEdit) variable() string {
	return "_" + strings.ReplaceAll(e.name, "-", "_")
}

func (e *dbRestoreEdit) refuse(format string, a ...any) error {
	alias := e.alias
	if alias == "" {
		alias = "forge"
	}
	cp := *e
	cp.alias = alias
	if cp.runtime == "" {
		cp.runtime = "<the source database's runtime>"
	}
	snippet := indentLines(cp.variable()+" = "+cp.literal(true), "    ") + "\n\n    databases = [<the source database>, " + cp.variable() + "]\n"
	return &errDBRestoreNoAnchor{why: fmt.Sprintf(format, a...), snippet: snippet}
}

// managedDatabaseName is a ManagedDatabase literal's name when it is a plain
// string literal.
func managedDatabaseName(l *kclSchemaLit) (string, bool) {
	if l.opaque {
		return "", false
	}
	name, found := "", false
	for _, it := range l.items {
		if it.key != "name" {
			continue
		}
		if found || kclType(it.vn) != "StringLit" || it.op != "Override" {
			return "", false
		}
		name, _ = it.vn["value"].(string)
		found = true
	}
	return name, found
}

// kclInsert is one insertion at an offset of the original.
type kclInsert struct {
	at   int
	text string
}

// applyKCLInserts applies insertions to src, the last offset first.
func applyKCLInserts(src string, ins []kclInsert) string {
	sort.SliceStable(ins, func(i, j int) bool { return ins[i].at > ins[j].at })
	for _, in := range ins {
		src = src[:in.at] + in.text + src[in.at:]
	}
	return src
}

// insertOf turns kclAppend's result back into the insertion it made.
func insertOf(orig, next string, lo int) kclInsert {
	return kclInsert{at: lo, text: next[lo : lo+len(next)-len(orig)]}
}

// restoreInKCL returns src with a ManagedDatabase `name`, restored from
// `source` at pointInTime ("" = as far as the backups go), declared beside
// the source. source "" means the file's only ManagedDatabase. It also
// returns the source's name it resolved. Any doubt is an
// *errDBRestoreNoAnchor and nothing is to be written.
func restoreInKCL(src, source, name, pointInTime string) (string, string, error) {
	next, resolved, _, err := restoreInKCLInserts(src, source, name, pointInTime)
	return next, resolved, err
}

// restoreInKCLInserts is restoreInKCL, also returning the insertions it made
// (for a preview).
func restoreInKCLInserts(src, source, name, pointInTime string) (string, string, []kclInsert, error) {
	e := &dbRestoreEdit{source: source, name: name, pointInTime: pointInTime, alias: "forge"}
	a, err := parseKCLForEdit(src)
	if err != nil {
		return "", "", nil, e.refuse("forge cannot parse this file safely (%v)", err)
	}
	f := collectKCLFacts(a)
	dbs := f.schemas["ManagedDatabase"]
	if len(dbs) > 0 && dbs[0].alias != "" {
		e.alias = dbs[0].alias
	}
	byName := map[string]*kclSchemaLit{}
	var names []string
	for _, l := range dbs {
		n, ok := managedDatabaseName(l)
		if !ok {
			return "", "", nil, e.refuse("the ManagedDatabase at line %d has a computed name (or unpacks a value), so forge cannot tell which database it is", kclLine(l.w))
		}
		if _, dup := byName[n]; dup {
			return "", "", nil, e.refuse("this file declares database %q twice, so forge cannot tell which declaration is the env's", n)
		}
		byName[n] = l
		names = append(names, n)
	}
	switch {
	case len(dbs) == 0:
		return "", "", nil, e.refuse("this file declares no forge.ManagedDatabase literal (the env's database may be declared in another file)")
	case e.source == "" && len(dbs) > 1:
		return "", "", nil, e.refuse("this file declares %d databases (%s): name the one to restore with --database", len(dbs), strings.Join(names, ", "))
	case e.source == "":
		e.source = names[0]
	}
	srcLit, ok := byName[e.source]
	if !ok {
		return "", "", nil, e.refuse("this file declares no ManagedDatabase named %q (it declares %s)", e.source, strings.Join(names, ", "))
	}
	if e.name == "" {
		e.name = e.source + "-restored"
	}
	if !managedDatabaseNameRE.MatchString(e.name) || len(e.name) > maxManagedDatabaseName {
		return "", "", nil, fmt.Errorf("database name %q must be a lowercase RFC-1123 label of at most %d characters: pass a shorter one with --as", e.name, maxManagedDatabaseName)
	}
	if _, taken := byName[e.name]; taken {
		return "", "", nil, fmt.Errorf("this env already declares a database named %q: a restore always creates a NEW database, so pass another name with --as", e.name)
	}
	for _, it := range srcLit.items {
		switch it.key {
		case "runtime", "namespace":
			start, end, ok := a.span(it.vw)
			if !ok {
				return "", "", nil, e.refuse("forge could not locate %s's %s at line %d exactly", e.source, it.key, kclLine(it.w))
			}
			text := src[start:end]
			if strings.Contains(text, "\n") {
				return "", "", nil, e.refuse("%s's %s at line %d spans several lines; forge copies only a one-line value", e.source, it.key, kclLine(it.w))
			}
			if it.key == "runtime" {
				e.runtime = text
			} else {
				e.namespace = text
			}
		}
	}

	// Where the source is listed: a Bundle's `databases = [...]` literal
	// holding either the source's literal itself, or the identifier it is
	// assigned to.
	stmt, variable := topLevelAssignOf(a, srcLit.w)
	if variable != "" && topLevelAssigned(a, e.variable()) {
		return "", "", nil, fmt.Errorf("this file already assigns %s: pass another database name with --as", e.variable())
	}
	var lists []kclItem
	for _, b := range f.schemas["Bundle"] {
		for _, it := range b.items {
			if it.key == "databases" {
				lists = append(lists, it)
			}
		}
	}
	var target *kclItem
	inline := false
	for i, it := range lists {
		if kclType(it.vn) != "List" {
			continue
		}
		if kclDirectElement(it.vn, srcLit.w) {
			target, inline = &lists[i], true
			break
		}
		if variable != "" && listNamesIdent(it.vn, variable) {
			target = &lists[i]
			break
		}
	}
	if target == nil {
		return "", "", nil, e.refuse("forge found no `databases = [...]` list literal in a Bundle that names %s, so it cannot list the restored database beside it", e.source)
	}
	if target.op != "Override" {
		return "", "", nil, e.refuse("the databases list at line %d is computed (not assigned with =), so forge cannot see what it holds", kclLine(target.vw))
	}

	var ins []kclInsert
	if inline {
		next, lo, _, err := kclAppend(a, target.vw, target.vn, "elts", "[", "]", e.literal(false))
		if err != nil {
			return "", "", nil, e.refuse("%v", err)
		}
		ins = append(ins, insertOf(src, next, lo))
	} else {
		if stmt == nil {
			return "", "", nil, e.refuse("%s's declaration at line %d is not a top-level assignment", e.source, kclLine(srcLit.w))
		}
		_, end, ok := a.span(stmt)
		if !ok {
			return "", "", nil, e.refuse("forge could not locate %s's declaration at line %d exactly", e.source, kclLine(stmt))
		}
		ins = append(ins, kclInsert{at: end, text: "\n\n" + e.comment() + e.variable() + " = " + e.literal(true)})
		next, lo, _, err := kclAppend(a, target.vw, target.vn, "elts", "[", "]", e.variable())
		if err != nil {
			return "", "", nil, e.refuse("%v", err)
		}
		in := insertOf(src, next, lo)
		if in.at < end {
			// The Bundle lists the database before the declaration ends:
			// impossible for a top-level assignment it references, so the
			// AST is not what this edit assumes.
			return "", "", nil, e.refuse("the databases list at line %d sits inside %s's declaration", kclLine(target.vw), e.source)
		}
		ins = append(ins, in)
	}
	out := applyKCLInserts(src, append([]kclInsert(nil), ins...))
	if err := verifyDBRestoreEdit(e, src, out, len(dbs)); err != nil {
		return "", "", nil, e.refuse("%v", err)
	}
	sort.SliceStable(ins, func(i, j int) bool { return ins[i].at < ins[j].at })
	return out, e.source, ins, nil
}

// topLevelAssignOf is the top-level `<name> = <value>` statement whose value
// is the node at w, and its name; nil, "" when w is not such a value.
func topLevelAssignOf(a *kclAST, w map[string]any) (map[string]any, string) {
	body, _ := a.body.([]any)
	for _, s := range body {
		sw, sn, ok := kclNode(s)
		if !ok || kclType(sn) != "Assign" {
			continue
		}
		vw, _, ok := kclNode(sn["value"])
		if !ok || fmt.Sprint(vw["line"], vw["column"]) != fmt.Sprint(w["line"], w["column"]) {
			continue
		}
		if name := assignTarget(sn); name != "" {
			return sw, name
		}
	}
	return nil, ""
}

// topLevelAssigned reports whether the file assigns name at top level.
func topLevelAssigned(a *kclAST, name string) bool {
	body, _ := a.body.([]any)
	for _, s := range body {
		if _, sn, ok := kclNode(s); ok && kclType(sn) == "Assign" && assignTarget(sn) == name {
			return true
		}
	}
	return false
}

// assignTarget is an Assign node's single plain target name, "" otherwise.
func assignTarget(n map[string]any) string {
	targets, _ := n["targets"].([]any)
	if len(targets) != 1 {
		return ""
	}
	_, tn, _ := kclNode(targets[0])
	if paths, _ := tn["paths"].([]any); len(paths) != 0 {
		return ""
	}
	nw, _ := tn["name"].(map[string]any)
	s, _ := nw["node"].(string)
	return s
}

// listNamesIdent reports whether the list node has the bare identifier name
// as an element.
func listNamesIdent(list map[string]any, name string) bool {
	elts, _ := list["elts"].([]any)
	for _, x := range elts {
		_, n, ok := kclNode(x)
		if !ok {
			continue
		}
		if names := kclIdentNames(n); kclType(n) == "Identifier" && len(names) == 1 && names[0] == name {
			return true
		}
	}
	return false
}

// verifyDBRestoreEdit checks the result parses, declares exactly one more
// ManagedDatabase, and that one is the restore as asked.
func verifyDBRestoreEdit(e *dbRestoreEdit, orig, next string, before int) error {
	if len(next) <= len(orig) {
		return fmt.Errorf("internal: the edit added nothing")
	}
	a, err := parseKCLForEdit(next)
	if err != nil {
		return fmt.Errorf("internal: the edited file does not parse (%v)", err)
	}
	f := collectKCLFacts(a)
	dbs := f.schemas["ManagedDatabase"]
	if len(dbs) != before+1 {
		return fmt.Errorf("internal: the edit declares %d databases, want %d", len(dbs), before+1)
	}
	found := 0
	for _, l := range dbs {
		if n, ok := managedDatabaseName(l); ok && n == e.name {
			found++
		}
	}
	if found != 1 {
		return fmt.Errorf("internal: the edited file declares %q %d times, want once", e.name, found)
	}
	return nil
}
