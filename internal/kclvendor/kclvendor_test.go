package kclvendor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"
	forgekcl "github.com/reliant-labs/forge/kcl"
)

// useTempCache points the module cache at a per-test directory, so tests
// never touch a developer's real <UserCacheDir>/forge/kcl.
func useTempCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(SetCacheDirForTest(dir))
	return dir
}

func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestModuleDir_IsTheEmbeddedModuleInTheUserCache: the module lives OUTSIDE
// any project, under the cache root, and holds exactly the embedded files.
func TestModuleDir_IsTheEmbeddedModuleInTheUserCache(t *testing.T) {
	cache := useTempCache(t)
	dir, err := ModuleDir()
	if err != nil {
		t.Fatalf("ModuleDir: %v", err)
	}
	if !strings.HasPrefix(dir, cache+string(filepath.Separator)) {
		t.Fatalf("ModuleDir = %s, want a directory under the cache root %s", dir, cache)
	}
	files, err := embeddedModuleFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("embedded module is empty — this test would pass vacuously")
	}
	for _, p := range files {
		want, _ := fs.ReadFile(forgekcl.Module, p)
		if got := readFile(t, filepath.Join(dir, filepath.FromSlash(p))); got != string(want) {
			t.Errorf("%s in the cache differs from the embedded module", p)
		}
	}
	for _, absent := range []string{"tests", "example", "embed.go", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
			t.Errorf("%s is not part of the embedded module and must not be materialized", absent)
		}
	}

	// Memoized and stable: the same directory, and the arg names it.
	again, err := ModuleDir()
	if err != nil || again != dir {
		t.Fatalf("second ModuleDir = (%s, %v), want (%s, nil)", again, err, dir)
	}
	arg, err := ExternalPkgArg()
	if err != nil || arg != "forge="+dir {
		t.Fatalf("ExternalPkgArg = (%q, %v), want %q", arg, err, "forge="+dir)
	}
}

// TestModuleDir_RebuildsAPartialEntry: a directory a crashed process left
// without its completion marker is rebuilt, never trusted.
func TestModuleDir_RebuildsAPartialEntry(t *testing.T) {
	cache := useTempCache(t)
	hash, err := moduleHash()
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(cache, hash)
	writeFile(t, partial, "schema.k", "# truncated by a crash\n")

	dir, err := ModuleDir()
	if err != nil {
		t.Fatalf("ModuleDir: %v", err)
	}
	want, _ := fs.ReadFile(forgekcl.Module, "schema.k")
	if got := readFile(t, filepath.Join(dir, "schema.k")); got != string(want) {
		t.Fatal("a partial cache entry was served instead of rebuilt")
	}
}

// legacyVendoredKclMod is exactly what forge scaffolded and maintained
// before this change: the marker-delimited `.forge-kcl` block.
const legacyVendoredKclMod = `[package]
name = "proj-deploy"
edition = "v0.11.0"
version = "0.0.1"

# The ` + "`forge`" + ` KCL module ships the typed schemas.
[dependencies]
` + MarkerHeader + `
#
# ` + "`forge generate`" + ` materializes the KCL module embedded in the forge
# binary into ` + "`.forge-kcl/`" + ` at the project root and points this
# dependency at it by RELATIVE path. That copy travels with the repo, so
# containers, CI checkouts and other machines resolve the identical
# module — with no network, no git auth, and nothing to publish.
#
# Commit ` + "`.forge-kcl/`" + `. It refreshes on every ` + "`forge generate`" + `.
forge = { path = "../../.forge-kcl" }
`

const legacyForgeLock = `[dependencies]
  [dependencies.forge]
    name = "forge"
    full_name = "forge_0.1.0"
    version = "0.1.0"
`

