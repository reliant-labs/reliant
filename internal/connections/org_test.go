// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Org-owned connections (control-plane docs/design/org-sharing.md §7): the
// readers. An org connection is sealed under the org's vault tenant, used by
// any live member in a run of that org that names it, never reached by an
// unnamed reference (D10), and every use is attributed to the acting member.

// orgMembers answers membership from a fixed table: org -> member ids.
type orgMembers map[string][]string

func (m orgMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	for _, u := range m[orgID] {
		if u == userID {
			return true, true, nil
		}
	}
	return false, true, nil
}

// membershipOutage is a control plane that cannot be asked.
type membershipOutage struct{}

func (membershipOutage) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, false, errors.New("control plane unreachable")
}

// orgConnection stores an org-owned GitHub API-key connection of orgID,
// created by admin, its key sealed under the org's tenant (as the create path
// does), and returns it.
func (e *env) orgConnection(orgID, admin, name, key string) *core.Connection {
	e.t.Helper()
	ctx := context.Background()
	id := "conn_org_" + uuid.NewString()[:8]
	ct, err := e.vault.Seal(ctx, vault.OrgTenant(orgID), []byte(key), connections.SecretAAD(id, core.SecretFieldAPIKey))
	require.NoError(e.t, err)
	keyID, err := vault.KeyIDOf(ct)
	require.NoError(e.t, err)
	conn := &core.Connection{
		ID: id, OwnerKind: core.ConnectionOwnerOrg, OrgID: &orgID, UserID: admin, IntegrationID: "github",
		AuthKind: core.ConnectionAuthAPIKey, Name: name, Status: core.ConnectionStatusActive, Scopes: []string{},
	}
	require.NoError(e.t, e.store.CreateConnection(ctx, conn,
		[]core.ConnectionSecret{{ConnectionID: id, Field: core.SecretFieldAPIKey, VaultKeyID: keyID, Ciphertext: ct}},
		core.ConnectionEvent{UserID: admin, Kind: core.ConnectionEventCreated, Actor: connections.ActorUser(admin)}))
	return conn
}

// newRunInOrg is newRun for a chat (and run) of orgID.
func (e *env) newRunInOrg(userID, orgID string) string {
	e.t.Helper()
	ctx := context.Background()
	now := fixedNow
	chatID := uuid.NewString()
	org := orgID
	require.NoError(e.t, e.repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "t", ProjectID: "test-project", UserID: userID, OrgID: &org,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := e.repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(e.t, err)
	owner := userID
	require.NoError(e.t, e.repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID,
		Status: db.Active(), CreatedAt: now, OwnerUserID: &owner, OrgID: &org,
	}))
	return chatID
}

// fixedNow stamps the rows these tests create; nothing here depends on the
// time, so it is a constant rather than the wall clock.
var fixedNow = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)

func serverCall(run string) connections.CallSite {
	return connections.CallSite{RunID: run, NodeID: "n1", ToolCallID: "tc1", Placement: connections.PlacementServer}
}

func TestOrgConnection_SealedUnderTheOrgTenantOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.orgConnection("org-1", "admin", "Org GitHub", "org-key-0001")
	secrets, err := e.store.GetOwnedSecrets(ctx, core.OrgConnectionOwner("org-1"), conn.ID)
	require.NoError(t, err)
	sec := secrets[core.SecretFieldAPIKey]

	plain, err := e.vault.Open(ctx, vault.OrgTenant("org-1"), sec.Ciphertext, connections.SecretAAD(conn.ID, sec.Field))
	require.NoError(t, err)
	require.Equal(t, "org-key-0001", string(plain))
	for _, wrong := range []vault.Tenant{vault.UserTenant("admin"), vault.UserTenant("org-1"), vault.OrgTenant("org-2")} {
		_, err := e.vault.Open(ctx, wrong, sec.Ciphertext, connections.SecretAAD(conn.ID, sec.Field))
		require.ErrorIs(t, err, vault.ErrTenantMismatch, "an org connection's secret must not open under %+v", wrong)
	}
	require.Equal(t, 1, e.count(`SELECT count(*) FROM vault_keys WHERE tenant_kind='org' AND tenant_id='org-1'`))
	require.Equal(t, 0, e.count(`SELECT count(*) FROM vault_keys WHERE tenant_kind='user' AND tenant_id='admin'`),
		"creating an org connection must not touch the admin's own tenant")
}

