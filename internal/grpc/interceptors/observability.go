// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/observability"
)

var grpcTracer = otel.Tracer("reliant.grpc")

// NewObservabilityInterceptor creates a ConnectRPC interceptor for Prometheus
// metrics and OTel tracing.
func NewObservabilityInterceptor() *ObservabilityInterceptor {
	return &ObservabilityInterceptor{}
}

// ObservabilityInterceptor records Prometheus metrics and starts OTel spans
// for every ConnectRPC call.
type ObservabilityInterceptor struct{}

// WrapUnary implements connect.Interceptor for unary RPCs.
func (o *ObservabilityInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		observability.GRPCInFlight.Inc()
		defer observability.GRPCInFlight.Dec()

		procedure := req.Spec().Procedure // e.g. "/reliant.v1.ChatService/CreateChat"
		service, method := splitProcedure(procedure)

		ctx, span := grpcTracer.Start(ctx, procedure,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "connect"),
				attribute.String("rpc.service", service),
				attribute.String("rpc.method", method),
			),
		)
		defer span.End()

		resp, err := next(ctx, req)

		duration := time.Since(start).Seconds()
		code := codeFromError(err)

		span.SetAttributes(attribute.String("rpc.connect_rpc.status_code", code))

		observability.GRPCRequestsTotal.WithLabelValues(service, method, code).Inc()
		observability.GRPCRequestDuration.WithLabelValues(service, method, code).Observe(duration)

		return resp, err
	}
}

// WrapStreamingClient implements connect.Interceptor for streaming client RPCs.
func (o *ObservabilityInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // Client-side streaming doesn't need server-side instrumentation
}

// WrapStreamingHandler implements connect.Interceptor for streaming handler RPCs.
func (o *ObservabilityInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		observability.GRPCInFlight.Inc()
		defer observability.GRPCInFlight.Dec()

		procedure := conn.Spec().Procedure
		service, method := splitProcedure(procedure)

		ctx, span := grpcTracer.Start(ctx, procedure,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "connect"),
				attribute.String("rpc.service", service),
				attribute.String("rpc.method", method),
			),
		)
		defer span.End()

		err := next(ctx, conn)

		duration := time.Since(start).Seconds()
		code := codeFromError(err)

		span.SetAttributes(attribute.String("rpc.connect_rpc.status_code", code))

		observability.GRPCRequestsTotal.WithLabelValues(service, method, code).Inc()
		observability.GRPCRequestDuration.WithLabelValues(service, method, code).Observe(duration)

		return err
	}
}

func splitProcedure(procedure string) (service, method string) {
	// procedure is like "/reliant.v1.ChatService/CreateChat"
	parts := strings.Split(strings.TrimPrefix(procedure, "/"), "/")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return procedure, ""
}

func codeFromError(err error) string {
	if err == nil {
		return "ok"
	}
	if connectErr, ok := err.(*connect.Error); ok {
		return connectErr.Code().String()
	}
	return "unknown"
}
