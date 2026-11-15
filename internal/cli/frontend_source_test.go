package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
)

// TestParseKCLEntities_FrontendGitSource pins the KCL → Go projection of
// a cross-repo frontend: `source` arrives typed, and `path` is absent
// (the KCL render projects it as null for a source-declared frontend).
func TestParseKCLEntities_FrontendGitSource(t *testing.T) {
	raw := `{
	  "frontends": [
	    {"name": "reliant-web", "type": "vite-spa", "path": null,
	     "source": {"repo": "github.com/reliant-labs/reliant", "ref": "v1.6.3", "subdir": "web"}},
	    {"name": "admin-web", "type": "nextjs", "path": "admin-web", "source": null}
	  ]
	}`
	entities, err := parseKCLEntities([]byte(raw))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if len(entities.Frontends) != 2 {
		t.Fatalf("frontends = %d, want 2", len(entities.Frontends))
	}

	sourced := entities.Frontends[0]
	if sourced.Source == nil {
		t.Fatal("source-declared frontend parsed with a nil Source")
	}
	if sourced.Source.Repo != "github.com/reliant-labs/reliant" || sourced.Source.Ref != "v1.6.3" || sourced.Source.Subdir != "web" {
		t.Errorf("Source = %+v, want the declared pin", *sourced.Source)
	}
	if sourced.Path != "" {
		t.Errorf("Path = %q, want empty for a source-declared frontend", sourced.Path)
	}

	// Regression guard: the path-declared shape is untouched.
	local := entities.Frontends[1]
	if local.Source != nil {
		t.Errorf("path-declared frontend parsed with Source = %+v, want nil", *local.Source)
	}
	if local.Path != "admin-web" {
		t.Errorf("Path = %q, want admin-web", local.Path)
	}
}

// TestFrontendEntity_SourceOmittedFromJSONWhenAbsent guards the additive
// contract from the other direction: a frontend with no source must not
// grow a `source` key, so anything comparing rendered JSON is unchanged.
func TestFrontendEntity_SourceOmittedFromJSONWhenAbsent(t *testing.T) {
	out, err := json.Marshal(FrontendEntity{Name: "web", Type: "nextjs", Path: "frontends/web"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "source") {
		t.Errorf("a source-less frontend marshalled with a source key: %s", out)
	}
}

// fakeSourceFetcher materializes a recognizable tree without a network.
type fakeSourceFetcher struct {
	calls int
	sub   string
}

func (f *fakeSourceFetcher) Fetch(_ context.Context, _ gitsource.Source, dst string) (string, error) {
	f.calls++
	dir := dst
	if f.sub != "" {
		dir = filepath.Join(dst, f.sub)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return "abc123", os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644)
}

// resolveWith runs the same resolution the build path does, but against
// an injected fetcher and a temp cache — no network, no shared state.
func resolveWith(t *testing.T, projectDir string, f gitsource.Fetcher, overrides map[string]string, apply func(*gitsource.Resolver) error) {
	t.Helper()
	r, err := gitsource.NewResolver(projectDir,
		gitsource.WithFetcher(f),
		gitsource.WithCacheRoot(filepath.Join(t.TempDir(), "cache")),
		gitsource.WithOverrides(overrides))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if err := apply(r); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

// TestResolveFrontendSources_RewritesPathToTheFetchedDir confirms the
// build-path contract: after resolution, fe.Path is a real directory the
// build can shell into, so the ~20 existing fe.Path readers need no
// changes.
func TestResolveFrontendSources_RewritesPathToTheFetchedDir(t *testing.T) {
	projectDir := t.TempDir()
	fetcher := &fakeSourceFetcher{sub: "web"}
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{{
		Name: "reliant-web", Type: "vite-spa",
		Source: &config.GitSource{Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web"},
	}}}

	resolveWith(t, projectDir, fetcher, map[string]string{}, func(r *gitsource.Resolver) error {
		res, err := r.Resolve(context.Background(), toGitSource(cfg.Frontends[0].Source))
		if err != nil {
			return err
		}
		cfg.Frontends[0] = cfg.Frontends[0].WithDir(res.Dir)
		return nil
	})

	got := cfg.Frontends[0].DeclaredDir()
	if got == "" {
		t.Fatal("Path was not rewritten")
	}
	if filepath.Base(got) != "web" {
		t.Errorf("Path = %q, want it to end in the declared subdir", got)
	}
	if _, err := os.Stat(filepath.Join(got, "package.json")); err != nil {
		t.Errorf("resolved Path is not a materialized frontend dir: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("fetches = %d, want 1", fetcher.calls)
	}
}

// TestResolveFrontendSources_NoSourcesIsANoOp is the byte-identical
// guarantee for every project that exists today: with no `source:`
// anywhere, resolution constructs nothing and changes nothing.
func TestResolveFrontendSources_NoSourcesIsANoOp(t *testing.T) {
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
		config.FrontendConfig{Name: "admin", Type: "nextjs"}.WithDir("admin-web"),
	}}
	before := append([]config.FrontendConfig(nil), cfg.Frontends...)

	if err := resolveFrontendSources(context.Background(), t.TempDir(), cfg); err != nil {
		t.Fatalf("resolveFrontendSources: %v", err)
	}
	for i := range before {
		if cfg.Frontends[i].DeclaredDir() != before[i].DeclaredDir() {
			t.Errorf("frontend %s Path changed from %q to %q",
				before[i].Name, before[i].DeclaredDir(), cfg.Frontends[i].DeclaredDir())
		}
	}
}

// TestResolveFrontendEntitySources_NoSourcesIsANoOp — the same guarantee
// on the deploy path, which reads KCL entities rather than the config.
func TestResolveFrontendEntitySources_NoSourcesIsANoOp(t *testing.T) {
	entities := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Type: "nextjs", Path: "frontends/web"},
	}}
	if err := resolveFrontendEntitySources(context.Background(), t.TempDir(), entities); err != nil {
		t.Fatalf("resolveFrontendEntitySources: %v", err)
	}
	if entities.Frontends[0].Path != "frontends/web" {
		t.Errorf("Path = %q, want it unchanged", entities.Frontends[0].Path)
	}
}

