// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initTracing sets up the OTel TracerProvider with an OTLP gRPC exporter.
func initTracing(cfg Config) (*sdktrace.TracerProvider, error) {
	ctx := context.Background()

	// otlptracegrpc.WithEndpoint wants a "host:port", not a full URL — passing
	// "http://host:port" produces a malformed double-prefixed URL. Strip the
	// scheme if present so OTEL_EXPORTER_OTLP_ENDPOINT can be set to either
	// form in the deployment manifest.
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.OTLPEndpoint, "https://"), "http://")
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}
	if cfg.OTLPInsecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	// resource.Default() ships the SDK's bundled semconv schema URL (v1.40.0),
	// which conflicts with our pinned semconv import. resource.Merge rejects
	// mismatched schema URLs. Build a single schema-pinned resource directly
	// — service identity is what spans actually need.
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(cfg.ServiceName),
		semconv.ServiceVersionKey.String(cfg.Version),
		semconv.DeploymentEnvironmentKey.String(cfg.Environment),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	return tp, nil
}
