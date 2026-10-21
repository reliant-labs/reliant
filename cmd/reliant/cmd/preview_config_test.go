// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/serverapi"
)

// The preview settings control-plane sets on the API server and the worker
// (WORKSPACE_BASE_DOMAIN everywhere; dev's http scheme and host-mapped port)
// reach both roles; a scheme or port the proxy cannot serve stops the role.
func TestPreviewEnvContract(t *testing.T) {
	isolateServerRoleEnv(t)
	for _, name := range []string{"PROXY_HOST", "WORKSPACE_BASE_DOMAIN", "WORKSPACE_URL_SCHEME", "WORKSPACE_URL_PORT"} {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
	load := func() (tools.PreviewSettings, tools.PreviewSettings, error) {
		t.Helper()
		apiCmd, _, err := NewRootCmd().Find([]string{"server", "api"})
		require.NoError(t, err)
		var api serverapi.Options
		if err := loadServerAPIConfig(apiCmd, &api); err != nil {
			return tools.PreviewSettings{}, tools.PreviewSettings{}, err
		}
		workerCmd, _, err := NewRootCmd().Find([]string{"server", "worker"})
		require.NoError(t, err)
		var worker workerOptions
		err = loadServerWorkerConfig(workerCmd, &worker)
		return api.Preview, worker.Preview, err
	}
	api, worker, err := load()
	require.NoError(t, err)
	assert.Equal(t, tools.PreviewSettings{WorkspaceURLScheme: "https"}, api)
	assert.Equal(t, api, worker)

	t.Setenv("WORKSPACE_BASE_DOMAIN", "preview.reliant.test")
	t.Setenv("WORKSPACE_URL_SCHEME", "http")
	t.Setenv("WORKSPACE_URL_PORT", "28080")
	t.Setenv("PROXY_HOST", " localhost:28080 ")
	api, worker, err = load()
	require.NoError(t, err)
	want := tools.PreviewSettings{ProxyHost: "localhost:28080", WorkspaceBaseDomain: "preview.reliant.test",
		WorkspaceURLScheme: "http", WorkspaceURLPort: 28080}
	assert.Equal(t, want, api)
	assert.Equal(t, want, worker)

	for name, value := range map[string]string{"WORKSPACE_URL_SCHEME": "ftp", "WORKSPACE_URL_PORT": "http"} {
		t.Run(name+"="+value, func(t *testing.T) {
			t.Setenv(name, value)
			_, _, err := load()
			require.Error(t, err)
		})
	}
}
