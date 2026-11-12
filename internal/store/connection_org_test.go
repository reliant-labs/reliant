// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// An org's connection is never read or changed as a person's, not even as the
// admin's whose user_id it carries for attribution, and the owner-scoped reads
// find it only under its own org (control-plane docs/design/org-sharing.md §7).
func TestOrgConnectionsAreScopedToTheirOrg(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const admin = storetest.SeedUserID
	if _, err := env.SQL.ExecContext(ctx, `INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status)
		VALUES ('conn-org', 'org', $1, 'org-1', 'slack', 'api_key', 'Org bot', 'active'),
		       ('conn-mine', 'user', $1, NULL, 'slack', 'api_key', 'Mine', 'active')`, admin); err != nil {
		t.Fatalf("insert connections: %v", err)
	}
	if _, err := env.SQL.ExecContext(ctx, `INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		VALUES ('vk', 'org', 'org-1', 1, 'v1', '\x01', 'primary')`); err != nil {
		t.Fatalf("insert vault key: %v", err)
	}
	if _, err := env.SQL.ExecContext(ctx, `INSERT INTO connection_secrets (connection_id, field, vault_key_id, ciphertext)
		VALUES ('conn-org', 'api_key', 'vk', '\x01')`); err != nil {
		t.Fatalf("insert secret: %v", err)
	}

	// The personal (userID) paths do not see it.
	if _, err := store.GetUserConnection(ctx, env.ORM, admin, "conn-org"); !errors.Is(err, core.ErrConnectionNotFound) {
		t.Fatalf("GetUserConnection(org row) err = %v, want not found", err)
	}
	if _, err := store.GetConnectionSecrets(ctx, env.ORM, admin, "conn-org"); !errors.Is(err, core.ErrConnectionNotFound) {
		t.Fatalf("GetConnectionSecrets(org row) err = %v, want not found", err)
	}
	mine, err := store.ListUserConnections(ctx, env.ORM, admin, "")
	if err != nil || len(mine) != 1 || mine[0].ID != "conn-mine" {
		t.Fatalf("ListUserConnections = %v, %v; want only the personal row", mine, err)
	}
	ev := core.ConnectionEvent{UserID: admin, Kind: core.ConnectionEventRenamed, Actor: "user:" + admin}
	for name, write := range map[string]func() error{
		"rename":  func() error { return store.RenameConnection(ctx, env.ORM, admin, "conn-org", "x", ev) },
		"default": func() error { return store.SetDefaultConnection(ctx, env.ORM, admin, "conn-org", ev) },
		"delete":  func() error { return store.SoftDeleteConnection(ctx, env.ORM, admin, "conn-org", ev) },
		"test":    func() error { return store.RecordConnectionTestResult(ctx, env.ORM, admin, "conn-org", "label", "") },
		"reauth": func() error {
			_, err := store.ReauthorizeConnection(ctx, env.ORM, admin, "conn-org", core.ConnectionUpdate{}, nil, ev)
			return err
		},
	} {
		if err := write(); !errors.Is(err, core.ErrConnectionNotFound) {
			t.Fatalf("%s through the personal path err = %v, want not found", name, err)
		}
	}

	// The org's owner-scoped reads find it, and only under its own org.
	org := core.OrgConnectionOwner("org-1")
	got, err := store.GetOwnedConnection(ctx, env.ORM, org, "conn-org")
	if err != nil || got.Owner() != org || got.UserID != admin {
		t.Fatalf("GetOwnedConnection(org-1) = %+v, %v", got, err)
	}
	if secrets, err := store.GetOwnedConnectionSecrets(ctx, env.ORM, org, "conn-org"); err != nil || len(secrets) != 1 {
		t.Fatalf("GetOwnedConnectionSecrets(org-1) = %v, %v", secrets, err)
	}
	for _, other := range []core.ConnectionOwner{core.OrgConnectionOwner("org-2"), core.OrgConnectionOwner(""), core.UserConnectionOwner(admin), {Kind: "team", ID: "org-1"}} {
		if _, err := store.GetOwnedConnection(ctx, env.ORM, other, "conn-org"); !errors.Is(err, core.ErrConnectionNotFound) {
			t.Fatalf("GetOwnedConnection(%+v) err = %v, want not found", other, err)
		}
		if _, err := store.GetOwnedConnectionSecrets(ctx, env.ORM, other, "conn-org"); !errors.Is(err, core.ErrConnectionNotFound) {
			t.Fatalf("GetOwnedConnectionSecrets(%+v) err = %v, want not found", other, err)
		}
	}
	if err := store.RenameOwnedConnection(ctx, env.ORM, org, "conn-org", "Org bot 2", ev); err != nil {
		t.Fatalf("RenameOwnedConnection(org-1): %v", err)
	}
	if err := store.RenameOwnedConnection(ctx, env.ORM, core.OrgConnectionOwner("org-2"), "conn-org", "y", ev); !errors.Is(err, core.ErrConnectionNotFound) {
		t.Fatalf("RenameOwnedConnection(org-2) err = %v, want not found", err)
	}
}

