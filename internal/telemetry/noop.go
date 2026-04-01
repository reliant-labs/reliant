// Copyright (c) 2025 Reliant Labs
package telemetry

// NoopReporter is a no-op implementation of ErrorReporter that does nothing
// This is used when crash reporting is disabled for privacy
type NoopReporter struct{}

// NewNoopReporter creates a new no-op error reporter
func NewNoopReporter() *NoopReporter {
	return &NoopReporter{}
}

func (r *NoopReporter) CaptureException(err error) string {
	// No-op
	return ""
}

func (r *NoopReporter) CaptureExceptionWithContext(err error, tags map[string]string, extra map[string]interface{}) string {
	// No-op
	return ""
}

func (r *NoopReporter) CaptureMessage(message string) string {
	// No-op
	return ""
}

func (r *NoopReporter) SetUser(userID string, email string) {
	// No-op
}

func (r *NoopReporter) SetTag(key string, value string) {
	// No-op
}

func (r *NoopReporter) Flush(timeoutSeconds int) bool {
	// No-op
	return true
}

// Ensure NoopReporter implements ErrorReporter and ContextualErrorReporter.
var _ ErrorReporter = (*NoopReporter)(nil)
var _ ContextualErrorReporter = (*NoopReporter)(nil)