func TestOrgConnection_MemberUsesItByIDInARunOfTheOrg(t *testing.T) {
	e := newEnv(t)
	ctx := authz.WithMembership(context.Background(), orgMembers{"org-1": {"admin", "dana"}})
	conn := e.orgConnection("org-1", "admin", "Org GitHub", "org-key-0001")
	run := e.newRunInOrg("dana", "org-1")

	got, err := e.resolver.ForCall(ctx, serverCall(run), connections.Ref{ConnectionID: conn.ID, IntegrationID: "github"})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, got.Apply(req))
	require.Equal(t, "Bearer org-key-0001", req.Header.Get("Authorization"))

	// Attributed to the acting member, not the admin who created it.
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events
		WHERE connection_id=$1 AND kind='used' AND user_id='dana' AND run_id=$2 AND actor='worker'`, conn.ID, run))
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used' AND user_id='admin'`, conn.ID))
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections WHERE id=$1 AND last_used_at IS NOT NULL`, conn.ID))

	usable, err := e.resolver.Usable(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		[]connections.Ref{{ConnectionID: conn.ID}})
	require.NoError(t, err)
	require.NoError(t, usable[connections.Ref{ConnectionID: conn.ID}])
}

func TestOrgConnection_RefusedOutsideALiveMembersRunOfThatOrg(t *testing.T) {
	e := newEnv(t)
	members := orgMembers{"org-1": {"admin", "dana"}, "org-2": {"erin", "dana"}}
	conn := e.orgConnection("org-1", "admin", "Org GitHub", "org-key-0001")
	ref := connections.Ref{ConnectionID: conn.ID}

	for name, tc := range map[string]struct {
		run        func() string
		membership authz.Membership
		code       connections.ErrorCode
	}{
		"a non-member in a run of the org (removed, or never joined)": {
			run: func() string { return e.newRunInOrg("mallory", "org-1") }, membership: members, code: connections.CodeFailedPrecondition,
		},
		"a member of both, in a run of another org": {
			run: func() string { return e.newRunInOrg("dana", "org-2") }, membership: members, code: connections.CodeFailedPrecondition,
		},
		"a member, in a run with no org": {
			run: func() string { return e.newRun("dana") }, membership: members, code: connections.CodeFailedPrecondition,
		},
		"a deployment with no membership answer": {
			run: func() string { return e.newRunInOrg("dana", "org-1") }, membership: authz.NoOrgs{}, code: connections.CodeFailedPrecondition,
		},
		"a membership outage (not an answer, so retryable)": {
			run: func() string { return e.newRunInOrg("dana", "org-1") }, membership: membershipOutage{}, code: connections.CodeUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := authz.WithMembership(context.Background(), tc.membership)
			_, err := e.resolver.ForCall(ctx, serverCall(tc.run()), ref)
			require.Error(t, err)
			require.Equal(t, tc.code, connections.CodeOf(err), "%v", err)
		})
	}
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used'`, conn.ID),
		"a refused use must not be recorded as a use")
}

