// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"
	"runtime/debug"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// RecoveryInterceptor catches panics in gRPC handlers and reports them to Sentry
type RecoveryInterceptor struct{}

// NewRecoveryInterceptor creates a new recovery interceptor
func NewRecoveryInterceptor() *RecoveryInterceptor {
	return &RecoveryInterceptor{}
}

// WrapUnary implements connect.Interceptor for unary RPCs
func (i *RecoveryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		defer func() {
			if r := recover(); r != nil {
				// Get stack trace
				stack := string(debug.Stack())

				// Log the panic
				logging.Error("[gRPC] Panic recovered",
					"procedure", req.Spec().Procedure,
					"panic", r,
					"stack", stack,
				)

				// Report to Sentry
				panicErr := fmt.Errorf("panic in %s: %v", req.Spec().Procedure, r)
				reportPanicToSentry(panicErr, req.Spec().Procedure, stack)

				// Return an internal error to the client
				err = connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error"))
			}
		}()

		return next(ctx, req)
	}
}

// WrapStreamingClient implements connect.Interceptor for streaming client RPCs
func (i *RecoveryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // Client-side streaming doesn't need recovery on server
}

// WrapStreamingHandler implements connect.Interceptor for streaming handler RPCs
func (i *RecoveryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer func() {
			if r := recover(); r != nil {
				// Get stack trace
				stack := string(debug.Stack())

				// Log the panic
				logging.Error("[gRPC] Panic recovered in streaming handler",
					"procedure", conn.Spec().Procedure,
					"panic", r,
					"stack", stack,
				)

				// Report to Sentry
				panicErr := fmt.Errorf("panic in streaming %s: %v", conn.Spec().Procedure, r)
				reportPanicToSentry(panicErr, conn.Spec().Procedure, stack)

				// Return an internal error
				err = connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error"))
			}
		}()

		return next(ctx, conn)
	}
}

// reportPanicToSentry reports a panic to telemetry with context.
func reportPanicToSentry(err error, procedure string, stack string) {
	telemetry.CaptureExceptionWithContext(err, map[string]string{
		"type":      "grpc_panic",
		"procedure": procedure,
	}, map[string]interface{}{
		"stack_trace": stack,
	})
}
