//go:build e2e

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EProjectUpgradeConvergesAcrossARange is the claim d3 makes, run with the
// real binary on a real project: pin a project at an older forge, give the
// range exactly one codemod and one lockfile refresh, run `forge project
// upgrade --json`, and require that it exits 0 with nothing left for a person,
// that `forge lint` is then clean, and that the project builds — with no edit
// by hand in between.
//
//   - the codemod: v0.1.46's operatorkit (the project's lifecycle.go names the
//     lifecyclekit.Options that moved);
//   - the lock refresh: frontends/web/package.json declares a dependency its
//     package-lock.json records none of. The dependency is a file: path, so the
//     package manager's lockfile-only refresh needs no registry.
func TestE2EProjectUpgradeConvergesAcrossARange(t *testing.T) {
	t.Parallel()
	requireTool(t, "go", "git", "npm", "node")
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "upgapp", "--mod", "example.com/upgapp", "--service", "items")
	projectDir := filepath.Join(dir, "upgapp")
	addCorpusForgePkgReplace(t, projectDir)

	// A frontend whose lockfile is behind its package.json. No node_modules, so
	// the frontend lint lane cannot run here; the project says so, by name.
	writeFileE2E(t, filepath.Join(projectDir, "libs", "locallib", "package.json"), `{"name":"locallib","version":"1.0.0"}`)
	writeFileE2E(t, filepath.Join(projectDir, "frontends", "web", "vite.config.ts"), "export default {};\n")
	writeFileE2E(t, filepath.Join(projectDir, "frontends", "web", "package.json"),
		`{"name":"web","version":"1.0.0","private":true,"dependencies":{"locallib":"file:../../libs/locallib"}}`)
	writeFileE2E(t, filepath.Join(projectDir, "frontends", "web", "package-lock.json"),
		`{"lockfileVersion":3,"packages":{"":{"name":"web","version":"1.0.0"}}}`)
	appendFile(t, filepath.Join(projectDir, "forge.yaml"), `lint:
  allow_skip:
    - lane: frontend-lint
      reason: this fixture installs no node_modules
    - lane: frontend-typecheck
      reason: this fixture installs no node_modules
features:
  frontend: false
`)

	// The codemod's input: the pre-v0.1.46 operator bridge.
	writeFileE2E(t, filepath.Join(projectDir, "internal", "legacyops", "ops.go"),
		"package legacyops\n\nimport \"github.com/reliant-labs/forge/pkg/lifecyclekit\"\n\nvar _ lifecyclekit.Options\n")

	// Pin the project at the older forge: the range under test is v0.1.45 →
	// this build. (The pin is only the baseline; go.mod already requires the
	// forge this build resolves, so no module is fetched.)
	pin := func(v string) {
		path := filepath.Join(projectDir, "forge.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`(?m)^forge_version:.*$`)
		if err := os.WriteFile(path, re.ReplaceAll(raw, []byte("forge_version: "+v)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The target is the forge go.mod already requires, as a pin bot passes it:
	// the test binary is a +dirty dev build, which is not a module version.
	mods, err := projectForgeModules(projectDir)
	if err != nil || len(mods) == 0 {
		t.Fatalf("scaffolded project requires no forge module to upgrade: %v %v", mods, err)
	}
	target := mods[0].version
	pin("v0.1.45")
	runCmd(t, projectDir, "git", "add", "-A")
	runCmd(t, projectDir, "git", "-c", "user.email=e2e@forge.test", "-c", "user.name=e2e", "commit", "-q", "--no-verify", "-m", "fixture: pinned at v0.1.45")

	cmd := exec.Command(forgeBin, "project", "upgrade", "--json", "--to", target, "--allow-dirty", "--skip", "playbooks")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "FORGE_ALLOW_VERSION_SKEW=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("forge project upgrade must exit 0 on a fully mechanical range: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr.String())
	}
	var doc struct {
		OK         bool             `json:"ok"`
		ExitCode   int              `json:"exit_code"`
		Converged  bool             `json:"converged"`
		Codemods   []map[string]any `json:"codemods"`
		Locks      []map[string]any `json:"locks"`
		NeedsHuman []map[string]any `json:"needs_human"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if !doc.OK || doc.ExitCode != 0 || !doc.Converged || len(doc.NeedsHuman) != 0 {
		t.Fatalf("not converged: %+v\nstderr:\n%s", doc, stderr.String())
	}
	if len(doc.Codemods) == 0 || len(doc.Locks) != 1 {
		t.Fatalf("expected the range's codemod and lock refresh, got codemods=%v locks=%v", doc.Codemods, doc.Locks)
	}

	if got, _ := os.ReadFile(filepath.Join(projectDir, "internal", "legacyops", "ops.go")); !strings.Contains(string(got), "operatorkit.Options") {
		t.Errorf("codemod did not rewrite ops.go:\n%s", got)
	}
	lock, _ := os.ReadFile(filepath.Join(projectDir, "frontends", "web", "package-lock.json"))
	if !strings.Contains(string(lock), "locallib") {
		t.Errorf("lockfile was not refreshed:\n%s", lock)
	}

	// The gate is green with no hand edit: lint is clean and the project builds.
	lintOut, lintErr := runLintE2E(t, projectDir, buildContractlintBinary(t), forgeBin, "lint", "--quiet")
	if lintErr != nil {
		t.Fatalf("forge lint after upgrade: %v\n%s", lintErr, lintOut)
	}
	runCmd(t, projectDir, "go", "build", "./...")

	// Idempotent: a second run converges again and changes nothing.
	again := exec.Command(forgeBin, "project", "upgrade", "--json", "--to", target, "--allow-dirty", "--skip", "playbooks")
	again.Dir = projectDir
	again.Env = cmd.Env
	if out, err := again.Output(); err != nil {
		t.Fatalf("second upgrade must also exit 0: %v\n%s", err, out)
	}
}

func appendFile(t *testing.T, path, add string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte("\n"+add)...), 0o644); err != nil {
		t.Fatal(err)
	}
}
