// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/reliant-labs/reliant/internal/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Config holds observability configuration.
type Config struct {
	ServiceName string // e.g. "reliant-api", "reliant-worker"
	Environment string // e.g. "production", "development"
	Version     string // build version

	// OTel tracing
	OTLPEndpoint string // OTLP HTTP endpoint for traces (empty = disabled)
	OTLPInsecure bool   // use HTTP instead of HTTPS for OTLP

	// Prometheus
	PrometheusEnabled bool // expose /metrics endpoint
}

// ConfigFromEnv builds a Config from environment variables.
func ConfigFromEnv(serviceName string) Config {
	return Config{
		ServiceName:       getEnv("OTEL_SERVICE_NAME", serviceName),
		Environment:       getEnv("RELIANT_ENV", "development"),
		Version:           getEnv("RELIANT_VERSION", "dev"),
		OTLPEndpoint:      os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		OTLPInsecure:      os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") != "false",
		PrometheusEnabled: os.Getenv("PROMETHEUS_ENABLED") != "false",
	}
}

// Provider holds the initialized observability providers.
type Provider struct {
	config     Config
	shutdownFn func(ctx context.Context) error
}

// Init initializes Prometheus metrics and OTel tracing based on the config.
// Must be called early in startup, after logging.
func Init(cfg Config) (*Provider, error) {
	p := &Provider{config: cfg}
	var shutdowns []func(ctx context.Context) error

	// Set up W3C Trace Context propagation globally.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Initialize Prometheus metrics registry and collectors.
	initMetrics()

	// Wire the dead-end error counter into the slog metrics handler.
	logging.SetDeadEndErrorCounter(DeadEndErrorsTotal)

	// Initialize OTel tracing if endpoint is configured.
	if cfg.OTLPEndpoint != "" {
		tp, err := initTracing(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to init OTel tracing: %w", err)
		}
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	// Initialize OTel metrics provider backed by Prometheus.
	if cfg.PrometheusEnabled {
		mp, err := initOTelMetrics()
		if err != nil {
			return nil, fmt.Errorf("failed to init OTel metrics: %w", err)
		}
		shutdowns = append(shutdowns, mp.Shutdown)
	}

	p.shutdownFn = func(ctx context.Context) error {
		var firstErr error
		for _, fn := range shutdowns {
			if err := fn(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	return p, nil
}

// Shutdown flushes and closes all observability providers.
func (p *Provider) Shutdown() error {
	if p == nil || p.shutdownFn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.shutdownFn(ctx)
}

// MetricsHandler returns an HTTP handler for the /metrics endpoint.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// Registry is the global Prometheus registry used for all application metrics.
var Registry = prometheus.NewRegistry()

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
