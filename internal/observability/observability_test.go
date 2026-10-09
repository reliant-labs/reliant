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
		ServiceName:       "reliant-api-server",
		Environment:       "development",
		Version:           "test",
		InstanceID:        "test-instance",
		PrometheusEnabled: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown()) })

	recorder := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "go_gc_duration_seconds")
}

func TestInitDisabledPrometheusEndpointReturnsNotFound(t *testing.T) {
	provider, err := Init(Config{ServiceName: "reliant-api-server", PrometheusEnabled: false})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown()) })

	recorder := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestInitRejectsEnabledRuntimeWithoutCollector(t *testing.T) {
	_, err := Init(Config{ServiceName: "reliant-api-server", OTLPEnabled: true})
	require.ErrorContains(t, err, "OTEL_EXPORTER_OTLP_ENDPOINT is empty")
}

func TestInitRejectsOTLPHTTPProtocol(t *testing.T) {
	_, err := Init(Config{
		ServiceName:  "reliant-api-server",
		OTLPEnabled:  true,
		OTLPProtocol: "http/protobuf",
		OTLPEndpoint: "http://otel-collector:4317",
	})
	require.ErrorContains(t, err, "Forge requires OTLP/gRPC")
}

func TestInitRejectsOTLPHTTPPort(t *testing.T) {
	_, err := Init(Config{
		ServiceName:  "reliant-api-server",
		OTLPEnabled:  true,
		OTLPEndpoint: "http://otel-collector:4318/v1/traces",
	})
	require.ErrorContains(t, err, "OTLP/HTTP port 4318")
}

func TestInitEnabledModeAcceptsGRPCCollectorEndpoint(t *testing.T) {
	provider, err := Init(Config{
		ServiceName:       "reliant-api-server",
		Environment:       "test",
		Version:           "test-version",
		InstanceID:        "test-instance",
		OTLPEnabled:       true,
		OTLPEndpoint:      "http://127.0.0.1:4317",
		PrometheusEnabled: true,
	})
	require.NoError(t, err)
	require.NotNil(t, provider)
}

func TestConfigFromEnvCurrentProductionRolloutDisabled(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("RELIANT_ENV", "")
	t.Setenv("OTEL_ENABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	cfg := ConfigFromEnv("reliant-api-server")
	require.Equal(t, "production", cfg.Environment)
	require.False(t, cfg.OTLPEnabled)
	require.Empty(t, cfg.OTLPEndpoint)
	require.NoError(t, func() error { _, err := Init(cfg); return err }())
}

func TestConfigFromEnvProductionEnabledWithoutCollectorFails(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("RELIANT_ENV", "")
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	cfg := ConfigFromEnv("reliant-api-server")
	require.True(t, cfg.OTLPEnabled)
	_, err := Init(cfg)
	require.ErrorContains(t, err, "OTEL_EXPORTER_OTLP_ENDPOINT is empty")
}

func TestConfigFromEnvEnvironmentPrecedence(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "sentry-environment")
	t.Setenv("ENVIRONMENT", "deployment-environment")
	t.Setenv("RELIANT_ENV", "legacy-environment")
	cfg := ConfigFromEnv("reliant-api-server")
	require.Equal(t, "sentry-environment", cfg.Environment)

	t.Setenv("SENTRY_ENVIRONMENT", "")
	cfg = ConfigFromEnv("reliant-api-server")
	require.Equal(t, "deployment-environment", cfg.Environment)

	t.Setenv("ENVIRONMENT", "")
	cfg = ConfigFromEnv("reliant-api-server")
	require.Equal(t, "legacy-environment", cfg.Environment)
	require.NotEmpty(t, strings.TrimSpace(cfg.InstanceID))
}
