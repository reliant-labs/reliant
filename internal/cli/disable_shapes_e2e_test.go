//go:build e2e

package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestE2EProjectNewDisableShapes holds every shape `forge project new
// --disable <feature>` makes to what any scaffold owes its project: straight
// after `project new`, `forge generate` runs clean, `go build ./...` passes and
// `forge lint` is green. A disabled feature is a project shape, not an
// unsupported corner.
//
// It was born red on codegen, which also made the frontend-only shape
// (TestE2EFrontendOnlyLifecycle's) unusable: `project new` already printed
// "initial code generation failed", because
//
//   - go.mod required and replaced a gen module (`<mod>/gen => ./gen`) that a
//     project without codegen never gets, so every go command failed on it;
//   - no go.work was written, so --link-forge had nothing to bridge and the
//     project resolved the published forge instead of the build generating it;
//   - root.go called rootCommands, which only the codegen lane defines (and
//     its registerCommand registry had no file left to use it).
//
// `project new` itself exits non-zero when that first generate fails, so the
// scaffold step fails the row before the explicit generate does.
//
// Each row adds every component its feature allows — a service and a
// frontend — so the scaffold's generate run exercises as much of the shape
// as it can. codegen and frontend rows drop the component they leave out:
// `--service` with `--disable codegen` and `--frontend` with `--disable
// frontend` are refused (TestRefuseDisabledComponents), not scaffolded.
//
// Frontend lanes are skipped (--skip-frontends): `project new`'s npm install
// is best-effort, and nothing a --disable changes reaches eslint or tsc.
func TestE2EProjectNewDisableShapes(t *testing.T) {
	t.Parallel()
	requireTool(t, "golangci-lint", "node", "npm")
	forgeBin := buildforgeBinary(t)
	contractlintDir := buildContractlintBinary(t)
	// The frontends' installs resolve @reliantlabs/forge-web-runtime over a
	// file: link into this checkout; build it once so the parallel installs
	// do not race its prepare script.
	prebuildWebRuntimeE2E(t)

	service := []string{"--service", "orders"}
	frontend := []string{"--frontend", "web"}
	both := slices.Concat(service, frontend)
	type shape struct {
		name       string
		disable    string
		components []string
	}
	rows := []shape{
		{name: "orm", disable: "orm", components: both},
		{name: "codegen", disable: "codegen", components: frontend},
		{name: "migrations", disable: "migrations", components: both},
		{name: "ci", disable: "ci", components: both},
		{name: "build", disable: "build", components: both},
		{name: "deploy", disable: "deploy", components: both},
		{name: "contracts", disable: "contracts", components: both},
		{name: "frontend", disable: "frontend", components: service},
		{name: "observability", disable: "observability", components: both},
		{name: "hot_reload", disable: "hot_reload", components: both},
		// TestE2EFrontendOnlyLifecycle's shape: a static site with no backend.
		{name: "frontend-only", disable: "orm,migrations,codegen,observability,ci,contracts",
			components: slices.Concat(frontend, []string{"--output", "static", "--routes", "none"})},
	}
	// Every feature --disable accepts has its row, so a feature added to the
	// list cannot ship without a working shape.
	for _, f := range disableableFeatures {
		if !slices.ContainsFunc(rows, func(r shape) bool { return r.disable == f }) {
			t.Errorf("--disable %s has no row: add one", f)
		}
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			args := append([]string{"project", "new", "shape", "--mod", "example.com/shape", "--disable", tc.disable, "--skip-tools"}, tc.components...)
			// project new exits non-zero when its own generate run fails.
			runCmd(t, dir, forgeBin, args...)
			projectDir := filepath.Join(dir, "shape")

			runCmdTimeout(t, projectDir, 10*time.Minute, forgeBin, "generate")
			runCmdTimeout(t, projectDir, 10*time.Minute, "go", "build", "./...")

			out, err := runLintE2E(t, projectDir, contractlintDir, forgeBin, "lint", "--no-fix", "--skip-frontends")
			if err != nil {
				t.Fatalf("forge lint must exit 0 on a fresh `--disable %s` project, got: %v\n%s", tc.disable, err, out)
			}
			if strings.Contains(out, "❌") {
				t.Errorf("forge lint printed error findings on a fresh `--disable %s` project:\n%s", tc.disable, out)
			}
			// A lane that did not run makes the exit-0 vacuous.
			for _, banned := range []string{"golangci-lint not found", "contractlint not available"} {
				if strings.Contains(out, banned) {
					t.Errorf("lint lane was skipped (%q) — the exit-0 assertion is vacuous:\n%s", banned, out)
				}
			}
			if !strings.Contains(out, "✓ golangci-lint") {
				t.Errorf("golangci-lint did not run:\n%s", out)
			}
		})
	}
}
