package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// A schema whose every table opted out with forge:no-seed seeds nothing on
// purpose. `forge db seed apply` must say so rather than fail with "seeded
// nothing: ... That is the wrong migrations directory".
func TestSeedOutcome_EveryTableNoSeedIsNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; runs in task test")
	}
	const migration = `
CREATE TABLE job_leases (
    job_id TEXT PRIMARY KEY,
    holder TEXT NOT NULL
);
COMMENT ON TABLE job_leases IS 'forge:no-seed — the control plane writes leases';
`
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(migration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	migDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg := seedplan.DefaultConfig()
	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, "", cfg)
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	res, err := seedplan.Apply(ctx, db, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := seedOutcomeError(ctx, db, plan, cfg, res, migDir); err != nil {
		t.Errorf("an all-forge:no-seed schema is a deliberate empty seed, got: %v", err)
	}
}