// D10: a reference that names only an integration resolves to the acting
// user's own default, never to the org's, even when they have none.
func TestOrgConnection_UnnamedReferenceNeverFallsBackToTheOrg(t *testing.T) {
	e := newEnv(t)
	ctx := authz.WithMembership(context.Background(), orgMembers{"org-1": {"admin", "dana"}})
	conn := e.orgConnection("org-1", "admin", "Org GitHub", "org-key-0001")
	require.True(t, conn.IsDefault, "the org's first GitHub connection is the org's default")
	run := e.newRunInOrg("dana", "org-1")

	_, err := e.resolver.ForCall(ctx, serverCall(run), connections.Ref{IntegrationID: "github"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "no github connection")

	// Once Dana has her own, an unnamed reference is hers, not the org's.
	mine := e.connect("dana", "mine")
	got, err := e.resolver.ForCall(ctx, serverCall(run), connections.Ref{IntegrationID: "github"})
	require.NoError(t, err)
	require.Equal(t, mine.ID, got.ConnectionID)
}

// A trigger of the org may listen through the org's connection; its owner
// must still be a live member, and a trigger of another org may not.
func TestOrgConnection_TriggerOfTheOrgResolvesIt(t *testing.T) {
	e := newEnv(t)
	members := orgMembers{"org-1": {"admin", "dana"}}
	conn := e.orgConnection("org-1", "admin", "Org GitHub", "org-key-0001")
	trigger := func(owner, org string) string {
		id := "trg-" + uuid.NewString()[:8]
		connID := conn.ID
		now := fixedNow
		require.NoError(t, e.repo.CreateTrigger(context.Background(), &core.Trigger{
			ID: id, UserID: owner, OrgID: &org, ProjectID: "test-project", Name: id, Kind: core.TriggerKindIntegration,
			Enabled: true, Workflow: "w", NoMachine: true, ConnectionID: &connID,
			Config: []byte(`{"integration":"github"}`), CreatedAt: now, UpdatedAt: now,
		}))
		return id
	}
	ctx := authz.WithMembership(context.Background(), members)
	site := func(id string) connections.TriggerSite {
		return connections.TriggerSite{TriggerID: id, Placement: connections.PlacementServer}
	}

	ok := trigger("dana", "org-1")
	got, err := e.resolver.ForTrigger(ctx, site(ok))
	require.NoError(t, err)
	require.Equal(t, conn.ID, got.ConnectionID)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND user_id='dana' AND actor=$2`,
		conn.ID, connections.ActorTrigger(ok)))

	_, err = e.resolver.ForTrigger(ctx, site(trigger("dana", "org-2")))
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	_, err = e.resolver.ForTrigger(ctx, site(trigger("mallory", "org-1")))
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
}

// An org connection that refreshes keeps its secrets under the org's tenant.
func TestOrgConnection_OAuthRefreshSealsUnderTheOrgTenant(t *testing.T) {
	e := newEnv(t)
	ctx := authz.WithMembership(context.Background(), orgMembers{"org-1": {"admin", "dana"}})
	org := "org-1"
	id := "conn_org_oauth"
	var secrets []core.ConnectionSecret
	for field, value := range map[string]string{core.SecretFieldAccessToken: "ghu_old", core.SecretFieldRefreshToken: "ghr_old"} {
		ct, err := e.vault.Seal(ctx, vault.OrgTenant(org), []byte(value), connections.SecretAAD(id, field))
		require.NoError(t, err)
		keyID, err := vault.KeyIDOf(ct)
		require.NoError(t, err)
		secrets = append(secrets, core.ConnectionSecret{ConnectionID: id, Field: field, VaultKeyID: keyID, Ciphertext: ct})
	}
	client := clientID
	require.NoError(t, e.store.CreateConnection(ctx, &core.Connection{
		ID: id, OwnerKind: core.ConnectionOwnerOrg, OrgID: &org, UserID: "admin", IntegrationID: "github",
		AuthKind: core.ConnectionAuthOAuth2, Name: "Org app", Status: core.ConnectionStatusActive, Scopes: []string{},
		OAuthClient: &client,
	}, secrets, core.ConnectionEvent{UserID: "admin", Kind: core.ConnectionEventCreated, Actor: connections.ActorUser("admin")}))
	e.expireAccessToken(id)

	got, err := e.resolver.ForCall(ctx, serverCall(e.newRunInOrg("dana", org)), connections.Ref{ConnectionID: id})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, got.Apply(req))
	require.Regexp(t, `^Bearer ghu_access_\d+$`, req.Header.Get("Authorization"))
	require.Equal(t, 1, int(e.gh.refreshCalls.Load()))

	stored, err := e.store.GetOwnedSecrets(ctx, core.OrgConnectionOwner(org), id)
	require.NoError(t, err)
	for field, sec := range stored {
		_, err := e.vault.Open(ctx, vault.OrgTenant(org), sec.Ciphertext, connections.SecretAAD(id, field))
		require.NoError(t, err, "refreshed %s must be sealed under the org tenant", field)
	}
}

// ListOrg shows the org's connections to a live member only.
func TestOrgConnection_ListedToMembersOfTheOrg(t *testing.T) {
	e := newEnv(t)
	ctx := authz.WithMembership(context.Background(), orgMembers{"org-1": {"admin", "dana"}})
	conn := e.orgConnection("org-1", "admin", "Org GitHub", "org-key-0001")
	e.connect("dana", "mine")

	mine, err := e.svc.List(ctx, "dana", "")
	require.NoError(t, err)
	require.Len(t, mine, 1, "the personal list never carries the org's connections")
	require.Equal(t, core.ConnectionOwnerUser, mine[0].OwnerKind)

	orgs, err := e.svc.ListOrg(ctx, "dana", "org-1", "")
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	require.Equal(t, conn.ID, orgs[0].ID)
	require.Equal(t, core.OrgConnectionOwner("org-1"), orgs[0].Owner())

	_, err = e.svc.ListOrg(ctx, "mallory", "org-1", "")
	require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err))
	_, err = e.svc.ListOrg(authz.WithMembership(context.Background(), membershipOutage{}), "dana", "org-1", "")
	require.Equal(t, connections.CodeUnavailable, connections.CodeOf(err))

	// Nor can the creating admin reach it through the personal rpcs.
	_, err = e.svc.Get(ctx, "admin", conn.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)
	require.ErrorIs(t, e.svc.Delete(ctx, "admin", conn.ID), connections.ErrNotFound)
}
