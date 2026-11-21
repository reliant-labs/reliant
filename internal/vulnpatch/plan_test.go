package vulnpatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// fakeRunner records every command and fails the ones fail names (by argv
// prefix). It runs nothing.
type fakeRunner struct {
	root string
	mu   sync.Mutex
	cmds []string // "<dir rel to root>: argv"
	fail map[string]string
}

func (f *fakeRunner) run(_ context.Context, c vulnscan.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := strings.Join(append([]string{c.Path}, c.Args...), " ")
	f.cmds = append(f.cmds, relTo(f.root, c.Dir)+": "+argv)
	for prefix, out := range f.fail {
		if strings.HasPrefix(argv, prefix) {
			_, _ = fmt.Fprint(c.Stdout, out)
			return errors.New("exit status 1")
		}
	}
	return nil
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func goFinding(id, mod, version string, fixed ...string) vulnscan.Finding {
	return vulnscan.Finding{
		ID: id, Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: fixed,
		Package: vulnscan.Package{Name: mod, Version: version, Type: "go-module", PURL: "pkg:golang/" + mod + "@" + version},
	}
}

func npmFinding(id, name, version string, fixed ...string) vulnscan.Finding {
	return vulnscan.Finding{
		ID: id, Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: fixed,
		Package: vulnscan.Package{Name: name, Version: version, Type: "npm", PURL: "pkg:npm/" + name + "@" + version},
	}
}

const goModApp = `module example.com/app

go 1.22

require (
	example.com/lib v1.2.0
	example.com/other v0.3.0 // indirect
)
`

func packageLock(deps map[string]string) string {
	pkgs := map[string]any{"": map[string]any{"name": "web"}}
	for path, v := range deps {
		pkgs[path] = map[string]any{"version": v}
	}
	b, _ := json.MarshalIndent(map[string]any{"name": "web", "lockfileVersion": 3, "packages": pkgs}, "", "  ")
	return string(b)
}

// patch plans, applies and verifies findings against root with the fake.
func patch(t *testing.T, root string, findings []vulnscan.Finding, opts Options, fail map[string]string, verify *VerifyOptions) (Document, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{root: root, fail: fail}
	p, err := NewPlan(root, findings, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(context.Background(), fr.run); err != nil && !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	if verify != nil {
		v := *verify
		v.Run = fr.run
		p.Verify(context.Background(), v)
	}
	return p.Document("prod", "v1.0.0", opts.AllowMajor), fr
}

func wantCmds(t *testing.T, fr *fakeRunner, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(fr.cmds, want) {
		t.Errorf("commands =\n  %s\nwant\n  %s", strings.Join(fr.cmds, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestGoDirectAndTransitive(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"go.mod": goModApp})
	doc, fr := patch(t, root, []vulnscan.Finding{
		goFinding("GO-2026-0001", "example.com/lib", "v1.2.0", "v1.2.5", "v2.0.1"),
		goFinding("GO-2026-0002", "example.com/other", "v0.3.0", "v0.3.2"),
		// Not in go.mod: the one module gets it through MVS.
		goFinding("GO-2026-0003", "example.com/deep", "v1.0.0", "v1.0.4"),
	}, Options{}, nil, nil)
	wantCmds(t, fr,
		".: go get example.com/lib@v1.2.5 example.com/other@v0.3.2 example.com/deep@v1.0.4",
		".: go mod tidy")
	if doc.ExitCode != ExitOK || !doc.Changed || doc.Summary.Bumped != 3 {
		t.Fatalf("exit %d changed %v summary %+v", doc.ExitCode, doc.Changed, doc.Summary)
	}
	if b := doc.Bumps[0]; b.Manifest != "go.mod" || b.From != "v1.2.0" || b.To != "v1.2.5" || !b.Direct || b.Ecosystem != EcosystemGo {
		t.Errorf("bump = %+v", b)
	}
	if b := doc.Bumps[2]; b.Direct || b.From != "v1.0.0" {
		t.Errorf("transitive bump = %+v", b)
	}
}

func TestGoWorkspace(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.work":                 "go 1.22\n\nuse (\n\t./a\n\t./b\n)\n",
		"a/go.mod":                "module example.com/a\n\ngo 1.22\n\nrequire example.com/lib v1.2.0\n",
		"b/go.mod":                "module example.com/b\n\ngo 1.22\n\nrequire example.com/lib v1.3.0\n",
		"c/go.mod":                "module example.com/c\n\ngo 1.22\n\nrequire example.com/lib v1.4.0\n",
		"vendor/x/go.mod":         "module example.com/vendored\n\nrequire example.com/lib v1.0.0\n",
		"web/node_modules/go.mod": "module example.com/nm\n\nrequire example.com/lib v1.0.0\n",
	}
	root := writeTree(t, files)
	doc, fr := patch(t, root, []vulnscan.Finding{
		goFinding("GO-1", "example.com/lib", "v1.2.0", "v1.3.1"),
		goFinding("GO-2", "example.com/nobody", "v1.0.0", "v1.0.1"),
		goFinding("GO-3", "example.com/a", "v0.0.0", "v0.0.1"),
	}, Options{}, nil, nil)
	// Every module that requires it below the fix, in its own directory;
	// c already requires a fixed version; vendor and node_modules are not
	// the tree's.
	wantCmds(t, fr,
		"a: go get example.com/lib@v1.3.1", "a: go mod tidy",
		"b: go get example.com/lib@v1.3.1", "b: go mod tidy")
	r := doc.Findings
	if r[0].Status != StatusBumped || !reflect.DeepEqual(r[0].Manifests, []string{"a/go.mod", "b/go.mod", "c/go.mod"}) {
		t.Errorf("lib = %+v", r[0])
	}
	if r[1].Status != StatusSkipped || !strings.Contains(r[1].Reason, "ambiguous") {
		t.Errorf("a module no go.mod requires, in a workspace: %+v", r[1])
	}
	if r[2].Status != StatusSkipped || !strings.Contains(r[2].Reason, "module of this tree") {
		t.Errorf("the tree's own module: %+v", r[2])
	}
	if doc.ExitCode != ExitNothingToDo {
		t.Errorf("exit = %d, want 2 (two findings have nothing deterministic)", doc.ExitCode)
	}
}

func TestGoAlreadyFixedAndLocationHint(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"a/go.mod": "module example.com/a\n\nrequire example.com/lib v1.2.0\n",
		"b/go.mod": "module example.com/b\n\nrequire example.com/lib v1.2.0\n",
	})
	hinted := goFinding("GO-1", "example.com/lib", "v1.2.0", "v1.2.1")
	hinted.Locations = []string{"/b/go.mod"}
	_, fr := patch(t, root, []vulnscan.Finding{hinted}, Options{}, nil, nil)
	wantCmds(t, fr, "b: go get example.com/lib@v1.2.1", "b: go mod tidy")

	root = writeTree(t, map[string]string{"go.mod": "module example.com/x\n\nrequire example.com/lib v1.5.0\n"})
	doc, fr := patch(t, root, []vulnscan.Finding{goFinding("GO-1", "example.com/lib", "v1.2.0", "v1.2.1")}, Options{}, nil, nil)
	wantCmds(t, fr)
	if doc.Findings[0].Status != StatusFixed || doc.Changed || doc.ExitCode != ExitOK {
		t.Errorf("already fixed: %+v changed=%v exit=%d", doc.Findings[0], doc.Changed, doc.ExitCode)
	}
}

func TestNoPatcherYet(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"go.mod": goModApp})
	osPkg := vulnscan.Finding{ID: "CVE-1", FixState: "fixed", FixedIn: []string{"1.4-r0"}, Package: vulnscan.Package{Name: "zlib", Version: "1.3.2-r1", Type: "apk"}}
	py := vulnscan.Finding{ID: "CVE-2", FixState: "fixed", FixedIn: []string{"1.1"}, Package: vulnscan.Package{Name: "requests", Version: "1.0", Type: "python"}}
	jar := vulnscan.Finding{ID: "CVE-3", FixState: "fixed", FixedIn: []string{"2.0"}, Package: vulnscan.Package{Name: "log4j", Version: "1.0", Type: "java-archive"}}
	unfixed := goFinding("GO-4", "example.com/lib", "v1.2.0")
	unfixed.FixState = vulnscan.FixStateNotFixed
	doc, fr := patch(t, root, []vulnscan.Finding{osPkg, py, jar, unfixed}, Options{}, nil, nil)
	wantCmds(t, fr)
	for i, want := range []string{"OS package (apk), and the tree has no Dockerfile", "in no Python manifest", "java-archive", "no fixed version (fix state: not-fixed)"} {
		if r := doc.Findings[i]; r.Status != StatusSkipped || !strings.Contains(r.Reason, want) {
			t.Errorf("finding %d = %s %q, want skipped mentioning %q", i, r.Status, r.Reason, want)
		}
	}
	if doc.ExitCode != ExitNothingToDo || doc.Changed {
		t.Errorf("exit = %d changed = %v", doc.ExitCode, doc.Changed)
	}
	// --ecosystem npm leaves every one of them out.
	doc, _ = patch(t, root, []vulnscan.Finding{osPkg, py, jar, unfixed}, Options{Ecosystems: []Ecosystem{EcosystemNPM}}, nil, nil)
	if len(doc.Findings) != 0 || doc.ExitCode != ExitOK {
		t.Errorf("--ecosystem npm kept %+v (exit %d)", doc.Findings, doc.ExitCode)
	}
}

