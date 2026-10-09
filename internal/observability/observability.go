// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	forgeobserve "github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

// Config holds observability configuration.
type Config struct {
	// ServiceName is the canonical workload identity (for example,
	// "reliant-api-server"), not a collector-specific name.
	ServiceName string
	Environment string
	Version     string
	InstanceID  string

	// OTLPEndpoint is the vendor-neutral OTLP/gRPC collector address. Empty is
	// an explicit disabled mode in development and tests only.
	OTLPEndpoint string
	// OTLPEnabled distinguishes an intentionally disabled development/test
	// runtime from a production misconfiguration with no collector endpoint.
	OTLPEnabled bool

	// PrometheusEnabled retains the legacy toggle for the application metrics
	// registry. Forge always keeps its OTel Prometheus reader available.
	PrometheusEnabled bool
}

// ConfigFromEnv builds a Config from environment variables. Service identity is
// chosen by the owning binary and cannot be replaced with an arbitrary collector
// value through OTEL_SERVICE_NAME.
func ConfigFromEnv(serviceName string) Config {
	instanceID, err := os.Hostname()
	if err != nil || instanceID == "" {
		instanceID = getEnv("HOSTNAME", "reliant-local")
	}
	environment := getEnv("RELIANT_ENV", "development")
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	enabled := endpoint != "" && os.Getenv("OTEL_ENABLED") != "false"
	if isProduction(environment) {
		enabled = true
	}
	return Config{
		ServiceName:       serviceName,
		Environment:       environment,
		Version:           buildVersion(),
		InstanceID:        instanceID,
		OTLPEndpoint:      endpoint,
		OTLPEnabled:       enabled,
		PrometheusEnabled: os.Getenv("PROMETHEUS_ENABLED") != "false",
	}
}

func buildVersion() string {
	v := strings.TrimSpace(version.Get().Version)
	if v == "" || v == "unknown" {
		return "dev"
	}
	return v
}

func isProduction(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "production", "prod":
		return true
	default:
		return false
	}
}

var metricsOnce sync.Once

// Provider holds the initialized Forge observability runtime.
type Provider struct {
	config     Config
	shutdownFn func(ctx context.Context) error
}

// Init initializes the application Prometheus registry and Forge's OTel runtime.
// It validates configuration without probing the collector, so a temporary
// collector outage never prevents a process from serving traffic.
func Init(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.ServiceName) == "" {
		return nil, fmt.Errorf("observability service name is required")
	}
	if cfg.OTLPEnabled && strings.TrimSpace(cfg.OTLPEndpoint) == "" {
		return nil, fmt.Errorf("observability is enabled but OTEL_EXPORTER_OTLP_ENDPOINT is empty")
	}

	metricsOnce.Do(initMetrics)
	logging.SetDeadEndErrorCounter(DeadEndErrorsTotal)

	endpoint := ""
	if cfg.OTLPEnabled {
		endpoint = cfg.OTLPEndpoint
	}
	shutdown, _, err := forgeobserve.Setup(context.Background(), forgeobserve.Config{
		ServiceName:    cfg.ServiceName,
		ServiceVersion: cfg.Version,
		OTLPEndpoint:   endpoint,
		InstanceID:     cfg.InstanceID,
	})
	if err != nil {
		return nil, fmt.Errorf("setup Forge observability: %w", err)
	}
	return &Provider{config: cfg, shutdownFn: shutdown}, nil
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

// MetricsHandler returns an HTTP handler for the application Prometheus
// registry. Forge's OTel metric reader uses its own registry, so application
// metrics remain available without duplicate registrations.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// Registry is the global Prometheus registry used for all application metrics.
var Registry = prometheus.NewRegistry()

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