// TestResolveFrontendSources_LocalOverrideWins is the local-iteration
// clause end to end at the CLI layer: with an override in place the
// frontend resolves to the working copy and the pin is never fetched.
func TestResolveFrontendSources_LocalOverrideWins(t *testing.T) {
	projectDir := t.TempDir()
	working := filepath.Join(projectDir, "sibling-reliant", "web")
	if err := os.MkdirAll(working, 0o755); err != nil {
		t.Fatalf("create working copy: %v", err)
	}

	fetcher := &fakeSourceFetcher{sub: "web"}
	src := &config.GitSource{Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web"}
	var resolved string

	resolveWith(t, projectDir, fetcher,
		map[string]string{"github.com/reliant-labs/reliant": "sibling-reliant"},
		func(r *gitsource.Resolver) error {
			res, err := r.Resolve(context.Background(), toGitSource(src))
			if err != nil {
				return err
			}
			if !res.Overridden {
				t.Error("Resolution.Overridden = false, want true")
			}
			resolved = res.Dir
			return nil
		})

	if resolved != filepath.Clean(working) {
		t.Errorf("resolved to %q, want the override %q", resolved, working)
	}
	if fetcher.calls != 0 {
		t.Errorf("fetches = %d, want 0 — an override must not fetch the pin", fetcher.calls)
	}
}

// TestCheckDeclaredFrontends_SkipsGitSourceFrontends is the fix for the
// reported failure: a cross-repo frontend has no directory in this tree
// BY DESIGN, so the forge.yaml ↔ filesystem cross-check must not fail it.
// This is the check that made `forge build` impossible in a CI checkout.
func TestCheckDeclaredFrontends_SkipsGitSourceFrontends(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{{
		Name: "reliant-web", Type: "vite-spa",
		Source: &config.GitSource{Repo: "github.com/reliant-labs/reliant", Ref: "v1.6.3", Subdir: "web"},
	}}}

	if findings := checkDeclaredFrontends(projectDir, cfg); len(findings) != 0 {
		t.Errorf("a source-declared frontend was reported as a mismatch: %v", findings)
	}
}

// TestCheckDeclaredFrontends_StillFailsMissingPathFrontend — the skip
// above must not weaken the check for ordinary frontends, which is what
// it exists to catch.
func TestCheckDeclaredFrontends_StillFailsMissingPathFrontend(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
	}}

	findings := checkDeclaredFrontends(projectDir, cfg)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one missing-path report", findings)
	}
	if !strings.Contains(findings[0], "does not exist") {
		t.Errorf("finding = %q, want the missing-path message", findings[0])
	}
}