func TestMajorBump(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"go.mod": goModApp})
	f := goFinding("GO-1", "example.com/lib", "v1.2.0", "v2.0.0")
	doc, _ := patch(t, root, []vulnscan.Finding{f}, Options{}, nil, nil)
	if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "needs a major bump") || doc.ExitCode != ExitNothingToDo {
		t.Errorf("env-wide: %+v exit %d", r, doc.ExitCode)
	}
	// Named explicitly, it is refused, and nothing runs.
	doc, fr := patch(t, root, []vulnscan.Finding{f, goFinding("GO-2", "example.com/other", "v0.3.0", "v0.3.2")}, Options{Explicit: true}, nil, nil)
	wantCmds(t, fr)
	if doc.ExitCode != ExitRefused || doc.Changed || len(doc.Refusals) != 1 || !strings.Contains(doc.Refusals[0], "--allow-major") {
		t.Errorf("explicit: exit %d changed %v refusals %v", doc.ExitCode, doc.Changed, doc.Refusals)
	}
	if r := doc.Findings[1]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "refused") {
		t.Errorf("a finding the refusal kept from running: %+v", r)
	}
	doc, fr = patch(t, root, []vulnscan.Finding{f}, Options{Explicit: true, AllowMajor: true}, nil, nil)
	wantCmds(t, fr, ".: go get example.com/lib@v2.0.0", ".: go mod tidy")
	if doc.ExitCode != ExitOK {
		t.Errorf("--allow-major: exit %d", doc.ExitCode)
	}
}

