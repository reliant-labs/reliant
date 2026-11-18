package codegen

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// billingOrgSchema is the control-plane billing-org shape as introspection
// reports it: users and daemons each gained a NULLABLE billing_org_id whose
// composite key (billing_org_id, id|owner_id) references org_memberships
// (org_id, user_id), and org_memberships.user_id references users — a cycle
// through users that only the nullable member can break.
func billingOrgSchema() map[string]schemadef.Table {
	col := func(name string, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: schemadef.TypeString, NotNull: notNull, IsPK: pk}
	}
	membership := func(name, other string) []schemadef.ForeignKey {
		keys, refs := []string{"billing_org_id", other}, []string{"org_id", "user_id"}
		return []schemadef.ForeignKey{
			{Column: "billing_org_id", RefTable: "org_memberships", RefColumn: "org_id", Name: name, KeyColumns: keys, RefKeyColumns: refs, OnDelete: "SET NULL"},
			{Column: other, RefTable: "org_memberships", RefColumn: "user_id", Name: name, KeyColumns: keys, RefKeyColumns: refs, OnDelete: "SET NULL"},
		}
	}
	tables := []schemadef.Table{
		{Name: "orgs", PKCols: []string{"id"}, Columns: []schemadef.Column{col("id", true, true), col("name", true, false)}},
		{
			Name: "users", PKCols: []string{"id"},
			Columns:     []schemadef.Column{col("id", true, true), col("email", true, false), col("billing_org_id", false, false)},
			ForeignKeys: membership("fk_users_billing_org_membership", "id"),
		},
		{
			Name: "org_memberships", PKCols: []string{"org_id", "user_id"},
			Columns: []schemadef.Column{col("org_id", true, true), col("user_id", true, true), col("role", true, false)},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "org_id", RefTable: "orgs", RefColumn: "id", Name: "org_memberships_org_id_fkey"},
				{Column: "user_id", RefTable: "users", RefColumn: "id", Name: "org_memberships_user_id_fkey"},
			},
		},
		{
			Name: "daemons", PKCols: []string{"id"},
			Columns:     []schemadef.Column{col("id", true, true), col("owner_id", true, false), col("billing_org_id", false, false)},
			ForeignKeys: membership("fk_daemons_billing_org_membership", "owner_id"),
		},
	}
	byName := map[string]schemadef.Table{}
	for _, t := range tables {
		byName[t.Name] = t
	}
	return byName
}

// Adding a NULLABLE composite foreign key must not cost an entity its test
// factory: the key can be left NULL (MATCH SIMPLE), so the minimal row still
// inserts. It did — the NOT NULL member read the edge as required, the cycle
// through users could not be broken, and generate deleted NewUser/NewDaemon.
func TestBakeEntityFactory_KeepsAnEntityWithANullableCompositeFK(t *testing.T) {
	byName := billingOrgSchema()
	var all []schemadef.Table
	for _, tb := range byName {
		all = append(all, tb)
	}
	pools, bounds := seedplan.PoolsFromTables(all), seedplan.BoundsFromTables(all)
	for _, root := range []string{"users", "daemons"} {
		t.Run(root, func(t *testing.T) {
			ent := dbEntity{goName: strings.ToUpper(root[:1]) + root[1:len(root)-1], table: root, pkColumn: "id", pkString: true}
			for _, minimal := range []bool{true, false} {
				spec, ok := bakeEntityFactory(byName, root, ent, pools, bounds, nil, minimal)
				if !ok {
					t.Fatalf("minimal=%v: no factory for %s — a nullable composite foreign key dropped it", minimal, root)
				}
				if !strings.HasPrefix(spec.rootSQL, "INSERT INTO ") || !strings.Contains(spec.rootSQL, "$1") {
					t.Fatalf("minimal=%v: root statement %q", minimal, spec.rootSQL)
				}
				// The parents run BEFORE the root. A table ordered after it —
				// org_memberships, which references users — is not a parent
				// and must not be inserted first: its row would name a user
				// that does not exist yet.
				if root == "users" && strings.Contains(spec.parentSQL, "org_memberships") {
					t.Errorf("minimal=%v: users' parents insert a membership of the user not yet inserted:\n%s", minimal, spec.parentSQL)
				}
			}
		})
	}
}
