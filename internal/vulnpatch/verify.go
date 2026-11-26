package vulnpatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// The verify command's sources, for VerifyResult.Source.
const (
	VerifySourceFlag     = "flag"
	VerifySourceConfig   = "forge.yaml"
	VerifySourceTaskfile = "taskfile"
	VerifySourceGo       = "go"
	VerifySourceNone     = "none"
)

// DefaultVerify is the project's own gate for a patched tree at dir: `task
// test:short` when its Taskfile has that task; else, for a Go module, `go
// build ./... && go test -short ./...`; else the package manager's `test`
// when package.json has a test script; else, for a uv or poetry project
// that uses pytest, `uv run pytest -q` or `poetry run pytest -q`.
// ("", VerifySourceNone) when the tree has none of them (a project pinned
// only by requirements files among them); the rescan still runs.
func DefaultVerify(dir string) (command, source string) {
	for _, name := range []string{"Taskfile.yml", "Taskfile.yaml", "taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var tf struct {
			Tasks map[string]yaml.Node `yaml:"tasks"`
		}
		if yaml.Unmarshal(data, &tf) == nil {
			if _, ok := tf.Tasks["test:short"]; ok {
				return "task test:short", VerifySourceTaskfile
			}
		}
		break
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "go build ./... && go test -short ./...", VerifySourceGo
	}
	if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
		return "go build ./... && go test -short ./...", VerifySourceGo
	}
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && hasTestScript(data) {
		pm := string(EcosystemNPM)
		switch {
		case exists(filepath.Join(dir, "pnpm-lock.yaml")):
			pm = string(EcosystemPNPM)
		case exists(filepath.Join(dir, "yarn.lock")):
			pm = string(EcosystemYarn)
		}
		return pm + " test", pm
	}
	if c, src := pythonVerify(dir); c != "" {
		return c, src
	}
	return "", VerifySourceNone
}

