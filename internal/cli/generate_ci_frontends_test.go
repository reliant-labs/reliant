package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// writeKCLEnvs creates deploy/kcl/<env>/main.k under root so ListEnvs
// discovers each env. The file content is irrelevant — the rendered
// entities come from FORGE_KCL_RENDER_FIXTURE — but its PRESENCE is what
// makes the env exist.
func writeKCLEnvs(t *testing.T, root string, envs ...string) {
	t.Helper()
	for _, env := range envs {
		dir := filepath.Join(root, "deploy", "kcl", env)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte("manifests = []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// useKCLFixture points RenderKCL at a literal JSON document instead of
// shelling out to a real KCL toolchain.
func useKCLFixture(t *testing.T, doc string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "render.json")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", path)
}

// THE REGRESSION. A project whose forge.yaml carries NO `frontends:` key —
// the shape of every current project, since the key is retired and
// `forge generate` strips it — must still derive its frontend directory,
// from the KCL that actually declares the topology.
//
// Before the fix these builders read cfg.Frontends and got nothing, so
// HasFrontends was false and FrontendPath was "". That is not a visibly
// broken render: it silently drops the frontend jobs. Combined with the CI
// workflows being write-once scaffolds, it meant whatever setup-node block a
// long-ago forge had written could never be re-derived — so control-plane's
// admin-web → internal-console rename left `frontends/admin-web` frozen in
// e2e.yml and broke e2e CI with no way to regenerate out of it.
func TestDiscoverCIFrontends_DerivedFromKCLWithNoForgeYAMLKey(t *testing.T) {
	root := t.TempDir()
	writeKCLEnvs(t, root, "dev")
	useKCLFixture(t, `{
	  "frontends": [
	    {"name": "internal-console", "type": "nextjs", "path": "frontends/internal-console"}
	  ]
	}`)

	// Exactly control-plane's forge.yaml: no frontends key at all.
	cfg := &config.ProjectConfig{Name: "control-plane"}

	got := discoverCIFrontends(root, cfg)
	if len(got) != 1 {
		t.Fatalf("want 1 frontend discovered from KCL, got %d: %+v", len(got), got)
	}
	if got[0].Name != "internal-console" || got[0].DeclaredDir() != "frontends/internal-console" {
		t.Fatalf("frontend must come from KCL, got %+v", got[0])
	}

	// Every workflow that drives Node must see it. e2e.yml needs an opt-in
	// (or an e2e/ harness) to exist at all.
	cfg.Kind = config.ProjectKindService
	cfg.CI.E2E.Enabled = true
	files := ciWorkflowData(root, cfg)

	e2e := files[".github/workflows/e2e.yml"].(templates.E2EWorkflowData)
	if !e2e.HasFrontends {
		t.Fatal("e2e workflow must see the KCL-declared frontend (HasFrontends was false — the setup-node block would be dropped entirely)")
	}
	if e2e.FrontendPath != "frontends/internal-console" {
		t.Fatalf("e2e FrontendPath = %q, want frontends/internal-console", e2e.FrontendPath)
	}

	ci := files[".github/workflows/ci.yml"].(templates.CIWorkflowData)
	if len(ci.Frontends) != 1 || ci.Frontends[0].Path != "frontends/internal-console" {
		t.Fatalf("ci.yml frontends = %+v, want one entry at frontends/internal-console", ci.Frontends)
	}

	dep := files[".github/dependabot.yml"].(templates.DependabotData)
	if dep.FrontendName != "internal-console" {
		t.Fatalf("dependabot FrontendName = %q, want internal-console", dep.FrontendName)
	}
}

// ciWorkflowData is what `forge generate` renders each CI file with, keyed
// by destination.
func ciWorkflowData(root string, cfg *config.ProjectConfig) map[string]any {
	out := map[string]any{}
	for _, f := range generator.CIWorkflows(root, cfg, ciFrontends(root, cfg)) {
		out[f.Dest] = f.Data
	}
	return out
}

// A rename in the KCL must re-derive on the next generate. This is the
// property whose absence required PR #128's hand-edit: the same project,
// same forge.yaml, a renamed frontend, and the workflow data must follow.
func TestDiscoverCIFrontends_RenameReDerives(t *testing.T) {
	root := t.TempDir()
	writeKCLEnvs(t, root, "prod")
	useKCLFixture(t, `{
	  "frontends": [
	    {"name": "internal-console", "type": "nextjs", "path": "frontends/internal-console"}
	  ]
	}`)

	cfg := &config.ProjectConfig{Name: "control-plane", Kind: config.ProjectKindService}
	cfg.CI.E2E.Enabled = true
	e2e := ciWorkflowData(root, cfg)[".github/workflows/e2e.yml"].(templates.E2EWorkflowData)
	if p := e2e.FrontendPath; p != "frontends/internal-console" {
		t.Fatalf("after rename, e2e FrontendPath = %q, want the NEW name", p)
	}
}

// A frontend whose path points outside the repository (control-plane's
// reliant-web at ../reliant/web) has no directory in a CI checkout. Emitting
// npm steps for it would fail every run, so it must be filtered out — while
// the in-repo frontend beside it survives.
func TestDiscoverCIFrontends_SkipsOutOfRepoAndCrossRepoSources(t *testing.T) {
	root := t.TempDir()
	writeKCLEnvs(t, root, "prod")
	useKCLFixture(t, `{
	  "frontends": [
	    {"name": "reliant-web", "type": "vite", "path": "../reliant/web"},
	    {"name": "internal-console", "type": "nextjs", "path": "frontends/internal-console"},
	    {"name": "pinned", "type": "vite-spa", "path": "",
	     "source": {"repo": "github.com/reliant-labs/reliant", "ref": "v1.6.3", "subdir": "web"}}
	  ]
	}`)

	got := discoverCIFrontends(root, &config.ProjectConfig{Name: "control-plane"})
	if len(got) != 1 {
		t.Fatalf("want only the in-repo frontend, got %d: %+v", len(got), got)
	}
	if got[0].Name != "internal-console" {
		t.Fatalf("kept the wrong frontend: %+v", got[0])
	}
}

// CI workflows are not per-env: one ci.yml lints every frontend in the repo.
// A frontend declared in only some envs (control-plane's internal-console is
// absent from `e2e`, which declares `frontends = []`) must still appear, and
// a frontend in several envs must appear exactly once.
func TestDiscoverCIFrontends_UnionsEnvsAndDedupes(t *testing.T) {
	root := t.TempDir()
	writeKCLEnvs(t, root, "dev", "prod")
	useKCLFixture(t, `{
	  "frontends": [
	    {"name": "web", "type": "nextjs", "path": "frontends/web"},
	    {"name": "admin", "type": "nextjs", "path": "frontends/admin"}
	  ]
	}`)

	got := discoverCIFrontends(root, &config.ProjectConfig{Name: "demo"})
	if len(got) != 2 {
		t.Fatalf("want 2 deduped frontends across 2 envs, got %d: %+v", len(got), got)
	}
	// Sorted by name, so regenerating twice produces identical bytes.
	if got[0].Name != "admin" || got[1].Name != "web" {
		t.Fatalf("frontends must be name-sorted for deterministic output, got %+v", got)
	}
}

// A project with no deploy/kcl tree yet (freshly scaffolded, or one that
// predates the key's retirement and still carries a frontends: block) falls
// back to forge.yaml rather than losing its frontend jobs.
func TestDiscoverCIFrontends_FallsBackToConfigWhenNoKCL(t *testing.T) {
	root := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "legacy",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	got := discoverCIFrontends(root, cfg)
	if len(got) != 1 || got[0].DeclaredDir() != "frontends/web" {
		t.Fatalf("no-KCL project must fall back to forge.yaml, got %+v", got)
	}
}

// An empty KCL path means the conventional location, the same default the
// config loader applies to an in-repo frontend.
func TestDiscoverCIFrontends_EmptyPathDefaultsToConvention(t *testing.T) {
	root := t.TempDir()
	writeKCLEnvs(t, root, "dev")
	useKCLFixture(t, `{"frontends": [{"name": "web", "type": "nextjs", "path": ""}]}`)

	got := discoverCIFrontends(root, &config.ProjectConfig{Name: "demo"})
	if len(got) != 1 || got[0].DeclaredDir() != "frontends/web" {
		t.Fatalf("empty KCL path must default to frontends/<name>, got %+v", got)
	}
}
