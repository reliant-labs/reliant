package config

import (
	"path/filepath"
	"testing"
)

// TestFrontendDirParity is FrontendDirWithin's own test
// (frontend_inventory_test.go's TestFrontendDirWithinRejectsEscapes),
// ported onto the method that replaced it. It pins the three cases the
// retired helper covered — escape, empty-path convention, absolute
// in-repo path — so the consolidation is provably behavior-preserving on
// the rigor it inherited, not just on the ergonomics it gained.
func TestFrontendDirParity(t *testing.T) {
	root := t.TempDir()

	if _, ok := (FrontendConfig{Name: "reliant-web"}.WithDir("../reliant/web")).Dir(root); ok {
		t.Error("a path escaping the project root must not resolve as within it")
	}
	rel, ok := FrontendConfig{Name: "console"}.Dir(root)
	if !ok || rel != "frontends/console" {
		t.Errorf("empty path = (%q, %v), want the frontends/<name> convention", rel, ok)
	}
	abs := FrontendConfig{Name: "web"}.WithDir(filepath.Join(root, "apps", "web"))
	if rel, ok := abs.Dir(root); !ok || rel != "apps/web" {
		t.Errorf("absolute in-repo path = (%q, %v), want apps/web", rel, ok)
	}
}

// TestFrontendDirExcludesUnresolvedSource pins the ONE deliberate
// semantic difference between the two retired owners, resolved in favor
// of FrontendDirWithin's stricter answer.
//
// EffectivePath returned "frontends/<name>" for a frontend whose code
// comes from another repository — a directory that does not exist and
// never will, since its real location is wherever the pin materializes.
// Callers that only wanted a label were fine; the many that shelled into
// the result got a fabricated path and failed deep inside npm. Dir
// reports ok == false instead, so a caller must decide rather than
// inherit an invention.
//
// Once the pin IS resolved the path is rewritten (cli.resolveFrontendSources)
// and the frontend resolves normally — asserted here so the exclusion
// cannot be mistaken for "sourced frontends are never usable".
func TestFrontendDirExcludesUnresolvedSource(t *testing.T) {
	root := t.TempDir()
	sourced := FrontendConfig{
		Name:   "reliant-web",
		Source: &GitSource{Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3"},
	}

	if dir, ok := sourced.Dir(root); ok {
		t.Errorf("unresolved source frontend = (%q, true), want ok=false — "+
			"frontends/<name> is an invented directory for code that lives in another repo", dir)
	}

	resolved := sourced.WithDir(filepath.Join(root, ".forge", "sources", "reliant", "web"))
	dir, ok := resolved.Dir(root)
	if !ok || dir != ".forge/sources/reliant/web" {
		t.Errorf("resolved source frontend = (%q, %v), want the materialized dir", dir, ok)
	}
}

// TestFrontendDirUnnamed covers the remaining unusable shape: a frontend
// with neither a name nor a path has nothing for the convention to fill
// in, so it must not resolve to the project root itself. Without the
// guard filepath.Join("frontends", "") yields "frontends", and an
// emitter would generate into a directory shared by every frontend.
func TestFrontendDirUnnamed(t *testing.T) {
	root := t.TempDir()
	if dir, ok := (FrontendConfig{}).Dir(root); ok {
		t.Errorf("unnamed frontend = (%q, true), want ok=false", dir)
	}
}