func hasTestScript(pkgJSON []byte) bool {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if yaml.Unmarshal(pkgJSON, &pkg) != nil { // JSON is YAML
		return false
	}
	return strings.TrimSpace(pkg.Scripts["test"]) != ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RescanFunc scans a directory of the patched tree for vulnerabilities.
type RescanFunc func(ctx context.Context, dir string) ([]vulnscan.Finding, error)

// VerifyOptions steer Verify.
type VerifyOptions struct {
	// Command is run with `sh -c` in the tree; "" runs none.
	Command string
	// Source says where Command came from (VerifySource*).
	Source string
	// Run runs it; nil runs the real shell.
	Run vulnscan.Runner
	// Rescan rescans each changed manifest's directory; nil rescans
	// nothing, and every bumped finding stays "bumped".
	Rescan RescanFunc
	// ScanImage rescans the image of each Dockerfile Apply changed, after
	// Verify rebuilds it with docker; nil rebuilds nothing, and a finding
	// fixed in a Dockerfile stays "bumped".
	ScanImage ScanImageFunc
}

// Verify runs the gate, then rescans the directory of every manifest Apply
// changed, and settles each bumped finding as fixed or still_present.
func (p *Plan) Verify(ctx context.Context, o VerifyOptions) *VerifyResult {
	v := &VerifyResult{Command: o.Command, Source: o.Source, Passed: true, Rescanned: []string{}}
	if v.Source == "" {
		v.Source = VerifySourceNone
	}
	run := o.Run
	if run == nil {
		run = vulnscan.Exec
	}
	if o.Command != "" {
		var out bytes.Buffer
		err := run(ctx, vulnscan.Command{Path: "sh", Args: []string{"-c", o.Command}, Dir: p.Dir, Stdout: &out, Stderr: &out})
		v.Passed = err == nil
		v.Output = Tail(out.String(), 4000)
		if err != nil && v.Output == "" {
			v.Output = err.Error()
		}
	}
	p.verify = v
	byImage := p.verifyImages(ctx, run, o.ScanImage, v)
	if o.Rescan == nil {
		return v
	}
	dirs := map[string]bool{}
	byDir := map[int]bool{} // results a directory rescan settles
	for _, b := range p.bumps {
		if b.done != nil && b.eco != EcosystemDocker {
			dirs[b.dir] = true
			for _, r := range b.results {
				byDir[r] = !byImage[r]
			}
		}
	}
	var sorted []string
	for d := range dirs {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	// Each directory's rescan stays its own: a result is judged only by the
	// rescans of the directories its own bumps changed, so the same advisory
	// still reported in another tree never marks this one still present.
	scans := map[string][]vulnscan.Finding{}
	failed := map[string]bool{}
	var errs []string
	for _, d := range sorted {
		rel := relTo(p.Dir, d)
		fs, err := o.Rescan(ctx, d)
		if err != nil {
			errs = append(errs, rel+": "+err.Error())
			failed[d] = true
			continue
		}
		v.Rescanned = append(v.Rescanned, rel)
		scans[d] = fs
	}
	if len(errs) > 0 {
		// The image rescans (verifyImages) may have failed too: keep both.
		v.RescanError = strings.Join(append(nonEmpty(v.RescanError), errs...), "; ")
	}
	for i := range p.Results {
		r := &p.Results[i]
		if r.Status != StatusBumped || !byDir[i] {
			continue
		}
		rdirs, minTo := p.resultDirs(i)
		if len(rdirs) == 0 {
			r.Reason = "not confirmed: nothing was rescanned"
			continue
		}
		var unscanned []string
		for _, d := range rdirs {
			if failed[d] {
				unscanned = append(unscanned, relTo(p.Dir, d))
			}
		}
		if len(unscanned) > 0 {
			r.Reason = "not confirmed: the rescan of " + strings.Join(unscanned, ", ") + " failed"
			continue
		}
		r.Status = StatusFixed
	scan:
		for _, d := range rdirs {
			for _, f := range scans[d] {
				if !namesAny(f, r) || !samePackage(f.Package, r.Package) {
					continue
				}
				// The bumped copy is either still at the installed version
				// (the bump did not take) or at the target or later (the
				// fix did not fix it). Any other version is another copy
				// of the package, which this finding was not about.
				got := f.Package.Version
				if bumpedCopy(r, got, minTo) {
					r.Status = StatusStillPresent
					r.Reason = "the rescan of " + relTo(p.Dir, d) + " still reports it, in " + f.Package.Name + " " + got
					break scan
				}
				p.Notes = appendUnique(p.Notes, fmt.Sprintf("%s: the rescan of %s also reports it in %s %s, another copy than the one bumped", r.ID, relTo(p.Dir, d), f.Package.Name, got))
			}
		}
	}
	return v
}

// resultDirs are the directories of the applied bumps that result i needed,
// sorted, and the lowest version those bumps raised the package to.
func (p *Plan) resultDirs(i int) (dirs []string, minTo string) {
	for _, b := range p.bumps {
		// A Dockerfile is verified by rebuilding its image, never by a
		// directory rescan (verifyImages).
		if b.done == nil || b.eco == EcosystemDocker {
			continue
		}
		for _, r := range b.results {
			if r != i {
				continue
			}
			dirs = appendUnique(dirs, b.dir)
			if minTo == "" || compareIn(b.eco, b.to, minTo) < 0 {
				minTo = b.to
			}
		}
	}
	sort.Strings(dirs)
	return dirs, minTo
}

// samePackage reports whether a and b are the same package, whatever their
// versions: the same purl without its version, qualifiers and subpath when
// both have one, else the same patcher kind and name.
func samePackage(a, b vulnscan.Package) bool {
	if a.PURL != "" && b.PURL != "" {
		return purlBase(a.PURL) == purlBase(b.PURL)
	}
	ka, ea := classify(a)
	kb, eb := classify(b)
	return ka == kb && ea == eb && ka != kindOther && packageName(ka, a.Name) == packageName(kb, b.Name)
}

// packageName is a package's identity within its kind: a PyPI name as PEP
// 503 normalizes it ("PyYAML" from an image is "pyyaml" from a requirements
// file), any other as it is.
func packageName(k packageKind, name string) string {
	if k == kindPython {
		return pyName(name)
	}
	return name
}

// purlBase is a purl's type, namespace and name: pkg:npm/%40s/x@1.0?a=b →
// pkg:npm/%40s/x; a PyPI name normalized (pkg:pypi/PyYAML@6.0 → pkg:pypi/pyyaml).
func purlBase(purl string) string {
	if i := strings.IndexAny(purl, "?#"); i >= 0 {
		purl = purl[:i]
	}
	if slash := strings.LastIndexByte(purl, '/'); slash >= 0 {
		if at := strings.IndexByte(purl[slash:], '@'); at >= 0 {
			purl = purl[:slash+at]
		}
	}
	purl = strings.ToLower(purl)
	if name, ok := strings.CutPrefix(purl, "pkg:pypi/"); ok {
		return "pkg:pypi/" + pyName(name) // PEP 503, as the purl spec says
	}
	return purl
}

// bumpedCopy reports whether a rescan that reports r's advisory on r's
// package at version got is reporting the copy r bumped: still at the
// installed version (the bump did not take), or at minTo or later (the fix
// did not fix it). Any other version is another copy of the package. A Go
// standard library version is compared without its "go" prefix; a PyPI
// version as PEP 440 orders it ("2.31" is "2.31.0").
func bumpedCopy(r *Result, got, minTo string) bool {
	installed := r.Installed
	switch kind, _ := classify(r.Package); kind {
	case kindGoStdlib:
		got, installed, minTo = goBare(got), goBare(installed), goBare(minTo)
	case kindPython:
		_, okGot := parsePyVersion(got)
		_, okMin := parsePyVersion(minTo)
		return pyEqual(got, installed) || okGot && okMin && comparePyVersions(got, minTo) >= 0
	}
	return sameVersion(got, installed) || atLeast(got, minTo)
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// sameVersion reports whether a and b are one version, with or without a
// leading "v".
func sameVersion(a, b string) bool {
	if npmVersion(a) == npmVersion(b) {
		return true
	}
	return canonical(a) != "" && canonical(b) != "" && compareVersions(a, b) == 0
}

// atLeast reports whether v is min or later; false when either is not a
// version.
func atLeast(v, min string) bool {
	return canonical(v) != "" && canonical(min) != "" && compareVersions(v, min) >= 0
}

func namesAny(f vulnscan.Finding, r *Result) bool {
	if f.Names(r.ID) {
		return true
	}
	for _, a := range r.Aliases {
		if f.Names(a) {
			return true
		}
	}
	return false
}

// Document is the plan's outcome as `forge vuln patch --json` prints it,
// with the exit code it decides:
//
//   - 4 a refusal (nothing was changed);
//   - 1 a bump failed, a finding is still present, the verify command failed
//     or the rescan could not run: the agent takeover signal;
//   - 2 at least one requested finding has nothing deterministic to do
//     (skipped), whatever was bumped beside it;
//   - 0 every requested finding bumped (and, with --verify, verified), or
//     nothing to do at all.
func (p *Plan) Document(env, release string, allowMajor bool) Document {
	doc := Document{
		Schema: DocumentSchema, Env: env, Release: release, Dir: p.Dir, AllowMajor: allowMajor,
		Changed: p.applied && len(p.bumps) > 0, Findings: p.Results, Bumps: p.Bumps(), Verify: p.verify,
		Notes: append([]string(nil), p.Notes...), Refusals: append([]string(nil), p.Refusals...),
	}
	if doc.Findings == nil {
		doc.Findings = []Result{}
	}
	if len(p.Refusals) > 0 {
		doc.Findings = append([]Result(nil), doc.Findings...)
		for i := range doc.Findings {
			if r := &doc.Findings[i]; r.Status == StatusBumped {
				r.Status, r.Reason = StatusSkipped, "not applied: the patch was refused"
			}
		}
	}
	doc.Summary = summarize(doc.Findings)
	doc.ExitCode = p.exitCode()
	return doc
}

// Exit codes, as Document.ExitCode spells them (the shared table of
// internal/cli/exitcodes.go: 1 wrong, 2 undetermined, 4 refused).
const (
	ExitOK          = 0
	ExitVerify      = 1
	ExitNothingToDo = 2
	ExitRefused     = 4
)

func (p *Plan) exitCode() int {
	if len(p.Refusals) > 0 {
		return ExitRefused
	}
	s := summarize(p.Results)
	if s.Failed > 0 || s.StillPresent > 0 || (p.verify != nil && (!p.verify.Passed || p.verify.RescanError != "")) {
		return ExitVerify
	}
	if s.Skipped > 0 {
		return ExitNothingToDo
	}
	return ExitOK
}