func TestNPM(t *testing.T) {
	t.Parallel()
	pkgJSON := "{\n  \"name\": \"web\",\n  \"version\": \"1.0.0\",\n  \"dependencies\": {\n    \"ws\": \"^8.20.0\"\n  },\n  \"devDependencies\": {\n    \"vite\": \"^5.0.0\"\n  }\n}\n"
	root := writeTree(t, map[string]string{
		"web/package.json": pkgJSON,
		"web/package-lock.json": packageLock(map[string]string{
			"node_modules/ws":                          "8.20.0",
			"node_modules/cookie":                      "0.6.0",
			"node_modules/express/node_modules/cookie": "0.6.0",
		}),
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		npmFinding("GHSA-ws", "ws", "8.20.0", "8.21.0"),
		npmFinding("GHSA-cookie", "cookie", "0.6.0", "0.7.0"),
	}, Options{}, nil, nil)
	wantCmds(t, fr, "web: npm install", "web: npm install ws@8.21.0")
	got, _ := os.ReadFile(filepath.Join(root, "web/package.json"))
	want := strings.TrimSuffix(pkgJSON, "}\n") + "  ,\"overrides\": {\n    \"cookie\": \"0.7.0\"\n  }\n}\n"
	want = strings.Replace(want, "  }\n  ,\"overrides\"", "  },\n  \"overrides\"", 1)
	if string(got) != want {
		t.Errorf("package.json =\n%s\nwant\n%s", got, want)
	}
	if doc.ExitCode != ExitOK || !doc.Bumps[0].Direct || doc.Bumps[1].Direct || len(doc.Bumps[1].Edits) != 1 {
		t.Errorf("exit %d bumps %+v", doc.ExitCode, doc.Bumps)
	}
}

