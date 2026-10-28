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
	Packages []string          `json:"packages"`
	Why      map[string]string `json:"why,omitempty"`
	// Reach is how far a code change fans out: "all" (every package whose
	// test binary contains it, the default) or "direct" (the changed
	// packages and the packages that import one by name: the merge tier).
	// Deferred are the packages "all" would add, left to the full lane
	// (the release gate).
	Reach    string   `json:"reach"`
	Deferred []string `json:"deferred,omitempty"`
	// Build are the packages whose own build (not their tests') contains a
	// changed package, at every reach: the merge gate's build phase
	// compiles exactly these. Every other package is unchanged and already
	// compiled, so this is `go build ./...` without the cache lookups for
	// hundreds of untouched packages. Empty when the lane is Full: then it
	// is ./... .
	Build []string `json:"build,omitempty"`
}

// Reach modes (GoPlan.Reach).
const (
	ReachAll    = "all"
	ReachDirect = "direct"
)

// WebPlan is the vitest lane: `vitest run <Tests...>`.
type WebPlan struct {
	LaneFull
	// Tests are the test files to run, relative to frontends/web/.
	Tests []string          `json:"tests"`
	Why   map[string]string `json:"why,omitempty"`
	// Reach and Deferred mirror GoPlan's: under ReachDirect a changed module
	// selects only the tests within WebGraph.Near of it (the test imports it,
	// or imports a module that imports it), and the rest of its importers'
	// tests are Deferred to the full lane (the release gate).
	Reach    string   `json:"reach"`
	Deferred []string `json:"deferred,omitempty"`
	// Edges is how many import edges from a changed module ReachDirect
	// followed: 2, or 1 when two would have selected more than
	// webNearBudget files (0 under ReachAll: the whole closure).
	Edges int `json:"edges,omitempty"`
}

// ElectronPlan is the `node --test` lane (all or nothing: it is small).
type ElectronPlan struct {
	LaneFull
}

// ForgeGenPlan is the forge-generate lane (all or nothing): the pinned
// `forge generate` must leave the tree as it found it.
//
// HooksOnly says every reason is Go code that a forge.yaml generate hook's
// generator compiles, and nothing forge itself reads: then only Hooks (their
// forge.yaml names) can write anything different, and running just those
// hooks' commands is the same check as the whole `forge generate`, in seconds
// instead of minutes. HookCommands is each hook's forge.yaml command (filled
// in by plan, which reads forge.yaml; Classify stays a pure function).
type ForgeGenPlan struct {
	LaneFull
	HooksOnly    bool                `json:"hooks_only,omitempty"`
	Hooks        []string            `json:"hooks,omitempty"`
	HookCommands map[string][]string `json:"hook_commands,omitempty"`
}

// DocGenPlan is the docs-generate lane (all or nothing): every docs
// generator (Taskfile.yml's generate:docs) must leave the tree as it found it.
type DocGenPlan struct {
	LaneFull
}

// MigOrderPlan is the migration-order lane (all or nothing): a migration added
// under db/migrations must be versioned above origin/main's newest.
type MigOrderPlan struct {
	LaneFull
}

// Plan is the whole decision.
type Plan struct {
	Schema      int          `json:"schema"`
	InputDigest string       `json:"input_digest"`
	Base        string       `json:"base"`
	Head        string       `json:"head"`
	Files       []FileReport `json:"files"`
	Go          GoPlan       `json:"go"`
	Web         WebPlan      `json:"web"`
	Electron    ElectronPlan `json:"electron"`
	ForgeGen    ForgeGenPlan `json:"forge_generate"`
	DocGen      DocGenPlan   `json:"docs_generate"`
	MigOrder    MigOrderPlan `json:"migration_order"`
	// Lockfiles are the npm projects whose manifest or lockfile changed: the
	// runner checks each still installs from its lockfile (`npm ci`, or pnpm's
	// --frozen-lockfile) before any test runs.
	Lockfiles []LockfileCheck `json:"npm_lockfiles,omitempty"`
}

// LockfileCheck is one npm project whose package.json or package-lock.json
// changed. The lanes' tests run against whatever node_modules is already on
// disk, so without this nothing in the affected tier notices a lockfile that
// `npm ci` rejects: reliant #1343 bumped forge-web-runtime in package.json,
// hand-edited the lockfile, and every fresh install of main failed with
// "Missing: zone.js from lock file" while every local test stayed green.
type LockfileCheck struct {
	Dir      string `json:"dir"`
	Lockfile string `json:"lockfile"`
	Lanes    []lane `json:"lanes"`
	Why      string `json:"why"`
}

// npmProject is a directory with a committed lockfile: which one (the package
// manager follows from it), and the lanes whose tooling installs from it.
type npmProject struct {
	lockfile string
	lanes    []lane
}

