package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

// The contract lane under --scope loads the scope, not the module: the
// analyzers read the requested packages from source and every dependency from
// export data, and the exclusion gate's multi-implementation search gets a
// load of the main module's packages from source (see
// runContractAnalysisInProcess). These tests pin both halves: a scoped run
// reports exactly what an unscoped run reports about the scoped package —
// including implementations that only a source load can see, and one in a
// package the scoped package never imports — and the analysis load really
// leaves dependencies to export data.

// writeScopedGateFixture writes a module whose internal/kv excludes itself
// from the contract system while owning an interface with three
// implementations: one in kv, an UNEXPORTED one in kvdisk (a dependency of
// kv, so export data would not carry it) and an unexported one in kvfake,
// which only kv's tests import, plus an unexported one in kvwire, which nothing
// kv reaches imports: it implements Getter for dependency injection, the way a
// wiring package does. kvdisk carries a bare marker of its own, which is a
// finding about kvdisk alone.
func writeScopedGateFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"internal/kv/kv.go": `// Package kv looks values up.
//
//forge:exclude-contract: a tiny lookup helper
package kv

import "example.com/m/internal/kvdisk"

type Getter interface{ Get(key string) string }

type Memory struct{}

func (Memory) Get(string) string { return "" }

func Warm() { kvdisk.Open() }
`,
		"internal/kv/kv_test.go": `package kv

import (
	"testing"

	"example.com/m/internal/kvfake"
)

func TestWarm(t *testing.T) { kvfake.Use(); Warm() }
`,
		"internal/kvdisk/kvdisk.go": `// Package kvdisk reads values from disk.
//
//forge:exclude-contract
package kvdisk

type disk struct{}

func (disk) Get(string) string { return "disk" }

func Open() { _ = disk{} }
`,
		"internal/kvwire/kvwire.go": `// Package kvwire wires a Getter in by dependency injection.
package kvwire

type wired struct{}

func (wired) Get(string) string { return "wired" }

func Provide() any { return wired{} }
`,
		"internal/kvfake/kvfake.go": `// Package kvfake fakes values for tests.
package kvfake

type fake struct{}

func (fake) Get(string) string { return "fake" }

func Use() { _ = fake{} }
`,
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// gateFindingsIn returns the exclusion-gate diagnostics whose file lies under
// pkgDir (module-relative).
func gateFindingsIn(ds []contractDiagnostic, pkgDir string) []contractDiagnostic {
	var out []contractDiagnostic
	for _, d := range ds {
		if strings.HasPrefix(d.Analyzer, "forge-exclude-contract") && filepath.ToSlash(filepath.Dir(d.Pos.Filename)) == pkgDir {
			out = append(out, d)
		}
	}
	return out
}

func TestContractLane_ScopedRunReportsWhatTheModuleRunDoes(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a module with go/packages twice; runs in task test")
	}
	t.Chdir(writeScopedGateFixture(t))
	ctx := context.Background()

	whole, err := runContractAnalysisInProcess(ctx, []string{"./..."}, nil, contract.ExcludeGateOptions{})
	if err != nil {
		t.Fatalf("unscoped contract analysis: %v", err)
	}
	scoped, err := runContractAnalysisInProcess(ctx, []string{"./internal/kv"}, nil, contract.ExcludeGateOptions{})
	if err != nil {
		t.Fatalf("scoped contract analysis: %v", err)
	}

	wantKV := gateFindingsIn(whole, "internal/kv")
	gotKV := gateFindingsIn(scoped, "internal/kv")
	if len(wantKV) != 1 || wantKV[0].Analyzer != "forge-exclude-contract-multi-impl" {
		t.Fatalf("fixture drifted: the unscoped run should refuse kv's marker for its multi-impl interface, got %+v", wantKV)
	}
	for _, impl := range []string{"Memory", "kvdisk.disk", "kvfake.fake", "kvwire.wired"} {
		if !strings.Contains(wantKV[0].Message, impl) {
			t.Errorf("unscoped run missed implementation %s: %s", impl, wantKV[0].Message)
		}
	}
	if len(gotKV) != len(wantKV) || gotKV[0] != wantKV[0] {
		t.Errorf("the scoped run judged kv differently from the unscoped one:\n scoped:   %+v\n unscoped: %+v", gotKV, wantKV)
	}

	// kvdisk is searched for implementations under the scope, never judged.
	if len(gateFindingsIn(whole, "internal/kvdisk")) == 0 {
		t.Fatal("fixture drifted: the unscoped run should refuse kvdisk's bare marker")
	}
	if fs := gateFindingsIn(scoped, "internal/kvdisk"); len(fs) != 0 {
		t.Errorf("a scoped run judged a package outside its scope: %+v", fs)
	}
}

