// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"
)

const echoProcedure = "/reliant.observability.v1.EchoService/Echo"

func TestConnectAndHTTPInstrumentationPropagateTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	resourceAttrs := []attribute.KeyValue{
		attribute.String("service.name", "reliant-api-server"),
		attribute.String("service.version", "test-version"),
		attribute.String("service.instance.id", "test-instance"),
		attribute.String("deployment.environment", "test"),
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(resource.NewWithAttributes("", resourceAttrs...)),
	)
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("traceparent"))
		_, _ = w.Write([]byte("ok"))
	}))
	defer downstream.Close()

	handler := connect.NewUnaryHandler(echoProcedure, func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downstream.URL, nil)
		require.NoError(t, err)
		resp, err := (&http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}).Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		return connect.NewResponse(&emptypb.Empty{}), nil
	}, connect.WithInterceptors(newInterceptors(true, &testNamedInterceptor{})...))
	server := httptest.NewServer(handler)
	defer server.Close()

	stack, err := observe.NewClientStack(observe.ClientStackDeps{})
	require.NoError(t, err)
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](stack.HTTPClient, server.URL+echoProcedure, stack.ClientOptions...)
	_, err = client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.GreaterOrEqual(t, len(spans), 3)
	traceIDs := make(map[string]struct{})
	for _, span := range spans {
		traceIDs[span.SpanContext.TraceID().String()] = struct{}{}
		for _, want := range resourceAttrs {
			got, ok := span.Resource.Set().Value(want.Key)
			require.True(t, ok, "resource attribute %q missing", want.Key)
			require.Equal(t, want.Value.AsString(), got.AsString())
		}
	}
	require.Len(t, traceIDs, 1, "Connect and HTTP spans must share the inbound trace")
}

func TestUntrustedServerStartsNewRootAndLinksRemoteTrace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trust bool
	}{{"trusted public API continues the caller trace", true}, {"untrusted daemon server starts a new root", false}} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			previousProvider := otel.GetTracerProvider()
			previousPropagator := otel.GetTextMapPropagator()
			otel.SetTracerProvider(provider)
			otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
			t.Cleanup(func() {
				otel.SetTracerProvider(previousProvider)
				otel.SetTextMapPropagator(previousPropagator)
				require.NoError(t, provider.Shutdown(context.Background()))
			})

			handler := connect.NewUnaryHandler(echoProcedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
				return connect.NewResponse(&emptypb.Empty{}), nil
			}, connect.WithInterceptors(newInterceptors(tc.trust, &testNamedInterceptor{})...))
			server := httptest.NewServer(handler)
			defer server.Close()

			const remoteTrace = "0af7651916cd43dd8448eb211c80319c"
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, server.URL+echoProcedure)
			req := connect.NewRequest(&emptypb.Empty{})
			req.Header().Set("traceparent", "00-"+remoteTrace+"-b7ad6b7169203331-01")
			_, err := client.CallUnary(context.Background(), req)
			require.NoError(t, err)

			var serverSpans []tracetest.SpanStub
			for _, span := range exporter.GetSpans() {
				if span.SpanKind == oteltrace.SpanKindServer {
					serverSpans = append(serverSpans, span)
				}
			}
			require.Len(t, serverSpans, 1)
			span := serverSpans[0]
			if tc.trust {
				require.Equal(t, remoteTrace, span.SpanContext.TraceID().String())
				require.True(t, span.Parent.IsValid())
				return
			}
			require.NotEqual(t, remoteTrace, span.SpanContext.TraceID().String())
			require.False(t, span.Parent.IsValid(), "remote context must not be the parent")
			require.Len(t, span.Links, 1)
			require.Equal(t, remoteTrace, span.Links[0].SpanContext.TraceID().String())
		})
	}
}
