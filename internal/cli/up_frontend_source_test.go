package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
)

// writeSourceOverride points a repo pin at a local directory via
// .forge/source-overrides.yaml, so a resolve needs no network and no git.
func writeSourceOverride(t *testing.T, projectDir, repo, dir string) {
	t.Helper()
	od := filepath.Join(projectDir, gitsource.OverridesDirName)
	if err := os.MkdirAll(od, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", od, err)
	}
	body := "sources:\n  " + repo + ": " + dir + "\n"
	if err := os.WriteFile(filepath.Join(od, gitsource.OverridesFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
}

// TestUpResolvesFrontendSources is the guard for the bug itself: `forge env
// up` must resolve cross-repo frontend sources before its frontend phase
// reads a Path.
//
// build (build.go) and deploy (deploy.go) both routed their frontend entities
// through resolveFrontendEntitySources; up did not. upFrontends uses fe.Path
// as the dev server's cmd.Dir, and a `source:`-declared frontend has no `path`
// in the render — so cmd.Dir was empty, npm inherited forge's own working
// directory, and the launch died on the PROJECT ROOT's missing package.json:
//
//	npm error path /…/control-plane/package.json
//	npm error enoent Could not read package.json
//
// which reads as a dependency problem rather than a wiring one. A GitSource
// frontend could be built and deployed but never dev-served.
//
// This is asserted against the SOURCE rather than by driving runUp, which
// needs a real KCL render, a cluster and a live npm. The invariant that broke
// is "all three lanes resolve through the same resolver" (stated in the doc
// comment on resolveFrontendEntitySources), and that is exactly what a
// source-level check can hold. A behavioral test of the resolver itself
// cannot: it passes whether or not up ever calls it — verified by reverting
// the fix, which left such a test green.
func TestUpResolvesFrontendSources(t *testing.T) {
	src, err := os.ReadFile("up.go")
	if err != nil {
		t.Fatalf("read up.go: %v", err)
	}
	if !strings.Contains(string(src), "resolveFrontendEntitySources(") {
		t.Error("up.go never calls resolveFrontendEntitySources — a `source:` frontend " +
			"has no Path in the render, so upFrontends would use an empty cmd.Dir and " +
			"npm would run in the project root (\"Could not read package.json\"). " +
			"build.go and deploy.go both resolve; up must too.")
	}
}

// TestResolveFrontendEntitySourcesRewritesPath pins what up now depends on:
// the resolver turns a pinned source into a directory npm can actually run
// in, and leaves an ordinary in-repo frontend alone.
//
// The override file keeps this hermetic — it resolves the pin to a local
// directory, so there is no fetch, no network and no git binary involved.
func TestResolveFrontendEntitySourcesRewritesPath(t *testing.T) {
	projectDir := t.TempDir()

	// The stand-in for the sibling repo, with the subdir the pin selects.
	repoDir := filepath.Join(projectDir, "sibling-reliant")
	webDir := filepath.Join(repoDir, "web")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	// The file whose absence produced the reported failure.
	if err := os.WriteFile(filepath.Join(webDir, "package.json"), []byte(`{"name":"reliant-web"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	writeSourceOverride(t, projectDir, "github.com/reliant-labs/reliant", repoDir)

	entities := &KCLEntities{Frontends: []FrontendEntity{
		{
			Name: "reliant-web",
			Type: "vite",
			// No Path — exactly what the KCL render projects for a
			// source-declared frontend.
			Source: &config.GitSource{
				Repo:   "github.com/reliant-labs/reliant",
				Ref:    "v1.7.9",
				Subdir: "web",
			},
		},
		{Name: "internal-console", Type: "nextjs", Path: "frontends/internal-console"},
	}}

	if err := resolveFrontendEntitySources(context.Background(), projectDir, entities); err != nil {
		t.Fatalf("resolveFrontendEntitySources: %v", err)
	}

	got := entities.Frontends[0].Path
	if got == "" {
		t.Fatal("source-declared frontend still has an empty Path — up would run npm in the project root")
	}
	if got != webDir {
		t.Errorf("Path = %q, want the resolved subdir %q", got, webDir)
	}
	// The whole point: cmd.Dir must be a directory npm can run in.
	if _, err := os.Stat(filepath.Join(got, "package.json")); err != nil {
		t.Errorf("resolved Path has no package.json (%v) — the dev server would fail the same way", err)
	}

	// A path-declared frontend is untouched, so the fix cannot disturb the
	// ordinary in-repo case.
	if p := entities.Frontends[1].Path; p != "frontends/internal-console" {
		t.Errorf("path-declared frontend Path = %q, want it unchanged", p)
	}
}
