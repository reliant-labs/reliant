// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/controlplane"
)

// controlPlaneEnv is every variable ControlPlaneConfig reads, plus the two
// removed aliases, so a test that sets one starts from a clean environment.
var controlPlaneEnv = []string{"RELIANT_CONTROL_PLANE_URL", "CONTROL_PLANE_API_URL", "CONTROL_PLANE_BASE_URL", "INTERNAL_SERVICE_SECRET"}

// controlPlaneRoles loads each server role's typed config the way its RunE
// does and returns the control plane it hands its server package.
var controlPlaneRoles = map[string]func(*cobra.Command) (controlplane.Endpoint, error){
	"server api": func(cmd *cobra.Command) (controlplane.Endpoint, error) {
		var opts apiOptions
		err := loadServerAPIConfig(cmd, &opts)
		return opts.ControlPlane, err
	},
	"server worker": func(cmd *cobra.Command) (controlplane.Endpoint, error) {
		var opts workerOptions
		err := loadServerWorkerConfig(cmd, &opts)
		return opts.ControlPlane, err
	},
	"server gateway": func(cmd *cobra.Command) (controlplane.Endpoint, error) {
		role, err := loadGatewayRole(cmd)
		return role.controlPlane, err
	},
}

// TestControlPlaneEnvContract pins that every server role reads the control
// plane through ServerProcessConfig: RELIANT_CONTROL_PLANE_URL (an empty one
// is unset) and INTERNAL_SERVICE_SECRET, each trimmed. control-plane sets
// RELIANT_CONTROL_PLANE_URL on the api, the worker and the gateway (dev host
// env, the gateway pod, lib/env.k), so that name is a contract. The two older
// names, CONTROL_PLANE_API_URL and CONTROL_PLANE_BASE_URL, were removed and
// are not read.
func TestControlPlaneEnvContract(t *testing.T) {
	const notice = "is a deprecated name for RELIANT_CONTROL_PLANE_URL"
	cases := []struct {
		name string
		env  map[string]string
		want controlplane.Endpoint
	}{
		{name: "nothing set: self-hosted"},
		{name: "RELIANT_CONTROL_PLANE_URL", env: map[string]string{"RELIANT_CONTROL_PLANE_URL": "http://admin-server:8090"},
			want: controlplane.Endpoint{URL: "http://admin-server:8090"}},
		{name: "both, trimmed", env: map[string]string{"RELIANT_CONTROL_PLANE_URL": " http://admin-server:8090 ", "INTERNAL_SERVICE_SECRET": " s "},
			want: controlplane.Endpoint{URL: "http://admin-server:8090", InternalServiceSecret: "s"}},
		{name: "secret alone is still self-hosted", env: map[string]string{"INTERNAL_SERVICE_SECRET": "s"},
			want: controlplane.Endpoint{InternalServiceSecret: "s"}},
		{name: "RELIANT_CONTROL_PLANE_URL empty is unset", env: map[string]string{"RELIANT_CONTROL_PLANE_URL": ""}},
		{name: "CONTROL_PLANE_API_URL is not read", env: map[string]string{"CONTROL_PLANE_API_URL": "http://api"}},
		{name: "CONTROL_PLANE_BASE_URL is not read", env: map[string]string{"CONTROL_PLANE_BASE_URL": "http://base"}},
		{name: "the removed names do not override the primary", env: map[string]string{"RELIANT_CONTROL_PLANE_URL": "http://primary", "CONTROL_PLANE_API_URL": "http://api", "CONTROL_PLANE_BASE_URL": "http://base"},
			want: controlplane.Endpoint{URL: "http://primary"}},
	}
	for role, load := range controlPlaneRoles {
		for _, tc := range cases {
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				isolateControlPlaneEnv(t)
				for name, value := range tc.env {
					t.Setenv(name, value)
				}
				cmd, _, err := NewRootCmd().Find(strings.Fields(role))
				require.NoError(t, err)
				var stderr bytes.Buffer
				cmd.SetErr(&stderr)

				got, err := load(cmd)
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)

				assert.NotContains(t, stderr.String(), notice)
			})
		}
	}
}

// The control plane is a server concern: the daemon (which loads only the
// root config) does not read it, and a role reads it from the shared config
// file under its own key.
func TestControlPlaneScope(t *testing.T) {
	t.Run("daemon start does not read it", func(t *testing.T) {
		isolateControlPlaneEnv(t)
		t.Setenv("RELIANT_CONTROL_PLANE_URL", "http://admin-server:8090")
		file := filepath.Join(t.TempDir(), "reliant.yaml")
		require.NoError(t, os.WriteFile(file, []byte("controlPlane:\n  url: http://admin-server:8090\n"), 0o600))
		cmd, _, err := NewRootCmd().Find([]string{"daemon", "start"})
		require.NoError(t, err)
		require.NoError(t, cmd.ParseFlags([]string{"--config", file}))
		_, err = loadRootConfig(cmd)
		require.Error(t, err, "controlPlane is not a key any config `daemon start` loads")
	})
	t.Run("from RELIANT_CONFIG", func(t *testing.T) {
		isolateControlPlaneEnv(t)
		file := filepath.Join(t.TempDir(), "reliant.yaml")
		require.NoError(t, os.WriteFile(file, []byte("controlPlane:\n  url: http://admin-server:8090\n  internalServiceSecret: s\n"), 0o600))
		t.Setenv("RELIANT_CONFIG", file)
		for role, load := range controlPlaneRoles {
			cmd, _, err := NewRootCmd().Find(strings.Fields(role))
			require.NoError(t, err)
			got, err := load(cmd)
			require.NoError(t, err, role)
			assert.Equal(t, controlplane.Endpoint{URL: "http://admin-server:8090", InternalServiceSecret: "s"}, got, role)
		}
	})
}

func isolateControlPlaneEnv(t *testing.T) {
	t.Helper()
	for _, name := range append(append(controlPlaneEnv, serverAPIConfigEnv...), rootConfigEnv...) {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
	isolateServerRoleEnv(t)
}