// An org's first connection for an integration becomes the org's default
// whatever the creating admin has personally, and its names are unique within
// the org: two admins cannot name two of its Slack connections alike.
func TestOrgConnectionDefaultsAndNamesAreTheOrgs(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	insert := func(c *core.Connection) error {
		c.IntegrationID, c.AuthKind, c.Status, c.Scopes = "slack", core.ConnectionAuthAPIKey, core.ConnectionStatusActive, []string{}
		return store.InsertUserConnection(ctx, env.ORM, c, nil, core.ConnectionEvent{UserID: c.UserID, Kind: core.ConnectionEventCreated, Actor: "user:" + c.UserID})
	}
	org1 := "org-1"
	mine := &core.Connection{ID: "c-mine", OwnerKind: core.ConnectionOwnerUser, UserID: "admin-a", Name: "Mine"}
	orgA := &core.Connection{ID: "c-org-a", OwnerKind: core.ConnectionOwnerOrg, OrgID: &org1, UserID: "admin-a", Name: "Bot"}
	orgB := &core.Connection{ID: "c-org-b", OwnerKind: core.ConnectionOwnerOrg, OrgID: &org1, UserID: "admin-b", Name: "Bot 2"}
	for _, c := range []*core.Connection{mine, orgA, orgB} {
		if err := insert(c); err != nil {
			t.Fatalf("insert %s: %v", c.ID, err)
		}
	}
	if !mine.IsDefault || !orgA.IsDefault || orgB.IsDefault {
		t.Fatalf("defaults: mine=%v orgA=%v orgB=%v, want the first of each owner", mine.IsDefault, orgA.IsDefault, orgB.IsDefault)
	}
	dup := &core.Connection{ID: "c-org-dup", OwnerKind: core.ConnectionOwnerOrg, OrgID: &org1, UserID: "admin-b", Name: "Bot"}
	if err := insert(dup); !errors.Is(err, core.ErrConnectionNameTaken) {
		t.Fatalf("a second org connection named Bot err = %v, want name taken", err)
	}
	org := core.OrgConnectionOwner(org1)
	ev := core.ConnectionEvent{UserID: "admin-b", Kind: core.ConnectionEventDefaultChanged, Actor: "user:admin-b"}
	if err := store.SetDefaultOwnedConnection(ctx, env.ORM, org, "c-org-b", ev); err != nil {
		t.Fatalf("SetDefaultOwnedConnection: %v", err)
	}
	list, err := store.ListOwnedConnections(ctx, env.ORM, org, "slack")
	if err != nil {
		t.Fatalf("ListOwnedConnections: %v", err)
	}
	defaults := map[string]bool{}
	for _, c := range list {
		defaults[c.ID] = c.IsDefault
	}
	if len(list) != 2 || defaults["c-org-a"] || !defaults["c-org-b"] {
		t.Fatalf("org defaults after SetDefault = %v, want only c-org-b", defaults)
	}
	if got, _ := store.GetUserConnection(ctx, env.ORM, "admin-a", "c-mine"); got == nil || !got.IsDefault {
		t.Fatalf("the admin's personal default changed with the org's: %+v", got)
	}
}

// An integration event reaches a trigger through its owner's own connection,
// or through a connection of the trigger's own org; never through another
// org's, and never an org connection for a trigger with no org.
func TestIntegrationRoutingThroughOrgConnections(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if _, err := env.SQL.ExecContext(ctx, `INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status, external_account_id)
		VALUES ('conn-org-1', 'org', 'an-admin', 'org-1', 'slack', 'oauth2', 'Org 1 bot', 'active', 'T1'),
		       ('conn-org-2', 'org', 'an-admin', 'org-2', 'slack', 'oauth2', 'Org 2 bot', 'active', 'T2')`); err != nil {
		t.Fatalf("insert connections: %v", err)
	}
	trigger := func(id, conn string, org *string) *core.Trigger {
		tr := newStoreTrigger(id, id, base)
		tr.Kind, tr.ConnectionID, tr.OrgID = core.TriggerKindIntegration, &conn, org
		tr.DaemonID, tr.NoMachine = "", true
		tr.Config = json.RawMessage(`{"integration":"slack"}`)
		return tr
	}
	org1, org2 := "org-1", "org-2"
	for _, tr := range []*core.Trigger{
		trigger("trg-org-1", "conn-org-1", &org1),
		trigger("trg-org-2-on-org-1", "conn-org-1", &org2),
		trigger("trg-no-org-on-org-1", "conn-org-1", nil),
		trigger("trg-org-2", "conn-org-2", &org2),
	} {
		if err := store.InsertTrigger(ctx, env.ORM, tr); err != nil {
			t.Fatalf("InsertTrigger(%s): %v", tr.ID, err)
		}
	}
	routes, err := store.ListIntegrationTriggers(ctx, env.ORM, "slack")
	if err != nil {
		t.Fatalf("ListIntegrationTriggers: %v", err)
	}
	got := map[string]string{}
	for _, r := range routes {
		got[r.Trigger.ID] = r.ConnectionAccount
	}
	if len(got) != 2 || got["trg-org-1"] != "T1" || got["trg-org-2"] != "T2" {
		t.Fatalf("routes = %v, want trg-org-1 via T1 and trg-org-2 via T2 only", got)
	}

	// Access-routed: the same rule for the named connection.
	if _, err := env.SQL.ExecContext(ctx, `INSERT INTO integration_event_access
		(user_id, integration_id, account_key, resource_key, resource_label, refreshed_at)
		VALUES ($1, 'slack', 'T1', 'C1', 'general', now())`, storetest.SeedUserID); err != nil {
		t.Fatalf("insert access: %v", err)
	}
	access, err := store.ListAccessRoutedTriggers(ctx, env.ORM, "slack", "T1", "C1", base)
	if err != nil {
		t.Fatalf("ListAccessRoutedTriggers: %v", err)
	}
	ids := make([]string, 0, len(access))
	for _, tr := range access {
		ids = append(ids, tr.ID)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "trg-org-1" || ids[1] != "trg-org-2" {
		t.Fatalf("access-routed = %v, want the two triggers whose connection is their own org's", ids)
	}
}
