package vulnpatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// Options steer a Plan.
type Options struct {
	// AllowMajor lets a target leave the installed major version.
	AllowMajor bool
	// Ecosystems keeps only findings these patchers handle; empty is all.
	// A finding outside them is left out of the plan entirely, not skipped.
	Ecosystems []Ecosystem
	// Explicit says the user named the findings (advisory ids): a fix that
	// needs a major bump is then refused (exit 4) rather than skipped for
	// the agent.
	Explicit bool
}

func (o Options) wants(e Ecosystem) bool {
	if len(o.Ecosystems) == 0 {
		return true
	}
	for _, x := range o.Ecosystems {
		if x == e {
			return true
		}
	}
	return false
}

// Plan is what the patcher will do to a tree: each finding resolved to its
// manifests with a target, and the bumps that gets it there. NewPlan only
// reads; Apply changes the tree.
type Plan struct {
	// Dir is the tree, absolute.
	Dir string
	// Results are the findings, in the order given, and what happened.
	Results []Result
	// Refusals are why the patch must not run (exit 4); Apply refuses
	// while any is set.
	Refusals []string
	// Notes are what the reader should know that changed no status.
	Notes []string

	bumps   []*plannedBump
	applied bool
	verify  *VerifyResult
	failed  map[int]bool // results whose bump failed
}

// plannedBump is one package to raise in one manifest, for the results
// that need it.
type plannedBump struct {
	eco      Ecosystem
	manifest string // relative to the tree
	dir      string // the manifest's directory, absolute
	berry    bool
	pkg      string
	from, to string
	direct   bool
	results  []int
	done     *Bump
}

// index is every manifest in the tree.
type index struct {
	goMods    []*goModule
	lockfiles []*jsLockfile
	goWork    bool
}

// skipDirs are never searched for manifests: vendored and installed
// dependencies, fixtures, and tool state.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "testdata": true}

func scan(dir string) (*index, []string, error) {
	idx := &index{}
	var notes []string
	if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
		idx.goWork = true
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "go.mod":
			m, err := readGoMod(dir, path)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: unreadable, ignored: %v", relTo(dir, path), err))
				return nil
			}
			idx.goMods = append(idx.goMods, m)
		case "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock":
			l, err := readJSLockfile(dir, path)
			if err != nil {
				notes = append(notes, fmt.Sprintf("unreadable lockfile, ignored: %v", err))
				return nil
			}
			idx.lockfiles = append(idx.lockfiles, l)
		}
		return nil
	})
	return idx, notes, err
}

// NewPlan resolves findings against the tree at dir. It changes nothing.
func NewPlan(dir string, findings []vulnscan.Finding, opts Options) (*Plan, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	idx, notes, err := scan(abs)
	if err != nil {
		return nil, fmt.Errorf("read the manifests under %s: %w", abs, err)
	}
	p := &Plan{Dir: abs, Notes: notes, Results: []Result{}, failed: map[int]bool{}}
	for _, f := range findings {
		p.add(f, idx, opts)
	}
	return p, nil
}

// Missing records a requested advisory that names no finding: skipped, with
// reason.
func (p *Plan) Missing(id, reason string) {
	p.Results = append(p.Results, Result{ID: id, Status: StatusSkipped, Reason: reason})
}

// Refuse records a reason the patch must not run.
func (p *Plan) Refuse(reason string) { p.Refusals = append(p.Refusals, reason) }

// Changes reports whether the plan has any bump to apply.
func (p *Plan) Changes() bool { return len(p.bumps) > 0 }

func (p *Plan) add(f vulnscan.Finding, idx *index, opts Options) {
	r := Result{
		ID: f.ID, Aliases: f.Aliases, Severity: f.Severity, Package: f.Package,
		Installed: f.Package.Version, Status: StatusSkipped,
	}
	kind, eco := classify(f.Package)
	if kind == kindJS && !wantsAnyJS(opts) || kind != kindJS && eco != "" && !opts.wants(eco) {
		return
	}
	switch kind {
	case kindGoStdlib:
		r.Reason = "the Go standard library: raising the toolchain directive and the builder image has no patcher yet"
	case kindOS:
		r.Reason = fmt.Sprintf("an OS package (%s): refreshing the image's base has no patcher yet", f.Package.Type)
	case kindPython:
		r.Reason = "a Python package: no patcher yet"
	case kindOther:
		r.Reason = fmt.Sprintf("no patcher for %s packages", orUnknown(f.Package.Type))
	}
	if r.Reason != "" {
		if len(opts.Ecosystems) == 0 {
			p.Results = append(p.Results, r)
		}
		return
	}
	if !f.Fixable() {
		r.Reason = fmt.Sprintf("no fixed version (fix state: %s)", orUnknown(f.FixState))
		p.Results = append(p.Results, r)
		return
	}
	target, err := ChooseTarget(f.Package.Version, f.FixedIn, opts.AllowMajor)
	if err != nil {
		r.Reason = err.Error()
		if opts.Explicit && errors.Is(err, ErrNeedsMajor) {
			p.Refuse(fmt.Sprintf("%s (%s %s): %s", f.ID, f.Package.Name, f.Package.Version, err.Error()))
		}
		p.Results = append(p.Results, r)
		return
	}
	r.Target = target
	at := len(p.Results)
	switch kind {
	case kindGo:
		r.Ecosystem = EcosystemGo
		p.resolveGo(&r, f, idx, at)
	case kindJS:
		if !p.resolveJS(&r, f, idx, opts, at) {
			return
		}
	}
	p.Results = append(p.Results, r)
}

