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
	// Reach is forge's reach for this change: "direct" when forge.yaml's
	// ci.gate.affected_reach left transitive importers out (Deferred, run in
	// the release gate), "all" when every one ran. ReachCap is its
	// transitive_cap.
	Reach    string   `json:"reach"`
	Deferred []string `json:"deferred,omitempty"`
	ReachCap int      `json:"reach_cap,omitempty"`
	// Build are the packages whose own build (not their tests') contains a
	// changed package (forge's build set): the merge gate's build phase
	// compiles exactly these. Empty when the lane is Full: then it is ./... .
	Build []string `json:"build,omitempty"`
	// Modules are the nested modules go.mod replaces (sdk/go) whose own
	// tests the change reaches: forge's affected_modules.
	Modules []ModulePlan `json:"modules,omitempty"`
}

// ModulePlan is a nested module's own Go tests: every package (Full) or the
// ./relative Packages, run with `go test` in Dir.
type ModulePlan struct {
	Dir      string   `json:"dir"`
	Full     bool     `json:"full,omitempty"`
	Packages []string `json:"packages,omitempty"`
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

// WebDemoPlan is the forge demo bundle's build lane (all or nothing):
// frontends/web's `vite build --mode demo` must succeed, which is where
// vite-plugin-demo-bundle.ts fails on a Reliant-only module in the bundle.
// Selected when a changed file is in the bundle's import graph
// (WebGraph.Demo), is one of demoBuildInputs, or the web lane is FULL.
type WebDemoPlan struct {
	LaneFull
}

// WebBuildPlan is the renderer's release build lane (all or nothing):
// frontends/web's production `vite build` must succeed and its bundle pass
// .github/scripts/verify-bundle.mjs, as the release gate's webbuild step
// (task build:web:verify) requires. Selected when one of webBuildInputs
// changed or the web lane is FULL (its dependencies, lockfile or build
// config changed): a new dependency can import what only the bundler
// refuses (#2829's `node:module`). A web source change alone does not
// select it; tsc -b and vitest judge those, the release gate the bundle.
type WebBuildPlan struct {
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
	WebDemo     WebDemoPlan  `json:"web_demo"`
	WebBuild    WebBuildPlan `json:"web_build"`
	// Lockfiles are the npm projects whose manifest or lockfile changed: the
	// runner checks each still installs from its lockfile (`npm ci`, or pnpm's
	// --frozen-lockfile) before any test runs.
	Lockfiles []LockfileCheck `json:"npm_lockfiles,omitempty"`
}

// LockfileCheck is one Node project whose package.json or lockfile
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
	".":                   {pnpmLock, []lane{laneWeb, laneElectron}},
	"frontends/web":       {pnpmLock, []lane{laneWeb}},
	"electron":            {pnpmLock, []lane{laneElectron}},
	"tools/protoc-gen-es": {pnpmLock, []lane{laneForgeGen}},
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
	fd        *ForgeDecision
	reachMode string
	pl        *Plan

	full map[lane][]string

	// hookGenDeps maps every module package a forge.yaml generate hook's
	// generator compiles to the first generator (forgeHookGenerators) that
	// does; hookGens maps it to EVERY such generator. hooks and hookWhy
	// collect the hooks the diff reaches and the reasons that were only that
	// (ForgeGenPlan.HooksOnly).
	hookGenDeps map[string]string
	hookGens    map[string][]string
	hooks       map[string]bool
	hookWhy     map[string]bool

	web         *reasons        // web test file (relative to frontends/web/) -> why
	webNear     *reasons        // ReachDirect: the tests within two edges of a changed module
	webDirect   *reasons        // ReachDirect: the tests within one edge
	webDeferred map[string]bool // web tests ReachAll would add (ReachDirect only)
	webTouch    bool            // a non-test web module changed
	elecReads   []string        // why the electron lane runs, from tests naming a changed file
}

// Classify is the whole decision: forge's (fd) for the Go, electron and
// docs-generate lanes, and this tool's rules and graphs for the web, demo,
// release-build, forge-generate and migration-order lanes. reachMode is the
// web lane's (ReachAll or ReachDirect); the Go lane's reach is forge.yaml's
// ci.gate.affected_reach, already in fd.
func Classify(changes []Change, fd *ForgeDecision, g *Graph, reachMode string) *Plan {
	if fd == nil {
		fd = &ForgeDecision{}
	}
	c := &classifier{
		reachMode: reachMode,
		g:         g,
		fd:        fd,
		pl:        &Plan{},
		full:      map[lane][]string{},
		web:       newReasons(), webNear: newReasons(), webDirect: newReasons(), webDeferred: map[string]bool{},
		hookGenDeps: generatorDeps(g, func(dir string) bool { return slices.Contains(forgeHookGenerators, dir) }),
		hookGens:    generatorSets(g, func(dir string) bool { return slices.Contains(forgeHookGenerators, dir) }),
		hooks:       map[string]bool{}, hookWhy: map[string]bool{},
	}
	for _, ch := range changes {
		c.attribute(ch)
		c.noteLockfile(ch)
	}
	if fd.failSafe() {
		// forge could not place a changed file: nothing proves what reads
		// it, so every lane runs whole (the fail-safe).
		why := "forge: " + fd.Reason + " (fail-safe)"
		for _, l := range allLanes {
			if l != laneMigOrder {
				c.full[l] = append(c.full[l], why)
			}
		}
	}
	c.selectGo()
	c.selectWeb()
	c.selectElectron()
	if chk, ok := fd.check("docs-generate"); ok && chk.Runs {
		c.full[laneDocGen] = append(c.full[laneDocGen], "forge: "+chk.Note)
	}
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
	c.selectWebDemo()
	c.selectWebBuild()
	return c.pl
}

