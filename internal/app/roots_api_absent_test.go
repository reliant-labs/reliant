// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/reliant-labs/forge/pkg/graphkit"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/pkg/config"
)

// A deployment with no control plane has no Reliant key to heal, and one
// receiving no GitHub events has no access to refresh: each constructor says
// so by building nothing.
func TestAPIAbsentLoopsBuildNothing(t *testing.T) {
	sweep, err := NewReliantKeySweep(ReliantKeySweepDeps{Runtime: &ProcessRuntime{}})
	require.NoError(t, err)
	require.Nil(t, sweep, "no healer, no sweep")

	access, err := NewGitHubAccess(GitHubAccessDeps{Integrations: &IntegrationSettings{}})
	require.NoError(t, err)
	require.Nil(t, access, "no webhook secret, no refresher")

	senders, err := NewTriggerSenders(TriggerSendersDeps{Integrations: &IntegrationSettings{}})
	require.NoError(t, err)
	require.Nil(t, senders, "no webhook secret, nobody to look up")

	loop, err := NewGitHubAccessLoop(GitHubAccessLoopDeps{})
	require.NoError(t, err)
	require.Nil(t, loop, "no refresher, no loop")

	alerting, err := NewTriggerAlerting(TriggerAlertingDeps{Integrations: &IntegrationSettings{}})
	require.NoError(t, err)
	require.Nil(t, alerting, "no control plane, plain webhooks")
}

// What a built api graph supervises: without a control plane and a webhook
// secret, neither the Reliant-key sweep nor the GitHub access loop is a node
// at all; with both, both are. Built for real, on a migrated database, so
// the generated accessors' absence handling is what is under test.
func TestAPIOmitsAbsentLoops(t *testing.T) {
	cases := []struct {
		name    string
		process *config.ServerProcessConfig
		want    bool
	}{
		{name: "self-hosted, no GitHub events", process: &config.ServerProcessConfig{}},
		{name: "hosted, receiving GitHub events", want: true, process: &config.ServerProcessConfig{
			ControlPlane: &configv1.ControlPlaneConfig{Url: "http://control-plane.invalid", InternalServiceSecret: "secret"},
			Integrations: &configv1.IntegrationsConfig{
				GithubWebhookSecret: "webhook-secret",
				VaultKey:            "v1:c3VyZmFjZS12YXVsdC1rZXktMzItYnl0ZXMtbG9uZyE=",
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := storetest.New(t)
			tc.process.DataDir = t.TempDir()
			tc.process.Analytics = &configv1.AnalyticsConfig{Disabled: true}
			g := &Graph{
				Ctx:                 context.Background(),
				Cfg:                 &config.Config{},
				Log:                 slog.Default(),
				ServerProcessConfig: tc.process,
				DatabaseConfig: &config.DatabaseConfig{
					DatabaseDriver:      "postgres",
					DatabaseUrl:         env.DSN,
					DbMaxOpenConns:      2,
					DbMaxIdleConns:      1,
					DbSchemaWaitSeconds: 5,
				},
			}
			require.NoError(t, graphkit.Catch(func() {
				g.ReliantKeySweep()
				g.GitHubAccessLoop()
			}))
			t.Cleanup(func() { _ = g.Database().Close() })

			var supervised []string
			for _, n := range g.b.Nodes() {
				supervised = append(supervised, n.Name)
			}
			require.Contains(t, supervised, "db_repo")
			if tc.want {
				require.Contains(t, supervised, "reliant_key_sweep")
				require.Contains(t, supervised, "github_access_loop")
			} else {
				require.NotContains(t, supervised, "reliant_key_sweep")
				require.NotContains(t, supervised, "github_access_loop")
			}
		})
	}
}
