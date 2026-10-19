// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/internal/serverapi"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/serverworker"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// observabilityEnv is every variable ObservabilityConfig and SentryConfig
// read, plus the tier the environment falls back to.
var observabilityEnv = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_ENABLED", "PROMETHEUS_ENABLED", "SENTRY_ENVIRONMENT", "ENVIRONMENT",
	"SENTRY_ENABLED", "SENTRY_DSN", "SENTRY_TRACES_SAMPLE_RATE", "RELIANT_ENV", "NODE_ENV",
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
	var api serverapi.Options
	require.NoError(t, loadServerAPIConfig(roleCommand(t, "api"), &api))
	var worker serverworker.Options
	require.NoError(t, loadServerWorkerConfig(roleCommand(t, "worker"), &worker))
	var gateway servergateway.Options
	require.NoError(t, loadServerGatewayConfig(roleCommand(t, "gateway"), &gateway))
	return map[string]observability.Settings{"api": api.Observability, "worker": worker.Observability, "gateway": gateway.Observability}
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
	t.Setenv("SENTRY_ENVIRONMENT", "production")
	for role, got := range observabilityOf(t) {
		assert.Equal(t, observability.Settings{Environment: "production", OTLPEndpoint: "http://otel:4317", OTLPEnabled: true}, got, role)
	}
}

// The environment telemetry is filed under: SENTRY_ENVIRONMENT, else the
// deploy layer's ENVIRONMENT, else the tier as RELIANT_ENV spells it.
func TestObservabilityEnvironmentPrecedence(t *testing.T) {
	isolateObservabilityEnv(t)
	t.Setenv("SENTRY_ENVIRONMENT", "sentry-environment")
	t.Setenv("ENVIRONMENT", "deployment-environment")
	t.Setenv("RELIANT_ENV", "legacy-environment")
	assert.Equal(t, "sentry-environment", observabilityOf(t)["api"].Environment)

	t.Setenv("SENTRY_ENVIRONMENT", "")
	assert.Equal(t, "deployment-environment", observabilityOf(t)["worker"].Environment)

	t.Setenv("ENVIRONMENT", "")
	assert.Equal(t, "legacy-environment", observabilityOf(t)["gateway"].Environment)
}

// The API server and worker report to Sentry as prod/main.k configures them.
func TestSentryEnvContract(t *testing.T) {
	isolateObservabilityEnv(t)
	var api serverapi.Options
	require.NoError(t, loadServerAPIConfig(roleCommand(t, "api"), &api))
	assert.Equal(t, telemetry.SentryConfig{Enabled: true}, api.Sentry, "defaults: on, nothing to report to")

	t.Setenv("SENTRY_DSN", "https://public@example.ingest.sentry.io/1")
	t.Setenv("SENTRY_ENABLED", "true")
	t.Setenv("SENTRY_ENVIRONMENT", "production")
	t.Setenv("SENTRY_TRACES_SAMPLE_RATE", "0.1")
	var worker serverworker.Options
	require.NoError(t, loadServerWorkerConfig(roleCommand(t, "worker"), &worker))
	assert.Equal(t, telemetry.SentryConfig{Enabled: true, DSN: "https://public@example.ingest.sentry.io/1",
		Environment: "production", TracesSampleRate: 0.1}, worker.Sentry)
}

// A value the server cannot use stops it at load (each was a silent default).
func TestObservabilityMisconfigurationFailsBoot(t *testing.T) {
	for name, value := range map[string]string{
		"OTEL_ENABLED":              "yes",
		"PROMETHEUS_ENABLED":        "off",
		"SENTRY_ENABLED":            "nope",
		"SENTRY_TRACES_SAMPLE_RATE": "ten percent",
	} {
		t.Run(name+"="+value, func(t *testing.T) {
			isolateObservabilityEnv(t)
			t.Setenv(name, value)
			var api serverapi.Options
			err := loadServerAPIConfig(roleCommand(t, "api"), &api)
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
	t.Run("SENTRY_TRACES_SAMPLE_RATE=1.5", func(t *testing.T) {
		isolateObservabilityEnv(t)
		t.Setenv("SENTRY_TRACES_SAMPLE_RATE", "1.5")
		var worker serverworker.Options
		err := loadServerWorkerConfig(roleCommand(t, "worker"), &worker)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SENTRY_TRACES_SAMPLE_RATE")
	})
}

// roleCommand is `reliant server <role>`, built from the real root.
func roleCommand(t *testing.T, role string) *cobra.Command {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", role})
	require.NoError(t, err)
	return cmd
}
