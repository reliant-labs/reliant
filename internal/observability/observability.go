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
	dto "github.com/prometheus/client_model/go"
	forgeobserve "github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Config holds observability configuration.
type Config struct {
	// ServiceName is the canonical workload identity (for example,
	// "reliant-api-server"), not a collector-specific name.
	ServiceName string
	Environment string
	Version     string
	InstanceID  string

	// OTLPEndpoint is the vendor-neutral OTLP collector base URL. Reliant
	// does not infer a wire protocol from its port or scheme; the Forge
	// runtime owns protocol selection.
	OTLPEndpoint string
	// OTLPEnabled is the explicit rollout gate. A disabled runtime never
	// configures OTLP, including in production before the rollout is activated.
	OTLPEnabled bool

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
		if strings.TrimSpace(cfg.OTLPEndpoint) == "" {
			return nil, fmt.Errorf("observability is enabled but OTEL_EXPORTER_OTLP_ENDPOINT is empty")
		}
		if err := validateOTLPEndpoint(cfg.OTLPEndpoint); err != nil {
			return nil, err
		}
	}

	// TODO(forge#617): remove once the forge pin includes #617, which makes
	// observe.Setup install this propagator even when export is off. Until
	// then Setup only sets it in the OTLP branch, which would turn NATS and
	// Connect propagation into no-ops whenever a binary runs with OTEL off.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

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

// MetricsHandler returns the application Prometheus endpoint when enabled. It
// serves Reliant's custom registry plus the OTel instrumentation metrics
// (otelhttp, otelconnect) that Forge's Prometheus reader writes to the default
// registry.
func MetricsHandler() http.Handler {
	if !prometheusEnabled.Load() {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(metricsGatherer, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

var metricsGatherer = prometheus.Gatherers{Registry, prometheus.GathererFunc(gatherOTelMetrics)}

// gatherOTelMetrics returns the default registry's families minus the Go and
// process collectors, which Registry already exposes; serving both would emit
// duplicate series and fail the scrape.
func gatherOTelMetrics() ([]*dto.MetricFamily, error) {
	families, err := prometheus.DefaultGatherer.Gather()
	kept := families[:0]
	for _, mf := range families {
		name := mf.GetName()
		if strings.HasPrefix(name, "go_") || strings.HasPrefix(name, "process_") {
			continue
		}
		kept = append(kept, mf)
	}
	return kept, err
}

// validateOTLPEndpoint checks that the endpoint is a parseable http(s) base URL
// (or bare host:port) without a signal-specific /v1/<signal> path. It never
// infers a protocol from the port: 4318, 443 and custom ports are all valid.
func validateOTLPEndpoint(endpoint string) error {
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
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q must use an http or https scheme", endpoint)
	}
	if parsed.Host == "" {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q has no host", endpoint)
	}
	for _, signal := range []string{"traces", "metrics", "logs"} {
		if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v1/"+signal) {
			return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT %q must be a base URL, not a /v1/%s signal path", endpoint, signal)
		}
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
