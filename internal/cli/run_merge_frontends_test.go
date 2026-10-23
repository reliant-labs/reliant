package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestMergeConfigFrontends_KCLPortWinsOverForgeYAML pins that a frontend the
// env's KCL declares keeps the KCL-resolved port, whatever forge.yaml says.
// The KCL port is the one `plugin.resolve_port` stepped off a busy 3000 for;
// forge.yaml's is a literal written at scaffold time. Letting the literal win
// is what made `forge env up` probe 3000 — held by another stack — and refuse.
func TestMergeConfigFrontends_KCLPortWinsOverForgeYAML(t *testing.T) {
	e := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: "frontends/web", Port: 3004, DevRunner: "npm"}}}
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs", Port: 3000}.WithDir("frontends/web"),
	}}
	mergeConfigFrontends(e, cfg)
	if len(e.Frontends) != 1 {
		t.Fatalf("want 1 frontend, got %+v", e.Frontends)
	}
	if e.Frontends[0].Port != 3004 {
		t.Errorf("KCL-resolved port must win: got %d, want 3004", e.Frontends[0].Port)
	}
	if e.Frontends[0].Type != "nextjs" {
		t.Errorf("type missing from KCL should be filled from forge.yaml, got %q", e.Frontends[0].Type)
	}
}

// TestMergeConfigFrontends_PerFrontendBridge pins that the bridge is per
// frontend, not all-or-nothing. A project whose KCL declares one frontend and
// whose forge.yaml declares a second used to launch only the first: the merge
// returned early as soon as KCL carried ANY frontend.
func TestMergeConfigFrontends_PerFrontendBridge(t *testing.T) {
	e := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: "frontends/web", Port: 3004}}}
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
		config.FrontendConfig{Name: "admin", Type: "vite-spa", Port: 3100}.WithDir("frontends/admin"),
	}}
	mergeConfigFrontends(e, cfg)
	if len(e.Frontends) != 2 {
		t.Fatalf("want web (KCL) + admin (forge.yaml), got %+v", e.Frontends)
	}
	admin := e.Frontends[1]
	if admin.Name != "admin" || admin.Port != 3100 || admin.Path != "frontends/admin" {
		t.Errorf("forge.yaml-only frontend bridged wrong: %+v", admin)
	}
}

// TestMergeConfigFrontends_DevRunnerFromForgeYAML pins that forge.yaml's
// frontends[].dev_runner reaches the launcher for a frontend adopted through
// forge.yaml alone — the path a pnpm app takes before it has KCL of its own.
func TestMergeConfigFrontends_DevRunnerFromForgeYAML(t *testing.T) {
	e := &KCLEntities{}
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs", DevRunner: "pnpm"}.WithDir("web"),
	}}
	mergeConfigFrontends(e, cfg)
	if len(e.Frontends) != 1 || e.Frontends[0].DevRunner != "pnpm" {
		t.Fatalf("dev_runner not bridged: %+v", e.Frontends)
	}
}
