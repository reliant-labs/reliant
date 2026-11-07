package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memoryFor renders the framework memory for a minimal project: forge.yaml
// and a Next.js app at the root, plus a go.mod when goModule is set (the
// root app is then not the project's frontend: a Go project's root is its
// module).
func memoryFor(t *testing.T, goModule bool) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml":     "name: landing\n",
		"package.json":   `{"name":"landing","dependencies":{"next":"^16"}}`,
		"next.config.ts": "export default {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if goModule {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/landing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := RenderProjectMemory(root)
	if err != nil {
		t.Fatalf("RenderProjectMemory: %v", err)
	}
	return string(out)
}

// A static site (no go.mod, the app at the root) gets the frontend-only memory. Every session in
// it reads this first, so it must not frame forge as a Go/proto framework:
// an agent told "This is a Go project" and handed gopls, proto and migration
// rules reasons about a backend that does not exist.
func TestProjectMemoryForASiteIsFrontendOnly(t *testing.T) {
	memory := memoryFor(t, false)
	for _, banned := range []string{
		"This is a Go project",
		"gopls",
		"## Architecture in one screen",
		"## Where logic goes",
		"## Schema and proto conventions",
		"internal/handlers/",
		"db/migrations",
		"static sites included, needs billing",
	} {
		if strings.Contains(memory, banned) {
			t.Errorf("site memory contains %q", banned)
		}
	}
	for _, want := range []string{
		"static site",
		"env up dev",                             // run it
		"lint --static-export",                   // the export's constraints
		"env build prod --release",               // build once
		"env deploy prod",                        // ship it
		"domain add",                             // custom domains
		".forge/logs/",                           // frontend logs on disk
		"frontend/design",                        // design brief first
		"deploy/static-site",                     // the skill
		"One hosted static site per org is free", // the free tier …
		"counted across environments",            // … per env, precisely
		"exits 7",                                // beyond it: queued for billing
		"Adding an API later",                    // the path to a backend
		"When forge itself is wrong",             // shared sections survive
		"## Engineering disposition",
		"report-bug --severity high",             // main's reporting guidance survives
		"Never put credentials or file contents", // including its privacy boundary
	} {
		if !strings.Contains(memory, want) {
			t.Errorf("site memory lacks %q", want)
		}
	}
	if strings.Contains(memory, "{{") {
		t.Error("site memory carries an unrendered template action")
	}
}

// A Go project keeps the Go memory, and its billing line is the precise one:
// one free static site per org, everything else needs billing and queues.
func TestProjectMemoryForAGoProjectKeepsTheGoGuide(t *testing.T) {
	memory := memoryFor(t, true)
	for _, want := range []string{"This is a Go project", "## Architecture in one screen", "One hosted static site per org is free", "counted across environments", "exits 7"} {
		if !strings.Contains(memory, want) {
			t.Errorf("Go project memory lacks %q", want)
		}
	}
	for _, banned := range []string{"static sites included, needs billing", "## Adding an API later"} {
		if strings.Contains(memory, banned) {
			t.Errorf("Go project memory contains %q", banned)
		}
	}
}