// selectWebBuild decides the renderer's release build: a FULL web lane, for
// the reasons it is FULL, or a changed build input (attributeToWebBuild).
func (c *classifier) selectWebBuild() {
	if c.pl.Web.Full {
		c.full[laneWebBuild] = append(c.full[laneWebBuild], c.pl.Web.Reasons...)
	}
	c.pl.WebBuild.LaneFull = laneFull(c.full[laneWebBuild])
}

// attributeToWebBuild records why a changed file selects the release build:
// the build reads it (webBuildInputs).
func (c *classifier) attributeToWebBuild(ch Change) {
	for _, pat := range webBuildInputs {
		if matchGlob(pat, ch.Path) {
			c.full[laneWebBuild] = append(c.full[laneWebBuild], ch.Path+": the renderer's release build reads it")
			return
		}
	}
}

// selectWebDemo decides the forge demo bundle's build. A FULL web lane (its
// dependencies or build config changed, or a path nothing claims) builds it
// too, for the same reasons: a new dependency can carry a module the guard
// refuses. Otherwise the reasons attributeToDemo collected decide.
func (c *classifier) selectWebDemo() {
	if c.pl.Web.Full {
		c.full[laneWebDemo] = append(c.full[laneWebDemo], c.pl.Web.Reasons...)
	}
	c.pl.WebDemo.LaneFull = laneFull(c.full[laneWebDemo])
}

// attributeToDemo records why a changed file selects the demo build: it is in
// the bundle's import graph (directly, or as the TypeScript a .proto
// regenerates), or the build reads it. Without the bundle's entry the graph
// is unknown, so any web change selects it (fail-safe).
func (c *classifier) attributeToDemo(ch Change) {
	if !strings.HasPrefix(ch.Path, "frontends/web/") && !strings.HasSuffix(ch.Path, ".proto") {
		return
	}
	for _, pat := range demoBuildInputs {
		if matchGlob(pat, ch.Path) {
			c.full[laneWebDemo] = append(c.full[laneWebDemo], ch.Path+": the forge demo build reads it")
			return
		}
	}
	w := c.g.Web
	if w == nil {
		return
	}
	if w.Demo == nil {
		if strings.HasPrefix(ch.Path, "frontends/web/") {
			c.full[laneWebDemo] = append(c.full[laneWebDemo], ch.Path+": "+demoEntry+" is missing, so the demo bundle's graph is unknown (fail-safe)")
		}
		return
	}
	f := ch.Path
	if strings.HasSuffix(f, ".proto") {
		if !strings.HasPrefix(f, "proto/") && !strings.HasPrefix(f, "proto-vendor/controlplane/") {
			return
		}
		f = "frontends/web/" + webGenPath(f)
	}
	if w.Demo[f] {
		why := "in the forge demo bundle's import graph"
		if f != ch.Path {
			why = "regenerates " + path.Base(f) + ", " + why
		}
		c.full[laneWebDemo] = append(c.full[laneWebDemo], ch.Path+": "+why)
	}
}

func (c *classifier) attribute(ch Change) {
	rep := FileReport{Path: ch.Path, Deleted: ch.Deleted}
	for i := range rules {
		if rules[i].matches(ch.Path) {
			rep.Rules = append(rep.Rules, rules[i].id)
			c.applyRule(ch, &rules[i])
			if rules[i].note != "" && rep.Note == "" {
				rep.Note = rules[i].note
			}
		}
	}
	if strings.HasPrefix(ch.Path, "electron/") {
		// electron/test is 35 files and seconds long: forge's electron row
		// runs the whole lane on any change under electron/.
		rep.Rules = append(rep.Rules, "electron")
	}
	c.attributeToReaders(ch)
	c.attributeToDemo(ch)
	c.attributeToWebBuild(ch)
	if why, hook, ok := c.forgeGenInput(ch); ok {
		rep.Rules = append(rep.Rules, "forge-input")
		c.full[laneForgeGen] = append(c.full[laneForgeGen], ch.Path+": "+why)
		if hook {
			c.hookWhy[ch.Path+": "+why] = true
		}
	}
	if !ch.Deleted && isMigrationPath(ch.Path) {
		rep.Rules = append(rep.Rules, "migration-order")
		c.full[laneMigOrder] = append(c.full[laneMigOrder], ch.Path+": a migration must be versioned above origin/main's newest")
	}
	for _, f := range c.fd.Files {
		if f.Path == ch.Path {
			if f.Package != "" {
				rep.Pkg = rel(c.g.Module, f.Package)
			}
			if rep.Note == "" {
				rep.Note = "forge: " + f.Note
			}
		}
	}
	c.pl.Files = append(c.pl.Files, rep)
}

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

