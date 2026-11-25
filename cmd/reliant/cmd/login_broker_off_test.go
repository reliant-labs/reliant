// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// identityEnv is every variable the managed identity fills on reliant-api
// (forge.IdentityRef), the broker token among them.
var identityEnv = []string{
	"OIDC_CLIENT_ID", "OIDC_REDIRECT_URI", "OIDC_SCOPES", "IDP_LOGIN_URI", "IDP_BASE", "IDP_HOST",
	"IDP_BROKER_TOKEN", "IDP_BROKER_TOKEN_FILE", "IDP_LOGIN_POLICY",
}

// loadAPIServerConfig is `server api`'s ApiServerConfig as it loads from this
// process's environment, with nothing of the identity's but what env sets.
func loadAPIServerConfig(t *testing.T, env map[string]string) *config.ApiServerConfig {
	t.Helper()
	isolateServerAPIConfigEnv(t)
	for _, name := range identityEnv {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	api, err := config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	return api
}

// signInSessionsFor is what the api root's graph builds for the sign-in
// session node (graph newSignInSessions: app.NewSignInSessions over the
// database); nil is an absent node, and then the listener mounts no /auth/*
// (app.NewAPIListener mounts the broker only with a SignIn) and UserAuth
// resolves no session cookie.
func signInSessionsFor(t *testing.T, api *config.ApiServerConfig) *app.SignInSessions {
	t.Helper()
	s, err := app.NewSignInSessions(app.SignInSessionsDeps{
		API: api, DB: db.NewTestRepo(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	return s
}

// The fast half: `server api` loaded with no identity configured — how every
// deployment without a forge.ManagedIdentity runs it, prod included today —
// builds no sign-in sessions, so the native sign-in stays off. The broker
// turns on only with both an identity's client id and its broker token.
func TestLoginBrokerIsOffWithoutAnIdentity(t *testing.T) {
	api := loadAPIServerConfig(t, nil)
	require.Error(t, app.LoginBrokerConfigured(api))
	require.Nil(t, signInSessionsFor(t, api), "no identity: no sign-in sessions, no /auth/* routes")

	api = loadAPIServerConfig(t, map[string]string{"OIDC_CLIENT_ID": "native-broker-client"})
	require.Nil(t, signInSessionsFor(t, api), "a client id without the broker token is not a configured broker")

	api = loadAPIServerConfig(t, map[string]string{"IDP_BROKER_TOKEN": "broker-token"})
	require.Nil(t, signInSessionsFor(t, api), "a broker token without a client id is not a configured broker")

	api = loadAPIServerConfig(t, map[string]string{"OIDC_CLIENT_ID": "native-broker-client", "IDP_BROKER_TOKEN": "broker-token"})
	require.NoError(t, app.LoginBrokerConfigured(api))
	s := signInSessionsFor(t, api)
	require.NotNil(t, s, "a configured identity turns the broker on")
	require.NotNil(t, s.Manager)
}

// TestLoginBrokerStaysOffWithoutAnIdentity pins the same on the path a
// deployment takes: an env's KCL (config_gen.workloadEnvMap for the api root)
// projects the identity's variables, `server api` loads its ApiServerConfig
// from them, and app.NewSignInSessions decides whether the broker exists. At
// the schema default (prod's ApiServerConfig, composed by control-plane) and
// in this repo's demo, desktop and release envs nothing is projected and the
// broker stays off; an identity's client id, with the broker token its secret
// store delivers, turns it on.
func TestLoginBrokerStaysOffWithoutAnIdentity(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/login_broker_off.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantOn   bool
	}{
		{"identity_default", false},
		{"demo_api", false},
		{"desktop_api", false},
		{"release_api", false},
		{"identity_set", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			projected := map[string]string{}
			for _, name := range identityEnv {
				if v, ok := env[name].(string); ok && v != "" {
					projected[name] = v
				}
			}
			if !tc.wantOn {
				// OIDC_SCOPES has a schema default whatever the identity;
				// every value an identity fills is empty.
				named := map[string]string{}
				for name, v := range projected {
					if name != "OIDC_SCOPES" {
						named[name] = v
					}
				}
				require.Empty(t, named, "no identity, yet the api server's env carries identity values")
			} else {
				require.Equal(t, "native-broker-client", projected["OIDC_CLIENT_ID"])
				// The broker token is a secret: the env map names it, the
				// secret store delivers its value.
				_, isLiteral := env["IDP_BROKER_TOKEN"].(string)
				require.False(t, isLiteral, "the broker token must not be projected as a literal")
				projected["IDP_BROKER_TOKEN"] = "broker-token"
			}

			api := loadAPIServerConfig(t, projected)
			s := signInSessionsFor(t, api)
			if tc.wantOn {
				require.NotNil(t, s, "an identity turns the broker on")
			} else {
				require.Nil(t, s, "no identity: no sign-in sessions, no /auth/* routes")
			}
		})
	}
}
