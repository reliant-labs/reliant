// Copyright (c) 2025 Reliant Labs
package cmd

import (
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/observability"
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
