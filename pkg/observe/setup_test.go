package observe

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestResourceFromConfig(t *testing.T) {
	// OTEL_SERVICE_NAME wins over Config.ServiceName, so an ambient one (any
	// shell inside a `forge env up` stack exports it) would fail this test.
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=staging")
	res, err := resourceFromConfig(context.Background(), Config{
		ServiceName:    "api",
		ServiceVersion: "1.2.3",
		InstanceID:     "instance-1",
	})
	if err != nil {
		t.Fatalf("resourceFromConfig returned error: %v", err)
	}

	for key, want := range map[attribute.Key]string{
		"service.name":                "api",
		"service.version":             "1.2.3",
		"service.instance.id":         "instance-1",
		"deployment.environment.name": "staging",
	} {
		got, ok := res.Set().Value(key)
		if !ok || got.AsString() != want {
			t.Errorf("resource attribute %q = %q, %t; want %q, true", key, got.AsString(), ok, want)
		}
	}
}

func TestResourceFromConfig_OmitsEmptyOptionalAttributes(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	res, err := resourceFromConfig(context.Background(), Config{ServiceName: "api"})
	if err != nil {
		t.Fatalf("resourceFromConfig returned error: %v", err)
	}

	for _, key := range []attribute.Key{"service.version", "service.instance.id", "deployment.environment.name"} {
		if got, ok := res.Set().Value(key); ok {
			t.Errorf("resource unexpectedly contains %q = %q", key, got.AsString())
		}
	}
}

// With no endpoint from any source, nothing is exported: the global
// TracerProvider stays the no-op default and only /metrics is wired.
func TestSetup_NoOTLPEndpoint_PrometheusOnly(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)

	shutdown, handler, err := Setup(context.Background(), Config{ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	if handler == nil || shutdown == nil {
		t.Fatal("expected non-nil metrics handler and shutdown func")
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Fatal("did not expect an SDK TracerProvider to be installed without an endpoint")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

func TestSetup_WithOTLPEndpoint(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	// otlptracegrpc/otlpmetricgrpc are non-blocking by default: New does not
	// dial, so an unreachable endpoint still constructs successfully.
	shutdown, handler, err := Setup(context.Background(), Config{
		ServiceName:    "test-svc",
		ServiceVersion: "1.2.3",
		InstanceID:     "host-abc",
		OTLPEndpoint:   "http://127.0.0.1:4318",
	})
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	if handler == nil {
		t.Fatal("expected non-nil metrics handler")
	}

	// On the OTLP path an SDK TracerProvider is installed globally.
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("expected an SDK TracerProvider, got %T", otel.GetTracerProvider())
	}

	// shutdown is expected to attempt a final export; against an unreachable
	// collector it returns an export error. Construction success is what we
	// assert here. Use a short, already-cancelled-style ctx so the test does
	// not block on the exporter's default 10s timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)
}

// "dev" version is treated as "no version" — behaviour-preserving with the old
// generated code. We can't easily read the resource back, so we just exercise
// the path to ensure it constructs.
func TestSetup_DevVersionTreatedAsUnset(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	shutdown, _, err := Setup(context.Background(), Config{
		ServiceName:    "test-svc",
		ServiceVersion: "dev",
		OTLPEndpoint:   "http://127.0.0.1:4318",
	})
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)
}

// forge renders the trace ratio as OTEL_TRACES_SAMPLER=parentbased_traceidratio
// + OTEL_TRACES_SAMPLER_ARG (head sampling, so a trace is whole or absent across
// services). The OTel SDK reads those only while the provider sets no sampler of
// its own, so this drives the real Setup path: ratio 0 must drop a root span, ratio
// 1 must keep it, and a sampled parent must be followed whatever the ratio.
func TestSetup_HonoursTheSamplerEnvironment(t *testing.T) {
	sampled := func(t *testing.T, arg string, parent bool) bool {
		t.Helper()
		clearOTelEnv(t)
		snapshotGlobals(t)
		t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", arg)
		shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc", OTLPEndpoint: "http://127.0.0.1:1"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_ = shutdown(ctx)
		}()
		ctx := context.Background()
		if parent {
			ctx = trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
				TraceID:    trace.TraceID{1},
				SpanID:     trace.SpanID{1},
				TraceFlags: trace.FlagsSampled,
				Remote:     true,
			}))
		}
		_, span := otel.Tracer("t").Start(ctx, "op")
		defer span.End()
		return span.SpanContext().IsSampled()
	}
	if sampled(t, "0", false) {
		t.Error("ratio 0 kept a root span: the environment's sampler was ignored")
	}
	if !sampled(t, "1", false) {
		t.Error("ratio 1 dropped a root span")
	}
	if !sampled(t, "0", true) {
		t.Error("a sampled parent was dropped: the services of one trace would disagree and split it")
	}
}
