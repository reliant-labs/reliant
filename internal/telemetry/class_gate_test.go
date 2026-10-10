// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// countingReporter counts the exceptions the package-level capture functions
// hand it.
type countingReporter struct {
	NoopReporter
	sent []error
}

func (r *countingReporter) CaptureException(err error) string {
	r.sent = append(r.sent, err)
	return "event"
}

func (r *countingReporter) CaptureExceptionWithContext(err error, _ map[string]string, _ map[string]interface{}) string {
	r.sent = append(r.sent, err)
	return "event"
}

func installCountingReporter(t *testing.T) *countingReporter {
	t.Helper()
	previous := GetReporter()
	reporter := &countingReporter{}
	SetReporter(reporter)
	t.Cleanup(func() { SetReporter(previous) })
	return reporter
}

// Every route to Sentry that is not a log line — the RPC error reporter, a
// terminal activity failure, a recovered panic — goes through these two
// functions. A user error must not become an event on any of them.
func TestCapture_UserErrorsAndCancellationsAreNotSent(t *testing.T) {
	reporter := installCountingReporter(t)

	for name, err := range map[string]error{
		"machine offline": connect.NewError(connect.CodeUnavailable,
			svcerr.WithClass(errors.New("no daemon connected for user"), svcerr.ClassUser)),
		"invalid input":     connect.NewError(connect.CodeInvalidArgument, errors.New("model is not available")),
		"plan limit":        svcerr.PlanLimit("seat cap"),
		"benign activity":   temporal.NewApplicationErrorWithOptions("usage limit", "ProviderUsageLimit", temporal.ApplicationErrorOptions{Category: temporal.ApplicationErrorCategoryBenign}),
		"context canceled":  fmt.Errorf("stream: %w", context.Canceled),
		"nil is not sent":   nil,
		"marked under 5xx":  connect.NewError(connect.CodeInternal, fmt.Errorf("open session: %w", svcerr.WithClass(errors.New("machine suspended"), svcerr.ClassUser))),
		"temporal canceled": temporal.NewCanceledError("stopped"),
	} {
		assert.Empty(t, CaptureExceptionWithContext(err, nil, nil), name)
		assert.Empty(t, CaptureException(err), name)
	}
	assert.Empty(t, reporter.sent, "no user error or cancellation may reach Sentry")

	server := errors.New("pq: connection refused")
	assert.Equal(t, "event", CaptureExceptionWithContext(server, nil, nil))
	assert.Equal(t, "event", CaptureException(svcerr.Internal("charge failed")))
	assert.Len(t, reporter.sent, 2, "server faults must still be sent")
}

// TestCaptureBugReport_IsNotSubjectToTheUserErrorPolicy pins the orchestrator's
// requirement: an agent-filed bug report is its own Sentry event, whatever the
// report describes. The error-class gate covers exceptions only; a bug report
// about a condition that, as an error, would be a user error (a machine that
// never connects) must still be delivered.
func TestCaptureBugReport_IsNotSubjectToTheUserErrorPolicy(t *testing.T) {
	previous := GetReporter()
	t.Cleanup(func() { SetReporter(previous) })
	reporter, transport := newCapturingReporter(t)
	SetReporter(reporter)

	report := sampleBugReport()
	report.Title = "Machine never connects: no daemon connected for user after restart"
	report.Severity = "low"
	eventID, live := CaptureBugReport(report)

	require.True(t, live)
	event := transport.only(t)
	assert.Equal(t, string(event.EventID), eventID, "the bug report must be sent as its own event")
}
