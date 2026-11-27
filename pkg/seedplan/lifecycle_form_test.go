// File: pkg/seedplan/lifecycle_form_test.go
//
// Two spellings of a domain rule — "an approved estimate has an approval
// timestamp", one-way or two-way — and BOTH seed:
//
//	CHECK (status <> 'APPROVED' OR approved_at IS NOT NULL)     -- an implication
//	CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL))   -- a biconditional
//
// The implication is a status guard; the biconditional is a PAIR of them
// (biconditional.go), so guard.go merges every one over `status` into a single
// union and satisfies them together.
//
// Before forge read the biconditional, it cost a real dogfood run its dataset
// four times over, and the author's escape was to WEAKEN the schema — two
// constraints downgraded, then two more, then one deleted outright. These tests
// pin, against a real postgres, that neither spelling needs that.

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

// The other half of the contract: the two-way spelling seeds too, at every
// salt, and is placed silently.
func TestLifecycleForm_BiconditionalsSeedCleanly(t *testing.T) {
	tables := applyDDL(t, lifecycleBiconditionalDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	for _, w := range p.Warnings() {
		if strings.Contains(w, "estimates_") {
			t.Errorf("a placeable biconditional is reported as unplaceable:\n  %s", w)
		}
	}
	if rejected, err := insertAcrossSalts(t, lifecycleBiconditionalDDL); rejected != 0 {
		t.Errorf("biconditionals over one status column produced %d/6 rejected inserts (last: %v)", rejected, err)
	}
}

// Mixed: well-formed guards PLUS a biconditional over the same column. They
// merge into one union, so neither blocks the other.
func TestLifecycleForm_BiconditionalBesideGuardsSeedsCleanly(t *testing.T) {
	tables := applyDDL(t, lifecycleMixedDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	for _, w := range p.Warnings() {
		if strings.Contains(w, "estimates_") {
			t.Errorf("a guard or biconditional is reported as unplaceable:\n  %s", w)
		}
	}
	if rejected, err := insertAcrossSalts(t, lifecycleMixedDDL); rejected != 0 {
		t.Errorf("guards beside a biconditional produced %d/6 rejected inserts (last: %v)", rejected, err)
	}
}

// reliant's connector_grants, reduced to what the constraint touches: a
// biconditional whose presence side is a NULLABLE FOREIGN KEY, beside two
// one-way guards on the same discriminator, all NOT VALID as the migration
// adds them.
const connectorGrantsDDL = `
CREATE TABLE daemons (id TEXT PRIMARY KEY);
CREATE TABLE connector_grants (
    id TEXT PRIMARY KEY,
    daemon_id TEXT REFERENCES daemons(id) ON DELETE CASCADE,
    kind TEXT NOT NULL DEFAULT 'workspace',
    org_id TEXT NOT NULL DEFAULT '',
    path_root TEXT NOT NULL
);
ALTER TABLE connector_grants
    ADD CONSTRAINT connector_grants_kind_valid CHECK (kind = ANY (ARRAY['workspace'::text, 'forge'::text])) NOT VALID,
    ADD CONSTRAINT connector_grants_forge_org CHECK (kind <> 'forge' OR length(TRIM(BOTH FROM org_id)) > 0) NOT VALID,
    ADD CONSTRAINT connector_grants_daemon_by_kind CHECK ((kind = 'workspace') = (daemon_id IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT connector_grants_path_root_not_empty CHECK (kind <> 'workspace' OR length(TRIM(BOTH FROM path_root)) > 0) NOT VALID;
`

func TestLifecycleForm_ConnectorGrantsSeedsBothKinds(t *testing.T) {
	tables := applyDDL(t, connectorGrantsDDL)
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()
	ctx := context.Background()
	for _, salt := range []int{5, 11, 23, 42, 77, 101} {
		for _, minimal := range []bool{false, true} {
			p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: salt, Minimal: minimal})
			if err != nil {
				t.Fatalf("salt %d: BuildPlan: %v", salt, err)
			}
			p.SetBounds(BoundsFromTables(tables))
			for _, w := range p.Warnings() {
				if strings.Contains(w, "connector_grants") {
					t.Errorf("salt %d minimal=%v: %s", salt, minimal, w)
				}
			}
			if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS connector_grants, daemons CASCADE"); err != nil {
				t.Fatalf("reset: %v", err)
			}
			if _, err := db.ExecContext(ctx, connectorGrantsDDL); err != nil {
				t.Fatalf("apply ddl: %v", err)
			}
			for _, stmt := range p.Statements() {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("salt %d minimal=%v: seeded row rejected by its own schema: %v\n%s", salt, minimal, err, stmt)
				}
			}
			if minimal {
				continue
			}
			var workspace, forge int
			if err := db.QueryRowContext(ctx, `SELECT
			        count(*) FILTER (WHERE kind = 'workspace' AND daemon_id IS NOT NULL),
			        count(*) FILTER (WHERE kind = 'forge' AND daemon_id IS NULL)
			    FROM connector_grants`).Scan(&workspace, &forge); err != nil {
				t.Fatalf("count: %v", err)
			}
			if workspace == 0 || forge == 0 {
				t.Errorf("salt %d: rows cover workspace=%d forge=%d; both kinds must appear", salt, workspace, forge)
			}
		}
	}
}
