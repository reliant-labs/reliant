// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

	// OTLPEndpoint is the vendor-neutral OTLP/gRPC collector address. It must
	// target gRPC port 4317 when OTLP is enabled.
	OTLPEndpoint string
	// OTLPEnabled is the explicit rollout gate. A disabled runtime never
	// configures OTLP, including in production before the rollout is activated.
	OTLPEnabled bool
	// OTLPProtocol rejects a deployment's OTLP/HTTP selection because Forge
	// Setup only configures OTLP/gRPC.
	OTLPProtocol string

	// PrometheusEnabled controls Reliant's existing custom Prometheus endpoint.
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
	environment := environmentFromEnv()
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	return Config{
		ServiceName:       serviceName,
		Environment:       environment,
		Version:           buildVersion(),
		InstanceID:        instanceID,
		OTLPEndpoint:      endpoint,
		OTLPEnabled:       os.Getenv("OTEL_ENABLED") == "true",
		OTLPProtocol:      strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")),
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

// environmentFromEnv follows deployment precedence: Sentry's explicit
// environment is authoritative, then the typed deployment ENVIRONMENT, with the
// legacy RELIANT_ENV retained only for standalone development compositions.
func environmentFromEnv() string {
	for _, key := range []string{"SENTRY_ENVIRONMENT", "ENVIRONMENT", "RELIANT_ENV"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "development"
}

var (
	metricsOnce       sync.Once
	prometheusEnabled atomic.Bool
)

func init() {
	prometheusEnabled.Store(true)
}

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
	if cfg.OTLPEnabled {
		if cfg.OTLPProtocol != "" && cfg.OTLPProtocol != "grpc" {
			return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL=%q is unsupported; Forge requires OTLP/gRPC", cfg.OTLPProtocol)
		}
		if strings.TrimSpace(cfg.OTLPEndpoint) == "" {
			return nil, fmt.Errorf("observability is enabled but OTEL_EXPORTER_OTLP_ENDPOINT is empty")
		}
		if err := validateGRPCEndpoint(cfg.OTLPEndpoint); err != nil {
			return nil, err
		}
	}

	metricsOnce.Do(initMetrics)
	prometheusEnabled.Store(cfg.PrometheusEnabled)
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

// MetricsHandler returns the existing application Prometheus endpoint when
// enabled. Forge's independent OTel Prometheus handler is not mounted here;
// OTLP metrics still export through Forge's periodic reader.
func MetricsHandler() http.Handler {
	if !prometheusEnabled.Load() {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func validateGRPCEndpoint(endpoint string) error {
	trimmed := strings.TrimSpace(endpoint)
	parseTarget := trimmed
	if !strings.Contains(parseTarget, "://") {
		parseTarget = "http://" + parseTarget
	}
	parsed, err := url.Parse(parseTarget)
	if err != nil {
		return fmt.Errorf("invalid OTEL_EXPORTER_OTLP_ENDPOINT %q: %w", endpoint, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q must use an OTLP/gRPC endpoint", endpoint)
	}
	host := parsed.Host
	if strings.HasSuffix(host, ":4318") {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q uses OTLP/HTTP port 4318; Forge requires OTLP/gRPC port 4317", endpoint)
	}
	if !strings.HasSuffix(host, ":4317") {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q must target OTLP/gRPC port 4317", endpoint)
	}
	if parsed.Path != "" && parsed.Path != "/" && parsed.Host != "" {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q must not include an OTLP/HTTP path", endpoint)
	}
	return nil
}

// Registry is the global Prometheus registry used for all application metrics.
var Registry = prometheus.NewRegistry()

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
