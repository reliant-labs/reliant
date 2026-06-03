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

// reportError logs the error at a severity appropriate for its connect code and
// reports it to Sentry if it represents a genuine server-side failure.
//
// Log-level mapping is intentionally decoupled from Sentry reporting:
//
//   - Client-observable / transient codes (Unavailable, Canceled, NotFound,
//     etc.) are not logged here at all — forge's observe.LoggingInterceptor
//     already emits a WARN "rpc failed" line covering the same payload, so a
//     second log would be redundant. They are also skipped for Sentry.
//
//   - Genuine internal failures (Internal, DataLoss, Unknown, Unimplemented)
//     log at ERROR and report to Sentry — unless the error message reveals a
//     known transient cause (e.g. "broken pipe", "no daemon connected"), in
//     which case we demote to WARN and skip Sentry. This handles cases where
//     a downstream returns CodeInternal for what is actually a transient
//     client-facing condition without changing the underlying error type.
func (i *ErrorReporterInterceptor) reportError(err error, procedure string) {
	code := connect.CodeOf(err)

	// Codes that are already covered by the forge logging interceptor's WARN
	// line. No additional log here; no Sentry report.
	if isExpectedCode(code) {
		return
	}

	// Even on a "server-side" code, recognize transient/non-actionable error
	// strings and demote to WARN without reporting to Sentry.
	if isTransientErrorMessage(err) {
		logging.Warn("[gRPC] Request error (transient)",
			"procedure", procedure,
			"code", code.String(),
			"error", err,
		)
		return
	}

	// Genuine internal failure: log at ERROR and report to Sentry.
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

// isExpectedCode returns true for connect codes that indicate a
// client-observable or transient condition. These are not server bugs and are
// already logged at WARN by forge's observe.LoggingInterceptor, so we neither
// re-log them nor send them to Sentry.
//
// Only the genuinely-server-side codes (Internal, DataLoss, Unknown,
// Unimplemented) fall through to ERROR + Sentry.
func isExpectedCode(code connect.Code) bool {
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
		connect.CodeResourceExhausted, // Rate limiting is expected
		connect.CodeUnavailable:       // Backend not ready / reconnecting
		return true
	}
	return false
}

// isTransientErrorMessage matches error messages that indicate a transient,
// non-actionable condition even when the connect code looks server-side.
// Used to demote CodeInternal etc. to WARN when the inner error reveals the
// real cause (e.g. daemon not yet connected, dropped socket).
func isTransientErrorMessage(err error) bool {
	errMsg := strings.ToLower(err.Error())
	patterns := []string{
		"context canceled",
		"connection reset",
		"broken pipe",
		"client disconnected",
		"exit status 128",
		"streaming cancelled by user",
		"signal: killed",
		// Daemon-connection transients — the user has the app open but their
		// local daemon is still connecting / reconnecting. Surfaced to
		// operators via WARN; not a server bug.
		"no daemon connected",
		"daemon offline",
		"daemon connection closed",
		"unavailable:",
	}
	for _, pattern := range patterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}
