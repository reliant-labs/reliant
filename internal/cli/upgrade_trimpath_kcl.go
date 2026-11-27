package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codemod"
)

// The KCL step of `forge fix trimpath-default` (upgrade_trimpath_default.go):
// "-trimpath" into the flags of every GoBuild literal under deploy/kcl.
//
// It reads each file with KCL's own parser (parseKCLForEdit, the AST `forge
// vuln exempt` edits by) and edits bytes at the positions the AST names, so
// nothing else in the file moves:
//
//   - no `flags`: `, flags = ["-trimpath"]` before the closing brace of a
//     one-line literal, or a `flags = ["-trimpath"]` line at the attribute
//     indentation of a multi-line one;
//   - `flags` a list of plain strings: "-trimpath" as its first element.
//
// A literal whose source already says trimpath (-trimpath=false included) is
// left alone. Every other shape — flags bound to a variable or an expression,
// a list with a non-string element, a `**` spread or an if-entry, a schema
// derived from GoBuild — is reported with its file:line, never guessed at.
// An edited file must parse again and hold "-trimpath" in each edited
// literal's flags, or it is not written.

// trimpathFlagsAttr is the attribute the step writes into a literal.
const trimpathFlagsAttr = `flags = ["-trimpath"]`

// trimpathFlagsComment is the comment forge scaffolds above a GoBuild that
// declares -trimpath (internal/codegen's goBuildComment, without its
// indentation), so an upgraded project reads as a new one does. The step
// writes it above the line it adds the flag on.
var trimpathFlagsComment = []string{
	"# flags: -trimpath lets every worktree share Go's build cache; forge build,",
	"# go run and air all pass it, and debug builds stay untrimmed. To keep",
	`# absolute source paths, remove it or add "-trimpath=false" after it.`,
}

// trimpathCommentAt is the comment block for a line starting at ls with the
// given indentation, as an edit inserted at ls.
func trimpathCommentAt(ls int, indent, nl string) kclTrimpathEdit {
	var b strings.Builder
	for _, l := range trimpathFlagsComment {
		b.WriteString(indent + l + nl)
	}
	return kclTrimpathEdit{at: ls, text: b.String(), comment: true}
}

var (
	// kclImportRe is one `import <path> [as <alias>]` line.
	kclImportRe = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+([A-Za-z0-9_.]+)(?:[ \t]+as[ \t]+([A-Za-z_][A-Za-z0-9_]*))?[ \t]*(?:#.*)?$`)
	// kclGoBuildSchemaRe is a schema derived from GoBuild: `schema X(fw.GoBuild):`.
	kclGoBuildSchemaRe = regexp.MustCompile(`(?m)^[ \t]*schema[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*\([ \t]*([A-Za-z_][A-Za-z0-9_]*)\.GoBuild[ \t]*\)`)
)

// kclForgeAliases are the names a file reaches forge's KCL module (or one of
// its submodules) by: `import forge` is forge, `import forge.workloads as fw`
// is fw.
func kclForgeAliases(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range kclImportRe.FindAllStringSubmatch(src, -1) {
		path, alias := m[1], m[2]
		if path != "forge" && !strings.HasPrefix(path, "forge.") {
			continue
		}
		if alias == "" {
			alias = path[strings.LastIndex(path, ".")+1:]
		}
		out[alias] = true
	}
	return out
}

// trimpathKCLFiles are the project's own .k files under deploy/kcl, slash
// paths relative to the project, in lexical order: no config_gen/, no
// *_gen.k, nothing with a generated header, nothing hidden or vendored.
func trimpathKCLFiles(dir string) ([]string, error) {
	root := filepath.Join(dir, "deploy", "kcl")
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == root {
				return filepath.SkipAll
			}
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (name == "config_gen" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".k") || strings.HasSuffix(name, "_gen.k") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// kclHasGeneratedHeader reports a file a generator stamped as its own.
func kclHasGeneratedHeader(src string) bool {
	for _, line := range strings.SplitN(src, "\n", 12) {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "Code generated") && strings.Contains(line, "DO NOT EDIT") {
			return true
		}
	}
	return false
}

func trimpathKCLStep(dir string, write bool) (codemod.Report, error) {
	var rep codemod.Report
	files, err := trimpathKCLFiles(dir)
	if err != nil {
		return rep, err
	}
	for _, rel := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(path) //nolint:gosec // the project's own KCL
		if err != nil {
			return rep, err
		}
		src := string(b)
		if !strings.Contains(src, "GoBuild") || kclHasGeneratedHeader(src) {
			continue
		}
		out, rewrites, next := planKCLTrimpath(rel, src)
		rep.Next = append(rep.Next, next...)
		if out == src {
			continue
		}
		if write {
			if err := writeKeepingMode(path, []byte(out)); err != nil {
				return rep, err
			}
		}
		rep.Rewrites = append(rep.Rewrites, rewrites...)
	}
	if rep.Changed() {
		rep.Verify = append(rep.Verify, Name()+" env render <env> (each env: the edited KCL still renders)")
	}
	return rep, nil
}

