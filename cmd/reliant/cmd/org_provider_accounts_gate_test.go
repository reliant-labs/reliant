// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/org_provider_account"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/pkg/config"
)

// addOrgProviderAccountThrough builds the API server's OrgProviderAccountService
// from api the way newOrgProviderAccount does (app.NewOrgProviderAccount reads
// the gate from it) and has an org admin, signed in as a person, add an org
// API key with it.
func addOrgProviderAccountThrough(t *testing.T, api *configv1.ApiServerConfig) error {
	t.Helper()
	svc, err := app.NewOrgProviderAccount(api, org_provider_account.Deps{
		Logger: slog.Default(), Config: &config.Config{}, Store: db.NewTestRepo(t),
		ValidateKey: func(context.Context, models.Family, string) (bool, string) { return true, "" },
	})
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "olivia")
	ctx = auth.WithCredential(authz.WithMembership(ctx, orgAdmins{}), auth.CredentialSession)
	_, err = svc.AddOrgProviderAPIKey(ctx, connect.NewRequest(&pb.AddOrgProviderAPIKeyRequest{
		OrgId: "org-1", Provider: "anthropic", ApiKey: "sk-ant-gate-test-0001", Label: "Acme prod",
	}))
	return err
}

// TestOrgProviderAccountsGateReachesTheService pins the rollout gate for
// org-owned AI provider accounts on the path production takes: an env's KCL
// (config_gen.workloadEnvMap for the api root) projects
// RELIANT_ORGS_PROVIDER_ACCOUNTS_ENABLED, `server api` loads its
// ApiServerConfig from it, and app.NewOrgProviderAccount (what
// newOrgProviderAccount calls) builds the service with the gate it reads. At
// the schema default, and in this repo's dev and demo envs, the gate is off and
// an org admin's add is refused; set, it is admitted. The service tests set
// Deps.AddingEnabled directly, so without this a flipped default, a dropped
// env-map entry or a dropped wiring would go unseen.
func TestOrgProviderAccountsGateReachesTheService(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/org_provider_accounts_gate.k"
	const name = "RELIANT_ORGS_PROVIDER_ACCOUNTS_ENABLED"
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
			require.Equal(t, tc.wantOn, api.GetOrgs().GetProviderAccountsEnabled(), "ApiServerConfig.Orgs as loaded")
			require.Equal(t, tc.wantOn, app.APIOrgProviderAccounts(api))

			err = addOrgProviderAccountThrough(t, api)
			if tc.wantOn {
				require.NoError(t, err, "enabled: an org admin adds an org account")
			} else {
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "off: refused, got %v", err)
			}
		})
	}
}

// The fast half: with RELIANT_ORGS_PROVIDER_ACCOUNTS_ENABLED unset, as every
// env ships, the API server's service refuses an org admin's add; set, it
// admits it. No KCL, just the load and the real wiring.
func TestOrgProviderAccountsGateIsOffByDefault(t *testing.T) {
	isolateServerAPIConfigEnv(t)
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	api, err := config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	require.False(t, app.APIOrgProviderAccounts(api))
	err = addOrgProviderAccountThrough(t, api)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "got %v", err)

	t.Setenv("RELIANT_ORGS_PROVIDER_ACCOUNTS_ENABLED", "true")
	api, err = config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	require.True(t, app.APIOrgProviderAccounts(api))
	require.NoError(t, addOrgProviderAccountThrough(t, api))
}
