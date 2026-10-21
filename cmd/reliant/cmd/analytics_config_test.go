// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/serverapi"
)

var analyticsEnv = []string{"RELIANT_ANALYTICS_DISABLED"}

// The API server and the worker record product events unless the kill switch
// is set; there is nothing else to configure.
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
		var worker workerOptions
		require.NoError(t, loadServerWorkerConfig(workerCmd, &worker))
		return api.Analytics, worker.Analytics
	}
	api, worker := load()
	assert.Equal(t, analytics.Settings{}, api)
	assert.Equal(t, analytics.Settings{}, worker)

	t.Setenv("RELIANT_ANALYTICS_DISABLED", "true")
	want := analytics.Settings{Disabled: true}
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
