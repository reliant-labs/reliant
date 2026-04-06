// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// ErrorReporterInterceptor reports significant errors to Sentry
type ErrorReporterInterceptor struct{}

// NewErrorReporterInterceptor creates a new error reporter interceptor
func NewErrorReporterInterceptor() *ErrorReporterInterceptor {
	return &ErrorReporterInterceptor{}
}

// WrapUnary implements connect.Interceptor for unary RPCs
func (i *ErrorReporterInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err != nil {
			i.reportError(err, req.Spec().Procedure)
		}
		return resp, err
	}
}

// WrapStreamingClient implements connect.Interceptor for streaming client RPCs
func (i *ErrorReporterInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // Client-side streaming doesn't need error reporting on server
}

// WrapStreamingHandler implements connect.Interceptor for streaming handler RPCs
func (i *ErrorReporterInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		if err != nil {
			i.reportError(err, conn.Spec().Procedure)
		}
		return err
	}
}

// reportError reports an error to Sentry if it's significant
func (i *ErrorReporterInterceptor) reportError(err error, procedure string) {
	// Get the connect error code if available
	code := connect.CodeOf(err)

	// Skip reporting for expected/user errors
	if shouldSkipReporting(code, err) {
		return
	}

	// Log the error
	logging.Error("[gRPC] Request error",
		"procedure", procedure,
		"code", code.String(),
		"error", err,
	)

	telemetry.CaptureExceptionWithContext(err, map[string]string{
		"type":      "grpc_error",
		"procedure": procedure,
		"code":      code.String(),
	}, nil)
}

// shouldSkipReporting returns true for errors that shouldn't be reported to Sentry
func shouldSkipReporting(code connect.Code, err error) bool {
	// Skip user errors (client's fault, not ours) and expected errors
	switch code {
	case connect.CodeInvalidArgument,
		connect.CodeNotFound,
		connect.CodeAlreadyExists,
		connect.CodePermissionDenied,
		connect.CodeUnauthenticated,
		connect.CodeFailedPrecondition,
		connect.CodeAborted,
		connect.CodeOutOfRange,
		connect.CodeCanceled,
		connect.CodeDeadlineExceeded,  // Timeouts are expected and handled
		connect.CodeResourceExhausted: // Rate limiting is expected
		return true
	}

	// Skip common transient/non-actionable errors
	errMsg := strings.ToLower(err.Error())
	skipPatterns := []string{
		"context canceled",
		"connection reset",
		"broken pipe",
		"client disconnected",
		"exit status 128",
		"sqlite3: interrupted",
		"streaming cancelled by user",
		"signal: killed",
	}

	for _, pattern := range skipPatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}

	return false
}