func TestPNPMAndYarn(t *testing.T) {
	t.Parallel()
	pnpmLock := "lockfileVersion: '9.0'\n\nimporters:\n  .:\n    dependencies:\n      ws:\n        specifier: ^8.20.0\n        version: 8.20.0\n\npackages:\n  ws@8.20.0:\n    resolution: {integrity: sha512-x}\n  '@scope/cookie@0.6.0':\n    resolution: {integrity: sha512-y}\n\nsnapshots:\n  ws@8.20.0(bufferutil@4.0.0): {}\n  '@scope/cookie@0.6.0': {}\n"
	yarn1 := "# yarn lockfile v1\n\n\nws@^8.0.0, ws@^8.20.0:\n  version \"8.20.0\"\n  resolved \"https://registry.yarnpkg.com/ws/-/ws-8.20.0.tgz\"\n\n\"@scope/cookie@^0.6.0\":\n  version \"0.6.0\"\n"
	yarnBerry := "__metadata:\n  version: 8\n\n\"ws@npm:^8.20.0\":\n  version: 8.20.0\n  resolution: \"ws@npm:8.20.0\"\n\n\"@scope/cookie@npm:^0.6.0\":\n  version: 0.6.0\n"
	pkg := "{\n  \"name\": \"x\",\n  \"dependencies\": {\"ws\": \"^8.20.0\"}\n}\n"
	findings := []vulnscan.Finding{npmFinding("GHSA-ws", "ws", "8.20.0", "8.21.0"), npmFinding("GHSA-c", "@scope/cookie", "0.6.0", "0.6.1")}
	// One package manager per tree: lockfiles of several pinning the same
	// copy are ambiguous (TestJSEcosystemAmbiguity).
	for _, tc := range []struct {
		lockfile, body, overrideKey string
		eco                         Ecosystem
		cmds                        []string
	}{
		{"pnpm-lock.yaml", pnpmLock, `"pnpm": {`, EcosystemPNPM, []string{"p: pnpm install", "p: pnpm update ws@8.21.0"}},
		{"yarn.lock", yarn1, `"resolutions": {`, EcosystemYarn, []string{"p: yarn install", "p: yarn upgrade ws@8.21.0"}},
		{"yarn.lock", yarnBerry, `"resolutions": {`, EcosystemYarn, []string{"p: yarn install", "p: yarn up ws@8.21.0"}},
	} {
		root := writeTree(t, map[string]string{"p/package.json": pkg, "p/" + tc.lockfile: tc.body})
		doc, fr := patch(t, root, findings, Options{}, nil, nil)
		wantCmds(t, fr, tc.cmds...)
		got, _ := os.ReadFile(filepath.Join(root, "p", "package.json"))
		if !strings.Contains(string(got), tc.overrideKey) || !strings.Contains(string(got), `"@scope/cookie": "0.6.1"`) {
			t.Errorf("%s: package.json lacks the override:\n%s", tc.cmds[1], got)
		}
		if doc.ExitCode != ExitOK || doc.Findings[0].Ecosystem != tc.eco || !reflect.DeepEqual(doc.Findings[0].Manifests, []string{"p/" + tc.lockfile}) {
			t.Errorf("%s: exit %d findings %+v", tc.cmds[1], doc.ExitCode, doc.Findings)
		}
	}
	// --ecosystem pnpm touches only the pnpm lockfile, so the yarn tree
	// beside it is no ambiguity.
	root := writeTree(t, map[string]string{"p/package.json": pkg, "p/pnpm-lock.yaml": pnpmLock, "y/package.json": pkg, "y/yarn.lock": yarn1})
	_, fr := patch(t, root, findings[:1], Options{Ecosystems: []Ecosystem{EcosystemPNPM}}, nil, nil)
	wantCmds(t, fr, "p: pnpm update ws@8.21.0")
}

// A lockfile that pins the package at another version than the scanned one
// is not the finding's copy: it is never bumped on the finding's account,
// whether its copy is older or already past the fix.
func TestJSNoExactMatch(t *testing.T) {
	t.Parallel()
	pkg := "{\n  \"name\": \"web\",\n  \"dependencies\": {\"ws\": \"^8.0.0\"}\n}\n"
	for _, tc := range []struct{ name, pinned string }{
		{"an unrelated older copy", "8.19.0"},
		{"a copy past the fix", "8.22.0"},
	} {
		root := writeTree(t, map[string]string{
			"web/package.json":      pkg,
			"web/package-lock.json": packageLock(map[string]string{"node_modules/ws": tc.pinned}),
		})
		doc, fr := patch(t, root, []vulnscan.Finding{npmFinding("GHSA-ws", "ws", "8.17.0", "8.21.0")}, Options{}, nil, nil)
		wantCmds(t, fr)
		r := doc.Findings[0]
		if r.Status != StatusSkipped || !strings.Contains(r.Reason, "no lockfile pins the scanned ws 8.17.0") || !strings.Contains(r.Reason, "web/package-lock.json pins "+tc.pinned) {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if doc.Changed || len(doc.Bumps) != 0 || doc.ExitCode != ExitNothingToDo {
			t.Errorf("%s: changed %v bumps %+v exit %d", tc.name, doc.Changed, doc.Bumps, doc.ExitCode)
		}
	}
}

// Lockfiles of several package managers pinning the scanned copy leave which
// one builds the artifact ambiguous: skipped, whichever was parsed first. A
// location the scan names settles it.
func TestJSEcosystemAmbiguity(t *testing.T) {
	t.Parallel()
	pkg := "{\n  \"name\": \"web\",\n  \"dependencies\": {\"ws\": \"^8.20.0\"}\n}\n"
	yarn1 := "# yarn lockfile v1\n\n\nws@^8.20.0:\n  version \"8.20.0\"\n"
	pnpmLock := "lockfileVersion: '9.0'\n\npackages:\n  ws@8.20.0:\n    resolution: {integrity: sha512-x}\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"one directory", map[string]string{
			"web/package.json": pkg, "web/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}), "web/yarn.lock": yarn1,
		}},
		{"two trees", map[string]string{
			"a/package.json": pkg, "a/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
			"b/package.json": pkg, "b/pnpm-lock.yaml": pnpmLock,
		}},
	} {
		root := writeTree(t, tc.files)
		doc, fr := patch(t, root, []vulnscan.Finding{npmFinding("GHSA-ws", "ws", "8.20.0", "8.21.0")}, Options{}, nil, nil)
		wantCmds(t, fr)
		r := doc.Findings[0]
		if r.Status != StatusSkipped || !strings.Contains(r.Reason, "several package managers") || r.Ecosystem != "" || len(r.Manifests) != 0 {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if doc.Changed || doc.ExitCode != ExitNothingToDo {
			t.Errorf("%s: changed %v exit %d", tc.name, doc.Changed, doc.ExitCode)
		}
	}
	root := writeTree(t, map[string]string{
		"a/package.json": pkg, "a/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
		"b/package.json": pkg, "b/pnpm-lock.yaml": pnpmLock,
	})
	hinted := npmFinding("GHSA-ws", "ws", "8.20.0", "8.21.0")
	hinted.Locations = []string{"/b/pnpm-lock.yaml"}
	doc, fr := patch(t, root, []vulnscan.Finding{hinted}, Options{}, nil, nil)
	wantCmds(t, fr, "b: pnpm update ws@8.21.0")
	if r := doc.Findings[0]; r.Status != StatusBumped || r.Ecosystem != EcosystemPNPM {
		t.Errorf("located: %+v", r)
	}
}