// TestMigrateKclMod_RemovesEveryLegacyDeclaration: every shape an older forge
// wrote is removed — the vendored block with its marker comments, a bare
// relative path, an absolute host path, the unpublished git tag — and user
// content around it survives. A lock recording forge is stripped to what kpm
// itself writes for a dependency-free package.
func TestMigrateKclMod_RemovesEveryLegacyDeclaration(t *testing.T) {
	for name, tc := range map[string]struct {
		mod      string
		keep     []string
		gone     []string
		wantDeps string
	}{
		"vendored block with marker": {
			mod:  legacyVendoredKclMod,
			keep: []string{`name = "proj-deploy"`, "ships the typed schemas", "[dependencies]"},
			gone: []string{"forge =", MarkerHeader, "travels with the repo", "Commit `.forge-kcl/`"},
		},
		"unpublished git tag": {
			mod:  "[package]\nname = \"p\"\n\n[dependencies]\nforge = { git = \"https://github.com/reliant-labs/forge.git\", tag = \"kcl-v0.1.0\" }\n",
			keep: []string{`name = "p"`},
			gone: []string{"forge =", "kcl-v0.1.0"},
		},
		"absolute host path keeps the user's comment": {
			mod:  "[package]\nname = \"p\"\n\n[dependencies]\n# Local-dev override: resolve the module from the local clone.\nforge = { path = \"/Users/someone/src/forge/kcl\" }\nother = { path = \"../other\" }\n",
			keep: []string{"Local-dev override", `other = { path = "../other" }`},
			gone: []string{"forge ="},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			modPath := writeFile(t, dir, "deploy/kcl/kcl.mod", tc.mod)
			lockPath := writeFile(t, dir, "deploy/kcl/kcl.mod.lock", legacyForgeLock)

			res, err := MigrateKclMod(modPath)
			if err != nil {
				t.Fatalf("MigrateKclMod: %v", err)
			}
			if !res.Changed || res.Warning != "" {
				t.Fatalf("want Changed with no warning, got %+v", res)
			}
			got := readFile(t, modPath)
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Errorf("user content %q was removed:\n%s", k, got)
				}
			}
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survived migration:\n%s", g, got)
				}
			}
			if has, _ := HasForgeDep(modPath); has {
				t.Errorf("kcl.mod still declares forge after migration:\n%s", got)
			}
			if lock := readFile(t, lockPath); lock != "" {
				t.Errorf("lock = %q; a lock whose only entry was forge must become the empty lock kpm writes", lock)
			}

			// Idempotent: a migrated file is a byte-identical no-op.
			res, err = MigrateKclMod(modPath)
			if err != nil || res.Changed || res.Warning != "" {
				t.Fatalf("second MigrateKclMod = (%+v, %v), want a no-op", res, err)
			}
			if again := readFile(t, modPath); again != got {
				t.Errorf("second migration changed bytes")
			}
		})
	}
}

// TestMigrateKclMod_KeepsOtherLockEntries: only the forge package leaves the
// lock; a project's own KCL dependencies stay pinned.
func TestMigrateKclMod_KeepsOtherLockEntries(t *testing.T) {
	dir := t.TempDir()
	modPath := writeFile(t, dir, "deploy/kcl/kcl.mod", legacyVendoredKclMod)
	lock := legacyForgeLock + "  [dependencies.k8s]\n    name = \"k8s\"\n    version = \"1.31\"\n"
	lockPath := writeFile(t, dir, "deploy/kcl/kcl.mod.lock", lock)
	if _, err := MigrateKclMod(modPath); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, lockPath)
	if strings.Contains(got, "dependencies.forge") {
		t.Errorf("forge entry survived:\n%s", got)
	}
	if !strings.Contains(got, "[dependencies.k8s]") || !strings.Contains(got, `version = "1.31"`) {
		t.Errorf("the project's own dependency was dropped:\n%s", got)
	}
}

// TestMigrateKclMod_UnmanagedShapesWarnAndNoop: a spelling forge never
// wrote is not edited.
func TestMigrateKclMod_UnmanagedShapesWarnAndNoop(t *testing.T) {
	for name, mod := range map[string]string{
		"toml table":  "[package]\nname = \"p\"\n\n[dependencies.forge]\npath = \"../../.forge-kcl\"\n",
		"two entries": "[dependencies]\nforge = { path = \"a\" }\nforge = { path = \"b\" }\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			modPath := writeFile(t, dir, "deploy/kcl/kcl.mod", mod)
			res, err := MigrateKclMod(modPath)
			if err != nil {
				t.Fatal(err)
			}
			if res.Changed || res.Warning == "" {
				t.Fatalf("want an untouched file with a warning, got %+v", res)
			}
			if got := readFile(t, modPath); got != mod {
				t.Errorf("unmanaged kcl.mod was edited:\n%s", got)
			}
		})
	}
	// A missing file is silent.
	if res, err := MigrateKclMod(filepath.Join(t.TempDir(), "kcl.mod")); err != nil || res != (Result{}) {
		t.Errorf("missing file: (%+v, %v), want a silent no-op", res, err)
	}
}