// kclTrimpathEdit is one insertion into the source.
type kclTrimpathEdit struct {
	at   int
	text string
	lit  int // index of the literal it edits, in source order
	line int
	what string
	// comment marks forge's comment block above a line, which only the
	// re-parse verifies.
	comment bool
}

// planKCLTrimpath is one file's edit: the new source (src when nothing
// changes), one report line per edited literal, and the manual steps.
func planKCLTrimpath(rel, src string) (string, []string, []string) {
	aliases := kclForgeAliases(src)
	if len(aliases) == 0 {
		return src, nil, nil
	}
	var next []string
	type pendingStep struct {
		line int
		why  string
	}
	var pending []pendingStep
	manual := func(line int, why string) { pending = append(pending, pendingStep{line, why}) }
	shifted := kclLineShift(src)
	// report formats the manual steps at their lines in the file as it is
	// left.
	report := func(applied []kclTrimpathEdit) []string {
		for _, p := range pending {
			next = append(next, fmt.Sprintf("%s:%d: %s, so it was left alone. Add \"-trimpath\" to that GoBuild's flags by hand (%s, or first in its list)",
				rel, shifted(p.line, applied), p.why, trimpathFlagsAttr))
		}
		return next
	}
	for _, m := range kclGoBuildSchemaRe.FindAllStringSubmatchIndex(src, -1) {
		if aliases[src[m[2]:m[3]]] {
			manual(1+strings.Count(src[:m[0]], "\n"), "a schema derived from GoBuild sets its builds' defaults where forge does not edit")
		}
	}
	a, err := parseKCLForEdit(src)
	if err != nil {
		next = append(report(nil), fmt.Sprintf("%s: forge cannot parse it (%v), so its GoBuild literals were left alone. Add %s to each by hand", rel, err, trimpathFlagsAttr))
		return src, nil, next
	}
	lits := goBuildLiterals(a, aliases)
	var edits []kclTrimpathEdit
	commented := map[int]bool{} // line starts that already get the comment
	for i, lit := range lits {
		start, end, ok := a.span(lit.w)
		line := kclLine(lit.w)
		if !ok {
			manual(line, "forge could not place this GoBuild literal in the file")
			continue
		}
		if strings.Contains(src[start:end], "trimpath") {
			continue // already declared, or opted out with -trimpath=false
		}
		if lit.opaque || lit.config == nil {
			manual(line, "this GoBuild literal has entries forge cannot read (a ** spread, an if-entry, or arguments)")
			continue
		}
		var flags []kclItem
		for _, it := range lit.items {
			if it.key == "flags" {
				flags = append(flags, it)
			}
		}
		switch {
		case len(flags) > 1:
			manual(line, "this GoBuild literal sets flags more than once")
		case len(flags) == 1:
			ed, why := prependTrimpathFlag(a, flags[0])
			if why != "" {
				manual(kclLine(flags[0].w), why)
				continue
			}
			ed.lit, ed.line, ed.what = i, line, "\"-trimpath\" first in its flags"
			edits = append(edits, ed)
		default:
			ed, comment, why := addTrimpathFlags(a, lit)
			if why != "" {
				manual(line, why)
				continue
			}
			ed.lit, ed.line, ed.what = i, line, trimpathFlagsAttr
			edits = append(edits, ed)
			if comment != nil && !commented[comment.at] {
				commented[comment.at] = true
				edits = append(edits, *comment)
			}
		}
	}
	if len(edits) == 0 {
		return src, nil, report(nil)
	}
	out := src
	sorted := append([]kclTrimpathEdit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].at > sorted[j].at })
	for _, ed := range sorted {
		out = out[:ed.at] + ed.text + out[ed.at:]
	}
	if why := verifyKCLTrimpath(out, aliases, len(lits), edits); why != "" {
		next = append(report(nil), fmt.Sprintf("%s: forge's edit did not verify (%s), so the file was left alone. Add %s to each GoBuild literal by hand", rel, why, trimpathFlagsAttr))
		return src, nil, next
	}
	var rewrites []string
	for _, ed := range edits {
		if ed.comment {
			continue
		}
		rewrites = append(rewrites, fmt.Sprintf("%s:%d: GoBuild gets %s", rel, shifted(ed.line, edits), ed.what))
	}
	return out, rewrites, report(edits)
}