const (
	npmLock  = "package-lock.json"
	pnpmLock = "pnpm-lock.yaml"
)

// npmProjects are the repo's npm projects. TestNPMProjectsNameEveryLockfile
// holds this to the tree.
var npmProjects = map[string]npmProject{
	".":                   {npmLock, []lane{laneWeb, laneElectron}},
	"frontends/web":       {pnpmLock, []lane{laneWeb}},
	"electron":            {npmLock, []lane{laneElectron}},
	"tools/protoc-gen-es": {npmLock, []lane{laneForgeGen}},
}

func (c *classifier) noteLockfile(ch Change) {
	base := path.Base(ch.Path)
	dir := path.Dir(ch.Path)
	proj, ok := npmProjects[dir]
	if !ok || (base != "package.json" && base != proj.lockfile && base != "pnpm-workspace.yaml") {
		return
	}
	if base == "pnpm-workspace.yaml" && proj.lockfile != pnpmLock {
		return
	}
	lanes := proj.lanes
	for i := range c.pl.Lockfiles {
		if c.pl.Lockfiles[i].Dir == dir {
			c.pl.Lockfiles[i].Why += ", " + base
			return
		}
	}
	c.pl.Lockfiles = append(c.pl.Lockfiles, LockfileCheck{Dir: dir, Lockfile: proj.lockfile, Lanes: lanes, Why: "changed: " + base})
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
	g         *Graph
	reachMode string
	pl        *Plan

	full map[lane][]string
	// buildAll is set by a deleted Go package (buildsModule).
	buildAll bool

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
	// hookGens maps a package to EVERY hook generator that compiles it;
	// hooks and hookWhy collect the hooks the diff reaches and the reasons
	// that were only that (ForgeGenPlan.HooksOnly).
	hookGens map[string][]string
	hooks    map[string]bool
	hookWhy  map[string]bool

	web         *reasons        // web test file (relative to frontends/web/) -> why
	webNear     *reasons        // ReachDirect: the tests within two edges of a changed module
	webDirect   *reasons        // ReachDirect: the tests within one edge
	webDeferred map[string]bool // web tests ReachAll would add (ReachDirect only)
	webTouch    bool            // a non-test web module changed
	elecReads   []string        // why the electron lane runs, from tests naming a changed file
}

// Classify is the whole decision, as a pure function of the diff and the graph,
// fanning code changes out to every reverse dependency (ReachAll).
func Classify(changes []Change, g *Graph) *Plan { return ClassifyReach(changes, g, ReachAll) }

