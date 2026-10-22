// Copyright (c) 2025 Reliant Labs
package main

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// Change is one changed path of the diff under judgement.
type Change struct {
	Path    string
	Deleted bool
}

// FileReport is the per-file explanation the table prints.
type FileReport struct {
	Path    string   `json:"path"`
	Deleted bool     `json:"deleted,omitempty"`
	Rules   []string `json:"rules"`
	Pkg     string   `json:"package,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// LaneFull is a lane that cannot be narrowed, and why.
type LaneFull struct {
	Full    bool     `json:"full"`
	Reasons []string `json:"reasons,omitempty"`
}

// GoPlan is the Go lane: `go test -short` over Packages.
type GoPlan struct {
	LaneFull
	// Packages are the packages to test, as ./relative paths.
	Packages []string `json:"packages"`
	// DBOnly is the subset of Packages selected only because a migration
	// changed: their DB-backed tests are skipped under -short, so the runner
	// runs them (REQUIRE_TEST_DB=1) when DATABASE_URL is set and says so when not.
	DBOnly []string          `json:"db_only"`
	Why    map[string]string `json:"why,omitempty"`
}

// WebPlan is the vitest lane: `vitest run <Tests...>`.
type WebPlan struct {
	LaneFull
	// Tests are the test files to run, relative to frontends/web/.
	Tests []string          `json:"tests"`
	Why   map[string]string `json:"why,omitempty"`
}

// ElectronPlan is the `node --test` lane (all or nothing: it is small).
type ElectronPlan struct {
	LaneFull
}

// ForgeGenPlan is the forge-generate lane (all or nothing): the pinned
// `forge generate` must leave the tree as it found it.
type ForgeGenPlan struct {
	LaneFull
}

// DocGenPlan is the docs-generate lane (all or nothing): every docs
// generator (Taskfile.yml's generate:docs) must leave the tree as it found it.
type DocGenPlan struct {
	LaneFull
}

// Plan is the whole decision.
type Plan struct {
	Base     string       `json:"base"`
	Head     string       `json:"head"`
	Files    []FileReport `json:"files"`
	Go       GoPlan       `json:"go"`
	Web      WebPlan      `json:"web"`
	Electron ElectronPlan `json:"electron"`
	ForgeGen ForgeGenPlan `json:"forge_generate"`
	DocGen   DocGenPlan   `json:"docs_generate"`
}

type reasons struct {
	m     map[string][]string
	order []string
}

func newReasons() *reasons { return &reasons{m: map[string][]string{}} }

func (r *reasons) add(key, why string) {
	if _, ok := r.m[key]; !ok {
		r.order = append(r.order, key)
	}
	for _, w := range r.m[key] {
		if w == why {
			return
		}
	}
	r.m[key] = append(r.m[key], why)
}

func (r *reasons) has(key string) bool { _, ok := r.m[key]; return ok }

func (r *reasons) why(key string) string {
	ws := r.m[key]
	if len(ws) <= 3 {
		return strings.Join(ws, "; ")
	}
	return strings.Join(ws[:3], "; ") + fmt.Sprintf("; +%d more", len(ws)-3)
}

type classifier struct {
	g  *Graph
	pl *Plan

	full map[lane][]string

	code     *reasons // package whose built code changed: fans out to importers' tests
	embed    *reasons // package whose embedded file changed: fans out like code
	testOnly *reasons // package whose own tests must run
	anyGo    bool
	dbFan    bool
	dbSel    *reasons // package selected only because a migration changed

	// docGenDeps maps every module package a docs generator compiles to the
	// first generator (tools/docgen/<name>) that does.
	docGenDeps map[string]string
	// hookGenDeps is the same for forge.yaml's generate hooks
	// (forgeHookGenerators).
	hookGenDeps map[string]string

	web       *reasons // web test file (relative to frontends/web/) -> why
	webTouch  bool     // a non-test web module changed
	elecReads []string // why the electron lane runs, from tests naming a changed file
}

// Classify is the whole decision, as a pure function of the diff and the graph.
func Classify(changes []Change, g *Graph) *Plan {
	c := &classifier{
		g:    g,
		pl:   &Plan{},
		full: map[lane][]string{},
		code: newReasons(), embed: newReasons(), testOnly: newReasons(), dbSel: newReasons(),
		web:         newReasons(),
		docGenDeps:  generatorDeps(g, func(dir string) bool { return strings.HasPrefix(dir+"/", docGenDir) }),
		hookGenDeps: generatorDeps(g, func(dir string) bool { return slices.Contains(forgeHookGenerators, dir) }),
	}
	for _, ch := range changes {
		c.attribute(ch)
	}
	c.selectGo()
	c.selectWeb()
	c.selectElectron()
	c.pl.ForgeGen.LaneFull = laneFull(c.full[laneForgeGen])
	c.pl.DocGen.LaneFull = laneFull(c.full[laneDocGen])
	return c.pl
}

func (c *classifier) attribute(ch Change) {
	rep := FileReport{Path: ch.Path, Deleted: ch.Deleted}
	var matched []*rule
	for i := range rules {
		if rules[i].matches(ch.Path) {
			matched = append(matched, &rules[i])
			rep.Rules = append(rep.Rules, rules[i].id)
		}
	}
	isGo := strings.HasSuffix(ch.Path, ".go")
	if isGo {
		c.anyGo = true
	}

	skipGraph := false
	for _, r := range matched {
		skipGraph = skipGraph || r.noGraph
	}
	owned := false
	if !skipGraph {
		owned = c.attributeToPackage(ch, isGo, &rep)
	}
	for _, r := range matched {
		if c.applyRule(ch, r, &rep) {
			owned = true
		}
	}
	c.attributeToReaders(ch)
	if why, ok := c.forgeGenInput(ch); ok {
		rep.Rules = append(rep.Rules, "forge-input")
		c.full[laneForgeGen] = append(c.full[laneForgeGen], ch.Path+": "+why)
	}
	if why, ok := c.docGenInput(ch); ok {
		rep.Rules = append(rep.Rules, "docgen-input")
		c.full[laneDocGen] = append(c.full[laneDocGen], ch.Path+": "+why)
	}
	if len(matched) == 0 && !owned {
		why := fmt.Sprintf("%s: claimed by no rule and no Go package (fail-safe)", ch.Path)
		for _, l := range allLanes {
			c.full[l] = append(c.full[l], why)
		}
		rep.Note = "UNCLAIMED"
	}
	if rep.Note == "" {
		switch {
		case rep.Pkg != "":
			rep.Note = "go package " + rep.Pkg
		default:
			for _, r := range matched {
				if r.note != "" {
					rep.Note = r.note
					break
				}
			}
		}
	}
	c.pl.Files = append(c.pl.Files, rep)
}

// docGenDir is where the docs generators live: every package under it is one.
const docGenDir = "tools/docgen/"

// generatorDeps is the union of the build deps of every generator package
// (a package whose directory isGen accepts), each mapped to the first
// generator that compiles it.
func generatorDeps(g *Graph, isGen func(dir string) bool) map[string]string {
	deps := map[string]string{}
	for _, dir := range sortedKeys(g.DirPkg) {
		if !isGen(dir) {
			continue
		}
		gen := g.DirPkg[dir]
		if _, ok := deps[gen]; !ok {
			deps[gen] = dir
		}
		for _, p := range sortedKeys(g.BuildDeps[gen]) {
			if _, ok := deps[p]; !ok {
				deps[p] = dir
			}
		}
	}
	return deps
}

// docGenInput reports whether a docs generator reads or writes the file, and
// why: a path in docGenPaths, built code (or an embedded file) of a package a
// generator compiles, or a .proto whose generated package one compiles.
func (c *classifier) docGenInput(ch Change) (string, bool) {
	if isDocGenPath(ch.Path) {
		return "a docs generator reads or writes it", true
	}
	return c.compiledBy(ch, c.docGenDeps, "")
}

// forgeGenInput is docGenInput for the forge-generate lane: a path in
// forgeInputs, or code a forge.yaml generate hook's generator compiles.
func (c *classifier) forgeGenInput(ch Change) (string, bool) {
	if isForgeInput(ch.Path) {
		return "forge generate reads or writes it", true
	}
	return c.compiledBy(ch, c.hookGenDeps, "the forge.yaml generate hook ")
}

// compiledBy reports whether ch is built code (or an embedded file) of a
// package in deps, or a .proto whose generated package is, naming the
// generator that compiles it.
func (c *classifier) compiledBy(ch Change, deps map[string]string, label string) (string, bool) {
	compiled := func(pkg string) (string, bool) {
		gen, ok := deps[pkg]
		if !ok {
			return "", false
		}
		return fmt.Sprintf("%s%s compiles %s", label, gen, rel(c.g.Module, pkg)), true
	}
	if fi, ok := c.g.Files[ch.Path]; ok {
		if fi.Kind != KindCode {
			return "", false // a test or a build-tagged file is not in a generator's build
		}
		return compiled(fi.Pkg)
	}
	if pkg, ok := protoToGen(c.g, ch.Path); ok {
		return compiled(pkg)
	}
	// A Go file the graph no longer lists (deleted or renamed) was code of the
	// package owning its directory.
	if strings.HasSuffix(ch.Path, ".go") && !strings.HasSuffix(ch.Path, "_test.go") {
		if pkg, ok := ownerByDir(c.g, ch.Path); ok {
			return compiled(pkg)
		}
	}
	return "", false
}

// attributeToPackage maps a file to the Go package that owns it and records
// what kind of change that is to the package.
func (c *classifier) attributeToPackage(ch Change, isGo bool, rep *FileReport) bool {
	g := c.g
	if fi, ok := g.Files[ch.Path]; ok {
		rep.Pkg = rel(g.Module, fi.Pkg)
		cause := path.Base(ch.Path) + " in " + rel(g.Module, fi.Pkg)
		switch {
		case fi.Embedded:
			rep.Note = "embedded by " + rel(g.Module, fi.Pkg)
			c.embed.add(fi.Pkg, cause)
		case fi.Kind == KindCode:
			c.code.add(fi.Pkg, cause)
		case fi.Kind == KindTest:
			c.testOnly.add(fi.Pkg, cause)
		case fi.Kind == KindIgnored:
			rep.Note = "build-tagged: `go test -short` does not build it"
		}
		return true
	}
	pkg, ok := ownerByDir(g, ch.Path)
	if !ok {
		return false
	}
	rep.Pkg = rel(g.Module, pkg)
	cause := path.Base(ch.Path) + " in " + rel(g.Module, pkg)
	switch {
	case isGo && strings.HasSuffix(ch.Path, "_test.go"):
		c.testOnly.add(pkg, cause)
		rep.Note = "not in the build graph (deleted or renamed): treated as a test change"
	case isGo:
		c.code.add(pkg, cause)
		rep.Note = "not in the build graph (deleted or renamed): treated as a code change"
	case g.HasEmbed[pkg] && ch.Deleted:
		c.embed.add(pkg, cause)
		rep.Note = "deleted file under a package that embeds files: treated as an embed change"
	default:
		c.testOnly.add(pkg, cause)
		rep.Note = "data file in the package directory: its tests may read it"
	}
	return true
}

// applyRule records what one matched rule selects. It reports whether the rule
// attributed the file to something of its own.
func (c *classifier) applyRule(ch Change, r *rule, rep *FileReport) bool {
	claimed := false
	for _, l := range r.full {
		c.full[l] = append(c.full[l], fmt.Sprintf("%s: %s", ch.Path, r.reason))
	}
	if r.dbFan {
		c.dbFan = true
	}
	if r.protoGen {
		pkg, ok := protoToGen(c.g, ch.Path)
		if ok {
			c.code.add(pkg, ch.Path+" regenerates "+rel(c.g.Module, pkg))
			claimed = true
		} else {
			c.full[laneGo] = append(c.full[laneGo], fmt.Sprintf("%s: no generated Go package under gen/ for this proto (fail-safe)", ch.Path))
		}
		if w := c.g.Web; w != nil {
			gen := "frontends/web/" + webGenPath(ch.Path)
			for _, t := range w.Reach[gen] {
				c.web.add(strings.TrimPrefix(t, "frontends/web/"), ch.Path+" regenerates "+path.Base(gen))
			}
			c.webTouch = true
		}
	}
	if r.webSrc {
		c.attributeToWeb(ch)
		claimed = true
	}
	if r.electron {
		c.attributeToElectron(ch)
		claimed = true
	}
	return claimed
}

func (c *classifier) attributeToWeb(ch Change) {
	if ch.Deleted {
		if !IsWebTestFile(ch.Path) {
			c.full[laneWeb] = append(c.full[laneWeb], fmt.Sprintf("%s: a deleted web module (its importers can no longer be found)", ch.Path))
		}
		return
	}
	if IsWebTestFile(ch.Path) {
		c.web.add(strings.TrimPrefix(ch.Path, "frontends/web/"), "the test itself changed")
		return
	}
	c.webTouch = true
	w := c.g.Web
	if w == nil {
		return
	}
	for _, t := range w.Reach[ch.Path] {
		c.web.add(strings.TrimPrefix(t, "frontends/web/"), "imports "+path.Base(ch.Path))
	}
}

func (c *classifier) attributeToElectron(ch Change) {
	// electron/test is 35 files and seconds long: a change under electron/ runs
	// the whole lane rather than guessing which test requires which module.
	c.full[laneElectron] = append(c.full[laneElectron], fmt.Sprintf("%s: electron changed (the whole lane is small)", ch.Path))
}

// attributeToReaders selects the tests that name this file: Go packages whose
// sources spell its path, and web or electron tests whose text names it.
func (c *classifier) attributeToReaders(ch Change) {
	for _, pkg := range sortedKeys(c.g.Readers) {
		if readsPath(c.g.Readers[pkg], ch.Path) {
			c.testOnly.add(pkg, "its sources name "+ch.Path)
		}
	}
	if w := c.g.Web; w != nil {
		for _, t := range sortedKeys(w.Text) {
			if t != ch.Path && namesFile(w.Text[t], ch.Path) {
				c.web.add(strings.TrimPrefix(t, "frontends/web/"), "reads "+ch.Path)
			}
		}
	}
	for _, t := range sortedKeys(c.g.ElectronText) {
		if t != ch.Path && namesFile(c.g.ElectronText[t], ch.Path) {
			c.elecReads = append(c.elecReads, fmt.Sprintf("%s: electron/%s reads it", ch.Path, strings.TrimPrefix(t, "electron/")))
		}
	}
}

func (c *classifier) selectGo() {
	g := c.g
	if c.anyGo {
		for _, w := range sortedKeys(g.Walkers) {
			c.testOnly.add(w, "walks every Go file of the module")
		}
	}
	if c.dbFan {
		for _, p := range sortedKeys(g.DBTests) {
			c.dbSel.add(p, "a migration changed: it reaches DB-backed tests")
		}
	}
	gp := &c.pl.Go
	gp.Why = map[string]string{}
	sel := map[string]string{}
	dbOnly := map[string]bool{}
	for _, p := range sortedKeys(g.TestDeps) {
		if c.testOnly.has(p) {
			sel[p] = c.testOnly.why(p)
		} else if r := reach(g.TestDeps[p], c.code, c.embed); r != "" {
			sel[p] = r
		} else if c.dbSel.has(p) {
			sel[p] = c.dbSel.why(p)
			dbOnly[p] = true
		}
	}
	for _, p := range sortedKeys(sel) {
		rp := "./" + rel(g.Module, p)
		if p == g.Module {
			rp = "."
		}
		gp.Packages = append(gp.Packages, rp)
		gp.Why[rp] = sel[p]
		if dbOnly[p] {
			gp.DBOnly = append(gp.DBOnly, rp)
		}
	}
	gp.LaneFull = laneFull(c.full[laneGo])
}

func (c *classifier) selectWeb() {
	wp := &c.pl.Web
	wp.Why = map[string]string{}
	if w := c.g.Web; w != nil && c.webTouch {
		for _, t := range w.Walkers {
			c.web.add(strings.TrimPrefix(t, "frontends/web/"), "reads whole directories of frontends/web/")
		}
	}
	for _, f := range sortedKeys(c.web.m) {
		wp.Tests = append(wp.Tests, f)
		wp.Why[f] = c.web.why(f)
	}
	wp.LaneFull = laneFull(c.full[laneWeb])
}

func (c *classifier) selectElectron() {
	c.pl.Electron.LaneFull = laneFull(append(c.full[laneElectron], c.elecReads...))
}

func laneFull(rs []string) LaneFull {
	rs = dedupe(rs)
	return LaneFull{Full: len(rs) > 0, Reasons: rs}
}

// reach returns why a test binary with these deps contains a changed package,
// or "" when it does not.
func reach(deps map[string]bool, sets ...*reasons) string {
	for _, s := range sets {
		for _, p := range s.order {
			if deps[p] {
				return fmt.Sprintf("contains %s (%s)", lastTwo(p), s.why(p))
			}
		}
	}
	return ""
}

func lastTwo(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

func rel(module, pkg string) string {
	if pkg == module {
		return "."
	}
	return strings.TrimPrefix(pkg, module+"/")
}

// ownerByDir finds the package whose directory contains p, walking up past
// data directories (testdata, fixtures) but never to the repo root.
func ownerByDir(g *Graph, p string) (string, bool) {
	for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if pkg, ok := g.DirPkg[d]; ok {
			return pkg, true
		}
	}
	return "", false
}

// protoToGen maps proto/<dir>/x.proto (or proto-vendor/controlplane/<dir>/x.proto)
// to the Go package generated from it.
func protoToGen(g *Graph, p string) (string, bool) {
	if !strings.HasSuffix(p, ".proto") {
		return "", false
	}
	var dir string
	switch {
	case strings.HasPrefix(p, "proto-vendor/controlplane/"):
		dir = "controlplane/" + path.Dir(strings.TrimPrefix(p, "proto-vendor/controlplane/"))
	case strings.HasPrefix(p, "proto/"):
		dir = path.Dir(strings.TrimPrefix(p, "proto/"))
	default:
		return "", false
	}
	// buf's go_package puts the connect stubs in a subpackage; the message
	// package is the directory itself.
	if pkg, ok := g.DirPkg["gen/"+dir]; ok {
		return pkg, true
	}
	return "", false
}

// webGenPath maps a .proto to its generated TypeScript client, relative to frontends/web/.
func webGenPath(p string) string {
	name := strings.TrimSuffix(path.Base(p), ".proto") + "_pb.ts"
	switch {
	case strings.HasPrefix(p, "proto-vendor/controlplane/"):
		return path.Join("src/gen/controlplane", path.Dir(strings.TrimPrefix(p, "proto-vendor/controlplane/")), name)
	default:
		return path.Join("src/gen", path.Dir(strings.TrimPrefix(p, "proto/")), name)
	}
}

// readsPath reports whether any literal path a package names covers p.
func readsPath(lits []string, p string) bool {
	for _, c := range lits {
		if p == c || strings.HasPrefix(p, c+"/") {
			return true
		}
	}
	return false
}

// namesFile reports whether a test's source text names the file: by its full
// repo path, or by its base name as a whole quoted or slash-delimited segment
// (how path.join(root, "..", ".github", "scripts", "x.mjs") spells it).
func namesFile(text, p string) bool {
	if strings.Contains(text, p) {
		return true
	}
	b := path.Base(p)
	for _, pre := range []string{"/", `"`, "'", "`"} {
		for _, post := range []string{`"`, "'", "`"} {
			if strings.Contains(text, pre+b+post) {
				return true
			}
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