// kclLineShift maps a line of src to where it is once edits are applied:
// past the lines the edits inserted above it.
func kclLineShift(src string) func(line int, applied []kclTrimpathEdit) int {
	starts := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return func(line int, applied []kclTrimpathEdit) int {
		if line < 1 || line > len(starts) {
			return line
		}
		out := line
		for _, ed := range applied {
			if ed.at <= starts[line-1] {
				out += strings.Count(ed.text, "\n")
			}
		}
		return out
	}
}

// goBuildLiterals are the file's GoBuild literals spelled through a forge
// alias, in source order.
func goBuildLiterals(a *kclAST, aliases map[string]bool) []*kclSchemaLit {
	var out []*kclSchemaLit
	for _, lit := range collectKCLFacts(a).schemas["GoBuild"] {
		if aliases[lit.alias] {
			out = append(out, lit)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, _, _ := a.span(out[i].w)
		sj, _, _ := a.span(out[j].w)
		return si < sj
	})
	return out
}

// prependTrimpathFlag puts "-trimpath" first in a flags list of plain
// strings; why says why it cannot.
func prependTrimpathFlag(a *kclAST, it kclItem) (kclTrimpathEdit, string) {
	if it.op != "Override" && it.op != "Union" && it.op != "Insert" {
		return kclTrimpathEdit{}, "this GoBuild's flags is not set with = , : or +="
	}
	if kclType(it.vn) != "List" {
		return kclTrimpathEdit{}, "this GoBuild's flags is a variable or an expression, not a list forge can add to"
	}
	elts, _ := it.vn["elts"].([]any)
	for _, e := range elts {
		if _, en, ok := kclNode(e); !ok || kclType(en) != "StringLit" {
			return kclTrimpathEdit{}, "this GoBuild's flags list has an element that is not a plain string"
		}
	}
	vs, _, ok := a.span(it.vw)
	if !ok || a.src[vs] != '[' {
		return kclTrimpathEdit{}, "forge could not place this GoBuild's flags list in the file"
	}
	if len(elts) == 0 {
		return kclTrimpathEdit{at: vs + 1, text: `"-trimpath"`}, ""
	}
	ew, _, _ := kclNode(elts[0])
	es, _, ok := a.span(ew)
	if !ok {
		return kclTrimpathEdit{}, "forge could not place this GoBuild's flags list in the file"
	}
	if kclLine(ew) > kclLine(it.vw) {
		// One element per line: a line of its own, at the elements' indent.
		ls := strings.LastIndexByte(a.src[:es], '\n') + 1
		return kclTrimpathEdit{at: es, text: `"-trimpath",` + lineBreakAt(a.src, es) + a.src[ls:es]}, ""
	}
	return kclTrimpathEdit{at: vs + 1, text: `"-trimpath", `}, ""
}

