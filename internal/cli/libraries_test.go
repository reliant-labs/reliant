package cli

// `forge project libraries` must answer, from the real package set, the
// question three separate agents once answered with a full-disk `find /`:
// where is forge/pkg, and what is in it.
//
// These tests run against this repo's own pkg/ tree — the same truth source
// the command reads at runtime, reached here by path because pkg/ is a
// SIBLING MODULE and there is nothing to import-reflect.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoPkgDir is the pkg/ library tree in this checkout. It is a directory in
// the forge module, not a module of its own, so there is no go.mod to look
// for — the directory's presence is the marker.
func repoPkgDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "pkg"))
	if err != nil {
		t.Fatalf("resolve pkg dir: %v", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("forge pkg/ library tree not found at %s: %v", dir, err)
	}
	return dir
}

// realPkgSubdirs is the set of subpackage names on disk — the truth the
// command's output must equal.
func realPkgSubdirs(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(repoPkgDir(t))
	if err != nil {
		t.Fatalf("read forge/pkg: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "testdata" {
			continue
		}
		out[n] = true
	}
	if len(out) == 0 {
		t.Fatal("forge/pkg has no subpackages — the truth source is wrong, so every assertion below would pass vacuously")
	}
	return out
}

// TestReadForgePkgPackages_EnumeratesEveryRealSubpackage is the property
// that makes this command worth trusting: the inventory IS the directory.
// Adding a package to forge/pkg must show up with no edit anywhere, and a
// package that disappears must stop being advertised.
//
// This is the guard the hand-written forge-libraries skill never had — its
// table listed a `pkg/dialects` that does not exist while omitting nine
// packages that do.
func TestReadForgePkgPackages_EnumeratesEveryRealSubpackage(t *testing.T) {
	t.Parallel()
	want := realPkgSubdirs(t)

	got, err := readForgePkgPackages(repoPkgDir(t))
	if err != nil {
		t.Fatalf("readForgePkgPackages: %v", err)
	}

	seen := map[string]bool{}
	for _, l := range got {
		seen[l.Name] = true
		if !want[l.Name] {
			t.Errorf("%s is advertised but is not a directory under forge/pkg", l.Name)
		}
		if l.ImportPath != forgePkgModule+"/"+l.Name {
			t.Errorf("%s: import path = %q, want %q", l.Name, l.ImportPath, forgePkgModule+"/"+l.Name)
		}
		if _, err := os.Stat(l.Dir); err != nil {
			t.Errorf("%s: advertised Dir %q is not readable: %v", l.Name, l.Dir, err)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("forge/pkg/%s exists on disk but the inventory omits it", name)
		}
	}
}

// TestReadForgePkgPackages_SortedAndNonEmpty pins the ordering contract and
// refuses a vacuous pass.
func TestReadForgePkgPackages_SortedAndNonEmpty(t *testing.T) {
	t.Parallel()
	got, err := readForgePkgPackages(repoPkgDir(t))
	if err != nil {
		t.Fatalf("readForgePkgPackages: %v", err)
	}
	if len(got) < 10 {
		t.Fatalf("only %d packages enumerated — the walk is broken", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Errorf("inventory is not sorted: %q before %q", got[i-1].Name, got[i].Name)
		}
	}
}

