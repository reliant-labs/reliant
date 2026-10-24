// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/runenv"
)

// observabilityEnv is every variable ObservabilityConfig reads, plus the tier
// the environment falls back to.
var observabilityEnv = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_ENABLED", "PROMETHEUS_ENABLED", "ENVIRONMENT", "RELIANT_ENV", "NODE_ENV",
}

func isolateObservabilityEnv(t *testing.T) {
	t.Helper()
	isolateServerRoleEnv(t)
	runenv.ConfigureForTest(t, "") // loadRootConfig sets the tier; undo it after t
	for _, name := range observabilityEnv {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
}

// observabilityOf loads every server role and returns what each hands
// internal/observability.
func observabilityOf(t *testing.T) map[string]observability.Settings {
	t.Helper()
	var api apiOptions
	require.NoError(t, loadServerAPIConfig(roleCommand(t, "api"), &api))
	var worker workerOptions
	require.NoError(t, loadServerWorkerConfig(roleCommand(t, "worker"), &worker))
	gateway, err := loadGatewayRole(roleCommand(t, "gateway"))
	require.NoError(t, err)
	return map[string]observability.Settings{"api": api.Observability, "worker": worker.Observability, "gateway": gateway.observability}
}

// The prod values control-plane sets (prod/main.k) reach every role.
func TestObservabilityEnvContract(t *testing.T) {
	isolateObservabilityEnv(t)
	for role, got := range observabilityOf(t) {
		assert.Equal(t, observability.Settings{PrometheusEnabled: true}, got, "%s defaults", role)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel:4317")
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("PROMETHEUS_ENABLED", "false")
	t.Setenv("ENVIRONMENT", "production")
	for role, got := range observabilityOf(t) {
		assert.Equal(t, observability.Settings{Environment: "production", OTLPEndpoint: "http://otel:4317", OTLPEnabled: true}, got, role)
	}
}

// The environment telemetry is filed under: the deploy layer's ENVIRONMENT,
// else the tier as RELIANT_ENV spells it.
func TestObservabilityEnvironmentPrecedence(t *testing.T) {
	isolateObservabilityEnv(t)
	t.Setenv("ENVIRONMENT", "deployment-environment")
	t.Setenv("RELIANT_ENV", "legacy-environment")
	assert.Equal(t, "deployment-environment", observabilityOf(t)["api"].Environment)
	assert.Equal(t, "deployment-environment", observabilityOf(t)["worker"].Environment)

	t.Setenv("ENVIRONMENT", "")
	assert.Equal(t, "legacy-environment", observabilityOf(t)["gateway"].Environment)
}

// A value the server cannot use stops it at load (each was a silent default).
func TestObservabilityMisconfigurationFailsBoot(t *testing.T) {
	for name, value := range map[string]string{
		"OTEL_ENABLED":       "yes",
		"PROMETHEUS_ENABLED": "off",
	} {
		t.Run(name+"="+value, func(t *testing.T) {
			isolateObservabilityEnv(t)
			t.Setenv(name, value)
			var api apiOptions
			err := loadServerAPIConfig(roleCommand(t, "api"), &api)
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}

// roleCommand is `reliant server <role>`, built from the real root.
func roleCommand(t *testing.T, role string) *cobra.Command {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", role})
	require.NoError(t, err)
	return cmd
}
