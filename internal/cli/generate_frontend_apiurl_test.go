package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestDevAPIURL_ServiceKindDefaultsToDevPort pins the F2 fix: a service-kind
// project whose descriptor declares no service yet (services are
// descriptor-discovered, ports live in KCL) must still resolve DEV_API_URL to
// the canonical dev port instead of "" — otherwise connect.ts throws at Next
// prerender / vitest on a fresh frontend. projectDir is an empty temp dir (no
// proto descriptor), so the resolution falls through to the service-kind
// default, exactly the fresh-project case.
func TestDevAPIURL_ServiceKindDefaultsToDevPort(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{Name: "demo", Kind: config.ProjectKindService}

	got := devAPIURL(cfg, dir)
	want := "http://localhost:8080"
	if got != want {
		t.Errorf("devAPIURL(service-kind) = %q, want %q (must not be empty for a project with a dev backend)", got, want)
	}
}

// TestDevAPIURL_NonServiceKindEmpty pins that a CLI/library project with a
// stray frontend and no backend yields "" so connect.ts fails loud rather
// than pointing at a port nobody serves.
func TestDevAPIURL_NonServiceKindEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{Name: "tool", Kind: config.ProjectKindCLI}
	if got := devAPIURL(cfg, dir); got != "" {
		t.Errorf("devAPIURL(cli-kind, no backend) = %q, want \"\"", got)
	}
}
