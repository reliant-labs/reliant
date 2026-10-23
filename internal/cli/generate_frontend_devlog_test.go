package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// TestGenerateFrontendNav_RelocatesPrivateDevLogRoute pins the upgrade path
// for Next.js frontends scaffolded before the dev-log receiver moved out of
// the App Router's private-folder namespace. A project carrying
// src/app/__forge/log/route.ts never received a browser log line (the route
// 404s), so generate must move it to src/app/%5F_forge/log/route.ts — the
// spelling that actually serves /__forge/log — preserving any edits, and
// remove the now-empty private folder.
func TestGenerateFrontendNav_RelocatesPrivateDevLogRoute(t *testing.T) {
	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")
	legacy := filepath.Join(appDir, "__forge", "log", "route.ts")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	const edited = "// user edit survives the move\nexport async function POST() { return new Response(null, { status: 204 }); }\n"
	if err := os.WriteFile(legacy, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	if err := generateFrontendNav(cfg, nil, projectDir, nil, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}

	moved := filepath.Join(appDir, "%5F_forge", "log", "route.ts")
	got, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("expected route relocated to %s: %v", moved, err)
	}
	if string(got) != edited {
		t.Errorf("relocated route lost its content:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(appDir, "__forge")); !os.IsNotExist(err) {
		t.Errorf("private __forge folder should be gone after relocation, stat err = %v", err)
	}
}

// TestGenerateFrontendNav_DevLogRelocationLeavesBothAlone covers the
// conflict: when BOTH spellings exist the user has already fixed it by hand
// (or is mid-edit), and forge must not clobber either copy.
func TestGenerateFrontendNav_DevLogRelocationLeavesBothAlone(t *testing.T) {
	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")
	legacy := filepath.Join(appDir, "__forge", "log", "route.ts")
	current := filepath.Join(appDir, "%5F_forge", "log", "route.ts")
	for p, body := range map[string]string{legacy: "legacy\n", current: "current\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	if err := generateFrontendNav(cfg, nil, projectDir, nil, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}
	if b, _ := os.ReadFile(current); string(b) != "current\n" {
		t.Errorf("current route clobbered: %q", b)
	}
	if b, _ := os.ReadFile(legacy); string(b) != "legacy\n" {
		t.Errorf("legacy route touched despite conflict: %q", b)
	}
}

// TestGenerateFrontendNav_DevLogRelocationHonorsOptOut: a user who deleted the
// fixed route (its birth is on the ledger) opted out, and a stale __forge copy
// must not be moved back into its place.
func TestGenerateFrontendNav_DevLogRelocationHonorsOptOut(t *testing.T) {
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")
	legacy := filepath.Join(appDir, "__forge", "log", "route.ts")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksums.RecordScaffold(projectDir, filepath.Join("frontends", "web", "src", "app", "%5F_forge", "log", "route.ts"))

	cfg := &config.ProjectConfig{Name: "demo", Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}}}
	if err := generateFrontendNav(cfg, nil, projectDir, nil, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "%5F_forge", "log", "route.ts")); !os.IsNotExist(err) {
		t.Errorf("an opted-out route was resurrected from the stale copy (stat err = %v)", err)
	}
}
