package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/config"
)

// A fresh scaffold makes gen/ its own module (gen/go.mod + `use gen`). An
// in-place adoption of a module that already owns gen/ (GenInRootModule)
// must keep it in the root module: no gen/go.mod, and a go.work that uses
// only the root. reliant is the case — control-plane imports its
// gen/reliant/v1 through reliant's ROOT module, so a gen/go.mod would break
// that build and leave the root's own `go mod tidy` unable to resolve gen/.
//
// A project without codegen has no gen/ at all. It once got go.mod's
// `require <mod>/gen` + `replace => ./gen` anyway, so every go command in it
// failed reading a gen/go.mod nothing wrote, and no go.work, so --link-forge
// had nothing to bridge. The go.mod require, the replace, gen/go.mod and
// go.work's `use gen` follow one predicate; go.work itself exists for every
// kind that imports forge.
func TestProjectGenerator_GenModuleLayout(t *testing.T) {
	for _, tc := range []struct {
		name            string
		genInRootModule bool
		noCodegen       bool
		kind            string
		wantGenGoMod    bool
		wantUses        []string // nil: no go.work
	}{
		{name: "fresh scaffold: gen is its own module", wantGenGoMod: true, wantUses: []string{".", "gen"}},
		{name: "root module owns gen", genInRootModule: true, wantGenGoMod: false, wantUses: []string{"."}},
		{name: "no codegen: no gen module, a workspace to bridge", noCodegen: true, wantGenGoMod: false, wantUses: []string{"."}},
		{name: "cli: gen module without codegen", kind: config.ProjectKindCLI, noCodegen: true, wantGenGoMod: true, wantUses: []string{".", "gen"}},
		{name: "library: imports no forge, no workspace", kind: config.ProjectKindLibrary, wantGenGoMod: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "proj")
			g := NewProjectGenerator("proj", root, "example.com/proj")
			if tc.kind != "" {
				g.Kind = tc.kind
				g.ApplyKindFeatureDefaults(tc.kind)
			}
			g.GenInRootModule = tc.genInRootModule
			if tc.noCodegen {
				g.Features = g.Features.With(config.FeatureCodegen, false)
			}
			if err := g.Generate(); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}

			_, err := os.Stat(filepath.Join(root, "gen", "go.mod"))
			if gotGenGoMod := err == nil; gotGenGoMod != tc.wantGenGoMod {
				t.Errorf("gen/go.mod exists = %v, want %v", gotGenGoMod, tc.wantGenGoMod)
			}

			// go.mod requires (and replaces) gen exactly when gen/go.mod
			// exists. The CLI's go.mod requires no gen at all: nothing in
			// its scaffold imports it.
			goModPath := filepath.Join(root, "go.mod")
			goMod, err := os.ReadFile(goModPath)
			if err != nil {
				t.Fatal(err)
			}
			mf, err := modfile.Parse(goModPath, goMod, nil)
			if err != nil {
				t.Fatalf("go.mod does not parse: %v\n%s", err, goMod)
			}
			genRequired := false
			for _, r := range mf.Require {
				genRequired = genRequired || r.Mod.Path == "example.com/proj/gen"
			}
			for _, r := range mf.Replace {
				genRequired = genRequired || r.Old.Path == "example.com/proj/gen"
			}
			if genRequired && !tc.wantGenGoMod {
				t.Errorf("go.mod requires or replaces example.com/proj/gen, which has no gen/go.mod:\n%s", goMod)
			}
			if !genRequired && tc.wantGenGoMod && tc.kind == "" {
				t.Errorf("go.mod does not require the gen module it ships:\n%s", goMod)
			}

			workPath := filepath.Join(root, "go.work")
			data, err := os.ReadFile(workPath)
			if tc.wantUses == nil {
				if err == nil {
					t.Errorf("go.work written for a project with nothing to bridge:\n%s", data)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wf, err := modfile.ParseWork(workPath, data, nil)
			if err != nil {
				t.Fatalf("go.work does not parse: %v\n%s", err, data)
			}
			var uses []string
			for _, u := range wf.Use {
				uses = append(uses, u.Path)
			}
			if strings.Join(uses, " ") != strings.Join(tc.wantUses, " ") {
				t.Errorf("go.work uses %q, want %q:\n%s", uses, tc.wantUses, data)
			}
		})
	}
}

// root.go is the same file in every shape: it calls rootCommands and keeps
// the registerCommand registry. A project without codegen gets neither from
// the codegen lane, so the scaffold births them — the projection of zero roots
// (roots_gen.go) and `metrics schema`, the conditional file every service
// registers — or the tree fails `go build` (undefined: rootCommands) and lint
// (registerCommand unused).
func TestProjectGenerator_NoCodegenCommandTreeIsComplete(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	g := NewProjectGenerator("proj", root, "example.com/proj")
	g.Features = g.Features.With(config.FeatureCodegen, false)
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	tree := filepath.Join(root, "cmd", "proj", "cmd")
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(tree, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(b)
	}
	if !strings.Contains(read("root.go"), "rootCommands(deps)") {
		t.Fatal("root.go no longer calls rootCommands; this test pins the shape-invariant root.go")
	}
	if !strings.Contains(read("roots_gen.go"), "func rootCommands(") {
		t.Error("roots_gen.go does not define the rootCommands root.go calls")
	}
	if !strings.Contains(read("metrics.go"), "registerCommand(") {
		t.Error("metrics.go does not register through root.go's registry")
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "metrics", "metrics.go")); err != nil {
		t.Errorf("internal/metrics/metrics.go (imported by metrics.go): %v", err)
	}
}

// The scaffold applies the feature graph the loader applies to every later
// load of the tree it writes: --disable codegen (a pin) takes the ORM and
// migrations with it, so no db/migrations is written for a project that
// derives them off, while the frontend the user named is still written — the
// frontend APP is what --frontend asked for, frontend CODEGEN is the feature
// that follows codegen.
func TestProjectGenerator_FollowsTheFeatureGraph(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	g := NewProjectGenerator("proj", root, "example.com/proj")
	g.FrontendName = "web"
	g.Features, _ = g.Features.Pin(config.FeatureCodegen, false)
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	f := g.Features
	if f.ORMEnabled() || f.MigrationsEnabled() || f.FrontendEnabled() {
		t.Errorf("orm=%v migrations=%v frontend=%v after Generate, want the graph to have taken them off with codegen",
			f.ORMEnabled(), f.MigrationsEnabled(), f.FrontendEnabled())
	}
	if _, err := os.Stat(filepath.Join(root, "db", "migrations")); !os.IsNotExist(err) {
		t.Errorf("db/migrations written for a project that derives migrations off (stat: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "frontends", "web", "package.json")); err != nil {
		t.Errorf("the named frontend was not written: %v", err)
	}
}