// addTrimpathFlags adds `flags = ["-trimpath"]` to a literal that has none,
// with forge's comment: above the literal's line when the flag goes on that
// line (comment is that edit), else above the new flags line.
func addTrimpathFlags(a *kclAST, lit *kclSchemaLit) (ed kclTrimpathEdit, comment *kclTrimpathEdit, why string) {
	cs, ce, ok := a.span(lit.config)
	if !ok || a.src[cs] != '{' || a.src[ce-1] != '}' {
		return ed, nil, "forge could not place this GoBuild literal's braces in the file"
	}
	if len(lit.items) == 0 {
		return ed, nil, "this GoBuild literal is empty"
	}
	last := lit.items[len(lit.items)-1]
	_, le, ok := a.span(last.w)
	if !ok {
		return ed, nil, "forge could not place this GoBuild literal's last attribute in the file"
	}
	closeLine := 1 + strings.Count(a.src[:ce-1], "\n")
	lastLine := 1 + strings.Count(a.src[:le], "\n")
	if lastLine == closeLine {
		// One line, or a closing brace on the last attribute's line.
		j := ce - 2
		for j > cs && (a.src[j] == ' ' || a.src[j] == '\t') {
			j--
		}
		ed = kclTrimpathEdit{at: j + 1, text: ", " + trimpathFlagsAttr}
		if a.src[j] == ',' {
			ed.text = " " + trimpathFlagsAttr
		}
		ls := strings.LastIndexByte(a.src[:j+1], '\n') + 1
		indent := a.src[ls:]
		indent = indent[:len(indent)-len(strings.TrimLeft(indent, " \t"))]
		c := trimpathCommentAt(ls, indent, lineBreakAt(a.src, ls))
		return ed, &c, ""
	}
	// Multi-line: lines of their own after the last attribute's line, at the
	// first attribute's indentation, with a comma when the attributes have one.
	first, _, ok := a.span(lit.items[0].w)
	if !ok {
		return ed, nil, "forge could not place this GoBuild literal's first attribute in the file"
	}
	ls := strings.LastIndexByte(a.src[:first], '\n') + 1
	indent := a.src[ls:first]
	if strings.TrimLeft(indent, " \t") != "" {
		return ed, nil, "this GoBuild literal's first attribute does not start its line"
	}
	comma := ""
	if rest := strings.TrimLeft(a.src[le:], " \t"); strings.HasPrefix(rest, ",") {
		comma = ","
	}
	eol := le + strings.IndexByte(a.src[le:], '\n')
	nl := "\n"
	if eol > 0 && a.src[eol-1] == '\r' {
		eol--
		nl = "\r\n"
	}
	var b strings.Builder
	for _, l := range trimpathFlagsComment {
		b.WriteString(nl + indent + l)
	}
	return kclTrimpathEdit{at: eol, text: b.String() + nl + indent + trimpathFlagsAttr + comma}, nil, ""
}

// lineBreakAt is the newline style of the line holding offset i.
func lineBreakAt(src string, i int) string {
	if j := strings.IndexByte(src[i:], '\n'); j > 0 && src[i+j-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// verifyKCLTrimpath proves an edited file: it parses, holds the same number
// of GoBuild literals, and each edited one's flags is a list whose first
// element is "-trimpath". It returns why not, "" when it does.
func verifyKCLTrimpath(out string, aliases map[string]bool, want int, edits []kclTrimpathEdit) string {
	a, err := parseKCLForEdit(out)
	if err != nil {
		return fmt.Sprintf("the result does not parse: %v", err)
	}
	lits := goBuildLiterals(a, aliases)
	if len(lits) != want {
		return fmt.Sprintf("the result has %d GoBuild literals, not %d", len(lits), want)
	}
	for _, ed := range edits {
		if ed.comment {
			continue
		}
		var flags *kclItem
		for i, it := range lits[ed.lit].items {
			if it.key == "flags" {
				flags = &lits[ed.lit].items[i]
			}
		}
		if flags == nil || kclType(flags.vn) != "List" {
			return fmt.Sprintf("line %d has no flags list afterwards", ed.line)
		}
		elts, _ := flags.vn["elts"].([]any)
		if len(elts) == 0 {
			return fmt.Sprintf("line %d's flags is empty afterwards", ed.line)
		}
		_, en, _ := kclNode(elts[0])
		if v, _ := en["value"].(string); v != "-trimpath" {
			return fmt.Sprintf("line %d's flags does not start with \"-trimpath\" afterwards", ed.line)
		}
	}
	return ""
}