// ClassifyReach is Classify with the Go lane's reach chosen (ReachAll or
// ReachDirect).
func ClassifyReach(changes []Change, g *Graph, reachMode string) *Plan {
	c := &classifier{
		reachMode: reachMode,
		g:         g,
		pl:        &Plan{},
		full:      map[lane][]string{},
		code:      newReasons(), embed: newReasons(), testOnly: newReasons(), dbSel: newReasons(),
		web: newReasons(), webNear: newReasons(), webDirect: newReasons(), webDeferred: map[string]bool{},
		docGenDeps:  generatorDeps(g, func(dir string) bool { return strings.HasPrefix(dir+"/", docGenDir) }),
		hookGenDeps: generatorDeps(g, func(dir string) bool { return slices.Contains(forgeHookGenerators, dir) }),
		hookGens:    generatorSets(g, func(dir string) bool { return slices.Contains(forgeHookGenerators, dir) }),
		hooks:       map[string]bool{}, hookWhy: map[string]bool{},
	}
	for _, ch := range changes {
		c.attribute(ch)
		c.noteLockfile(ch)
	}
	c.selectGo()
	c.selectWeb()
	c.selectElectron()
	c.pl.ForgeGen.LaneFull = laneFull(c.full[laneForgeGen])
	if c.pl.ForgeGen.Full {
		only := true
		for _, r := range c.pl.ForgeGen.Reasons {
			only = only && c.hookWhy[r]
		}
		if only && len(c.hooks) > 0 {
			c.pl.ForgeGen.HooksOnly, c.pl.ForgeGen.Hooks = true, sortedKeys(c.hooks)
		}
	}
	c.pl.DocGen.LaneFull = laneFull(c.full[laneDocGen])
	c.pl.MigOrder.LaneFull = laneFull(c.full[laneMigOrder])
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
	if why, hook, ok := c.forgeGenInput(ch); ok {
		rep.Rules = append(rep.Rules, "forge-input")
		c.full[laneForgeGen] = append(c.full[laneForgeGen], ch.Path+": "+why)
		if hook {
			c.hookWhy[ch.Path+": "+why] = true
		}
	}
	if why, ok := c.docGenInput(ch); ok {
		rep.Rules = append(rep.Rules, "docgen-input")
		c.full[laneDocGen] = append(c.full[laneDocGen], ch.Path+": "+why)
	}
	if !ch.Deleted && isMigrationPath(ch.Path) {
		rep.Rules = append(rep.Rules, "migration-order")
		c.full[laneMigOrder] = append(c.full[laneMigOrder], ch.Path+": a migration must be versioned above origin/main's newest")
	}
	if len(matched) == 0 && !owned && c.deletedPackage(ch) {
		// A whole Go package is gone. Each importer had to change with it, and
		// those changes select their own tests; one that still imports it no
		// longer compiles, so the module is compiled rather than every lane
		// run.
		c.buildAll = true
		owned = true
		rep.Note = "deleted Go package: its importers changed with it; the whole module is compiled"
	}
	if len(matched) == 0 && !owned {
		why := fmt.Sprintf("%s: claimed by no rule and no Go package (fail-safe)", ch.Path)
		for _, l := range allLanes {
			if l == laneMigOrder {
				continue // judged by path alone: an unclaimed file is no migration
			}
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

// generatorSets is generatorDeps keeping EVERY generator that compiles a
// package, not only the first.
func generatorSets(g *Graph, isGen func(dir string) bool) map[string][]string {
	sets := map[string][]string{}
	for _, dir := range sortedKeys(g.DirPkg) {
		if !isGen(dir) {
			continue
		}
		gen := g.DirPkg[dir]
		sets[gen] = append(sets[gen], dir)
		for _, p := range sortedKeys(g.BuildDeps[gen]) {
			if !slices.Contains(sets[p], dir) {
				sets[p] = append(sets[p], dir)
			}
		}
	}
	return sets
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
// forgeInputs, or code a forge.yaml generate hook's generator compiles (hook
// is true then, and every such hook is recorded in c.hooks).
func (c *classifier) forgeGenInput(ch Change) (why string, hook, ok bool) {
	if isForgeInput(ch.Path) {
		return "forge generate reads or writes it", false, true
	}
	why, ok = c.compiledBy(ch, c.hookGenDeps, "the forge.yaml generate hook ")
	if !ok {
		return "", false, false
	}
	pkg, _ := c.compiledPkg(ch)
	for _, gen := range c.hookGens[pkg] {
		c.hooks[forgeHookOf[gen]] = true
	}
	return why, true, true
}

// compiledBy reports whether ch is built code (or an embedded file) of a
// package in deps, or a .proto whose generated package is, naming the
// generator that compiles it.
func (c *classifier) compiledBy(ch Change, deps map[string]string, label string) (string, bool) {
	pkg, ok := c.compiledPkg(ch)
	if !ok {
		return "", false
	}
	gen, ok := deps[pkg]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s%s compiles %s", label, gen, rel(c.g.Module, pkg)), true
}

// compiledPkg is the package whose BUILT code ch is: a code (or embedded)
// file's package, a .proto's generated package, or the owner of a deleted Go
// file's directory. A test or build-tagged file is in no generator's build.
func (c *classifier) compiledPkg(ch Change) (string, bool) {
	if fi, ok := c.g.Files[ch.Path]; ok {
		if fi.Kind != KindCode {
			return "", false
		}
		return fi.Pkg, true
	}
	if pkg, ok := protoToGen(c.g, ch.Path); ok {
		return pkg, true
	}
	// A Go file the graph no longer lists (deleted or renamed) was code of the
	// package owning its directory.
	if strings.HasSuffix(ch.Path, ".go") && !strings.HasSuffix(ch.Path, "_test.go") {
		return ownerByDir(c.g, ch.Path)
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
		if c.g.Web != nil {
			gen := "frontends/web/" + webGenPath(ch.Path)
			c.addWebReach(gen, ch.Path+" regenerates "+path.Base(gen))
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
	if IsWebTestFile(ch.Path) {
		// A deleted test selects nothing: there is nothing left to run.
		if !ch.Deleted {
			c.web.add(strings.TrimPrefix(ch.Path, "frontends/web/"), "the test itself changed")
		}
		return
	}
	c.webTouch = true
	if c.g.Web == nil {
		return
	}
	// A deleted module needs no full run. Every module that imported it had
	// to change too (or it no longer resolves), and each of those changes
	// selects its own tests. What is left is a module that STILL imports the
	// deleted one: the graph resolves specifiers against the base tree's
	// files as well as HEAD's, so it finds those, and selects the tests that
	// reach them (tsc -b fails on the dangling import before they run).
	// Before this, a deleted module forced every vitest file: 71 of 97 full
	// web runs in the merge queue (2026-10-26), ~27 minutes each.
	why := "imports " + path.Base(ch.Path)
	if ch.Deleted {
		why = "still imports " + path.Base(ch.Path) + ", which this change deletes"
	}
	c.addWebReach(ch.Path, why)
}

// addWebReach selects the vitest files that reach the web file f: every one
// under ReachAll; under ReachDirect those within WebGraph.Near, or only
// WebGraph.Direct when f is a hub (webHubFanout). The others are recorded as
// deferred to the full lane.
func (c *classifier) addWebReach(f, why string) {
	w := c.g.Web
	trim := func(t string) string { return strings.TrimPrefix(t, "frontends/web/") }
	if c.reachMode != ReachDirect {
		for _, t := range w.Reach[f] {
			c.web.add(trim(t), why)
		}
		return
	}
	near := w.Near[f]
	if len(near) > webHubFanout {
		near = w.Direct[f]
	}
	for _, t := range near {
		c.webNear.add(trim(t), why)
	}
	for _, t := range w.Direct[f] {
		c.webDirect.add(trim(t), why)
	}
	for _, t := range w.Reach[f] {
		c.webDeferred[trim(t)] = true
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
	sel, deferred := map[string]string{}, []string{}
	for _, p := range sortedKeys(g.TestDeps) {
		if c.testOnly.has(p) {
			sel[p] = c.testOnly.why(p)
		} else if r := reach(g.TestDeps[p], c.code, c.embed); r != "" {
			if c.reachMode != ReachDirect {
				sel[p] = r
			} else if d := reachDirect(p, g.DirectImports[p], c.code, c.embed); d != "" {
				sel[p] = d
			} else if c.dbSel.has(p) {
				sel[p] = c.dbSel.why(p)
			} else {
				deferred = append(deferred, p)
			}
		} else if c.dbSel.has(p) {
			sel[p] = c.dbSel.why(p)
		}
	}
	dot := func(p string) string {
		if p == g.Module {
			return "."
		}
		return "./" + rel(g.Module, p)
	}
	for _, p := range sortedKeys(sel) {
		rp := dot(p)
		gp.Packages = append(gp.Packages, rp)
		gp.Why[rp] = sel[p]
	}
	gp.Reach = ReachAll
	if c.reachMode == ReachDirect {
		gp.Reach = ReachDirect
		for _, p := range deferred {
			gp.Deferred = append(gp.Deferred, dot(p))
		}
	}
	gp.LaneFull = laneFull(c.full[laneGo])
	if !gp.Full {
		for _, p := range sortedKeys(g.BuildDeps) {
			if reach(g.BuildDeps[p], c.code, c.embed) != "" {
				gp.Build = append(gp.Build, dot(p))
			}
		}
		if c.buildAll {
			gp.Build = []string{"./..."}
		}
	}
}

// deletedPackage reports whether ch deletes a Go file whose directory no
// longer holds a package at all.
func (c *classifier) deletedPackage(ch Change) bool {
	if !ch.Deleted || !strings.HasSuffix(ch.Path, ".go") {
		return false
	}
	_, ok := c.g.DirPkg[path.Dir(ch.Path)]
	return !ok
}

func (c *classifier) selectWeb() {
	wp := &c.pl.Web
	wp.Why = map[string]string{}
	if c.reachMode == ReachDirect {
		// Two edges from every changed module, unless together they reach
		// more than webNearBudget files: then one edge from each.
		pick, edges := c.webNear, 2
		if len(c.webNear.m) > webNearBudget {
			pick, edges = c.webDirect, 1
		}
		wp.Edges = edges
		for _, f := range pick.order {
			for _, why := range pick.m[f] {
				c.web.add(f, why)
			}
		}
	}
	if w := c.g.Web; w != nil && c.webTouch {
		for _, t := range w.Walkers {
			c.web.add(strings.TrimPrefix(t, "frontends/web/"), "reads whole directories of frontends/web/")
		}
	}
	for _, f := range sortedKeys(c.web.m) {
		wp.Tests = append(wp.Tests, f)
		wp.Why[f] = c.web.why(f)
	}
	wp.Reach = ReachAll
	if c.reachMode == ReachDirect {
		wp.Reach = ReachDirect
		for _, f := range sortedKeys(c.webDeferred) {
			if !c.web.has(f) {
				wp.Deferred = append(wp.Deferred, f)
			}
		}
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

// reachDirect is reach for ReachDirect: p is itself a changed package, or one
// of its files or tests imports one by name.
func reachDirect(p string, imports map[string]bool, sets ...*reasons) string {
	for _, s := range sets {
		if s.has(p) {
			return fmt.Sprintf("changed (%s)", s.why(p))
		}
	}
	for _, s := range sets {
		for _, q := range s.order {
			if imports[q] {
				return fmt.Sprintf("imports %s (%s)", lastTwo(q), s.why(q))
			}
		}
	}
	return ""
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