// TestForgePkgPackagesDocumentThemselves: every hand-written forge/pkg
// package must carry a package doc comment, because that comment IS the
// "purpose" column of `forge project libraries`. A package with no doc
// shows up in the inventory as an unexplained name, which is the state that
// sent agents to the filesystem in the first place.
//
// Generated packages are exempt, and the exemption is DERIVED from the
// "Code generated … DO NOT EDIT." banner the generator stamps — never from
// a hand-listed set, which would rot the same way the skill table did.
func TestForgePkgPackagesDocumentThemselves(t *testing.T) {
	t.Parallel()
	pkgs, err := readForgePkgPackages(repoPkgDir(t))
	if err != nil {
		t.Fatalf("readForgePkgPackages: %v", err)
	}

	checked := 0
	for _, l := range pkgs {
		if allFilesGenerated(t, l.Dir) {
			continue
		}
		checked++
		if strings.TrimSpace(l.Synopsis) == "" {
			t.Errorf("forge/pkg/%s has no package doc comment — `forge project libraries` "+
				"has nothing to say about it. Add one (conventionally in %s/doc.go).", l.Name, l.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no hand-written packages were checked — the generated-file detection is over-matching")
	}
}

// allFilesGenerated reports whether every non-test .go file in dir carries
// the standard generated-code banner.
func allFilesGenerated(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	found := false
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		found = true
		src, rerr := os.ReadFile(filepath.Join(dir, n))
		if rerr != nil {
			return false
		}
		// The convention is a first-line "Code generated ... DO NOT EDIT."
		head, _, _ := strings.Cut(string(src), "\n")
		if !strings.Contains(head, "Code generated") || !strings.Contains(head, "DO NOT EDIT") {
			return false
		}
	}
	return found
}

// TestStripPackagePrefix keeps the summary column readable: the godoc
// "Package foo …" lead-in is dropped, and the remainder keeps its original
// case so it reads as a continuation of the name rather than "Is the …".
func TestStripPackagePrefix(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"Package svcerr provides the canonical mapping.", "provides the canonical mapping."},
		{"Package tdd is a table-driven test library.", "is a table-driven test library."},
		{"Package cli — the command surface.", "the command surface."},
		{"Package orm - the data layer.", "the data layer."},
		{"Runs an already-composed forge server.", "Runs an already-composed forge server."},
		{"Package", "Package"},
	}
	for _, c := range cases {
		if got := stripPackagePrefix(c.in); got != c.want {
			t.Errorf("stripPackagePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWriteLibraries_TextNamesTheDirectoryAndEveryPackage: the resolved
// path is the single most valuable line of output — it is the literal
// answer to the `find /` those agents ran — so it must be present, absolute,
// and accompanied by the zero-file-reading way to go deeper.
func TestWriteLibraries_TextNamesTheDirectoryAndEveryPackage(t *testing.T) {
	t.Parallel()
	spec := LibrariesSpec{
		Module: forgePkgModule,
		Dir:    "/abs/path/to/pkg",
		Packages: []LibrarySpec{
			{Name: "svcerr", ImportPath: forgePkgModule + "/svcerr", Dir: "/abs/path/to/pkg/svcerr", Synopsis: "maps service errors."},
			{Name: "mystery", ImportPath: forgePkgModule + "/mystery", Dir: "/abs/path/to/pkg/mystery"},
		},
	}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, false); err != nil {
		t.Fatalf("writeLibraries: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"/abs/path/to/pkg",         // the answer to "where is it"
		"svcerr",                   // the package
		"maps service errors.",     // its purpose
		"go doc " + forgePkgModule, // how to go deeper without reading files
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output is missing %q:\n%s", want, out)
		}
	}
	// An undocumented package must be reported as such, not as a blank line
	// that reads like a rendering bug.
	if !strings.Contains(out, "no package doc") {
		t.Errorf("an undocumented package must be labelled, not left blank:\n%s", out)
	}
}

// TestWriteLibraries_JSONRoundTrips keeps the machine surface honest.
func TestWriteLibraries_JSONRoundTrips(t *testing.T) {
	t.Parallel()
	spec := LibrariesSpec{
		Module:   forgePkgModule,
		Version:  "v1.2.3",
		Dir:      "/abs/path/to/pkg",
		Packages: []LibrarySpec{{Name: "svcerr", ImportPath: forgePkgModule + "/svcerr", Dir: "/abs/path/to/pkg/svcerr", Synopsis: "maps service errors."}},
	}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, true); err != nil {
		t.Fatalf("writeLibraries json: %v", err)
	}
	var got LibrariesSpec
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.Dir != spec.Dir || got.Version != spec.Version || len(got.Packages) != 1 {
		t.Errorf("round-trip lost data: %+v", got)
	}
	if got.Packages[0].ImportPath != forgePkgModule+"/svcerr" {
		t.Errorf("import_path = %q", got.Packages[0].ImportPath)
	}
}

// TestBuildLibrariesSpec_ResolvesInThisRepo exercises the real `go list -m`
// resolution end to end. This repo's go.work uses ./pkg, so the resolved
// directory must be this checkout's own pkg/ — which is exactly the dev
// case an agent working on a forge project hits.
func TestBuildLibrariesSpec_ResolvesInThisRepo(t *testing.T) {
	spec, err := buildLibrariesSpec(context.Background())
	if err != nil {
		t.Fatalf("buildLibrariesSpec: %v", err)
	}
	if spec.Module != forgePkgModule {
		t.Errorf("module = %q, want %q", spec.Module, forgePkgModule)
	}
	if !filepath.IsAbs(spec.Dir) {
		t.Errorf("resolved Dir %q is not absolute — an agent cannot act on a relative path", spec.Dir)
	}
	if len(spec.Packages) == 0 {
		t.Fatal("resolved zero packages")
	}
	// svcerr is the package the dogfood agents were hunting; if the
	// resolution ever stops finding it, this command has stopped answering
	// the question it exists for.
	var found bool
	for _, l := range spec.Packages {
		if l.Name == "svcerr" {
			found = true
		}
	}
	if !found {
		t.Errorf("svcerr missing from the resolved inventory of %s", spec.Dir)
	}
}

// TestDetectLibraryDivergence_NoDivergenceWhenResolvedInsideModuleCache
// asserts the "not diverged" branch: a resolved directory that IS (or is
// under) GOMODCACHE is the normal, no-workspace steady state, and must
// report nil regardless of anything else — this is the case that must
// stay silent, so it is tested in isolation from any real go.mod
// resolution.
func TestDetectLibraryDivergence_NoDivergenceWhenResolvedInsideModuleCache(t *testing.T) {
	modCache, err := goEnv(context.Background(), "GOMODCACHE")
	if err != nil || modCache == "" {
		t.Fatalf("goEnv GOMODCACHE: %v (%q)", err, modCache)
	}
	resolvedDir := filepath.Join(modCache, "github.com/reliant-labs/forge/pkg@v0.0.3")

	got := detectLibraryDivergence(context.Background(), resolvedDir)
	if got != nil {
		t.Errorf("expected nil divergence for a resolved dir under GOMODCACHE, got %+v", got)
	}
}

// TestDetectLibraryDivergence_DetectsGoWorkOverrideInAConsumerProject is the
// positive case: a project that pins a published forge but bridges to a local
// checkout with a go.work. The resolved directory sits outside GOMODCACHE, so
// the divergence must be reported with the go.mod-pinned version and
// Source == "go.work".
//
// This used to run against forge's OWN go.work, which `use`d ./pkg. The
// single-module collapse deleted that file, and inside forge the question is
// meaningless anyway — you are not a project consuming forge, you are forge,
// and the main module has no version to diverge from. A fixture consumer is
// what the detector is actually for.
func TestDetectLibraryDivergence_DetectsGoWorkOverrideInAConsumerProject(t *testing.T) {
	dir := bridgedConsumerProject(t)

	got := detectLibraryDivergence(context.Background(), dir)
	if got == nil {
		t.Fatal("expected a divergence for a project whose go.work bridges to local forge, got nil")
	}
	if got.Source != "go.work" {
		t.Errorf("Source = %q, want \"go.work\"", got.Source)
	}
	if got.GoModVersion == "" {
		t.Error("GoModVersion is empty — the go.mod pin should still be readable with GOWORK=off")
	}
	wantResolved, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if got.ResolvedDir != wantResolved {
		t.Errorf("ResolvedDir = %q, want %q", got.ResolvedDir, wantResolved)
	}
}

// TestBuildLibrariesSpec_PopulatesDivergenceForABridgedProject asserts the
// field is wired all the way through buildLibrariesSpec, not just reachable
// via the helper directly.
func TestBuildLibrariesSpec_PopulatesDivergenceForABridgedProject(t *testing.T) {
	bridgedConsumerProject(t)

	spec, err := buildLibrariesSpec(context.Background())
	if err != nil {
		t.Fatalf("buildLibrariesSpec: %v", err)
	}
	if spec.Divergence == nil {
		t.Fatal("expected spec.Divergence to be populated for a go.work-bridged project")
	}
	if spec.Divergence.Source != "go.work" {
		t.Errorf("Source = %q, want \"go.work\"", spec.Divergence.Source)
	}
}

// bridgedConsumerProject builds a consumer module that REQUIRES a published
// forge but resolves it through a go.work `use` of a local forge stub, chdirs
// the test into it, and returns the pkg/ directory forge resolves to.
//
// The stub carries a pkg/ tree with one documented package, which is the
// minimum buildLibrariesSpec accepts (it refuses to report an empty library
// list rather than print a confident nothing).
func bridgedConsumerProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	forgeStub := filepath.Join(root, "forge")
	libDir := filepath.Join(forgeStub, "pkg", "svcerr")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(forgeStub, "go.mod"),
		"module github.com/reliant-labs/forge\n\ngo 1.24\n")
	writeFileT(t, filepath.Join(libDir, "doc.go"),
		"// Package svcerr maps errors to Connect codes.\npackage svcerr\n")

	project := filepath.Join(root, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(project, "go.mod"), strings.Join([]string{
		"module example.com/app",
		"",
		"go 1.24",
		"",
		"require github.com/reliant-labs/forge v0.1.15",
		"",
	}, "\n"))
	writeFileT(t, filepath.Join(project, "doc.go"), "package app\n")
	// The bridge: resolve forge from the stub instead of the proxy.
	writeFileT(t, filepath.Join(project, "go.work"), strings.Join([]string{
		"go 1.24",
		"",
		"use (",
		"\t.",
		"\t../forge",
		")",
		"",
	}, "\n"))

	t.Chdir(project)
	return filepath.Join(forgeStub, "pkg")
}