// A package.json forge could not write back faithfully is never edited, and
// one whose direct dependencies are ambiguous leaves its tree alone.
func TestJSAmbiguousPackageJSONIsSkipped(t *testing.T) {
	t.Parallel()
	lock := packageLock(map[string]string{"node_modules/ws": "8.20.0", "node_modules/express/node_modules/cookie": "0.6.0"})
	for _, tc := range []struct {
		name, pkg, finding, want string
	}{
		{"trailing data", "{\n  \"name\": \"web\"\n}\n{\"name\": \"other\"}\n", "cookie", "data after the top-level object"},
		{"a duplicate override", "{\n  \"name\": \"web\",\n  \"overrides\": {\"cookie\": \"0.6.0\", \"cookie\": \"0.6.1\"}\n}\n", "cookie", `duplicate key "cookie"`},
		{"an override block that is no object", "{\n  \"name\": \"web\",\n  \"overrides\": []\n}\n", "cookie", "not a JSON object"},
		{"no package.json", "", "cookie", "cannot take an override safely"},
		{"a duplicate top-level key", "{\n  \"dependencies\": {\"ws\": \"^8.0.0\"},\n  \"dependencies\": {}\n}\n", "ws", `duplicate key "dependencies"`},
		{"a duplicate dependency", "{\n  \"dependencies\": {\"ws\": \"^8.0.0\", \"ws\": \"^8.1.0\"}\n}\n", "ws", `duplicate key "ws"`},
	} {
		files := map[string]string{"web/package-lock.json": lock}
		if tc.pkg != "" {
			files["web/package.json"] = tc.pkg
		}
		root := writeTree(t, files)
		version := map[string]string{"ws": "8.20.0", "cookie": "0.6.0"}[tc.finding]
		doc, fr := patch(t, root, []vulnscan.Finding{npmFinding("GHSA-x", tc.finding, version, "8.21.0", "0.7.0")}, Options{AllowMajor: true}, nil, nil)
		wantCmds(t, fr)
		if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, tc.want) {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if got, _ := os.ReadFile(filepath.Join(root, "web", "package.json")); string(got) != tc.pkg {
			t.Errorf("%s: package.json changed:\n%s", tc.name, got)
		}
		if doc.Changed {
			t.Errorf("%s: the tree changed", tc.name)
		}
	}
}

