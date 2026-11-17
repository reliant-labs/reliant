package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestNoForgeYAMLSkipsProjectOnlyLanes pins `forge lint` in a repo that is not
// a forge project (a Node app with migrations): the lanes that need a Go
// module or forge's read-only role skip with a reason instead of failing,
// and migration safety still runs, because it judges any migration.
func TestNoForgeYAMLSkipsProjectOnlyLanes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "db", "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "migrations", "001_x.up.sql"), []byte("ALTER TABLE t DROP COLUMN c;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	rc := &lintRunCtx{cwd: dir}

	for _, lane := range []string{"golangci-lint", "contract linter", "db-grants lint"} {
		run, msg := findStep(t, lane).shouldRun(rc)
		if run {
			t.Errorf("%s: runs in a repo with no forge.yaml and no go.mod; it should skip", lane)
			continue
		}
		if !strings.Contains(msg, "no forge.yaml") {
			t.Errorf("%s: skip reason %q does not say why (no forge.yaml)", lane, msg)
		}
	}
	if run, _ := findStep(t, "migration safety lint").shouldRun(rc); !run {
		t.Error("migration safety lint must still run with no forge.yaml: it judges any migration")
	}

	// A Go module with no forge.yaml keeps its Go lanes.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"golangci-lint", "contract linter"} {
		if run, msg := findStep(t, lane).shouldRun(rc); !run {
			t.Errorf("%s: skipped in a Go module with no forge.yaml (%q); it should run", lane, msg)
		}
	}

	// A forge project keeps db-grants.
	if run, msg := findStep(t, "db-grants lint").shouldRun(&lintRunCtx{cwd: dir, cfg: &config.ProjectConfig{}}); !run {
		t.Errorf("db-grants lint: skipped in a forge project (%q); it should run", msg)
	}
}
