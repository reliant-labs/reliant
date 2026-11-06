package contractkit

import (
	"log/slog"
	"time"
)

// LogCallErr emits a single slog.Info record summarising a wrapped
// method call that returns an error. start should be captured
// immediately before the inner call; err is the method's terminal error
// result (which may be nil — the attribute is always emitted to match
// the previous generated behaviour).
//
// The record is identical in shape to the previous generated per-method
// log line:
//
//	logger.Info("<method>", "duration", time.Since(start), "error", err)
//
// Always-emit-error semantics are part of the locked behavioural
// fingerprint (see TestLogCallErr_FingerprintLocked).
func LogCallErr(logger *slog.Logger, method string, start time.Time, err error) {
	if logger == nil {
		return
	}
	logger.Info(method, "duration", time.Since(start), "error", err)
}

// LogCall emits a single slog.Info record summarising a wrapped method
// call that does NOT return an error. start should be captured
// immediately before the inner call.
//
// The record is identical in shape to the previous generated per-method
// log line for void methods:
//
//	logger.Info("<method>", "duration", time.Since(start))
func LogCall(logger *slog.Logger, method string, start time.Time) {
	if logger == nil {
		return
	}
	logger.Info(method, "duration", time.Since(start))
}