// TestAnalysisLoadLeavesDependenciesToExportData pins the memory half: the
// contract lane's load parses the requested packages and nothing it merely
// imports. When it type-checked every dependency from source, a 14-package
// scope of reliant parsed 2,658 packages and peaked at 5.5 GiB.
func TestAnalysisLoadLeavesDependenciesToExportData(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a module with go/packages; runs in task test")
	}
	t.Chdir(writeScopedGateFixture(t))

	pkgs, err := loadPackagesForAnalysis(context.Background(), []string{"./internal/kv"}, analysisLoad{tests: true})
	if err != nil {
		t.Fatal(err)
	}
	var parsed, fromExport []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath == "unsafe" {
			return // go/packages fills unsafe in by hand, whatever the mode
		}
		if p.TypesInfo != nil {
			parsed = append(parsed, p.ID)
		} else {
			fromExport = append(fromExport, p.ID)
		}
	})
	for _, id := range parsed {
		if !strings.HasPrefix(id, "example.com/m/internal/kv") || strings.HasPrefix(id, "example.com/m/internal/kvdisk") || strings.HasPrefix(id, "example.com/m/internal/kvfake") {
			t.Errorf("%s was type-checked from source; only the requested package and its test variants should be", id)
		}
	}
	if len(fromExport) == 0 {
		t.Error("no dependency came from export data")
	}

	// The gate needs kvdisk, kvfake and kvwire from source, so the lane asks
	// for a second load of the whole main module.
	load, err := excludeGateFor(context.Background(), pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if load == nil {
		t.Fatal("excludeGateFor: kv declares a marker and its main-module dependencies came from export data, so the gate needs its own load")
	}
	if got, want := strings.Join(load.roots, ","), "example.com/m/internal/kv"; got != want {
		t.Errorf("gate roots = %s, want %s", got, want)
	}
	if got, want := strings.Join(load.search, ","), "./..."; got != want {
		t.Errorf("gate search = %s, want %s (a directory pattern, which stops at nested modules)", got, want)
	}
}

// writeFiles writes files (slash paths relative to dir) under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestContractLane_ExcludeGateStopsAtNestedModules is control-plane#2164: the
// exclusion gate's module search named the main module by its import path
// (`<module>/...`), which also matches a nested module the main module
// requires — control-plane's tools/registry-reliant, its own module with its
// own go.sum. Loaded under the main module, the nested module's dependencies
// had no go.sum entries and every `forge lint` failed with "packages failed to
// load: ... missing go.sum entry". Here the nested module tools/reg depends on
// example.com/dep, which only its own go.mod resolves (a replace the main module
// does not inherit); with module lookup off, loading tools/reg under the main
// module fails exactly as an unresolvable go.sum entry does.
func TestContractLane_ExcludeGateStopsAtNestedModules(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a module with go/packages; runs in task test")
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"m/go.mod": "module example.com/m\n\ngo 1.24\n\n" +
			"require example.com/m/tools/reg v0.0.0\n\n" +
			"replace example.com/m/tools/reg => ./tools/reg\n",
		"m/internal/kv/kv.go": `// Package kv looks values up.
//
//forge:exclude-contract: a tiny lookup helper
package kv

type Getter interface{ Get(key string) string }
`,
		"m/tools/reg/go.mod": "module example.com/m/tools/reg\n\ngo 1.24\n\n" +
			"require example.com/dep v1.0.0\n\n" +
			"replace example.com/dep => ../../../dep\n",
		"m/tools/reg/main.go": "package main\n\nimport \"example.com/dep\"\n\nfunc main() { dep.X() }\n",
		"dep/go.mod":          "module example.com/dep\n\ngo 1.24\n",
		"dep/dep.go":          "package dep\n\nfunc X() {}\n",
	})
	t.Chdir(filepath.Join(root, "m"))
	// Hermetic: nothing may be fetched, so reaching into the nested module's
	// dependencies is a load error rather than a network round trip.
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	ctx := context.Background()

	for _, paths := range [][]string{{"./..."}, {"./internal/kv"}} {
		if _, err := runContractAnalysisInProcess(ctx, paths, nil, contract.ExcludeGateOptions{}); err != nil {
			t.Errorf("contract analysis of %v reached into the nested module tools/reg: %v", paths, err)
		}
	}
	if b, err := os.ReadFile("go.mod"); err != nil || strings.Contains(string(b), "example.com/dep") {
		t.Errorf("the main module's go.mod gained the nested module's dependency (err %v):\n%s", err, b)
	}
	if _, err := os.Stat("go.sum"); err == nil {
		b, _ := os.ReadFile("go.sum")
		if strings.Contains(string(b), "example.com/dep") {
			t.Errorf("the main module's go.sum gained the nested module's dependency:\n%s", b)
		}
	}

	if got, want := modulePackagesPattern("/p/m", "/p/m/tools/reg"), "./tools/reg/..."; got != want {
		t.Errorf("modulePackagesPattern under cwd = %s, want %s", got, want)
	}
	if got := modulePackagesPattern("/p/m", "/elsewhere/mod"); got != filepath.ToSlash(filepath.Join("/elsewhere/mod", "...")) {
		t.Errorf("modulePackagesPattern outside cwd = %s, want an absolute directory pattern", got)
	}
}