func TestNPMResolutionAmbiguity(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"a/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.18.0"}),
		"b/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.19.0"}),
		"c/package-lock.json": packageLock(map[string]string{
			"node_modules/ws":                    "8.20.0",
			"node_modules/old/node_modules/ws":   "7.5.0",
			"node_modules/newer/node_modules/ws": "8.22.0",
		}),
		"d/package.json":      "{}\n",
		"d/package-lock.json": packageLock(map[string]string{"node_modules/qs": "6.0.0", "node_modules/x/node_modules/qs": "6.9.0"}),
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		npmFinding("GHSA-1", "ws", "8.17.0", "8.21.0"), // no lockfile pins 8.17.0; others pin other copies
		npmFinding("GHSA-2", "ws", "8.20.0", "8.21.0"), // c pins another major too
		npmFinding("GHSA-3", "left-pad", "1.0.0", "1.0.1"),
		npmFinding("GHSA-4", "qs", "6.0.0", "6.5.0"), // a copy already past the fix is not lowered
	}, Options{}, nil, nil)
	r := doc.Findings
	if r[0].Status != StatusSkipped || !strings.Contains(r[0].Reason, "no lockfile pins the scanned ws 8.17.0") {
		t.Errorf("no exact copy: %+v", r[0])
	}
	if r[1].Status != StatusSkipped || !strings.Contains(r[1].Reason, "another major") {
		t.Errorf("several majors: %+v", r[1])
	}
	if r[2].Status != StatusSkipped || !strings.Contains(r[2].Reason, "no lockfile") {
		t.Errorf("absent: %+v", r[2])
	}
	if r[3].Status != StatusBumped || doc.Bumps[0].To != "6.9.0" {
		t.Errorf("override = %+v %+v", r[3], doc.Bumps)
	}
	wantCmds(t, fr, "d: npm install")
}

func TestApplyFailureIsTheTakeoverSignal(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"a/go.mod": "module example.com/a\n\nrequire example.com/lib v1.2.0\n",
		"b/go.mod": "module example.com/b\n\nrequire example.com/lib v1.2.0\n",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{goFinding("GO-1", "example.com/lib", "v1.2.0", "v1.2.1")}, Options{},
		map[string]string{"go mod tidy": "go: updates to go.mod needed\n"}, nil)
	// a's failure does not stop b.
	if len(fr.cmds) != 4 {
		t.Errorf("commands = %v", fr.cmds)
	}
	if r := doc.Findings[0]; r.Status != StatusFailed || !strings.Contains(r.Reason, "go mod tidy") {
		t.Errorf("result = %+v", r)
	}
	if doc.ExitCode != ExitVerify || !strings.Contains(doc.Bumps[0].Error, "updates to go.mod needed") {
		t.Errorf("exit %d bump %+v", doc.ExitCode, doc.Bumps[0])
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	tree := map[string]string{"go.mod": goModApp}
	findings := []vulnscan.Finding{goFinding("GO-1", "example.com/lib", "v1.2.0", "v1.2.5"), goFinding("GO-2", "example.com/other", "v0.3.0", "v0.3.2")}
	clean := func(context.Context, string) ([]vulnscan.Finding, error) { return nil, nil }

	root := writeTree(t, tree)
	doc, fr := patch(t, root, findings, Options{}, nil, &VerifyOptions{Command: "task test:short", Source: VerifySourceTaskfile, Rescan: clean})
	if doc.ExitCode != ExitOK || doc.Summary.Fixed != 2 || !doc.Verify.Passed || !reflect.DeepEqual(doc.Verify.Rescanned, []string{"."}) {
		t.Errorf("clean: exit %d summary %+v verify %+v", doc.ExitCode, doc.Summary, doc.Verify)
	}
	if last := fr.cmds[len(fr.cmds)-1]; last != ".: sh -c task test:short" {
		t.Errorf("verify ran %q", last)
	}

	root = writeTree(t, tree)
	doc, _ = patch(t, root, findings, Options{}, map[string]string{"sh -c": "--- FAIL: TestX\n"}, &VerifyOptions{Command: "go test ./...", Rescan: clean})
	if doc.ExitCode != ExitVerify || doc.Verify.Passed || !strings.Contains(doc.Verify.Output, "FAIL: TestX") {
		t.Errorf("failing gate: exit %d verify %+v", doc.ExitCode, doc.Verify)
	}

	root = writeTree(t, tree)
	still := func(context.Context, string) ([]vulnscan.Finding, error) {
		f := goFinding("CVE-9", "example.com/lib", "v1.2.5", "v1.2.6")
		f.Aliases = []string{"GO-1"}
		return []vulnscan.Finding{f}, nil
	}
	doc, _ = patch(t, root, findings, Options{}, nil, &VerifyOptions{Rescan: still})
	if doc.ExitCode != ExitVerify || doc.Findings[0].Status != StatusStillPresent || doc.Findings[1].Status != StatusFixed {
		t.Errorf("still present: exit %d %+v", doc.ExitCode, doc.Findings)
	}

	root = writeTree(t, tree)
	broken := func(context.Context, string) ([]vulnscan.Finding, error) {
		return nil, errors.New("grype: no database")
	}
	doc, _ = patch(t, root, findings, Options{}, nil, &VerifyOptions{Rescan: broken})
	if doc.ExitCode != ExitVerify || doc.Verify.RescanError == "" || doc.Findings[0].Status != StatusBumped {
		t.Errorf("rescan failed: exit %d %+v", doc.ExitCode, doc.Verify)
	}
}

