// Copyright (c) 2025 Reliant Labs
package connection

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

// An org's linked GitHub App installations are listed as the org's GitHub
// connections, to a live member who names the org, and only then; a suspended
// one says so; an unreachable control plane leaves them out without failing
// the list.
func TestListConnections_SynthesizesTheOrgsGitHubAppInstallations(t *testing.T) {
	f := newConnectionFixture(t)
	f.createKey(t, "dana", "http", "mine")
	apps := &StubOrgGitHubApps{ListOrgGitHubInstallationsFunc: func(_ context.Context, org string) ([]gitcredentialclient.OrgInstallation, error) {
		require.Equal(t, "org-1", org)
		return []gitcredentialclient.OrgInstallation{
			{InstallationID: 4242, AccountLogin: "acme", AccountType: "Organization"},
			{InstallationID: 7, AccountLogin: "acme-labs", AccountType: "Organization", Suspended: true},
		}, nil
	}}
	svc := NewTestConnection(t, WithConnectionDeps(Deps{Connections: f.conns, OrgGitHubApps: apps}))
	members := orgMembership{"org-1": {"dana": true}}
	as := func(user string) context.Context { return authz.WithMembership(asConnUser(user), members) }
	org := "org-1"
	list := func(ctx context.Context, req *reliantv1.ListConnectionsRequest) ([]*reliantv1.Connection, error) {
		resp, err := svc.ListConnections(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Connections, nil
	}

	own, err := list(as("dana"), &reliantv1.ListConnectionsRequest{})
	require.NoError(t, err)
	require.Len(t, own, 1, "no org named: the caller's own, as before")
	require.Zero(t, apps.CallCount("ListOrgGitHubInstallations"), "control-plane is not asked without an org")

	all, err := list(as("dana"), &reliantv1.ListConnectionsRequest{OrgId: &org})
	require.NoError(t, err)
	require.Len(t, all, 3)
	app := all[1]
	require.Equal(t, "controlplane:github-app:4242", app.Id)
	require.Equal(t, "github", app.IntegrationId)
	require.Equal(t, reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG, app.OwnerKind)
	require.Equal(t, "org-1", app.OrgId)
	require.Equal(t, reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_DELEGATED, app.AuthKind)
	require.Equal(t, reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE, app.Status)
	require.Equal(t, "acme", app.AccountLabel)
	suspended := all[2]
	require.Equal(t, "controlplane:github-app:7", suspended.Id)
	require.Equal(t, reliantv1.ConnectionStatus_CONNECTION_STATUS_NEEDS_REAUTH, suspended.Status)
	require.Equal(t, "installation_suspended", suspended.StatusReason)

	gh := "github"
	filtered, err := list(as("dana"), &reliantv1.ListConnectionsRequest{OrgId: &org, IntegrationId: &gh})
	require.NoError(t, err)
	require.Len(t, filtered, 2, "the github filter keeps the installations and drops the http key")
	other := "http"
	filtered, err = list(as("dana"), &reliantv1.ListConnectionsRequest{OrgId: &org, IntegrationId: &other})
	require.NoError(t, err)
	require.Len(t, filtered, 1, "another integration's filter shows no installation")

	before := apps.CallCount("ListOrgGitHubInstallations")
	_, err = list(as("mallory"), &reliantv1.ListConnectionsRequest{OrgId: &org})
	requireConnectCode(t, err, connect.CodePermissionDenied)
	require.Equal(t, before, apps.CallCount("ListOrgGitHubInstallations"), "a non-member never reaches control-plane")

	apps.ListOrgGitHubInstallationsFunc = func(context.Context, string) ([]gitcredentialclient.OrgInstallation, error) {
		return nil, errors.New("control plane down")
	}
	degraded, err := list(as("dana"), &reliantv1.ListConnectionsRequest{OrgId: &org})
	require.NoError(t, err, "an unreachable control plane does not fail the list")
	require.Len(t, degraded, 1)
}
