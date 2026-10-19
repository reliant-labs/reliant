// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/serverapi"
	"github.com/reliant-labs/reliant/internal/serverworker"
)

var analyticsEnv = []string{"RELIANT_ANALYTICS_DISABLED", "STATSIG_CLIENT_KEY", "SUPABASE_URL", "SUPABASE_ANON_KEY", "RELIANT_APP_DATA_DIR"}

// The API server and the worker send analytics as control-plane configures
// them (STATSIG_CLIENT_KEY in prod/main.k, SUPABASE_URL in the dev host env).
func TestAnalyticsEnvContract(t *testing.T) {
	isolateServerRoleEnv(t)
	for _, name := range analyticsEnv {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
	load := func() (analytics.Settings, analytics.Settings) {
		t.Helper()
		apiCmd, _, err := NewRootCmd().Find([]string{"server", "api"})
		require.NoError(t, err)
		var api serverapi.Options
		require.NoError(t, loadServerAPIConfig(apiCmd, &api))
		workerCmd, _, err := NewRootCmd().Find([]string{"server", "worker"})
		require.NoError(t, err)
		var worker serverworker.Options
		require.NoError(t, loadServerWorkerConfig(workerCmd, &worker))
		return api.Analytics, worker.Analytics
	}
	api, worker := load()
	assert.Equal(t, analytics.Settings{}, api)
	assert.Equal(t, analytics.Settings{}, worker)

	t.Setenv("RELIANT_ANALYTICS_DISABLED", "true")
	t.Setenv("STATSIG_CLIENT_KEY", "secret-key")
	t.Setenv("SUPABASE_URL", "https://example.supabase.co")
	t.Setenv("SUPABASE_ANON_KEY", "sb_publishable_x")
	t.Setenv("RELIANT_APP_DATA_DIR", " /var/lib/reliant ")
	want := analytics.Settings{Disabled: true, StatsigKey: "secret-key", SupabaseURL: "https://example.supabase.co",
		SupabaseAnonKey: "sb_publishable_x", AppDataDir: "/var/lib/reliant"}
	api, worker = load()
	assert.Equal(t, want, api)
	assert.Equal(t, want, worker)

	t.Setenv("RELIANT_ANALYTICS_DISABLED", "yes")
	apiCmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	var opts serverapi.Options
	err = loadServerAPIConfig(apiCmd, &opts)
	require.Error(t, err, "a malformed kill switch stops the process")
	assert.Contains(t, err.Error(), "RELIANT_ANALYTICS_DISABLED")
}
