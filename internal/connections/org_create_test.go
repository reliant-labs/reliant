// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// The create half of org-owned connections (org-sharing.md §7.3, D8, D13):
// only an owner or admin, read fresh, while the deployment enables them.

// orgRoles is a control plane that names roles: org -> user -> role. Anyone
// with a role is a live member.
type orgRoles map[string]map[string]string

func (r orgRoles) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	_, ok := r[orgID][userID]
	return ok, true, nil
}

func (r orgRoles) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	role, ok := r[orgID][userID]
	if !ok {
		return "", true, nil
	}
	return role, true, nil
}

// roleOutage names members but cannot read a role.
type roleOutage struct{ orgRoles }

func (roleOutage) RoleFresh(context.Context, string, string) (string, bool, error) {
	return "", false, errors.New("control plane unreachable")
}

// unknownRoles is a control plane too old to name a role.
type unknownRoles struct{ orgRoles }

func (unknownRoles) RoleFresh(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

var acme = orgRoles{"org-1": {"olivia": "owner", "ada": "admin", "dana": "member"}}

// asIn is a request by a person signed in to the app, under membership m: the
// credential kind the auth edge installs (auth.WithCredential).
func asIn(m authz.Membership) context.Context {
	return authz.WithCredentialKind(authz.WithMembership(context.Background(), m), authz.CredentialKindPerson)
}

func (e *env) createOrgKey(ctx context.Context, userID, orgID, name string) (*core.Connection, error) {
	return e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: userID, OrgID: orgID, IntegrationID: "github", Name: name,
		Kind: connections.APIKeyKindAPIKey, Fields: map[string]string{"api_key": "org-key-" + name},
	})
}

func TestOrgCreate_AnAdminCreatesItSealedUnderTheOrg(t *testing.T) {
	e := newEnv(t)
	e.svc.WithOrgConnections(true)
	ctx := asIn(acme)
	for _, admin := range []string{"olivia", "ada"} {
		conn, err := e.createOrgKey(ctx, admin, "org-1", "by-"+admin)
		require.NoError(t, err, admin)
		require.Equal(t, core.OrgConnectionOwner("org-1"), conn.Owner())
		require.Equal(t, admin, conn.UserID, "user_id records who created it")

		secrets, err := e.store.GetOwnedSecrets(ctx, conn.Owner(), conn.ID)
		require.NoError(t, err)
		sec := secrets[core.SecretFieldAPIKey]
		_, err = e.vault.Open(ctx, vault.OrgTenant("org-1"), sec.Ciphertext, connections.SecretAAD(conn.ID, sec.Field))
		require.NoError(t, err)
		_, err = e.vault.Open(ctx, vault.UserTenant(admin), sec.Ciphertext, connections.SecretAAD(conn.ID, sec.Field))
		require.ErrorIs(t, err, vault.ErrTenantMismatch)
	}
	mine, err := e.svc.List(ctx, "ada", "")
	require.NoError(t, err)
	require.Empty(t, mine, "an org connection is never listed as its creator's own")

	// A member of the org then uses it in a run of the org.
	org, err := e.svc.ListOrg(ctx, "dana", "org-1", "github")
	require.NoError(t, err)
	require.Len(t, org, 2)
	_, err = e.resolver.ForCall(ctx, serverCall(e.newRunInOrg("dana", "org-1")), connections.Ref{ConnectionID: org[0].ID})
	require.NoError(t, err)
}

func TestOrgCreate_MemberUsesButCannotCreate(t *testing.T) {
	e := newEnv(t)
	e.svc.WithOrgConnections(true)
	ctx := asIn(acme)
	for _, who := range []string{"dana", "mallory"} {
		_, err := e.createOrgKey(ctx, who, "org-1", "nope")
		require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err), "%s: %v", who, err)
	}
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connections WHERE owner_kind='org'`))
}

func TestOrgCreate_RefusedWhileDisabledOrWhenTheRoleCannotBeRead(t *testing.T) {
	e := newEnv(t)
	_, err := e.createOrgKey(asIn(acme), "olivia", "org-1", "off")
	require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err), "off by default: %v", err)
	// The refusal carries a stable reason, so a client shows the org's
	// connections read-only and says why without reading the message.
	require.Equal(t, connections.ReasonOrgConnectionsDisabled, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader), "the reason: %v", err)
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "still FailedPrecondition on the wire")

	e.svc.WithOrgConnections(true)
	_, err = e.createOrgKey(asIn(unknownRoles{acme}), "olivia", "org-1", "unknown")
	require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err), "an unknown role is refused: %v", err)
	_, err = e.createOrgKey(asIn(roleOutage{acme}), "olivia", "org-1", "outage")
	require.Equal(t, connections.CodeUnavailable, connections.CodeOf(err), "an outage is retryable: %v", err)
	_, err = e.createOrgKey(asIn(orgMembers{"org-1": {"olivia"}}), "olivia", "org-1", "no-roles")
	require.Equal(t, connections.CodeUnavailable, connections.CodeOf(err), "a membership that names no roles fails closed: %v", err)
	_, err = e.createOrgKey(authz.WithCredentialOrg(asIn(acme), "org-2"), "olivia", "org-1", "bound")
	require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err), "a credential bound to another org: %v", err)
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connections WHERE owner_kind='org'`))
}

