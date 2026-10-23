// File: internal/cli/lint/lint_feature_disabled_test.go
//
// Regression coverage for the second half of the defect in
// internal/config/derive_fill.go: an EXPLICITLY REQUESTED lint that cannot run
// exiting 0.
//
// `forge lint --migration-safety` against a project with the migrations
// feature off printed one line and returned nil. The config bug that put a
// project in that state is fixed at the loader, but the exit code is a
// separate failure and the more dangerous one: a CI job whose whole purpose is
// that flag goes green having checked nothing, and the green is
// indistinguishable from a clean tree. It is the same hazard migrationlint's
// own Result.Skipped exists to prevent one layer down — a check that looked at
// no files has not earned a pass.
//
// The distinction these tests pin: `forge lint` with no flags SKIPS a disabled
// lane (correct — the user asked for whatever applies), while `forge lint
// --migration-safety` REFUSES (the user named a lane that cannot run, and
// silently doing nothing answers a question they did not ask).
package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// lintProject writes a forge.yaml into a temp dir, chdirs there for the test,
// and registers the project-store loader the lint substrate reaches
// package-level.
//
// The loader is normally installed by internal/cli's init(), which this
// package cannot import (internal/cli blank-imports the groups — see
// bootstrap.go). Registering an equivalent here is what makes the lint
// entry points reachable from a test at all; it reads the same forge.yaml
// through the same config.LoadProject.
func lintProject(t *testing.T, forgeYAML string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg", "app"), 0o755); err != nil {
		t.Fatalf("mkdir pkg/app: %v", err)
	}
	t.Chdir(root)

	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		path := filepath.Join(root, "forge.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, ErrProjectConfigNotFound
		}
		cfg, err := config.LoadProject(data, path)
		if err != nil {
			return nil, err
		}
		return projectstore.New(cfg), nil
	})
	return root
}

// migrationsOffYAML is a service project with NO db/migrations: lintProject
// does not create one, so the migrations feature derives off. The feature is
// off because the repo says so, not because a key was set.
const migrationsOffYAML = `
name: demo
module_path: example.com/demo
forge_version: v0.0.0-test
`

// TestMigrationSafetyFlagRefusesWhenFeatureDisabled is the core assertion.
// Returning nil here is the driver's word for "I ran and found nothing".
func TestMigrationSafetyFlagRefusesWhenFeatureDisabled(t *testing.T) {
	lintProject(t, migrationsOffYAML)

	stop := captureStdout(t)
	err := runLintStructured(t.Context(), lintFlags{migrationSafety: true}, []string{"./..."})
	report := stop()
	if err == nil {
		t.Fatal("forge lint --migration-safety exited 0 with the migrations feature off — " +
			"a CI job running this flag would go green having checked nothing")
	}
	msg := report + err.Error()
	for _, want := range []string{"migrations", "db/migrations"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must name the flag that turned the lane off and how to turn it back on;\nwant %q in: %s", want, msg)
		}
	}
}

// TestContractFlagRefusesWhenFeatureDisabled covers the sibling lane, which
// had the identical `println + return nil` shape. Fixing only the one that was
// reported would leave the same trap one flag over.
func TestContractFlagRefusesWhenFeatureDisabled(t *testing.T) {
	// Contracts derive ON for every kind, so the only way a project reaches
	// "off" is a config assembled with it forced off (a scaffold that left it
	// out). Install a loader that returns exactly that.
	root := lintProject(t, `
name: demo
module_path: example.com/demo
forge_version: v0.0.0-test
`)
	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		cfg, err := config.LoadProject([]byte("name: demo\nmodule_path: example.com/demo\n"), filepath.Join(root, "forge.yaml"))
		if err != nil {
			return nil, err
		}
		cfg.Features = cfg.Features.With(config.FeatureContracts, false)
		return projectstore.New(cfg), nil
	})

	stop := captureStdout(t)
	err := runLintStructured(t.Context(), lintFlags{contract: true}, []string{"./..."})
	report := stop()
	if err == nil {
		t.Fatal("forge lint --contract exited 0 with the contracts feature off")
	}
	if !strings.Contains(report, "contracts feature is off") {
		t.Errorf("the report should name the contracts feature, got:\n%s", report)
	}
	if strings.Contains(report+err.Error(), "features.contracts") {
		t.Errorf("the remedy must not name a forge.yaml key that no longer exists: %s", report)
	}
}

// TestFullLintStillSkipsDisabledLane is the boundary, and the reason this is
// not simply "make disabled an error everywhere". An unflagged `forge lint` on
// a project that legitimately has no database must still SKIP the lane rather
// than fail: the user asked for whatever applies, and migration safety does
// not. Asserted at the step gate, because running the whole pipeline would
// need a real Go module on disk and would be testing golangci-lint instead.
func TestFullLintStillSkipsDisabledLane(t *testing.T) {
	cfg, err := config.LoadProject([]byte(migrationsOffYAML), filepath.Join(t.TempDir(), "forge.yaml"))
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	for _, step := range lintPipeline() {
		if step.name != "migration safety lint" {
			continue
		}
		run, skipMsg := step.shouldRun(&lintRunCtx{cfg: cfg})
		if run {
			t.Fatal("migration safety step should not run with the feature off")
		}
		if skipMsg == "" {
			t.Error("a skipped lane must say so; an empty message renders as a silently absent step")
		}
		return
	}
	t.Fatal("migration safety step not found in the pipeline")
}

// TestMigrationSafetyJSONReportsNotOKWhenFeatureDisabled is the same contract
// through --json, which is what CI actually parses. A report with "ok": true
// is read as a pass by every consumer, so the skip has to land as not-ok or
// the fix does not reach the machine-readable surface where it matters most.
func TestMigrationSafetyJSONReportsNotOKWhenFeatureDisabled(t *testing.T) {
	lintProject(t, migrationsOffYAML)

	report, err := collectLintJSON(t.Context(), lintFlags{migrationSafety: true, jsonOut: true}, []string{"./..."})
	if err != nil {
		t.Fatalf("collectLintJSON: %v", err)
	}
	if report == nil {
		t.Fatal("nil report")
	}
	if report.OK {
		t.Error(`--migration-safety --json reported "ok": true with the lane disabled — ` +
			"CI reads that as a clean migration-safety pass")
	}
}
