package cigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// modulesFixture is a root module ex.com/m replacing ex.com/sdk => ./sdk/go:
// root package app imports ex.com/sdk/client; in the nested module,
// client has tests and util (imported by client) too.
func modulesFixture(t *testing.T) (string, func(context.Context, string, []string) (io.Reader, error)) {
	t.Helper()
	return modulesFixtureWithRootTests(t, false)
}

func modulesFixtureWithRootTests(t *testing.T, rootTests bool) (string, func(context.Context, string, []string) (io.Reader, error)) {
	t.Helper()
	root := t.TempDir()
	for f, body := range map[string]string{
		"go.mod":        "module ex.com/m\n\ngo 1.22\n\nrequire ex.com/sdk v0.0.0\n\nreplace ex.com/sdk => ./sdk/go\n",
		"sdk/go/go.mod": "module ex.com/sdk\n\ngo 1.22\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sdk := filepath.Join(root, "sdk", "go")
	render := func(pkgs []listedPackage) io.Reader {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		for _, p := range pkgs {
			_ = enc.Encode(p)
		}
		return &b
	}
	list := func(_ context.Context, dir string, _ []string) (io.Reader, error) {
		if dir == sdk {
			pkgs := []listedPackage{
				{ImportPath: "ex.com/sdk/client", Name: "client", Dir: filepath.Join(sdk, "client"), Deps: []string{"ex.com/sdk/util"}},
				{ImportPath: "ex.com/sdk/util", Name: "util", Dir: filepath.Join(sdk, "util"), EmbedFiles: []string{"tmpl/x.txt"}},
				{ImportPath: "ex.com/sdk/client.test", Name: "main", Dir: filepath.Join(sdk, "client"), Deps: []string{"ex.com/sdk/client [ex.com/sdk/client.test]", "ex.com/sdk/util"}},
				{ImportPath: "ex.com/sdk/util [ex.com/sdk/util.test]", Name: "util", Dir: filepath.Join(sdk, "util"), ForTest: "ex.com/sdk/util", EmbedFiles: []string{"tmpl/x.txt"}, TestEmbedFiles: []string{"golden.txt"}},
				{ImportPath: "ex.com/sdk/util.test", Name: "main", Dir: filepath.Join(sdk, "util"), Deps: []string{"ex.com/sdk/util [ex.com/sdk/util.test]"}},
			}
			if rootTests {
				pkgs = append(pkgs,
					listedPackage{ImportPath: "ex.com/sdk", Name: "sdk", Dir: sdk},
					listedPackage{ImportPath: "ex.com/sdk.test", Name: "main", Dir: sdk, Deps: []string{"ex.com/sdk [ex.com/sdk.test]"}},
				)
			}
			return render(pkgs), nil
		}
		return render([]listedPackage{
			{ImportPath: "ex.com/sdk/client", Name: "client", Dir: filepath.Join(sdk, "client")},
			{ImportPath: "ex.com/sdk/util", Name: "util", Dir: filepath.Join(sdk, "util"), EmbedFiles: []string{"tmpl/x.txt"}},
			{ImportPath: "ex.com/m/app", Name: "app", Dir: filepath.Join(root, "app"), Deps: []string{"ex.com/sdk/client", "ex.com/sdk/util"}},
			{ImportPath: "ex.com/m/other", Name: "other", Dir: filepath.Join(root, "other")},
			{ImportPath: "ex.com/m/app.test", Name: "main", Dir: filepath.Join(root, "app"), Deps: []string{"ex.com/m/app [ex.com/m/app.test]", "ex.com/sdk/client", "ex.com/sdk/util"}},
			{ImportPath: "ex.com/m/other.test", Name: "main", Dir: filepath.Join(root, "other"), Deps: []string{"ex.com/m/other [ex.com/m/other.test]"}},
		}), nil
	}
	return root, list
}

func TestComputeAffected_ReplacedModules(t *testing.T) {
	root, list := modulesFixture(t)
	run := func(mode string, changed ...string) Affected {
		return ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: mode,
			Git: fakeGit{branch: "f", changed: changed}.run, GoList: list})
	}
	cases := []struct {
		name, mode string
		changed    []string
		full       bool
		pkgs       []string
		mod        *ModuleTests
	}{
		{name: "default uses the root graph without nested tests", changed: []string{"sdk/go/util/u.go"}, pkgs: []string{"ex.com/m/app"}},
		{name: "source maps into the root graph and the module", mode: "auto", changed: []string{"sdk/go/util/u.go"},
			pkgs: []string{"ex.com/m/app"}, mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/client", "ex.com/sdk/util"}}},
		{name: "a nested go:embed asset selects the root importers and the nested tests", mode: "auto", changed: []string{"sdk/go/util/tmpl/x.txt"},
			pkgs: []string{"ex.com/m/app"}, mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/client", "ex.com/sdk/util"}}},
		{name: "a nested test-only embed selects only the nested tests", mode: "auto", changed: []string{"sdk/go/util/golden.txt"},
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/client", "ex.com/sdk/util"}}},
		{name: "Go testdata owned by a nested package selects its tests", mode: "auto", changed: []string{"sdk/go/util/testdata/input.go"},
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/client", "ex.com/sdk/util"}}},
		{name: "unowned Go testdata reaches the root fallback", mode: "auto", changed: []string{"sdk/go/testdata/input.go"}, full: true,
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Full: true, Reason: "sdk/go/testdata/input.go is not Go source, an embedded file or testdata of a package in the module"}},
		{name: "unowned Go testdata with a test suffix reaches the root fallback", mode: "auto", changed: []string{"sdk/go/testdata/input_test.go"}, full: true,
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Full: true, Reason: "sdk/go/testdata/input_test.go is not Go source, an embedded file or testdata of a package in the module"}},
		{name: "a nested file nobody embeds keeps the root's full suite", mode: "auto", changed: []string{"sdk/go/notes.txt"}, full: true,
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Full: true, Reason: "sdk/go/notes.txt is not Go source, an embedded file or testdata of a package in the module"}},
		// B1: a _test.go file is compiled only into its own package's test
		// binary; no root test binary links it (go list -test never puts a
		// dependency's test files in another package's Deps).
		{name: "a nested _test.go change selects only the nested module's tests", mode: "auto", changed: []string{"sdk/go/client/c_test.go"},
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/client"}}},
		{name: "module go.sum: module full, root narrowed", mode: "auto", changed: []string{"sdk/go/go.sum"},
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Full: true, Reason: "sdk/go/go.sum changed"}},
		{name: "root go.sum too: root full", mode: "auto", changed: []string{"go.sum", "sdk/go/go.sum"}, full: true,
			mod: &ModuleTests{Dir: "sdk/go", Path: "ex.com/sdk", Full: true, Reason: "sdk/go/go.sum changed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := run(tc.mode, tc.changed...)
			if a.Full != tc.full {
				t.Fatalf("Full = %v (%s), want %v", a.Full, a.Reason, tc.full)
			}
			if !tc.full && !equal(a.Packages, tc.pkgs) {
				t.Errorf("Packages = %v, want %v", a.Packages, tc.pkgs)
			}
			switch {
			case tc.mod == nil && len(a.Modules) != 0:
				t.Errorf("Modules = %+v, want none", a.Modules)
			case tc.mod != nil && (len(a.Modules) != 1 || a.Modules[0].Dir != tc.mod.Dir || a.Modules[0].Path != tc.mod.Path ||
				a.Modules[0].Full != tc.mod.Full || a.Modules[0].Reason != tc.mod.Reason || !equal(a.Modules[0].Packages, tc.mod.Packages)):
				t.Errorf("Modules = %+v, want [%+v]", a.Modules, *tc.mod)
			}
		})
	}
}