// forgeGenInput reports whether the forge-generate lane must run for ch: a
// path in forgeInputs, or code a forge.yaml generate hook's generator
// compiles (hook is true then, and every such hook is recorded in c.hooks).
func (c *classifier) forgeGenInput(ch Change) (why string, hook, ok bool) {
	if isForgeInput(ch.Path) {
		return "forge generate reads or writes it", false, true
	}
	pkg, ok := c.compiledPkg(ch)
	if !ok {
		return "", false, false
	}
	gen, ok := c.hookGenDeps[pkg]
	if !ok {
		return "", false, false
	}
	for _, g := range c.hookGens[pkg] {
		c.hooks[forgeHookOf[g]] = true
	}
	return fmt.Sprintf("the forge.yaml generate hook %s compiles %s", gen, rel(c.g.Module, pkg)), true, true
}

// compiledPkg is the package whose BUILT code ch is: a non-test Go or
// embedded file of a package a hook generator compiles, or, for a Go file
// the graph does not list (deleted or renamed), the package owning its
// directory.
func (c *classifier) compiledPkg(ch Change) (string, bool) {
	if fi, ok := c.g.Files[ch.Path]; ok {
		return fi.Pkg, true
	}
	if strings.HasSuffix(ch.Path, ".go") && !strings.HasSuffix(ch.Path, "_test.go") {
		pkg, ok := c.g.DirPkg[path.Dir(ch.Path)]
		return pkg, ok
	}
	return "", false
}

// applyRule records what one matched web rule selects.
func (c *classifier) applyRule(ch Change, r *rule) {
	if r.full {
		c.full[laneWeb] = append(c.full[laneWeb], fmt.Sprintf("%s: %s", ch.Path, r.reason))
	}
	if r.protoGen && c.g.Web != nil {
		gen := "frontends/web/" + webGenPath(ch.Path)
		c.addWebReach(gen, ch.Path+" regenerates "+path.Base(gen))
		c.webTouch = true
	}
	if r.webSrc {
		c.attributeToWeb(ch)
	}
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

// attributeToReaders selects the web and electron tests whose text names
// this file (forge's affected_readers does the same for Go packages).
func (c *classifier) attributeToReaders(ch Change) {
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

// selectGo is forge's Go decision in this plan's shape.
func (c *classifier) selectGo() {
	fd, gp := c.fd, &c.pl.Go
	dot := func(p string) string {
		if p == c.g.Module {
			return "."
		}
		return "./" + rel(c.g.Module, p)
	}
	gp.Why = map[string]string{}
	for _, p := range fd.Packages {
		gp.Packages = append(gp.Packages, dot(p))
	}
	for _, w := range fd.Why {
		gp.Why[dot(w.Package)] = w.File + " (" + w.Edge + ")"
	}
	for _, p := range fd.Deferred {
		gp.Deferred = append(gp.Deferred, dot(p))
	}
	for _, p := range fd.Build {
		gp.Build = append(gp.Build, dot(p))
	}
	gp.Reach, gp.ReachCap = ReachAll, fd.TransitiveCap
	if len(gp.Deferred) > 0 {
		gp.Reach = ReachDirect
	}
	for _, m := range fd.Modules {
		mp := ModulePlan{Dir: m.Dir, Full: m.Full}
		for _, p := range m.Packages {
			mp.Packages = append(mp.Packages, "./"+strings.TrimPrefix(strings.TrimPrefix(p, m.Path), "/"))
		}
		gp.Modules = append(gp.Modules, mp)
	}
	if fd.Full {
		c.full[laneGo] = append(c.full[laneGo], "forge: "+fd.Reason)
	}
	gp.LaneFull = laneFull(c.full[laneGo])
	if gp.Full {
		gp.Packages, gp.Build, gp.Deferred, gp.Why = nil, nil, nil, nil
	}
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
	if chk, ok := c.fd.check("electron"); ok && chk.Runs {
		c.full[laneElectron] = append(c.full[laneElectron], "forge: "+chk.Note)
	}
	c.pl.Electron.LaneFull = laneFull(append(c.full[laneElectron], c.elecReads...))
}

func laneFull(rs []string) LaneFull {
	rs = dedupe(rs)
	return LaneFull{Full: len(rs) > 0, Reasons: rs}
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
