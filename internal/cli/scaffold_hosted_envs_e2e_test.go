//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EFreshScaffoldRendersEveryEnv is the user's first five minutes,
// through the real binary: `forge project new` with two services, then
// `forge env render` of every env it was born with, then `forge env new
// <env> --check` (no placeholder, renders, admissible to the control plane).
//
// Hosting is the default deploy target, so staging and prod must render and
// pass the check AS SCAFFOLDED. Before, they bound every workload to a
// cluster the author had to name first. Both services are served by one
// hosted workload, the binary's `server`, at the one origin a frontend calls.
func TestE2EFreshScaffoldRendersEveryEnv(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "shop", "--mod", "example.com/shop", "--service", "orders,billing")
	projectDir := filepath.Join(dir, "shop")
	addCorpusForgePkgReplace(t, projectDir)

	for _, env := range []string{"dev", "staging", "prod"} {
		out := runCmdOutput(t, projectDir, forgeBin, "env", "render", env)
		if strings.Contains(out, "REPLACE_ME") {
			t.Errorf("%s renders a placeholder:\n%s", env, out)
		}
		if env == "dev" {
			continue
		}
		// orders and billing run in ONE hosted workload, `api` (the binary's
		// `server`), so a browser reaches both at one origin.
		for _, want := range []string{"cluster:   hosted", "api (service)", "migrate (job)", "shop (database)"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s render lacks %q — the API, migrate and the database are hosted as scaffolded:\n%s", env, want, out)
			}
		}
		for _, unwanted := range []string{"orders (service)", "billing (service)"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("%s render hosts %q on its own, beside the API that already serves it:\n%s", env, unwanted, out)
			}
		}
		runCmd(t, projectDir, forgeBin, "env", "new", env, "--check")
	}

	// The CI scaffolded beside hosted envs is the hosted pipeline.
	if _, err := os.Stat(filepath.Join(projectDir, ".github", "workflows", "release.yml")); err != nil {
		t.Errorf("a project born with hosted envs has no release.yml: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".github", "workflows", "deploy.yml")); err == nil {
		t.Errorf("a project whose every deployed env is hosted got a cluster deploy.yml")
	}
}
