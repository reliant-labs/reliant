// Package tmplfields checks, statically, that the data each
// `<X>Templates().Render("<name>.tmpl", data)` call site in this module hands
// its template has every field and method the template references.
//
// WHY. text/template resolves `.Field` only when it executes the action that
// names it, and fails then ("can't evaluate field Process in type struct {…}").
// So a template that grows a field fails every caller whose data lacks it —
// but only when a test renders that template with that data, down the branch
// that reads the field. #2683 added `.Process` to config-roots.k.tmpl; the
// test helper serverRootStub rendered it with a hand-built anonymous struct
// that had no Process, and only full-mode (Slow) tests used the helper, so the
// break first ran on main. Executing every template against zero data cannot
// catch this: a field inside an `if` or `range` body is never evaluated on
// zero data, and many call sites pass anonymous structs, so there is no named
// type to construct.
//
// HOW. Scan parses the module's Go files (tests included), finds each Render
// call whose category accessor and template name it can read (a literal, a
// const, or a range variable over a literal list of names), and resolves the
// data argument's type SYNTACTICALLY: an anonymous struct literal, a named
// struct type (in this package or another package of the module), a call to
// a module function or method (its declared result), and an identifier,
// selector or index whose definition in the enclosing function is one of
// those. It then walks the template's parse tree with that type as
// dot, following `.A.B` chains, `$.X`, `range`/`with` bodies and
// `{{template "n" pipe}}` calls into their defines.
//
// Anything the resolver cannot pin down — a function's result, a type outside
// the module, an interface, a map, a generic type, an embedded field it cannot
// resolve, an untracked template variable — is OPEN and never checked. So a
// finding is a reference the data provably cannot satisfy, never a guess; the
// cost is that an open site is unchecked, which the Report's counts expose.
package tmplfields

import (
	"fmt"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// Accessor returns a template's source for a category accessor: arg is the
// accessor's own string argument (CITemplates("github")), "" for one that
// takes none. ok is false when the template does not exist.
type Accessor struct {
	// TakesArg is true for an accessor called with one string argument.
	TakesArg bool
	// Get returns the template text as Render would parse it.
	Get func(arg, name string) (src []byte, ok bool)
}

// Config tells Scan how this module's templates are reached.
type Config struct {
	// ModulePath is the module's import path (go.mod's module line).
	ModulePath string
	// TemplatesPkg is the import path of the package declaring the accessors.
	// Inside that package, an accessor may be called unqualified.
	TemplatesPkg string
	// Accessors maps an accessor's function name (ProjectTemplates) to it.
	Accessors map[string]Accessor
}

// Report is what Scan found.
type Report struct {
	// Checked is the number of call sites whose data type was resolved and
	// whose template was walked against it.
	Checked int
	// Open is the number of call sites whose data type could not be resolved
	// (see the package doc), so nothing was checked.
	Open int
	// OpenSites lists the open call sites, "<site> <call>", for whoever
	// widens the resolver.
	OpenSites []string
	// Skipped counts call sites Scan could not attribute to one template, by
	// reason (a non-literal name, a template that is not a .tmpl, ...).
	Skipped map[string]int
	// Findings is sorted by Site, then by the order the template walk
	// reached each reference.
	Findings []Finding
}

// SkippedTotal is the sum of Skipped.
func (r Report) SkippedTotal() int {
	n := 0
	for _, v := range r.Skipped {
		n += v
	}
	return n
}

// Finding is one template reference the call site's data cannot satisfy.
type Finding struct {
	// Site is the call site, "<module-relative file>:<line>".
	Site string
	// Call is the call as written, `ProjectTemplates().Render("x.tmpl", …)`.
	Call string
	// At is the template position of the reference, "<name>:<line>:<col>".
	At string
	// Field is the missing field or method.
	Field string
	// Type describes the type that lacks Field.
	Type string
	// RootType describes the data argument's type.
	RootType string
	// HandBuilt is true when the type lacking Field is an anonymous struct
	// written at a call site, rather than a named type.
	HandBuilt bool
}

func (f Finding) String() string {
	fix := fmt.Sprintf("add the field to %s, or stop the template referencing it", f.Type)
	if f.HandBuilt {
		fix = "render through the production data builder (e.g. codegen.RootsKCL for config-roots.k.tmpl) instead of a hand-built struct, or add the field to the struct literal"
	}
	lacks := fmt.Sprintf("the data, %s, has no field or method %s", f.RootType, f.Field)
	if f.Type != f.RootType {
		lacks = fmt.Sprintf("in the data (%s), %s has no field or method %s", f.RootType, f.Type, f.Field)
	}
	return fmt.Sprintf("%s: %s: the template references .%s at %s, and %s. Fix: %s.",
		f.Site, f.Call, f.Field, f.At, lacks, fix)
}

// Scan checks every Render call site in files, a module-relative slash path
// → source map of the module's Go files.
func Scan(files map[string][]byte, cfg Config) (Report, error) {
	s := newScanner(files, cfg)
	rep := Report{Skipped: map[string]int{}}
	sites, err := s.callSites(rep.Skipped)
	if err != nil {
		return rep, err
	}
	tmpls := map[string]*template{}
	for _, site := range sites {
		key := site.accessor + "\x00" + site.arg + "\x00" + site.name
		tm, ok := tmpls[key]
		if !ok {
			tm, err = s.loadTemplate(site)
			if err != nil {
				return rep, err
			}
			tmpls[key] = tm
		}
		if tm.skip != "" {
			rep.Skipped[tm.skip]++
			continue
		}
		root := site.data()
		if root.members == nil {
			rep.Open++
			rep.OpenSites = append(rep.OpenSites, site.pos+" "+site.call)
			continue
		}
		rep.Checked++
		for _, m := range tm.check(root) {
			rep.Findings = append(rep.Findings, Finding{
				Site:      site.pos,
				Call:      site.call,
				At:        m.at,
				Field:     m.field,
				Type:      m.on.desc,
				RootType:  root.desc,
				HandBuilt: m.on.handBuilt,
			})
		}
	}
	// Sites are visited in file order already; a stable sort by site keeps
	// each site's findings in template walk order.
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		return siteLess(rep.Findings[i].Site, rep.Findings[j].Site)
	})
	return rep, nil
}

// siteLess orders "file:line" by file, then numerically by line.
func siteLess(a, b string) bool {
	fa, la := splitSite(a)
	fb, lb := splitSite(b)
	if fa != fb {
		return fa < fb
	}
	return la < lb
}

func splitSite(s string) (string, int) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, 0
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return s, 0
	}
	return s[:i], n
}

// position renders p as "<file>:<line>".
func position(fset *token.FileSet, p token.Pos) string {
	pp := fset.Position(p)
	return fmt.Sprintf("%s:%d", pp.Filename, pp.Line)
}