// TestCheckKclMods_RefusesUnmigratedProjects: render refuses a project whose
// kcl.mod or lock still declares forge — kpm would resolve that declaration
// ahead of the binary's module — and names `forge generate` as the fix.
func TestCheckKclMods_RefusesUnmigratedProjects(t *testing.T) {
	t.Run("declared dep", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "deploy/kcl/kcl.mod", legacyVendoredKclMod)
		err := CheckKclMods(dir)
		var ue *UnmigratedError
		if !errors.As(err, &ue) {
			t.Fatalf("CheckKclMods = %v, want *UnmigratedError", err)
		}
		if !strings.Contains(err.Error(), "forge generate") || !strings.Contains(err.Error(), "deploy/kcl/kcl.mod") {
			t.Errorf("refusal must name the file and the fix:\n%v", err)
		}
	})
	t.Run("stale lock only", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "deploy/kcl/kcl.mod", "[package]\nname = \"p\"\n\n[dependencies]\n")
		writeFile(t, dir, "deploy/kcl/kcl.mod.lock", legacyForgeLock)
		if err := CheckKclMods(dir); err == nil {
			t.Fatal("a lock that still records forge must be refused: it shadows the binary's module")
		}
	})
	t.Run("migrated and fresh projects pass", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "deploy/kcl/kcl.mod", "[package]\nname = \"p\"\n\n[dependencies]\n")
		writeFile(t, dir, "deploy/kcl/kcl.mod.lock", "")
		if err := CheckKclMods(dir); err != nil {
			t.Fatalf("CheckKclMods on a migrated project = %v", err)
		}
		if err := CheckKclMods(t.TempDir()); err != nil {
			t.Fatalf("CheckKclMods with no kcl.mod = %v", err)
		}
	})
}

// TestRemoveLegacyVendorDir deletes the project-local copy and is a no-op
// when there is none.
func TestRemoveLegacyVendorDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".forge-kcl/schema.k", "old\n")
	removed, err := RemoveLegacyVendorDir(dir)
	if err != nil || !removed {
		t.Fatalf("RemoveLegacyVendorDir = (%v, %v), want (true, nil)", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".forge-kcl")); !os.IsNotExist(err) {
		t.Errorf(".forge-kcl/ still present")
	}
	if removed, err := RemoveLegacyVendorDir(dir); err != nil || removed {
		t.Errorf("second call = (%v, %v), want (false, nil)", removed, err)
	}
}

// TestMigration_JournalsForRollback: a `forge generate` that fails after the
// migration step must hand kcl.mod, its lock and the legacy .forge-kcl/ back
// exactly as it found them. Bare writes and removals are invisible to the
// rollback journal, so a failed run would report an unchanged tree while
// leaving the project half-migrated (the #271 failure, carried forward).
func TestMigration_JournalsForRollback(t *testing.T) {
	dir := t.TempDir()
	modPath := writeFile(t, dir, "deploy/kcl/kcl.mod", legacyVendoredKclMod)
	lockPath := writeFile(t, dir, "deploy/kcl/kcl.mod.lock", legacyForgeLock)
	schemaPath := writeFile(t, dir, ".forge-kcl/schema.k", "# pre-run schema\n")
	stampPath := writeFile(t, dir, ".forge-kcl/.forge-version", "v0.1.17\n")

	checksums.BeginRollbackJournal(dir)
	t.Cleanup(checksums.CommitRollback)
	if res, err := MigrateKclMod(modPath); err != nil || !res.Changed {
		t.Fatalf("MigrateKclMod = %+v, %v; want a rewrite", res, err)
	}
	if removed, err := RemoveLegacyVendorDir(dir); err != nil || !removed {
		t.Fatalf("RemoveLegacyVendorDir = %v, %v; want a removal", removed, err)
	}
	checksums.RestoreRollback(dir)

	for path, want := range map[string]string{
		modPath:    legacyVendoredKclMod,
		lockPath:   legacyForgeLock,
		schemaPath: "# pre-run schema\n",
		stampPath:  "v0.1.17\n",
	} {
		if got := readFile(t, path); got != want {
			t.Errorf("%s not restored by rollback:\ngot  %q\nwant %q", path, got, want)
		}
	}
}

// TestScaffoldTemplateDeclaresNoForgeDependency: the scaffold kcl.mod must
// not declare the module in any spelling, and must already be what
// migration produces, so `forge generate` never rewrites a fresh scaffold
// and the file is identical under every forge build.
func TestScaffoldTemplateDeclaresNoForgeDependency(t *testing.T) {
	rendered, err := templates.DeployTemplates().Render("kcl/kcl.mod.tmpl", struct{ ProjectName string }{"proj"})
	if err != nil {
		t.Fatalf("render kcl.mod.tmpl: %v", err)
	}
	for _, banned := range []string{"forge =", ".forge-kcl", "git ="} {
		if strings.Contains(string(rendered), banned) {
			t.Errorf("kcl.mod.tmpl must not contain %q — forge supplies the module from the binary:\n%s", banned, rendered)
		}
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "deploy/kcl/kcl.mod", string(rendered))
	res, err := MigrateKclMod(path)
	if err != nil || res.Changed || res.Warning != "" {
		t.Fatalf("migration must be a no-op on the scaffold template, got (%+v, %v)", res, err)
	}
	if err := CheckKclMods(dir); err != nil {
		t.Fatalf("the scaffold template must render without migration: %v", err)
	}
}