func TestDefaultVerify(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		files       map[string]string
		cmd, source string
	}{
		{map[string]string{"Taskfile.yml": "version: '3'\ntasks:\n  test:short:\n    cmds: [go test -short ./...]\n", "go.mod": "module x\n"}, "task test:short", VerifySourceTaskfile},
		{map[string]string{"Taskfile.yml": "version: '3'\ntasks:\n  test: {}\n", "go.mod": "module x\n"}, "go build ./... && go test -short ./...", VerifySourceGo},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`, "pnpm-lock.yaml": ""}, "pnpm test", "pnpm"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`}, "npm test", "npm"},
		{map[string]string{"package.json": `{"scripts":{}}`}, "", VerifySourceNone},
	} {
		cmd, source := DefaultVerify(writeTree(t, tc.files))
		if cmd != tc.cmd || source != tc.source {
			t.Errorf("%v: DefaultVerify = %q, %q; want %q, %q", tc.files, cmd, source, tc.cmd, tc.source)
		}
	}
}

func TestSetJSONKeepsOrderAndIndent(t *testing.T) {
	t.Parallel()
	in := "{\n\t\"name\": \"x\",\n\t\"pnpm\": {\n\t\t\"overrides\": {\"a\": \"1.0.0\"},\n\t\t\"neverBuiltDependencies\": []\n\t},\n\t\"z\": 1\n}"
	out, err := setJSON([]byte(in), []string{"pnpm", "overrides"}, map[string]string{"b": "2.0.0", "a": "1.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n\t\"name\": \"x\",\n\t\"pnpm\": {\n\t\t\"overrides\": {\n\t\t\t\"a\": \"1.0.1\",\n\t\t\t\"b\": \"2.0.0\"\n\t\t},\n\t\t\"neverBuiltDependencies\": []\n\t},\n\t\"z\": 1\n}"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
}

// setJSON refuses a document it could not write back faithfully, at the top
// level and in every object on the path it edits; values it does not descend
// into are carried verbatim, duplicates included.
func TestSetJSONRefusesWhatItCannotRewrite(t *testing.T) {
	t.Parallel()
	path := []string{"pnpm", "overrides"}
	set := map[string]string{"a": "1.0.1"}
	for _, tc := range []struct{ in, want string }{
		{`{"name": "x"} {"name": "y"}`, "data after the top-level object"},
		{`{"name": "x"}]`, "data after the top-level object"},
		{`{"name": "x"} garbage`, "data after the top-level object"},
		{`{"name": "x", "name": "y"}`, `duplicate key "name"`},
		{`{"pnpm": {"overrides": {}}, "pnpm": {}}`, `duplicate key "pnpm"`},
		{`{"pnpm": {"overrides": {}, "overrides": {"b": "2"}}}`, `duplicate key "overrides"`},
		{`{"pnpm": {"overrides": {"a": "1", "a": "2"}}}`, `duplicate key "a"`},
		{`{"pnpm": "x"}`, "not a JSON object"},
		{`[]`, "not a JSON object"},
	} {
		if _, err := setJSON([]byte(tc.in), path, set); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("setJSON(%s) = %v, want an error mentioning %q", tc.in, err, tc.want)
		}
	}
	out, err := setJSON([]byte("{\"scripts\": {\"x\": \"1\", \"x\": \"2\"}}\n\n"), []string{"overrides"}, set)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"x": "1",`) || !strings.Contains(string(out), `"x": "2"`) {
		t.Errorf("an object setJSON does not edit was not carried verbatim:\n%s", out)
	}
}

// TestVerifyScopesRescansToTheirDirectory pins B3: the same advisory in two
// trees, fixed in one. Each result is judged by the rescan of its own
// tree only, and only on the same package (not a same-named one of another
// ecosystem) at the version the bump left or the one it replaced.
func TestVerifyScopesRescansToTheirDirectory(t *testing.T) {
	t.Parallel()
	pkg := "{\n  \"name\": \"x\",\n  \"dependencies\": {\"ws\": \"^8.20.0\"}\n}\n"
	root := writeTree(t, map[string]string{
		"a/package.json": pkg, "a/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
		"b/package.json": pkg, "b/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
	})
	inA := npmFinding("GHSA-ws", "ws", "8.20.0", "8.21.0")
	inA.Locations = []string{"/a/package-lock.json"}
	inB := npmFinding("GHSA-ws", "ws", "8.20.0", "8.21.0")
	inB.Locations = []string{"/b/package-lock.json"}
	rescan := func(_ context.Context, dir string) ([]vulnscan.Finding, error) {
		switch filepath.Base(dir) {
		case "a":
			// Cleared in a. A Go module named ws with the same advisory,
			// and an older copy of ws, are not the bumped copy.
			goWS := goFinding("GHSA-ws", "ws", "v8.21.0")
			other := npmFinding("CVE-2026-1", "ws", "7.5.0")
			other.Aliases = []string{"GHSA-ws"}
			return []vulnscan.Finding{goWS, other}, nil
		case "b":
			// Still in b, by its alias, at the version the bump left.
			still := npmFinding("CVE-2026-1", "ws", "8.21.0")
			still.Aliases = []string{"GHSA-ws"}
			return []vulnscan.Finding{still}, nil
		}
		return nil, fmt.Errorf("unexpected rescan of %s", dir)
	}
	doc, fr := patch(t, root, []vulnscan.Finding{inA, inB}, Options{}, nil, &VerifyOptions{Rescan: rescan})
	wantCmds(t, fr, "a: npm install ws@8.21.0", "b: npm install ws@8.21.0")
	if r := doc.Findings[0]; r.Status != StatusFixed {
		t.Errorf("a's result, cleared by a's rescan: %+v", r)
	}
	if r := doc.Findings[1]; r.Status != StatusStillPresent || !strings.Contains(r.Reason, "rescan of b") {
		t.Errorf("b's result, still in b's rescan: %+v", r)
	}
	if !reflect.DeepEqual(doc.Verify.Rescanned, []string{"a", "b"}) || doc.ExitCode != ExitVerify {
		t.Errorf("rescanned %v exit %d", doc.Verify.Rescanned, doc.ExitCode)
	}
	if !strings.Contains(strings.Join(doc.Notes, "\n"), "another copy than the one bumped") {
		t.Errorf("the older copy in a is not noted: %v", doc.Notes)
	}

	// A rescan that fails leaves only the results of its own tree
	// unconfirmed.
	root = writeTree(t, map[string]string{
		"a/package.json": pkg, "a/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
		"b/package.json": pkg, "b/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
	})
	half := func(_ context.Context, dir string) ([]vulnscan.Finding, error) {
		if filepath.Base(dir) == "b" {
			return nil, errors.New("grype: no database")
		}
		return nil, nil
	}
	doc, _ = patch(t, root, []vulnscan.Finding{inA, inB}, Options{}, nil, &VerifyOptions{Rescan: half})
	if doc.Findings[0].Status != StatusFixed || doc.Findings[1].Status != StatusBumped || !strings.Contains(doc.Findings[1].Reason, "rescan of b failed") {
		t.Errorf("half rescanned: %+v", doc.Findings)
	}
	if doc.ExitCode != ExitVerify || doc.Verify.RescanError == "" {
		t.Errorf("half rescanned: exit %d verify %+v", doc.ExitCode, doc.Verify)
	}
}

// TestDocumentGolden pins the forge.dev/vuln-patch/v1 document a mixed run
// prints. FORGE_UPDATE_GOLDEN=1 rewrites it.
func TestDocumentGolden(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"go.mod":                goModApp,
		"web/package.json":      "{\n  \"name\": \"web\",\n  \"dependencies\": {\"ws\": \"^8.20.0\"}\n}\n",
		"web/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
		"Dockerfile":            "FROM golang:1.22.3 AS build\nFROM alpine:3.19.1\n",
	})
	osPkg := vulnscan.Finding{ID: "CVE-2026-4738", Severity: vulnscan.SeverityHigh, FixState: "fixed", FixedIn: []string{"1.4-r0"}, Package: vulnscan.Package{Name: "zlib", Version: "1.3.2-r1", Type: "apk"}}
	reg := alpineRegistry()
	reg.digests[golangRepo+":1.22.5"] = dg("6")
	doc, _ := patch(t, root, []vulnscan.Finding{
		goFinding("GO-2026-0001", "example.com/lib", "v1.2.0", "v1.2.5"),
		npmFinding("GHSA-96hv-2xvq-fx4p", "ws", "8.20.0", "8.21.0"),
		osPkg,
		stdlibFinding("GO-2026-0002", "go1.22.3", "1.21.11", "1.22.5"),
	}, Options{Registry: reg}, nil, &VerifyOptions{
		Command: "task test:short", Source: VerifySourceTaskfile,
		Rescan:    func(context.Context, string) ([]vulnscan.Finding, error) { return nil, nil },
		ScanImage: func(context.Context, string) ([]vulnscan.Finding, error) { return nil, nil },
	})
	doc.Dir = "/repo"
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/vuln-patch.golden.json"
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("document differs from %s (FORGE_UPDATE_GOLDEN=1 rewrites it):\n%s", path, got)
	}
}
