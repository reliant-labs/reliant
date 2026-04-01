// Copyright (c) 2025 Reliant Labs
package telemetry

import "sync"

// ErrorReporter defines the interface for error reporting (crash reporting)
type ErrorReporter interface {
	// CaptureException captures an exception/error
	CaptureException(err error) string

	// CaptureMessage captures a message
	CaptureMessage(message string) string

	// SetUser sets the user context for error reports
	SetUser(userID string, email string)

	// SetTag sets a tag for error reports
	SetTag(key string, value string)

	// Flush waits for pending events to be sent
	Flush(timeoutSeconds int) bool
}

// ContextualErrorReporter optionally supports richer error reporting with tags and extra context.
type ContextualErrorReporter interface {
	CaptureExceptionWithContext(err error, tags map[string]string, extra map[string]interface{}) string
}

// Global error reporter instance
var (
	globalReporter ErrorReporter
	reporterMu     sync.RWMutex
)

// SetReporter sets the global error reporter
// This should be called during initialization
func SetReporter(reporter ErrorReporter) {
	reporterMu.Lock()
	defer reporterMu.Unlock()
	globalReporter = reporter
}

// GetReporter returns the global error reporter
// Returns a no-op reporter if none is set
func GetReporter() ErrorReporter {
	reporterMu.RLock()
	defer reporterMu.RUnlock()

	if globalReporter == nil {
		return &NoopReporter{}
	}
	return globalReporter
}

// CaptureException is a convenience function that captures an error using the global reporter
func CaptureException(err error) string {
	return GetReporter().CaptureException(err)
}

// CaptureExceptionWithContext captures an error using contextual reporting when supported,
// and falls back to basic exception capture otherwise.
func CaptureExceptionWithContext(err error, tags map[string]string, extra map[string]interface{}) string {
	reporter := GetReporter()
	if cr, ok := reporter.(ContextualErrorReporter); ok {
		return cr.CaptureExceptionWithContext(err, tags, extra)
	}
	return reporter.CaptureException(err)
}

// CaptureMessage is a convenience function that captures a message using the global reporter
func CaptureMessage(message string) string {
	return GetReporter().CaptureMessage(message)
}

// Flush flushes pending events using the global reporter
func Flush(timeoutSeconds int) bool {
	return GetReporter().Flush(timeoutSeconds)
}
