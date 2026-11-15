package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func writeFloorFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The cut records the project's own declarations from disk: a
// breaks-rollback migration (schema), a declaration file (api), and a forge
// baseline's adopted versions.
func TestCaptureRecordsTheProjectsFloorDeclarations(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "db", "migrations")
	writeFloorFile(t, filepath.Join(mig, "20260101000000_a.up.sql"), "-- forge:backward-compatible — additive\nSELECT 1;\n")
	writeFloorFile(t, filepath.Join(mig, "20260102000000_drop.up.sql"), "-- forge:breaks-rollback the 2026-09 release stopped reading plans.legacy_tier\nALTER TABLE plans DROP COLUMN legacy_tier;\n")
	writeFloorFile(t, filepath.Join(mig, "20260103000000_baseline.up.sql"), "-- forge:adopts forge 20251201000000 20251202000000\nSELECT 1;\n")
	writeFloorFile(t, filepath.Join(dir, release.FloorDeclarationsDir, "webhooks-v2.yaml"), "kind: api\nreason: v1 webhook receivers were removed\n")
	writeFloorFile(t, filepath.Join(dir, release.FloorDeclarationsDir, "README.md"), "not a declaration")

	m, notes, err := captureReleaseMigrations(dir, release.Provenance{Repo: "github.com/acme/app", Dirty: true}, nil)
	if err != nil || len(notes) != 0 {
		t.Fatalf("capture: %v, notes %v", err, notes)
	}
	if !m.DeclarationsRecorded {
		t.Fatal("a cut by this forge records that it read declarations")
	}
	want := []release.FloorDeclaration{
		{Source: "github.com/acme/app/db/migrations", ID: "20260102000000_drop.up.sql", Kind: release.DeclarationSchema, Reason: "the 2026-09 release stopped reading plans.legacy_tier"},
		{Source: "github.com/acme/app/deploy/rollback-floors", ID: "webhooks-v2", Kind: release.DeclarationAPI, Reason: "v1 webhook receivers were removed"},
	}
	if len(m.Declarations) != len(want) {
		t.Fatalf("declarations = %+v", m.Declarations)
	}
	for i := range want {
		if m.Declarations[i] != want[i] {
			t.Errorf("declaration %d = %+v, want %+v", i, m.Declarations[i], want[i])
		}
	}
	if got := m.Sets[0].Adopts; len(got) != 2 || got[0] != "20251201000000" || got[1] != "20251202000000" {
		t.Errorf("adopts = %v", got)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("captured manifest does not validate: %v", err)
	}

	// A project with declarations and no migrations still records them.
	only := t.TempDir()
	writeFloorFile(t, filepath.Join(only, release.FloorDeclarationsDir, "flag-x.yaml"), "kind: feature\nreason: x state\n")
	m2, _, err := captureReleaseMigrations(only, release.Provenance{Repo: "github.com/acme/site"}, nil)
	if err != nil || m2 == nil || len(m2.Sets) != 0 || len(m2.Declarations) != 1 || m2.Declarations[0].Kind != release.DeclarationFeature {
		t.Fatalf("declarations-only project: %+v, %v", m2, err)
	}
}

// A malformed declaration refuses the cut: skipping it would record a
// release that silently lacks the floor its author asked for.
func TestCaptureRefusesAMalformedDeclaration(t *testing.T) {
	dir := t.TempDir()
	writeFloorFile(t, filepath.Join(dir, release.FloorDeclarationsDir, "bad.yaml"), "kind: api\n")
	if _, _, err := captureReleaseMigrations(dir, release.Provenance{}, nil); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("want a refusal naming the missing reason, got %v", err)
	}
}

// A sibling's declarations are read from git at the commit the build
// recorded, not from its working tree.
func TestCaptureReadsASiblingsDeclarationsAtItsCommit(t *testing.T) {
	testkit.Slow(t, "execs git over a temp repository")
	root := t.TempDir()
	sib := filepath.Join(root, "reliant")
	proj := filepath.Join(root, "cp")
	if err := os.MkdirAll(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	migGit(t, sib, "init", "-q", "-b", "main")
	writeFloorFile(t, filepath.Join(sib, "forge.yaml"), "name: reliant\n")
	writeFloorFile(t, filepath.Join(sib, "db", "migrations", "20261101000000_a.up.sql"), "-- forge:backward-compatible — x\nSELECT 1;\n")
	writeFloorFile(t, filepath.Join(sib, "db", "migrations", "20261102000000_drop.up.sql"), "-- forge:breaks-rollback dropped chats.legacy\nSELECT 1;\n")
	writeFloorFile(t, filepath.Join(sib, release.FloorDeclarationsDir, "inbox-v2.yaml"), "kind: data\nreason: inbox rows rewritten\n")
	migGit(t, sib, "add", "-A")
	migGit(t, sib, "commit", "-q", "-m", "one")
	commit := migGit(t, sib, "rev-parse", "HEAD")
	// The working tree moves on after the build recorded its commit.
	writeFloorFile(t, filepath.Join(sib, release.FloorDeclarationsDir, "later.yaml"), "kind: api\nreason: not at the commit\n")

	src := &buildtarget.Source{Dir: sib, Repo: "github.com/acme/reliant", Commit: commit}
	m, notes, err := captureReleaseMigrations(proj, release.Provenance{Repo: "github.com/acme/cp"}, []sourceUse{{src: src}})
	if err != nil || len(notes) != 0 {
		t.Fatalf("capture: %v, notes %v", err, notes)
	}
	var ids []string
	for _, d := range m.Declarations {
		ids = append(ids, string(d.Kind)+":"+d.Source+":"+d.ID)
	}
	got := strings.Join(ids, ",")
	want := "schema:github.com/acme/reliant/db/migrations:20261102000000_drop.up.sql,data:github.com/acme/reliant/deploy/rollback-floors:inbox-v2"
	if got != want {
		t.Fatalf("declarations = %s\nwant %s", got, want)
	}
}