func TestComputeAffected_ReplacedModulePathRule(t *testing.T) {
	root, list := modulesFixtureWithRootTests(t, true)
	for _, tc := range []struct {
		name, changed string
		except        []string
		rootTests     []string
		moduleTests   []string
	}{
		{"source", "sdk/go/util/u.go", nil, []string{"ex.com/m/app"}, []string{"ex.com/sdk", "ex.com/sdk/client", "ex.com/sdk/util"}},
		{"test only", "sdk/go/client/c_test.go", nil, nil, []string{"ex.com/sdk", "ex.com/sdk/client", "ex.com/sdk/util"}},
		{"embedded asset", "sdk/go/util/tmpl/x.txt", nil, []string{"ex.com/m/app"}, []string{"ex.com/sdk", "ex.com/sdk/client", "ex.com/sdk/util"}},
		{"excluded", "sdk/go/util/u.go", []string{"sdk/go/util/**"}, []string{"ex.com/m/app"}, []string{"ex.com/sdk/client", "ex.com/sdk/util"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: AffectedModulesAuto,
				Git: fakeGit{branch: "f", changed: []string{tc.changed}}.run, GoList: list,
				Rules: []config.CIGateAffectedRule{{Paths: []string{"sdk/go/**"}, Except: tc.except, Packages: []string{"./sdk/go/..."}}}})
			if a.Full || !equal(a.Packages, tc.rootTests) {
				t.Fatalf("root: full=%v (%s), packages=%v, want scoped %v", a.Full, a.Reason, a.Packages, tc.rootTests)
			}
			if len(a.Modules) != 1 || a.Modules[0].Full || !equal(a.Modules[0].Packages, tc.moduleTests) {
				t.Fatalf("nested modules = %+v, want scoped %v", a.Modules, tc.moduleTests)
			}
			rows := WithModuleTests([]Check{{Name: UnitTests}}, a)
			if len(rows) != 2 || rows[1].Dir != "sdk/go" || !equal(rows[1].Argv, append([]string{"go", "test"}, tc.moduleTests...)) {
				t.Fatalf("checks = %+v, want the selected tests run from sdk/go", rows)
			}
		})
	}
}

