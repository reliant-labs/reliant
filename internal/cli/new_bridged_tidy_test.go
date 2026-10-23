package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
)

// TestRunNew_BridgedScaffoldIsTidyWithoutGenerate pins that a project
// scaffolded by a dev forge (go.work bridged to this checkout) is tidy against
// its OWN requirements the moment `forge project new` returns — in both the
// root and gen/ modules, with no `forge generate` in between.
//
// That is exactly what `forge env up`'s preflight checks (`go mod tidy -diff`
// with the module's own graph). `project new` used to run `go work sync`
// under the bridge instead of the bridged tidy the generate pipeline uses; sync
// never writes the `/go.mod` hashes a GOWORK=off tidy wants, so the root go.sum
// was stale on arrival and the very first `env up` told the user to tidy a
// project forge had just written.
func TestRunNew_BridgedScaffoldIsTidyWithoutGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a real project and runs `go mod tidy` in two modules (slow); runs in full mode and CI")
	}

	// Requires proto bootstrap during scaffold to write internal/app and gen/
	requireProtoToolchain(t)

	// Configure git identity for scaffold operations that may commit
	t.Setenv("GIT_AUTHOR_NAME", "forge-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "forge-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "forge-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "forge-test@example.com")

	buildinfo.SetDevBuild(true)
	t.Cleanup(buildinfo.ClearDevBuild)
	prevRoot := buildinfo.DevForgeRoot
	// The test binary runs in internal/cli, two levels below this checkout.
	forgeRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(forgeRoot, "cmd", "forge")); err != nil {
		t.Fatalf("%s is not the forge checkout: %v", forgeRoot, err)
	}
	buildinfo.DevForgeRoot = forgeRoot
	t.Cleanup(func() { buildinfo.DevForgeRoot = prevRoot })

	parent := t.TempDir()
	if err := runNew(t.Context(), "demo", parent, "example.com/demo", config.ProjectKindService,
		nil, nil, "", false, false, nil, "", true, "local", "", false, true, false, false, newFrontendOptions{}); err != nil {
		t.Fatalf("runNew: %v", err)
	}
	projectDir := filepath.Join(parent, "demo")

	if !devWorkspaceBridgesExternalModule(projectDir) {
		t.Fatalf("scaffold is not bridged to %s — this test would not exercise the bridged tidy path", buildinfo.DevForgeRoot)
	}

	for _, moduleDir := range []string{projectDir, filepath.Join(projectDir, "gen")} {
		cmd := exec.CommandContext(t.Context(), "go", "mod", "tidy", "-diff")
		cmd.Dir = moduleDir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			rel, _ := filepath.Rel(parent, moduleDir)
			// A GOWORK=off tidy resolves forge from the PUBLISHED module, so a
			// scaffold that imports a forge package the last release does not
			// have yet cannot be checked this way. That is the case the bridge
			// exists for, and tidyBridgedModule already treats it as "warn and
			// leave go.mod alone" (the go.work-aware build gates correctness).
			// Only that signature is excused; a stale go.mod/go.sum still fails.
			if strings.Contains(string(out), "does not contain package github.com/reliant-labs/forge/") {
				t.Logf("%s: tidy cannot be checked until the forge packages it imports are published (%s)", rel, strings.TrimSpace(string(out)))
				continue
			}
			t.Errorf("%s is not tidy right after `project new` (GOWORK=off go mod tidy -diff: %v):\n%s", rel, err, out)
		}
	}
}