func writeFileT(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWriteLibraryDivergence_NilIsSilent asserts the "no new noise" rule:
// a project with no override must print exactly what it printed before
// this feature existed.
func TestWriteLibraryDivergence_NilIsSilent(t *testing.T) {
	var buf bytes.Buffer
	if err := writeLibraryDivergence(&buf, forgePkgModule, nil); err != nil {
		t.Fatalf("writeLibraryDivergence: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for nil divergence, got:\n%s", buf.String())
	}
}

// TestWriteLibraryDivergence_NamesResolvedDirAndGoModVersion asserts the
// warning names both facts an agent needs: where the build actually
// resolves to, and what go.mod alone (what an offline `go doc` reads)
// says.
func TestWriteLibraryDivergence_NamesResolvedDirAndGoModVersion(t *testing.T) {
	div := &LibraryDivergence{
		GoModVersion: "v0.0.3",
		ResolvedDir:  "/Users/dev/src/forge/pkg",
		Source:       "go.work",
	}
	var buf bytes.Buffer
	if err := writeLibraryDivergence(&buf, forgePkgModule, div); err != nil {
		t.Fatalf("writeLibraryDivergence: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"/Users/dev/src/forge/pkg", "v0.0.3", "go.work", "go doc " + forgePkgModule} {
		if !strings.Contains(out, want) {
			t.Errorf("divergence output is missing %q:\n%s", want, out)
		}
	}
}

// TestWriteLibraries_JSONOmitsDivergenceWhenNil keeps the JSON surface
// as quiet as the text one: a normal project (no override) must not
// grow a "divergence" key at all.
func TestWriteLibraries_JSONOmitsDivergenceWhenNil(t *testing.T) {
	spec := LibrariesSpec{Module: forgePkgModule, Dir: "/abs/path/to/pkg"}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, true); err != nil {
		t.Fatalf("writeLibraries json: %v", err)
	}
	if strings.Contains(buf.String(), "divergence") {
		t.Errorf("expected no \"divergence\" key when Divergence is nil, got:\n%s", buf.String())
	}
}

// TestWriteLibraries_JSONCarriesDivergenceStructuredFields asserts the
// --json surface exposes the divergence as structured fields, per the
// task's requirement, not just folded into the text banner.
func TestWriteLibraries_JSONCarriesDivergenceStructuredFields(t *testing.T) {
	spec := LibrariesSpec{
		Module: forgePkgModule,
		Dir:    "/abs/path/to/pkg",
		Divergence: &LibraryDivergence{
			GoModVersion: "v0.0.3",
			ResolvedDir:  "/abs/path/to/pkg",
			Source:       "go.work",
		},
	}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, true); err != nil {
		t.Fatalf("writeLibraries json: %v", err)
	}
	var got LibrariesSpec
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.Divergence == nil {
		t.Fatal("expected divergence to round-trip through JSON")
	}
	if got.Divergence.GoModVersion != "v0.0.3" || got.Divergence.Source != "go.work" {
		t.Errorf("divergence round-trip lost data: %+v", got.Divergence)
	}
}
