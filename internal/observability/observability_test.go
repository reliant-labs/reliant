// Copyright (c) 2025 Reliant Labs
package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitDisabledModePreservesPrometheusEndpoint(t *testing.T) {
	provider, err := Init(Config{
		ServiceName: "reliant-api-server",
		Environment: "development",
		Version:     "test",
		InstanceID:  "test-instance",
		OTLPEnabled: false,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown()) })

	recorder := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "go_gc_duration_seconds")
}

func TestInitRejectsEnabledRuntimeWithoutCollector(t *testing.T) {
	_, err := Init(Config{ServiceName: "reliant-api-server", OTLPEnabled: true})
	require.ErrorContains(t, err, "OTEL_EXPORTER_OTLP_ENDPOINT is empty")
}

func TestInitEnabledModeAcceptsCollectorEndpoint(t *testing.T) {
	provider, err := Init(Config{
		ServiceName:  "reliant-api-server",
		Environment:  "test",
		Version:      "test-version",
		InstanceID:   "test-instance",
		OTLPEnabled:  true,
		OTLPEndpoint: "http://127.0.0.1:4317",
	})
	require.NoError(t, err)
	require.NotNil(t, provider)
}

func TestConfigFromEnvProductionRequiresCollector(t *testing.T) {
	t.Setenv("RELIANT_ENV", "production")
	t.Setenv("OTEL_ENABLED", "false")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	cfg := ConfigFromEnv("reliant-api-server")
	require.True(t, cfg.OTLPEnabled)
	require.Empty(t, cfg.OTLPEndpoint)
}

func TestConfigFromEnvAllowsExplicitDevelopmentDisable(t *testing.T) {
	t.Setenv("RELIANT_ENV", "development")
	t.Setenv("OTEL_ENABLED", "false")
	cfg := ConfigFromEnv("reliant-api-server")
	require.False(t, cfg.OTLPEnabled)
	require.Equal(t, "reliant-api-server", cfg.ServiceName)
	require.NotEmpty(t, strings.TrimSpace(cfg.InstanceID))
}