// An org's OAuth connection: the admin who started the flow finishes it as a
// person, still an admin, and it is stored as the org's.
func TestOrgCreate_OAuthFlowConnectsForTheOrg(t *testing.T) {
	e := newEnv(t)
	e.svc.WithOrgConnections(true)
	ctx := asIn(acme)
	start := func(ctx context.Context, userID string) string {
		authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{
			UserID: userID, OrgID: "org-1", IntegrationID: "github", Name: "Org app", ClientOrigin: testAppOrigin,
		})
		require.NoError(t, err)
		return mustQuery(t, authURL, "state")
	}

	done, err := e.svc.CompleteOAuthFlow(ctx, connections.CompleteParams{UserID: "ada", State: start(ctx, "ada"), Code: "c", Person: true})
	require.NoError(t, err)
	conn := done.Connection
	require.Equal(t, core.OrgConnectionOwner("org-1"), conn.Owner())
	secrets, err := e.store.GetOwnedSecrets(ctx, conn.Owner(), conn.ID)
	require.NoError(t, err)
	for field, sec := range secrets {
		_, err := e.vault.Open(ctx, vault.OrgTenant("org-1"), sec.Ciphertext, connections.SecretAAD(conn.ID, field))
		require.NoError(t, err, field)
	}

	// A member cannot start one.
	_, err = e.svc.StartOAuth(ctx, connections.StartParams{UserID: "dana", OrgID: "org-1", IntegrationID: "github", ClientOrigin: testAppOrigin})
	require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err))

	// Not finished by a credential an agent can hold (the plain
	// CompleteOAuth, which vouches for no person, included).
	_, err = e.svc.CompleteOAuthFlow(ctx, connections.CompleteParams{UserID: "ada", State: start(ctx, "ada"), Code: "c"})
	require.Equal(t, connections.CodeHumanOnly, connections.CodeOf(err))
	_, err = e.svc.CompleteOAuth(ctx, "ada", start(ctx, "ada"), "c")
	require.Equal(t, connections.CodeHumanOnly, connections.CodeOf(err))

	// Not by another admin of the same org.
	_, err = e.svc.CompleteOAuthFlow(ctx, connections.CompleteParams{UserID: "olivia", State: start(ctx, "ada"), Code: "c", Person: true})
	require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err))

	// Not by an admin demoted between the start and the finish: the role is
	// read again, fresh.
	state := start(ctx, "ada")
	demoted := orgRoles{"org-1": {"olivia": "owner", "ada": "member"}}
	_, err = e.svc.CompleteOAuthFlow(asIn(demoted), connections.CompleteParams{UserID: "ada", State: state, Code: "c", Person: true})
	require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err))

	// Not once the deployment turned org connections off.
	state = start(ctx, "ada")
	e.svc.WithOrgConnections(false)
	_, err = e.svc.CompleteOAuthFlow(ctx, connections.CompleteParams{UserID: "ada", State: state, Code: "c", Person: true})
	require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err))

	require.Equal(t, 1, e.count(`SELECT count(*) FROM connections WHERE owner_kind='org'`), "only the first flow stored a connection")
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connections WHERE owner_kind='user'`), "no org flow ever became a personal connection")
}

// Managing an org's connection is an owner's or admin's; the audit trail
// names every member who used it.
func TestOrgCreate_AdminsManageAndAuditIt(t *testing.T) {
	e := newEnv(t)
	e.svc.WithOrgConnections(true)
	ctx := asIn(acme)
	conn, err := e.createOrgKey(ctx, "ada", "org-1", "bot")
	require.NoError(t, err)
	_, err = e.resolver.ForCall(ctx, serverCall(e.newRunInOrg("dana", "org-1")), connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)

	for name, call := range map[string]func(string) error{
		"rename": func(u string) error { _, err := e.svc.RenameOrg(ctx, u, "org-1", conn.ID, "renamed"); return err },
		"default": func(u string) error {
			_, err := e.svc.SetDefaultOrg(ctx, u, "org-1", conn.ID)
			return err
		},
		"test":   func(u string) error { _, err := e.svc.TestOrg(ctx, u, "org-1", conn.ID); return err },
		"events": func(u string) error { _, err := e.svc.EventsOrg(ctx, u, "org-1", conn.ID, 0, 0); return err },
		"delete": func(u string) error { return e.svc.DeleteOrg(ctx, u, "org-1", conn.ID) },
	} {
		require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(call("dana")), "a member may not %s it", name)
	}

	got, err := e.svc.GetOrg(ctx, "dana", "org-1", conn.ID)
	require.NoError(t, err, "a member may read it")
	require.Equal(t, conn.ID, got.ID)

	renamed, err := e.svc.RenameOrg(ctx, "olivia", "org-1", conn.ID, "renamed")
	require.NoError(t, err)
	require.Equal(t, "renamed", renamed.Name)
	evs, err := e.svc.EventsOrg(ctx, "olivia", "org-1", conn.ID, 0, 0)
	require.NoError(t, err)
	byWho := map[string]string{}
	for _, ev := range evs {
		byWho[ev.Kind] = ev.UserID
	}
	require.Equal(t, "dana", byWho[core.ConnectionEventUsed], "the use is the acting member's")
	require.Equal(t, "olivia", byWho[core.ConnectionEventRenamed])
	require.Equal(t, "ada", byWho[core.ConnectionEventCreated])

	// Deleting is never gated by the rollout switch.
	e.svc.WithOrgConnections(false)
	require.NoError(t, e.svc.DeleteOrg(ctx, "ada", "org-1", conn.ID))
	_, err = e.svc.GetOrg(ctx, "dana", "org-1", conn.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)
}

// Names are the owner's: an admin of two orgs may call each org's connection
// "Bot", and keep a "Bot" of their own, while each owner holds one "Bot".
func TestOrgCreate_NamesAreEachOwnersOwn(t *testing.T) {
	e := newEnv(t)
	e.svc.WithOrgConnections(true)
	ctx := asIn(orgRoles{"org-1": {"ada": "admin"}, "org-2": {"ada": "owner"}})
	for _, org := range []string{"org-1", "org-2"} {
		_, err := e.createOrgKey(ctx, "ada", org, "Bot")
		require.NoError(t, err, org)
	}
	_, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "ada", IntegrationID: "github", Name: "Bot",
		Kind: connections.APIKeyKindAPIKey, Fields: map[string]string{"api_key": "mine"},
	})
	require.NoError(t, err, "a personal connection may share an org connection's name")

	_, err = e.createOrgKey(ctx, "ada", "org-1", "Bot")
	require.ErrorIs(t, err, connections.ErrAlreadyExists)
	_, err = e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "ada", IntegrationID: "github", Name: "Bot",
		Kind: connections.APIKeyKindAPIKey, Fields: map[string]string{"api_key": "mine-2"},
	})
	require.ErrorIs(t, err, connections.ErrAlreadyExists)
}

// Administering an org's connections is a person's act below the handler's
// human_only check too: authz.RequireOrgAdmin refuses a software credential
// (an agent's CLI login) or an unclassified one, even an owner's, so the
// service refuses it on any path, and stores nothing.
func TestOrgCreate_OnlyAPersonAdministers(t *testing.T) {
	e := newEnv(t)
	e.svc.WithOrgConnections(true)
	for name, ctx := range map[string]context.Context{
		"software credential":     authz.WithCredentialKind(authz.WithMembership(context.Background(), acme), authz.CredentialKindSoftware),
		"unclassified credential": authz.WithMembership(context.Background(), acme),
	} {
		_, err := e.createOrgKey(ctx, "olivia", "org-1", "agent-"+strings.ReplaceAll(name, " ", "-"))
		require.Equal(t, connections.CodePermissionDenied, connections.CodeOf(err), "%s: %v", name, err)
		require.ErrorIs(t, err, authz.ErrPersonRequired, name)
	}
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connections WHERE owner_kind='org'`))
}
