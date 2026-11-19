// File: pkg/seedplan/lifecycle_form_test.go
//
// Two spellings of the SAME domain rule — "an approved estimate has an
// approval timestamp" — seed in opposite ways, and nothing in the schema hints
// at which one you picked:
//
//	CHECK (status <> 'APPROVED' OR approved_at IS NOT NULL)     -- placeable
//	CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL))   -- not
//
// The implication is a status guard, so guard.go merges every one over `status`
// into a single union and satisfies them together. The biconditional has no
// top-level OR, so no pass reads it, `status` and `approved_at` are drawn
// independently, and the INSERT is rejected.
//
// This cost a real dogfood run its dataset four times over, and the author's
// escape was to WEAKEN the schema — two constraints downgraded, then two more,
// then one deleted outright and re-homed into service code. These tests pin the
// difference so the guidance in the db/seeding skill cannot silently rot.

package seedplan

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// Three implications over ONE lifecycle column. The count is the point: they
// are merged, so they are not rivals to each other.
const lifecycleImplicationDDL = `
CREATE TABLE estimates (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'ESTIMATE_STATUS_DRAFT'
        CHECK (status IN ('ESTIMATE_STATUS_DRAFT','ESTIMATE_STATUS_SENT','ESTIMATE_STATUS_APPROVED','ESTIMATE_STATUS_REJECTED')),
    sent_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    rejected_at TIMESTAMPTZ,
    CONSTRAINT estimates_sent_has_stamp     CHECK (status <> 'ESTIMATE_STATUS_SENT'     OR sent_at     IS NOT NULL),
    CONSTRAINT estimates_approved_has_stamp CHECK (status <> 'ESTIMATE_STATUS_APPROVED' OR approved_at IS NOT NULL),
    CONSTRAINT estimates_rejected_has_stamp CHECK (status <> 'ESTIMATE_STATUS_REJECTED' OR rejected_at IS NOT NULL)
);
`

// The same intent as biconditionals.
const lifecycleBiconditionalDDL = `
CREATE TABLE estimates (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'ESTIMATE_STATUS_DRAFT'
        CHECK (status IN ('ESTIMATE_STATUS_DRAFT','ESTIMATE_STATUS_SENT','ESTIMATE_STATUS_APPROVED','ESTIMATE_STATUS_REJECTED')),
    sent_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    CONSTRAINT estimates_sent_iff     CHECK ((status = 'ESTIMATE_STATUS_SENT')     = (sent_at     IS NOT NULL)),
    CONSTRAINT estimates_approved_iff CHECK ((status = 'ESTIMATE_STATUS_APPROVED') = (approved_at IS NOT NULL))
);
`

// Well-formed guards PLUS one biconditional over the same column. The
// biconditional is not merely unplaced itself — it blocks the guards, because
// it spans `status` too.
const lifecycleMixedDDL = `
CREATE TABLE estimates (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'ESTIMATE_STATUS_DRAFT'
        CHECK (status IN ('ESTIMATE_STATUS_DRAFT','ESTIMATE_STATUS_SENT','ESTIMATE_STATUS_APPROVED','ESTIMATE_STATUS_REJECTED')),
    sent_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    rejected_at TIMESTAMPTZ,
    CONSTRAINT estimates_sent_has_stamp     CHECK (status <> 'ESTIMATE_STATUS_SENT'     OR sent_at     IS NOT NULL),
    CONSTRAINT estimates_rejected_has_stamp CHECK (status <> 'ESTIMATE_STATUS_REJECTED' OR rejected_at IS NOT NULL),
    CONSTRAINT estimates_approved_iff       CHECK ((status = 'ESTIMATE_STATUS_APPROVED') = (approved_at IS NOT NULL))
);
`

// insertAcrossSalts seeds the schema at several salts and reports how many
// produced a row its own schema rejected.
//
// Several salts, not one: the distinction under test is not a coin flip in
// either direction, and a single-salt assertion would leave that unproven.
func insertAcrossSalts(t *testing.T, ddl string) (rejected int, lastErr error) {
	t.Helper()
	tables := applyDDL(t, ddl)

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()
	ctx := context.Background()

	for _, salt := range []int{5, 11, 23, 42, 77, 101} {
		p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: salt})
		if err != nil {
			t.Fatalf("salt %d: BuildPlan: %v", salt, err)
		}
		p.SetBounds(BoundsFromTables(tables))

		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS estimates CASCADE"); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("apply ddl: %v", err)
		}
		for _, stmt := range p.Statements() {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				rejected++
				lastErr = err
				break
			}
		}
	}
	return rejected, lastErr
}

// The guidance's promise: implications seed, however many of them share the
// column. If this fails, the db/seeding skill is telling authors to write
// something forge no longer places.
func TestLifecycleForm_ImplicationsSeedCleanly(t *testing.T) {
	rejected, err := insertAcrossSalts(t, lifecycleImplicationDDL)
	if rejected != 0 {
		t.Errorf("three one-way implications over one status column produced %d/6 rejected inserts "+
			"(last: %v) — the db/seeding skill promises this shape always seeds", rejected, err)
	}
}

// And they are placed SILENTLY: a warning on a constraint forge satisfies sends
// the author looking for a problem that is not there — which is how the
// measured run talked itself into weakening the schema.
func TestLifecycleForm_ImplicationsAreNotWarnedAbout(t *testing.T) {
	tables := applyDDL(t, lifecycleImplicationDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	for _, w := range p.Warnings() {
		if strings.Contains(w, "estimates_") {
			t.Errorf("a placeable implication is reported as unplaceable:\n  %s", w)
		}
	}
}

// The other half of the contract. If biconditionals ever become placeable this
// SHOULD fail — the guidance would then be over-strict, and the skill text
// (and this file's rationale) must be revisited rather than the test relaxed.
func TestLifecycleForm_BiconditionalsAreRefusedAndRejected(t *testing.T) {
	tables := applyDDL(t, lifecycleBiconditionalDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	joined := strings.Join(p.Warnings(), "\n")
	for _, name := range []string{"estimates_sent_iff", "estimates_approved_iff"} {
		if !strings.Contains(joined, name) {
			t.Errorf("unplaceable biconditional %q is not named in the plan warnings; got:\n%s", name, joined)
		}
	}

	rejected, _ := insertAcrossSalts(t, lifecycleBiconditionalDDL)
	if rejected == 0 {
		t.Errorf("biconditionals now seed cleanly at every salt — if forge learned to place them, " +
			"the db/seeding guidance is stale and should be revisited, not this assertion")
	}
}

// The mixing rule, which is the non-obvious half of the guidance: ONE
// biconditional blocks the well-formed guards beside it, because it spans the
// same column and forge cannot prove joint satisfiability.
func TestLifecycleForm_OneBiconditionalBlocksTheGuardsBesideIt(t *testing.T) {
	tables := applyDDL(t, lifecycleMixedDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	joined := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(joined, "estimates_sent_has_stamp") ||
		!strings.Contains(joined, "estimates_rejected_has_stamp") {
		t.Errorf("the guards blocked by the biconditional are not named, so an author cannot tell "+
			"WHICH constraint to rewrite; warnings:\n%s", joined)
	}
	if !strings.Contains(joined, "estimates_approved_iff") {
		t.Errorf("the blocking biconditional is not named; warnings:\n%s", joined)
	}
}
