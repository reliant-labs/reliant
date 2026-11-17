package seedplan

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// A NULLABLE composite foreign key whose other member is NOT NULL — the
// billing-org shape (control-plane, 2026-11-09): a user may name one of their
// own memberships as the org that pays, so (billing_org_id, id) references
// org_memberships (org_id, user_id), and org_memberships.user_id references
// users. Under MATCH SIMPLE a composite with any NULL member is not checked,
// so leaving billing_org_id NULL satisfies it whatever id holds.
//
// The NOT NULL member (users.id, the primary key; daemons.owner_id) made the
// planner read the users -> org_memberships edge as NOT NULL, so the cycle
// users -> org_memberships -> users could not be broken and BuildPlan
// refused the whole closure: users and daemons lost their test factories.
const nullableCompositeFKDDL = `
CREATE TABLE orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    billing_org_id TEXT
);

CREATE TABLE org_memberships (
    org_id TEXT NOT NULL REFERENCES orgs (id),
    user_id TEXT NOT NULL REFERENCES users (id),
    role TEXT NOT NULL,
    PRIMARY KEY (org_id, user_id)
);

ALTER TABLE users ADD CONSTRAINT fk_users_billing_org_membership
    FOREIGN KEY (billing_org_id, id) REFERENCES org_memberships (org_id, user_id)
    ON DELETE SET NULL (billing_org_id);

CREATE TABLE daemons (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    billing_org_id TEXT,
    CONSTRAINT fk_daemons_billing_org_membership
        FOREIGN KEY (billing_org_id, owner_id) REFERENCES org_memberships (org_id, user_id)
        ON DELETE SET NULL (billing_org_id)
);
`

// TestNullableCompositeFK_FactoryClosuresPlanAndInsert plans each root's FK
// closure the way the entity-factory generator does (one row, minimal), and
// lets postgres judge the INSERTs.
func TestNullableCompositeFK_FactoryClosuresPlanAndInsert(t *testing.T) {
	requirePG(t)
	tables := applyDDL(t, nullableCompositeFKDDL)
	byName := map[string]schemadef.Table{}
	for _, tb := range tables {
		byName[tb.Name] = tb
	}
	db := minimalHarness(t, nullableCompositeFKDDL)
	for _, root := range []string{"users", "daemons", "org_memberships"} {
		t.Run(root, func(t *testing.T) {
			var closure []schemadef.Table
			for _, n := range FKClosure(tables, root) {
				closure = append(closure, byName[n])
			}
			p, err := BuildPlan(closure, PoolsFromTables(closure), Config{Rows: 1, Minimal: true})
			if err != nil {
				t.Fatalf("BuildPlan(%s closure): %v — a NULLABLE composite foreign key must not make the closure unplannable", root, err)
			}
			p.SetBounds(BoundsFromTables(closure))
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			stmts := p.Statements()
			if len(stmts) == 0 {
				t.Fatal("no statements")
			}
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					t.Fatalf("postgres refused the plan:\n%s\n%v", strings.Join(stmts, "\n"), err)
				}
			}
		})
	}
}