func wantsAnyJS(o Options) bool {
	return o.wants(EcosystemNPM) || o.wants(EcosystemPNPM) || o.wants(EcosystemYarn)
}

// resolveGo ties a Go module to the go.mod files that must require the fix.
func (p *Plan) resolveGo(r *Result, f vulnscan.Finding, idx *index, at int) {
	path := f.Package.Name
	if len(idx.goMods) == 0 {
		r.Reason = "no go.mod in the tree"
		return
	}
	for _, m := range idx.goMods {
		if m.path == path {
			r.Reason = fmt.Sprintf("%s is a module of this tree (%s), not a dependency", path, m.rel)
			return
		}
	}
	mods := hinted(idx.goMods, f.Locations, func(m *goModule) string { return m.rel })
	var requiring []*goModule
	for _, m := range mods {
		if _, ok := m.require[path]; ok {
			requiring = append(requiring, m)
		}
	}
	if len(requiring) == 0 {
		if len(mods) > 1 {
			r.Reason = fmt.Sprintf("no go.mod requires %s, and which of the tree's %d modules builds the scanned artifact is ambiguous", path, len(mods))
			return
		}
		// The one module: the dependency comes in transitively, and
		// `go get` raises it through MVS.
		requiring = mods
	}
	target := goVersion(r.Target)
	if strings.HasSuffix(f.Package.Version, "+incompatible") && !strings.HasSuffix(target, "+incompatible") && semverMajorNum(target) >= 2 {
		target += "+incompatible"
	}
	already := 0
	for _, m := range requiring {
		r.Manifests = append(r.Manifests, m.rel)
		from := m.require[path]
		if from != "" && compareVersions(from, target) >= 0 {
			already++
			continue
		}
		if from == "" {
			from = f.Package.Version
		}
		p.plan(&plannedBump{eco: EcosystemGo, manifest: m.rel, dir: m.dir, pkg: path, from: from, to: target, direct: m.require[path] != ""}, at)
	}
	r.Status = StatusBumped
	r.Reason = ""
	if already == len(requiring) {
		r.Status = StatusFixed
		r.Reason = fmt.Sprintf("the tree already requires %s %s or later", path, target)
	}
}

