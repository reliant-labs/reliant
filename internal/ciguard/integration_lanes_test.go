package ciguard

import (
	"bufio"
	"go/build/constraint"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// integrationNotInCI is every package with `integration`-tagged tests that no
// ci.yml step runs, and why. pkg/obsquery's sat on a list like this one without
// anyone writing it down, and one of its tests was red on main for days
// (forge#1870). Adding a package to a lane is a runner-budget call (the org
// runs 20 jobs at once), so a new one either joins a push-to-main step or
// says here why not.
var integrationNotInCI = map[string]string{
	"./pkg/observe": "its headers-file smoke did not compile (an import cycle through pkg/deploy) until forge#2053; it then joins the container step with RUN_HEADERS_FILE_SMOKE=1 (boots two collector containers)",
}

// integrationTags matches a `go test` invocation's -tags naming integration.
var integrationTags = regexp.MustCompile(`-tags[= ]\S*\bintegration\b`)

// integrationTestFunc matches a top-level test function's declaration.
var integrationTestFunc = regexp.MustCompile(`(?m)^func (Test\w*)\(`)

// integrationStep is the packages one ci.yml integration step names (./a/b
// or ./a/...), and its -run filter; nil when it has none and runs every test.
type integrationStep struct {
	name     string
	patterns []string
	run      *regexp.Regexp
}

type stepsJob struct {
	If    string `yaml:"if"`
	Steps []struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
	} `yaml:"steps"`
}

// TestIntegrationPackagesRunOnMain proves every package with
// `integration`-tagged tests is run by a ci.yml step in the push-to-main tier,
// or is named in integrationNotInCI with a reason; and that no such step runs
// on a pull request or a merge-queue entry, whose five-minute budget a
// container-backed test does not fit. A step that narrows its run with -run
// (so a package's unit suite, which another job runs, is not run twice) must
// select every integration test of every package it names: a test added
// beside them would otherwise sit in a lane that never runs it.
func TestIntegrationPackagesRunOnMain(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflow, err)
	}
	var wf struct {
		Jobs map[string]stepsJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", ciWorkflow, err)
	}

	var steps []integrationStep
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !integrationTags.MatchString(s.Run) {
				continue
			}
			if !strings.Contains(j.If, "github.event_name == 'push'") ||
				strings.Contains(j.If, "pull_request") || strings.Contains(j.If, "merge_group") {
				t.Errorf("job %s step %q runs integration tests but the job is not push-to-main only (if: %q): they do not fit the pull-request tier's budget", name, s.Name, j.If)
			}
			step := integrationStep{name: s.Name}
			fields := strings.Fields(s.Run)
			for i, f := range fields {
				switch {
				case strings.HasPrefix(f, "./"):
					step.patterns = append(step.patterns, strings.TrimSuffix(f, "/"))
				case f == "-run" && i+1 < len(fields), strings.HasPrefix(f, "-run="):
					expr := strings.TrimPrefix(f, "-run=")
					if f == "-run" {
						expr = fields[i+1]
					}
					re, err := regexp.Compile(strings.Trim(expr, `'"`))
					if err != nil {
						t.Fatalf("job %s step %q: -run %s: %v", name, s.Name, expr, err)
					}
					step.run = re
				}
			}
			steps = append(steps, step)
		}
	}
	if len(steps) == 0 {
		t.Fatalf("no ci.yml step runs `go test -tags=integration ./<pkg>` — this guard would inspect nothing")
	}

	tests := integrationTests(t, filepath.Join("..", ".."))
	if len(tests) == 0 {
		t.Fatal("found no integration-tagged test files — this guard would inspect nothing")
	}
	pkgs := make([]string, 0, len(tests))
	for pkg := range tests {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	// covering is every step that names pkg.
	covering := func(pkg string) []integrationStep {
		var out []integrationStep
		for _, s := range steps {
			for _, p := range s.patterns {
				base, tree := strings.CutSuffix(p, "/...")
				if p == pkg || tree && (pkg == base || strings.HasPrefix(pkg, base+"/")) {
					out = append(out, s)
					break
				}
			}
		}
		return out
	}
	for _, pkg := range pkgs {
		_, listed := integrationNotInCI[pkg]
		by := covering(pkg)
		switch {
		case len(by) > 0 && listed:
			t.Errorf("%s is run by a ci.yml integration step and still listed in integrationNotInCI: delete the entry", pkg)
		case len(by) == 0 && !listed:
			t.Errorf("%s has integration-tagged tests that no ci.yml step runs: add it to a push-to-main step (go test -tags=integration %s), or to integrationNotInCI with the reason it stays out", pkg, pkg)
		}
		for _, name := range tests[pkg] {
			selected := len(by) == 0 // a package no step runs is reported above
			for _, s := range by {
				if s.run == nil || s.run.MatchString(name) {
					selected = true
				}
			}
			if !selected {
				t.Errorf("%s's integration test %s is not selected by the -run of the ci.yml step that names the package (%q): add it to the step's -run", pkg, name, by[0].name)
			}
		}
	}
	for pkg := range integrationNotInCI {
		if !slices.Contains(pkgs, pkg) {
			t.Errorf("integrationNotInCI names %s, which has no integration-tagged tests any more: delete the entry", pkg)
		}
	}
}

// integrationTests maps, as ./dir, every directory under root holding a
// _test.go file that builds only with the integration tag to the top-level
// tests those files declare. Read by this process, so the test cache sees an
// added or retagged file.
func integrationTests(t *testing.T, root string) map[string][]string {
	t.Helper()
	seen := map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor", ".forge", ".tools":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		only, err := integrationOnly(p)
		if err != nil {
			return err
		}
		if only {
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			pkg := "./" + path.Clean(filepath.ToSlash(rel))
			names := seen[pkg]
			for _, m := range integrationTestFunc.FindAllSubmatch(src, -1) {
				names = append(names, string(m[1]))
			}
			seen[pkg] = names
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for pkg := range seen {
		sort.Strings(seen[pkg])
	}
	return seen
}

// integrationOnly reports whether the file's //go:build line (before its
// package clause) is satisfied with the integration tag and not without it.
func integrationOnly(file string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return false, nil
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false, nil
		}
		with := expr.Eval(func(tag string) bool { return tag == "integration" || tag == "linux" })
		without := expr.Eval(func(tag string) bool { return tag == "linux" })
		return with && !without, nil
	}
	return false, sc.Err()
}
