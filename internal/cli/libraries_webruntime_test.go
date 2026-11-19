package cli

// The frontend half of `forge project libraries`.
//
// Every refusal the recursive-scan guard recorded in the last run was an
// agent hunting @reliantlabs/forge-web-runtime on the filesystem, because the
// verb that answers "where are forge's libraries and what is in them"
// covered forge/pkg and nothing else. These tests run against this repo's
// OWN web-runtime package — the same truth source the command reads at
// runtime — so a restructured barrel breaks them instead of silently
// emptying the inventory.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoWebRuntimeDir is the web-runtime package in this checkout.
func repoWebRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "web-runtime"))
	if err != nil {
		t.Fatalf("resolve web-runtime dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		t.Fatalf("web-runtime package not found at %s: %v", dir, err)
	}
	return dir
}

// webRuntimeBarrelIn resolves a package directory's public barrel the way the
// command does — by asking that package's manifest, never by remembering a
// layout.
func webRuntimeBarrelIn(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatalf("read %s manifest: %v", dir, err)
	}
	var m webRuntimeManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s manifest is not valid JSON: %v", dir, err)
	}
	path, err := webRuntimeBarrelPath(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// publishedWebRuntimeDir materializes the runtime in the shape a RELEASE
// consumes it: the manifest, declarations at the path that manifest declares,
// and NO src/ — the package stopped shipping sources when it started
// publishing built declarations.
//
// Every test here runs against that shape on purpose. The defect that made it
// necessary was structurally invisible to a checkout: the command read
// src/index.ts, a path that exists in this repo and in no installed copy, so
// off a released forge the verb printed the runtime with zero modules and no
// error while these tests stayed green.
//
// The bytes are the repo's own — the real package.json and the real barrel —
// laid out the published way. dist/ is a build output this repo does not
// track, so it cannot be the input to a Go test job that has no node; tsc's
// declaration emit re-emits the same `export … from` statements and preserves
// the JSDoc above them, which is exactly why the barrel carries JSDoc rather
// than line comments. TestWebRuntimePublishedBarrelMatchesTheEmit holds that
// equivalence to account wherever a dist/ has actually been built.
func publishedWebRuntimeDir(t *testing.T) string {
	t.Helper()
	repo := repoWebRuntimeDir(t)
	dir := t.TempDir()

	manifest, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		t.Fatalf("read the runtime manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	barrel, err := os.ReadFile(filepath.Join(repo, "src", "index.ts"))
	if err != nil {
		t.Fatalf("read the runtime barrel: %v", err)
	}
	dest := webRuntimeBarrelIn(t, dir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, barrel, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src")); !os.IsNotExist(err) {
		t.Fatalf("the released shape carries no src/, and this fixture must not either (stat err = %v)", err)
	}
	return dir
}

// TestReadWebRuntimeBarrel_EveryModuleIsRealSource asserts the inventory IS
// the package: every module the PUBLISHED barrel re-exports resolves to a
// real source module in the checkout, and every module contributes at least
// one symbol. A hand-listed index would rot exactly the way the
// forge-libraries skill's table did (it listed a pkg/dialects that never
// existed while omitting nine packages that did).
func TestReadWebRuntimeBarrel_EveryModuleIsRealSource(t *testing.T) {
	srcDir := filepath.Join(repoWebRuntimeDir(t), "src")
	mods, err := readWebRuntimeBarrel(webRuntimeBarrelIn(t, publishedWebRuntimeDir(t)))
	if err != nil {
		t.Fatalf("read the published barrel: %v", err)
	}
	for _, m := range mods {
		if len(m.Symbols) == 0 {
			t.Errorf("module %q contributes no symbols", m.Name)
		}
		found := false
		for _, ext := range []string{".ts", ".tsx"} {
			if _, err := os.Stat(filepath.Join(srcDir, m.Name+ext)); err == nil {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("barrel re-exports ./%s but no such source file exists under %s", m.Name, srcDir)
		}
	}
}

// TestWebRuntimePublishedBarrelMatchesTheEmit holds publishedWebRuntimeDir's one
// assumption to account: that the declarations tsc emits carry the same
// modules, symbols and synopses as the source barrel they are built from.
//
// It compares against a REAL dist/ wherever one exists — a runtime
// contributor's machine, and the frontend-runtime e2e, which npm-installs
// this package for real. dist/ is a gitignored build output, so a Go test job
// with no node has nothing to compare and says so rather than pretending.
func TestWebRuntimePublishedBarrelMatchesTheEmit(t *testing.T) {
	repo := repoWebRuntimeDir(t)
	emitted := webRuntimeBarrelIn(t, repo)
	if _, err := os.Stat(emitted); err != nil {
		t.Skipf("%s is not built (`npm run build` in %s), so the emit cannot be compared here; "+
			"the frontend-runtime e2e builds it for real", emitted, repo)
	}
	fromEmit, err := readWebRuntimeBarrel(emitted)
	if err != nil {
		t.Fatalf("read the emitted barrel: %v", err)
	}
	fromSource, err := readWebRuntimeBarrel(filepath.Join(repo, "src", "index.ts"))
	if err != nil {
		t.Fatalf("read the source barrel: %v", err)
	}
	if !reflect.DeepEqual(fromEmit, fromSource) {
		t.Errorf("the published inventory has drifted from the source barrel — the fixture every "+
			"other test here uses is no longer a faithful stand-in for what ships.\nemitted: %+v\nsource:  %+v",
			fromEmit, fromSource)
	}
}

// TestReadWebRuntimeBarrel_CarriesSymbolsAndPurpose pins the two things an
// agent actually needs from the index: the importable identifiers, and what
// the module is for. The purpose comes from the barrel's own comment above
// the group — the TS analogue of a Go package doc.
func TestReadWebRuntimeBarrel_CarriesSymbolsAndPurpose(t *testing.T) {
	mods, err := readWebRuntimeBarrel(webRuntimeBarrelIn(t, publishedWebRuntimeDir(t)))
	if err != nil {
		t.Fatalf("read the published barrel: %v", err)
	}
	byName := map[string]WebRuntimeModule{}
	for _, m := range mods {
		byName[m.Name] = m
	}

	// One entry per public concern the frontend-runtime skill documents.
	for mod, symbol := range map[string]string{
		"interceptors": "buildRuntimeInterceptors",
		"errors":       "normalizeError",
		"session":      "useSession",
		"resource":     "Resource",
		"providers":    "RuntimeShell",
	} {
		m, ok := byName[mod]
		if !ok {
			t.Errorf("module %q missing from the inventory", mod)
			continue
		}
		if !listsSymbol(m.Symbols, symbol) {
			t.Errorf("module %q does not list %q: %v", mod, symbol, m.Symbols)
		}
	}
	// `type Session` is exported as a type; an author imports `Session`.
	if m := byName["session"]; listsSymbol(m.Symbols, "type Session") || !listsSymbol(m.Symbols, "Session") {
		t.Errorf("type-only exports must be listed by identifier, not with the `type` qualifier: %v", m.Symbols)
	}
	if syn := byName["interceptors"].Synopsis; syn == "" {
		t.Error("the interceptor group carries a barrel comment; the synopsis must pick it up")
	}
}

func listsSymbol(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// newWebRuntimeProject builds a project tree with one frontend. Pass the
// package directory to install into its node_modules the way npm does, or ""
// for a project whose dependency is declared but not installed.
func newWebRuntimeProject(t *testing.T, declared, pkgDir string) string {
	t.Helper()
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "dashboard")
	if err := os.MkdirAll(fe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"dashboard","dependencies":{}}`
	if declared != "" {
		manifest = `{"name":"dashboard","dependencies":{"` + webRuntimePackage + `":"` + declared + `"}}`
	}
	if err := os.WriteFile(filepath.Join(fe, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if pkgDir != "" {
		scope := filepath.Join(fe, "node_modules", "@reliantlabs")
		if err := os.MkdirAll(scope, 0o755); err != nil {
			t.Fatal(err)
		}
		// npm materializes a file: dependency as a symlink; the command must
		// print the checkout, not the link, so an author can edit it.
		if err := os.Symlink(pkgDir, filepath.Join(scope, "forge-web-runtime")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBuildWebRuntimeSpec_ReadsWhatNpmInstalled(t *testing.T) {
	pkg := publishedWebRuntimeDir(t)
	root := newWebRuntimeProject(t, "file:../../../forge/web-runtime", pkg)
	spec, err := buildWebRuntimeSpec(root)
	if err != nil {
		t.Fatalf("buildWebRuntimeSpec: %v", err)
	}
	if spec == nil {
		t.Fatal("spec is nil for a project that has the package installed")
	}
	// An agent cannot act on a relative path.
	if !filepath.IsAbs(spec.Dir) {
		t.Errorf("Dir = %q, want an absolute path", spec.Dir)
	}
	wantDir, err := filepath.EvalSymlinks(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Dir != wantDir {
		t.Errorf("Dir = %q, want the symlink target %q", spec.Dir, wantDir)
	}
	if spec.Version == "" {
		t.Error("Version must come from the installed package's own package.json")
	}
	if spec.Declared != "file:../../../forge/web-runtime" {
		t.Errorf("Declared = %q — the dev bridge must be distinguishable from a published range", spec.Declared)
	}
	// Entry points are the exports map, minus the ./package.json resolver
	// plumbing nobody imports.
	if !listsSymbol(spec.EntryPoints, webRuntimePackage) ||
		!listsSymbol(spec.EntryPoints, webRuntimePackage+"/interceptors") {
		t.Errorf("EntryPoints = %v, want both published subpaths", spec.EntryPoints)
	}
	for _, ep := range spec.EntryPoints {
		if strings.HasSuffix(ep, "package.json") {
			t.Errorf("EntryPoints must not advertise %q as an import", ep)
		}
	}
	// The installed package is the RELEASED shape and carries no src/. The
	// inventory has to be complete off that, synopses included, which is the
	// whole reason the barrel path is asked of the manifest.
	if len(spec.Modules) == 0 {
		t.Fatal("Modules is empty — the inventory says the runtime exports nothing")
	}
	documented := 0
	for _, m := range spec.Modules {
		if m.Synopsis != "" {
			documented++
		}
	}
	if documented != len(spec.Modules) {
		t.Errorf("%d of %d modules carry no synopsis off a released install: %+v",
			len(spec.Modules)-documented, len(spec.Modules), spec.Modules)
	}
}

// TestBuildWebRuntimeSpec_UnreadableBarrelIsAnError mirrors the Go half's
// refusal to print a confident empty package list.
//
// The failure this forbids is not a crash: header, directory and entry
// points all print, with no symbols under them, and the reader concludes the
// runtime exports nothing and writes it all by hand. That is precisely how
// the src/index.ts path survived the package's move to published
// declarations — nothing anywhere was loud about an empty inventory.
func TestBuildWebRuntimeSpec_UnreadableBarrelIsAnError(t *testing.T) {
	// A package whose declarations were never built: the manifest is there
	// and names them, the file it names is not.
	pkg := t.TempDir()
	manifest, err := os.ReadFile(filepath.Join(repoWebRuntimeDir(t), "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := buildWebRuntimeSpec(newWebRuntimeProject(t, "^0.1.0", pkg))
	if err == nil {
		t.Fatalf("an unreadable barrel must fail loudly, got a spec instead: %+v", spec)
	}
	if spec != nil {
		t.Errorf("a failed read must not also hand back a spec: %+v", spec)
	}
	// The message has to name the package on disk, or the reader cannot tell
	// which install is broken.
	if real, rerr := filepath.EvalSymlinks(pkg); rerr == nil && !strings.Contains(err.Error(), real) {
		t.Errorf("error %q does not name the package at %s", err, real)
	}
}

// TestBuildWebRuntimeSpec_DeclaredButNotInstalled: a manifest that asks for
// the package while node_modules is missing is a normal state (a fresh
// clone). Say which manifest asked and what to run — do not report the
// runtime as absent.
func TestBuildWebRuntimeSpec_DeclaredButNotInstalled(t *testing.T) {
	root := newWebRuntimeProject(t, "^0.1.0", "")
	spec, err := buildWebRuntimeSpec(root)
	if err != nil {
		t.Fatalf("buildWebRuntimeSpec: %v", err)
	}
	if spec == nil || spec.Dir != "" {
		t.Fatalf("want a spec with no Dir, got %+v", spec)
	}
	// The manifest is named by its native path (backslashed on Windows).
	for _, want := range []string{filepath.FromSlash("frontends/dashboard/package.json"), "npm install"} {
		if !strings.Contains(spec.Reason, want) {
			t.Errorf("Reason %q must name %q", spec.Reason, want)
		}
	}
}

// TestBuildWebRuntimeSpec_NoFrontend: a worker-only or CLI project has no
// frontend at all. That is a shape, not a failure — the Go inventory must
// still print.
func TestBuildWebRuntimeSpec_NoFrontend(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := buildWebRuntimeSpec(root)
	if err != nil {
		t.Fatalf("a project with no frontend must not error: %v", err)
	}
	if spec != nil {
		t.Errorf("want nil spec for a frontend-less project, got %+v", spec)
	}
}

func TestWriteWebRuntime_NamesTheDirectoryAndTheSymbols(t *testing.T) {
	root := newWebRuntimeProject(t, "^0.1.0", publishedWebRuntimeDir(t))
	spec, err := buildWebRuntimeSpec(root)
	if err != nil {
		t.Fatalf("buildWebRuntimeSpec: %v", err)
	}
	var buf bytes.Buffer
	if err := writeWebRuntime(&buf, spec); err != nil {
		t.Fatalf("writeWebRuntime: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		webRuntimePackage,
		spec.Dir,
		"buildRuntimeInterceptors",
		"frontend-runtime", // the skill that answers "how do I compose it"
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteWebRuntime_NoFrontendSaysSo(t *testing.T) {
	var buf bytes.Buffer
	if err := writeWebRuntime(&buf, nil); err != nil {
		t.Fatalf("writeWebRuntime: %v", err)
	}
	if !strings.Contains(buf.String(), "No frontend in this project") {
		t.Errorf("a frontend-less project must be told so explicitly:\n%s", buf.String())
	}
}

// TestWriteLibraries_JSONCarriesTheWebRuntime: the --json consumer must be
// able to reach the frontend runtime too, or it goes back to the disk.
func TestWriteLibraries_JSONCarriesTheWebRuntime(t *testing.T) {
	root := newWebRuntimeProject(t, "^0.1.0", publishedWebRuntimeDir(t))
	rt, err := buildWebRuntimeSpec(root)
	if err != nil {
		t.Fatalf("buildWebRuntimeSpec: %v", err)
	}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, LibrariesSpec{Module: forgePkgModule, Dir: "/x", WebRuntime: rt}, true); err != nil {
		t.Fatalf("writeLibraries: %v", err)
	}
	var back LibrariesSpec
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back.WebRuntime == nil {
		t.Fatal("web_runtime absent from the JSON dump")
	}
	if back.WebRuntime.Dir != rt.Dir || len(back.WebRuntime.Modules) == 0 {
		t.Errorf("web_runtime did not round-trip: %+v", back.WebRuntime)
	}
}