// resolveJS ties an npm package to the lockfiles that pin it at the scanned
// version, and only those: a lockfile that pins the package at another
// version is not evidence that it ships the vulnerable copy (it may be
// another artifact's tree), so it is never bumped on the finding's account.
// When the finding names where it was found (a scan of this tree), only the
// lockfiles it names are candidates.
//
// Every candidate must belong to one package manager: lockfiles of several
// (a package-lock.json beside a yarn.lock, or an npm tree beside a pnpm
// one) leave which tool builds the artifact ambiguous, and the finding is
// skipped. Several lockfiles of one package manager that each pin the
// scanned version are all bumped: each ships the vulnerable copy.
//
// It reports false when no lockfile of a wanted ecosystem is involved (the
// finding is left out of the plan).
func (p *Plan) resolveJS(r *Result, f vulnscan.Finding, idx *index, opts Options, at int) bool {
	name, installed := f.Package.Name, f.Package.Version
	var lockfiles []*jsLockfile
	for _, l := range idx.lockfiles {
		if opts.wants(l.eco) {
			lockfiles = append(lockfiles, l)
		}
	}
	if len(opts.Ecosystems) > 0 && len(lockfiles) == 0 {
		return false
	}
	lockfiles = hinted(lockfiles, f.Locations, func(l *jsLockfile) string { return l.rel })
	var exact, present []*jsLockfile
	for _, l := range lockfiles {
		if len(l.versions[name]) == 0 {
			continue
		}
		present = append(present, l)
		if l.has(name, installed) {
			exact = append(exact, l)
		}
	}
	switch {
	case len(present) == 0:
		if len(opts.Ecosystems) > 0 {
			return false
		}
		r.Reason = fmt.Sprintf("%s is in no lockfile in the tree (package-lock.json, pnpm-lock.yaml, yarn.lock)", name)
		return true
	case len(exact) == 0:
		var pins []string
		for _, l := range present {
			pins = append(pins, fmt.Sprintf("%s pins %s", l.rel, strings.Join(l.versions[name], ", ")))
		}
		sort.Strings(pins)
		r.Reason = fmt.Sprintf("no lockfile pins the scanned %s %s (%s): which copy the finding is about is not known", name, installed, strings.Join(pins, "; "))
		return true
	}
	byEco := map[Ecosystem][]string{}
	for _, l := range exact {
		byEco[l.eco] = append(byEco[l.eco], l.rel)
	}
	if len(byEco) > 1 {
		var parts []string
		for _, e := range Ecosystems() {
			if rs := byEco[e]; len(rs) > 0 {
				parts = append(parts, fmt.Sprintf("%s: %s", e, strings.Join(rs, ", ")))
			}
		}
		r.Reason = fmt.Sprintf("%s %s is pinned by the lockfiles of several package managers (%s): which one builds the scanned artifact is ambiguous", name, installed, strings.Join(parts, "; "))
		return true
	}
	target := npmVersion(r.Target)
	var bumps []*plannedBump
	for _, l := range exact {
		r.Manifests = append(r.Manifests, l.rel)
		if l.pkgJSONErr != nil {
			r.Manifests = nil
			r.Reason = fmt.Sprintf("%s/package.json cannot be read unambiguously (%v): nothing beside it is changed", relTo(p.Dir, l.dir), l.pkgJSONErr)
			return true
		}
		b := &plannedBump{eco: l.eco, manifest: l.rel, dir: l.dir, berry: l.berry, pkg: name, from: npmVersion(installed), to: target, direct: l.direct[name]}
		if !b.direct {
			// An override pins every copy in the lockfile, so it must not
			// move another major, nor lower a copy already past the fix.
			var majors []string
			for _, v := range l.versions[name] {
				if !sameMajor(v, target) {
					majors = appendUnique(majors, v)
				} else if compareVersions(v, b.to) > 0 {
					b.to = npmVersion(v)
				}
			}
			if len(majors) > 0 {
				r.Manifests = nil
				r.Reason = fmt.Sprintf("%s also pins %s at %s, another major: an override would move every copy", l.rel, name, strings.Join(majors, ", "))
				return true
			}
			// The override is forge's own edit of package.json: refuse
			// one it could not write back faithfully.
			if err := checkPackageJSON(filepath.Join(l.dir, "package.json"), overridePath[l.eco]); err != nil {
				r.Manifests = nil
				r.Reason = fmt.Sprintf("%s is transitive, and %s/package.json cannot take an override safely (%v)", name, relTo(p.Dir, l.dir), err)
				return true
			}
		}
		bumps = append(bumps, b)
	}
	// Plan only once every lockfile is known to be patchable: a finding is
	// bumped everywhere it is pinned, or nowhere.
	for _, b := range bumps {
		p.plan(b, at)
	}
	r.Ecosystem = exact[0].eco
	r.Status, r.Reason = StatusBumped, ""
	return true
}

// overridePath is where each package manager reads an override in
// package.json.
var overridePath = map[Ecosystem][]string{
	EcosystemNPM:  {"overrides"},
	EcosystemPNPM: {"pnpm", "overrides"},
	EcosystemYarn: {"resolutions"},
}

// plan adds b, merging it into a bump of the same package in the same
// manifest (the higher target wins).
func (p *Plan) plan(b *plannedBump, result int) {
	for _, have := range p.bumps {
		if have.manifest == b.manifest && have.pkg == b.pkg {
			if compareVersions(b.to, have.to) > 0 {
				have.to = b.to
			}
			have.results = appendInt(have.results, result)
			return
		}
	}
	b.results = []int{result}
	p.bumps = append(p.bumps, b)
}

// hinted narrows candidates to those a finding's locations name, when the
// scan was of this tree and says where it found the package; otherwise it
// returns them all.
func hinted[T any](all []T, locations []string, rel func(T) string) []T {
	var out []T
	for _, c := range all {
		for _, loc := range locations {
			if strings.TrimPrefix(filepath.ToSlash(loc), "/") == rel(c) {
				out = append(out, c)
				break
			}
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

type packageKind int

const (
	kindOther packageKind = iota
	kindGo
	kindGoStdlib
	kindJS
	kindOS
	kindPython
)

// classify says which patcher a package belongs to, from its SBOM type or
// purl.
func classify(p vulnscan.Package) (packageKind, Ecosystem) {
	t := strings.ToLower(p.Type)
	if t == "" && strings.HasPrefix(p.PURL, "pkg:") {
		t = strings.SplitN(strings.TrimPrefix(p.PURL, "pkg:"), "/", 2)[0]
	}
	switch t {
	case "go-module", "golang", "go":
		if p.Name == "stdlib" {
			return kindGoStdlib, EcosystemGo
		}
		return kindGo, EcosystemGo
	case "npm":
		return kindJS, EcosystemNPM
	case "apk", "deb", "rpm", "alpm", "portage", "nix":
		return kindOS, ""
	case "python", "pypi":
		return kindPython, ""
	}
	return kindOther, ""
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func appendInt(xs []int, x int) []int {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

func semverMajorNum(v string) int {
	c := canonical(v)
	if c == "" {
		return 0
	}
	n := 0
	for _, ch := range strings.TrimPrefix(strings.SplitN(c, ".", 2)[0], "v") {
		n = n*10 + int(ch-'0')
	}
	return n
}
