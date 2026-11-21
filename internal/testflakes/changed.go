package testflakes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Which tests a change adds or edits: top-level Test and Fuzz functions in the
// *_test.go files it adds or modifies, found two ways in the diff (the port of
// scripts/ci-changed-tests.sh):
//
//   - a `+func TestX(` line: the test is new, or its signature line moved;
//   - a hunk whose header names `func TestX(`: git's default funcname rule
//     labels each hunk with the nearest preceding unindented line, which in
//     Go is the enclosing top-level declaration — so an edited body is found.
//
// An edit to a shared helper names no test and is not chased: this is a cheap
// gate on new and edited tests, not a substitute for the full suite.
//
// Files built only under the e2e or integration tag are skipped (they need the
// tag and their provisioned tools), and so is a test file outside the module's
// own packages (testdata/, a nested module): `go test ./<dir>` cannot build it
// from here.

var (
	addedTestRe = regexp.MustCompile(`^\+func ((?:Test|Fuzz)[A-Za-z0-9_]*)\(`)
	hunkTestRe  = regexp.MustCompile(`^@@ .* @@ func ((?:Test|Fuzz)[A-Za-z0-9_]*)\(`)
	taggedRe    = regexp.MustCompile(`(?m)^//go:build .*\b(e2e|integration)\b`)
)

// ChangedPackage is one package's added or edited tests.
type ChangedPackage struct {
	// Dir is the package directory, absolute.
	Dir string `json:"dir"`
	// Rel is Dir relative to the module root, slash-separated ("." for the
	// root package).
	Rel   string   `json:"rel"`
	Names []string `json:"names"`
}

// ChangedTests is what FindChangedTests found.
type ChangedTests struct {
	Base     string           `json:"base"`
	Packages []ChangedPackage `json:"packages"`
	// Skipped says, per file, why a changed test file was not considered.
	Skipped []string `json:"skipped,omitempty"`
}

// Names is every selected test name, sorted and unique. A name may repeat in
// another selected package; running it there too is extra coverage, never an
// omission.
func (c ChangedTests) Names() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Packages {
		for _, n := range p.Names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// RunRegex is the `-run` value selecting every name: `^(TestA|TestB)$`.
func (c ChangedTests) RunRegex() string {
	return "^(" + strings.Join(c.Names(), "|") + ")$"
}

// ChangedOptions configures FindChangedTests.
type ChangedOptions struct {
	// Root is the module root: where go list runs and the packages resolve.
	Root string
	// Base is the revision the change is measured from.
	Base string
	Git  Git
	List ListPackages
}

// FindChangedTests names the top-level tests the diff from Base to HEAD adds
// or edits, by package. An error means the change could not be read at all.
func FindChangedTests(ctx context.Context, o ChangedOptions) (ChangedTests, error) {
	if o.Git == nil {
		o.Git = ExecGit
	}
	if o.List == nil {
		o.List = ExecListPackages
	}
	out := ChangedTests{Base: o.Base}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return out, err
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	top, err := o.Git(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return out, err
	}
	dirs, err := o.List(ctx, root)
	if err != nil {
		return out, err
	}
	inModule := map[string]bool{}
	for _, d := range dirs {
		inModule[canonicalDir(d)] = true
	}
	files, err := o.Git(ctx, root, "diff", "--name-only", "--diff-filter=AM", o.Base, "HEAD", "--", "*_test.go")
	if err != nil {
		return out, err
	}
	byDir := map[string]map[string]bool{}
	for _, f := range strings.Split(files, "\n") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		abs := filepath.Join(top, filepath.FromSlash(f))
		data, err := os.ReadFile(abs)
		if err != nil {
			continue // deleted since, or not a regular file: nothing to run
		}
		// Canonical, as root and the package dirs are: the package's Rel is
		// computed against root below.
		dir := canonicalDir(filepath.Dir(abs))
		if !inModule[dir] {
			out.Skipped = append(out.Skipped, fmt.Sprintf("skip (not a package of this module): %s", f))
			continue
		}
		if taggedRe.Match(data) {
			out.Skipped = append(out.Skipped, fmt.Sprintf("skip (tagged): %s", f))
			continue
		}
		diff, err := o.Git(ctx, root, "diff", "-U0", o.Base, "HEAD", "--", abs)
		if err != nil {
			return out, err
		}
		for _, line := range strings.Split(diff, "\n") {
			m := addedTestRe.FindStringSubmatch(line)
			if m == nil {
				m = hunkTestRe.FindStringSubmatch(line)
			}
			if m == nil {
				continue
			}
			if byDir[dir] == nil {
				byDir[dir] = map[string]bool{}
			}
			byDir[dir][m[1]] = true
		}
	}
	for dir, names := range byDir {
		p := ChangedPackage{Dir: dir, Rel: "."}
		if rel, err := filepath.Rel(root, dir); err == nil {
			p.Rel = filepath.ToSlash(rel)
		}
		for n := range names {
			p.Names = append(p.Names, n)
		}
		sort.Strings(p.Names)
		out.Packages = append(out.Packages, p)
	}
	sort.Slice(out.Packages, func(i, j int) bool { return out.Packages[i].Rel < out.Packages[j].Rel })
	return out, nil
}

// canonicalDir is d with symlinks resolved and cleaned, so a package
// directory from `go list` and a changed file's directory compare equal
// however each was spelled: a symlinked temp dir on macOS, or a Windows
// 8.3 short name (RUNNER~1) that EvalSymlinks expands to the long one.
func canonicalDir(d string) string {
	if real, err := filepath.EvalSymlinks(d); err == nil {
		d = real
	}
	return filepath.Clean(d)
}