func TestComputeAffected_ReplacedModuleDifferentPath(t *testing.T) {
	root, list := modulesFixture(t)
	sdk := filepath.Join(root, "sdk", "go")
	if err := os.WriteFile(filepath.Join(sdk, "go.mod"), []byte("module ex.com/fork\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The root imports ex.com/sdk via its replacement; the nested module
	// lists itself under its own directive, ex.com/fork.
	goList := func(ctx context.Context, dir string, env []string) (io.Reader, error) {
		r, err := list(ctx, dir, env)
		if err != nil || dir != sdk {
			return r, err
		}
		data, err := io.ReadAll(r)
		return bytes.NewReader(bytes.ReplaceAll(data, []byte("ex.com/sdk"), []byte("ex.com/fork"))), err
	}
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: "auto",
		Git: fakeGit{branch: "f", changed: []string{"sdk/go/util/u.go"}}.run, GoList: goList})
	if a.Full || !equal(a.Packages, []string{"ex.com/m/app"}) {
		t.Fatalf("root selection = %+v, want affected app", a)
	}
	if len(a.Modules) != 1 || !equal(a.Modules[0].Packages, []string{"ex.com/fork/client", "ex.com/fork/util"}) {
		t.Fatalf("nested selection = %+v, want fork client and util", a.Modules)
	}
}

func TestComputeAffected_ReplacedModuleUnmappedSource(t *testing.T) {
	root, list := modulesFixture(t)
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: "auto",
		Git: fakeGit{branch: "f", changed: []string{"sdk/go/missing/m.go"}}.run, GoList: list})
	if !a.Full || a.Reason != "sdk/go/missing/m.go cannot be mapped to a package in the root graph" {
		t.Fatalf("selection = %+v, want conservative full root suite", a)
	}
	if len(a.Modules) != 1 || !a.Modules[0].Full {
		t.Fatalf("nested selection = %+v, want full nested suite", a.Modules)
	}
}

func TestWithModuleTests(t *testing.T) {
	checks := []Check{{Name: UnitTests}, {Name: "lint"}}
	if got := WithModuleTests(checks, Affected{}); len(got) != 2 {
		t.Fatalf("no modules: %d rows, want 2", len(got))
	}
	got := WithModuleTests(checks, Affected{Modules: []ModuleTests{
		{Dir: "sdk/go", Path: "ex.com/sdk", Packages: []string{"ex.com/sdk/client"}},
		{Dir: "tools/x", Path: "ex.com/x", Full: true, Reason: "go.mod changed"},
	}})
	if len(got) != 4 || got[1].Name != "test-mod-sdk-go" || got[2].Name != "test-mod-tools-x" {
		t.Fatalf("rows = %+v", got)
	}
	if got[1].Dir != "sdk/go" || !equal(got[1].Argv, []string{"go", "test", "ex.com/sdk/client"}) {
		t.Errorf("affected row = %+v", got[1])
	}
	if !equal(got[2].Argv, []string{"go", "test", "./..."}) {
		t.Errorf("full row = %+v", got[2])
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAnnotateModuleFiles(t *testing.T) {
	mods := []ModuleTests{{Dir: "sdk/go", Path: "ex.com/sdk"}}
	got := annotateModuleFiles([]FileReport{
		{Path: "sdk/go/client/c_test.go", Note: "unclaimed — runs the full suite"},
		{Path: "sdk/go/util/u.go", Package: "ex.com/sdk/util", Note: "package ex.com/sdk/util"},
		{Path: "other/o.go", Package: "ex.com/m/other", Note: "package ex.com/m/other"},
	}, nestedChanges{}, mods)
	want := []string{
		"nested module ex.com/sdk (test-mod-sdk-go)",
		"package ex.com/sdk/util; nested module ex.com/sdk (test-mod-sdk-go)",
		"package ex.com/m/other",
	}
	for i, w := range want {
		if got[i].Note != w {
			t.Errorf("%s: Note = %q, want %q", got[i].Path, got[i].Note, w)
		}
	}
}

func TestComputeAffected_NestedAssetOwnershipUnknown(t *testing.T) {
	root, list := modulesFixture(t)
	sdk := filepath.Join(root, "sdk", "go")
	failing := func(ctx context.Context, dir string, env []string) (io.Reader, error) {
		if dir == sdk {
			return nil, errors.New("go list: boom")
		}
		return list(ctx, dir, env)
	}
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Modules: AffectedModulesAuto,
		Git: fakeGit{branch: "f", changed: []string{"sdk/go/util/tmpl/x.txt"}}.run, GoList: failing})
	if !a.Full || !strings.Contains(a.Reason, "could not be read") {
		t.Fatalf("Full = %v (%s), want the root full because embed ownership is unknown", a.Full, a.Reason)
	}
}
