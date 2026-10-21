// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"fmt"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/pkg/config"
)

// observabilitySettings is a server role's ObservabilityConfig in the shape
// internal/observability takes. Its environment falls back to the process's
// tier as the operator spelled it (the root config's RELIANT_ENV).
func observabilitySettings(cfg *configv1.ObservabilityConfig, base *config.ReliantConfig) observability.Settings {
	environment := cfg.GetEnvironment()
	if environment == "" {
		environment = base.GetRuntime().GetEnvironment()
	}
	return observability.Settings{
		Environment:       environment,
		OTLPEndpoint:      cfg.GetOtlpEndpoint(),
		OTLPEnabled:       cfg.GetOtelEnabled(),
		PrometheusEnabled: cfg.GetPrometheusEnabled(),
	}
}

// sentrySettings is a server role's SentryConfig in the shape
// internal/telemetry takes. Events are filed under the deployment
// environment the deploy states (SENTRY_ENVIRONMENT, else ENVIRONMENT).
func sentrySettings(cfg *configv1.SentryConfig, obs *configv1.ObservabilityConfig) (telemetry.SentryConfig, error) {
	rate := cfg.GetTracesSampleRate()
	if rate < 0 || rate > 1 {
		return telemetry.SentryConfig{}, fmt.Errorf("config: SENTRY_TRACES_SAMPLE_RATE = %v is not a share between 0 and 1", rate)
	}
	return telemetry.SentryConfig{
		Enabled:          cfg.GetEnabled(),
		DSN:              cfg.GetDsn(),
		Environment:      obs.GetEnvironment(),
		TracesSampleRate: rate,
	}, nil
}
