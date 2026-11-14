// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/pkg/config"
)

// orgAdmins is a control plane where olivia administers org-1.
type orgAdmins struct{}

func (orgAdmins) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return userID == "olivia" && orgID == "org-1", true, nil
}

func (orgAdmins) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	if userID == "olivia" && orgID == "org-1" {
		return "admin", true, nil
	}
	return "", true, nil
}

// createOrgConnectionThrough builds the API server's connection service the
// way newAPIConnections does from api (app.NewAPIConnections, its gate from
// app.APIOrgConnections) and has an org admin, signed in as a person, create
// an org-owned connection with it.
func createOrgConnectionThrough(t *testing.T, api interface{ GetCorsAllowedOrigins() []string }, enabled bool) error {
	t.Helper()
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	repo := db.NewTestRepo(t)
	conns, err := app.NewAPIConnections(app.APIConnectionsDeps{
		DB: repo, Vault: vault.New(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)}),
		Integrations: &app.IntegrationSettings{}, AppOrigins: api.GetCorsAllowedOrigins(),
		OrgConnections: enabled,
	})
	require.NoError(t, err)
	ctx := authz.WithCredentialKind(authz.WithMembership(context.Background(), orgAdmins{}), authz.CredentialKindPerson)
	_, err = conns.Service.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "olivia", OrgID: "org-1", IntegrationID: "github", Name: "org key",
		Kind: connections.APIKeyKindAPIKey, Fields: map[string]string{"api_key": "org-key"},
	})
	return err
}

// TestOrgConnectionsGateReachesTheConnectionService pins the rollout gate for
// org-owned connections on the path production takes: an env's KCL
// (config_gen.workloadEnvMap for the api root) projects
// RELIANT_ORGS_CONNECTIONS_ENABLED, `server api` loads its ApiServerConfig
// from it, app.APIOrgConnections (what newAPIConnections reads) decides the
// gate, and app.NewAPIConnections builds the connection service with it. At
// the schema default, and in this repo's dev and demo envs, the gate is off
// and an org admin's create is refused; set, it is admitted. The service tests
// call WithOrgConnections directly, so without this a flipped default, a
// dropped env-map entry or a dropped wiring would go unseen.
func TestOrgConnectionsGateReachesTheConnectionService(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/org_connections_gate.k"
	const name = "RELIANT_ORGS_CONNECTIONS_ENABLED"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantEnv  string
		wantOn   bool
	}{
		{"gate_default", "false", false},
		{"dev_api", "false", false},
		{"demo_api", "false", false},
		{"gate_on", "true", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env[name]
			require.True(t, ok, "the api server's projected env has no %s: %v", name, env)
			require.Equal(t, tc.wantEnv, got)

			isolateServerAPIConfigEnv(t)
			t.Setenv(name, got.(string))
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			api, err := config.LoadApiServerConfig(cmd)
			require.NoError(t, err)
			require.Equal(t, tc.wantOn, api.GetOrgs().GetConnectionsEnabled(), "ApiServerConfig.Orgs as loaded")
			require.Equal(t, tc.wantOn, app.APIOrgConnections(api))

			err = createOrgConnectionThrough(t, api, app.APIOrgConnections(api))
			if tc.wantOn {
				require.NoError(t, err, "enabled: an org admin creates an org connection")
			} else {
				require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err), "off: refused, got %v", err)
			}
		})
	}
}

// The fast half: with RELIANT_ORGS_CONNECTIONS_ENABLED unset, as every env
// ships, the API server's connection service refuses an org admin's create;
// set, it admits it. No KCL, just the load and the real wiring.
func TestOrgConnectionsGateIsOffByDefault(t *testing.T) {
	isolateServerAPIConfigEnv(t)
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	api, err := config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	require.False(t, app.APIOrgConnections(api))
	err = createOrgConnectionThrough(t, api, app.APIOrgConnections(api))
	require.Equal(t, connections.CodeFailedPrecondition, connections.CodeOf(err), "got %v", err)

	t.Setenv("RELIANT_ORGS_CONNECTIONS_ENABLED", "true")
	api, err = config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	require.True(t, app.APIOrgConnections(api))
	require.NoError(t, createOrgConnectionThrough(t, api, app.APIOrgConnections(api)))
}
