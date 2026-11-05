package observe

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
)

// The constructors below pass an endpoint option only when the caller set
// Config.OTLPEndpoint. Otherwise the exporter reads OTEL_EXPORTER_OTLP_*
// itself — endpoint (and its per-signal variants), headers, timeout,
// compression and TLS material — which is how headers reach the wire. The one
// exception is OTEL_EXPORTER_OTLP_HEADERS_FILE (headers_file.go): its headers
// can change while the process runs, so they are attached per request by a
// RoundTripper (HTTP) or per-RPC credentials (gRPC).
//
// Every OTLP/HTTP exporter gets forge's http.Client (httpClientFor), never the
// SDK's default one: that keeps idle connections longer than a collector does,
// which fails exports at random (otlpIdleConnTimeout).

func newTraceExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	proto, err := protocolFor(signalTraces)
	if err != nil {
		return nil, err
	}
	url, typed, err := typedURL(cfg, proto, signalTraces)
	if err != nil {
		return nil, err
	}
	if proto == ProtocolGRPC {
		var opts []otlptracegrpc.Option
		if typed {
			opts = append(opts, otlptracegrpc.WithEndpointURL(url))
		}
		if src := headerSourceFor(signalTraces); src != nil {
			opts = append(opts, otlptracegrpc.WithDialOption(grpc.WithPerRPCCredentials(src)))
		}
		return otlptracegrpc.New(ctx, opts...)
	}
	var opts []otlptracehttp.Option
	if typed {
		opts = append(opts, otlptracehttp.WithEndpointURL(url))
	}
	hc, err := httpClientFor(signalTraces, headerSourceFor(signalTraces))
	if err != nil {
		return nil, err
	}
	opts = append(opts, otlptracehttp.WithHTTPClient(hc))
	return otlptracehttp.New(ctx, opts...)
}

func newMetricExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	proto, err := protocolFor(signalMetrics)
	if err != nil {
		return nil, err
	}
	url, typed, err := typedURL(cfg, proto, signalMetrics)
	if err != nil {
		return nil, err
	}
	if proto == ProtocolGRPC {
		var opts []otlpmetricgrpc.Option
		if typed {
			opts = append(opts, otlpmetricgrpc.WithEndpointURL(url))
		}
		if src := headerSourceFor(signalMetrics); src != nil {
			opts = append(opts, otlpmetricgrpc.WithDialOption(grpc.WithPerRPCCredentials(src)))
		}
		return otlpmetricgrpc.New(ctx, opts...)
	}
	var opts []otlpmetrichttp.Option
	if typed {
		opts = append(opts, otlpmetrichttp.WithEndpointURL(url))
	}
	hc, err := httpClientFor(signalMetrics, headerSourceFor(signalMetrics))
	if err != nil {
		return nil, err
	}
	opts = append(opts, otlpmetrichttp.WithHTTPClient(hc))
	return otlpmetrichttp.New(ctx, opts...)
}

func newLogExporter(ctx context.Context, cfg Config) (sdklog.Exporter, error) {
	proto, err := protocolFor(signalLogs)
	if err != nil {
		return nil, err
	}
	url, typed, err := typedURL(cfg, proto, signalLogs)
	if err != nil {
		return nil, err
	}
	if proto == ProtocolGRPC {
		var opts []otlploggrpc.Option
		if typed {
			opts = append(opts, otlploggrpc.WithEndpointURL(url))
		}
		if src := headerSourceFor(signalLogs); src != nil {
			opts = append(opts, otlploggrpc.WithDialOption(grpc.WithPerRPCCredentials(src)))
		}
		return otlploggrpc.New(ctx, opts...)
	}
	var opts []otlploghttp.Option
	if typed {
		opts = append(opts, otlploghttp.WithEndpointURL(url))
	}
	hc, err := httpClientFor(signalLogs, headerSourceFor(signalLogs))
	if err != nil {
		return nil, err
	}
	opts = append(opts, otlploghttp.WithHTTPClient(hc))
	return otlploghttp.New(ctx, opts...)
}

func typedURL(cfg Config, proto string, s signal) (url string, typed bool, err error) {
	if cfg.OTLPEndpoint == "" {
		return "", false, nil
	}
	url, err = typedEndpointURL(cfg.OTLPEndpoint, proto, s)
	return url, err == nil, err
}
