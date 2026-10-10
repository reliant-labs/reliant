// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reconcilerSite = "github.com/reliant-labs/reliant/internal/workflow/reconciliation.(*Reconciler).reapOrphanedThreads"

func reapLogEvent(chatID string, err error) LogEvent {
	return LogEvent{
		Message: "[Reconciler] Reaped orphaned threads — a terminal workflow did not cascade to its thread",
		Frames: []runtime.Frame{
			{Function: reconcilerSite, File: "/src/internal/workflow/reconciliation/reconciler.go", Line: 1918},
			{Function: "github.com/reliant-labs/reliant/internal/workflow/reconciliation.(*Reconciler).ReconcileRunningWorkflows", File: "/src/internal/workflow/reconciliation/reconciler.go", Line: 2499},
		},
		Err:   err,
		Tags:  map[string]string{"chatID": chatID, "workflowID": chatID},
		Extra: map[string]any{"rows": 1, "prompt": "never leaves the process"},
	}
}

// What Sentry receives for a forwarded log record, after BeforeSend: a message
// event titled by the log line, located and grouped by its call site, carrying
// the record's identifiers as tags — and no exception.
func TestCaptureLogEvent_SendsAGroupedMessageEvent(t *testing.T) {
	reporter, transport := newCapturingReporter(t)

	eventID := reporter.CaptureLogEvent(reapLogEvent("66a045ce-5872-4efe-a510-910f63fcb8a6", nil))

	event := transport.only(t)
	require.NotEmpty(t, eventID)
	assert.Empty(t, event.Exception, "a log line is not an exception: nothing may be titled errors.errorString")
	assert.Equal(t, "[Reconciler] Reaped orphaned threads — a terminal workflow did not cascade to its thread", event.Message)
	assert.Equal(t, sentry.LevelError, event.Level)
	assert.Equal(t, "slog", event.Logger)
	assert.Equal(t,
		[]string{"slog", reconcilerSite, "[Reconciler] Reaped orphaned threads — a terminal workflow did not cascade to its thread"},
		event.Fingerprint)
	assert.Equal(t, "reconciliation.(*Reconciler).reapOrphanedThreads", event.Transaction,
		"the call site is the issue's culprit")

	assert.Equal(t, "66a045ce-5872-4efe-a510-910f63fcb8a6", event.Tags["chatID"])
	assert.Equal(t, "66a045ce-5872-4efe-a510-910f63fcb8a6", event.Tags["workflowID"])
	assert.Equal(t, "slog", event.Tags["log_source"])
	assert.EqualValues(t, 1, event.Contexts["extra"]["rows"])
	assert.NotContains(t, event.Contexts["extra"], "prompt", "BeforeSend still applies the allowlist")

	require.Len(t, event.Threads, 1)
	frames := event.Threads[0].Stacktrace.Frames
	require.Len(t, frames, 2)
	assert.Equal(t, "(*Reconciler).reapOrphanedThreads", frames[1].Function, "Sentry orders frames outermost first")
	assert.Equal(t, 1918, frames[1].Lineno)
}

// One log line from one place is one issue: occurrences differing only in
// their ids, error text, or the line number the function moved to group
// together; another call site, or another message, does not.
func TestLogEventFingerprint_GroupsByMessageAndCallSite(t *testing.T) {
	reporter, transport := newCapturingReporter(t)

	reporter.CaptureLogEvent(reapLogEvent("chat-a", fmt.Errorf("reap: %w", fmt.Errorf("timeout after 30s"))))
	moved := reapLogEvent("chat-b", fmt.Errorf("reap: connection refused"))
	moved.Frames[0].Line = 1990
	reporter.CaptureLogEvent(moved)
	elsewhere := reapLogEvent("chat-c", nil)
	elsewhere.Frames[0].Function = "github.com/reliant-labs/reliant/internal/workflow/reconciliation.(*Reconciler).reapOrphanedDescendants"
	reporter.CaptureLogEvent(elsewhere)
	reworded := reapLogEvent("chat-d", nil)
	reworded.Message = "[Reconciler] Failed to reap orphaned threads"
	reporter.CaptureLogEvent(reworded)

	transport.mu.Lock()
	events := append([]*sentry.Event(nil), transport.events...)
	transport.mu.Unlock()
	require.Len(t, events, 4)

	assert.Equal(t, events[0].Fingerprint, events[1].Fingerprint, "ids, error text and line numbers do not split an issue")
	assert.NotEqual(t, events[0].Fingerprint, events[2].Fingerprint, "another call site is another issue")
	assert.NotEqual(t, events[0].Fingerprint, events[3].Fingerprint, "another message is another issue")
}

func TestLogEventFingerprint_FoldsVariablePartsAndClosureNames(t *testing.T) {
	assert.Equal(t,
		LogEventFingerprint("Workflow 66a045ce-5872-4efe-a510-910f63fcb8a6 failed after 3 attempts", "pkg.(*T).run.func2.1"),
		LogEventFingerprint("Workflow 3f03dc31-95a6-4945-8422-052575b549e0 failed after 12 attempts", "pkg.(*T).run.func1"),
		"interpolated ids and counts, and the compiler's closure numbering, are not part of an issue's identity")
	assert.Equal(t, []string{"slog", "unknown", "boom"}, LogEventFingerprint("boom", ""))
}

// A record's error rides as context and a tag, bounded by BeforeSend like any
// error text, and never as the exception.
func TestCaptureLogEvent_ErrorIsContextNotTheException(t *testing.T) {
	reporter, transport := newCapturingReporter(t)

	reporter.CaptureLogEvent(reapLogEvent("chat-a", fmt.Errorf("reap: %w", &stubPgError{})))

	event := transport.only(t)
	assert.Empty(t, event.Exception)
	assert.Equal(t, "telemetry.stubPgError", event.Tags["error_type"], "the innermost error's type, without the pointer star")
	assert.Equal(t, "reap: duplicate key value", event.Contexts["extra"]["error"])
}

type stubPgError struct{}

func (*stubPgError) Error() string { return "duplicate key value" }

// The log path keeps the class gate the exception path has: a log record
// carrying a user error or a cancellation is not a Sentry event, a server
// fault is.
func TestCaptureLogEvent_SendsOnlyServerFaults(t *testing.T) {
	reporter, transport := newCapturingReporter(t)
	previous := GetReporter()
	SetReporter(reporter)
	t.Cleanup(func() { SetReporter(previous) })

	for name, err := range map[string]error{
		"machine offline":  svcerr.WithClass(errors.New("no daemon connected for user"), svcerr.ClassUser),
		"plan limit":       svcerr.PlanLimit("seat cap"),
		"context canceled": fmt.Errorf("stream: %w", context.Canceled),
	} {
		assert.Empty(t, CaptureLogEvent(reapLogEvent("chat-"+name, err)), name)
	}
	assert.NotEmpty(t, CaptureLogEvent(reapLogEvent("chat-server", errors.New("stripe: 500"))))
	assert.NotEmpty(t, CaptureLogEvent(reapLogEvent("chat-no-error", nil)), "a record without an error is a server fault")

	transport.mu.Lock()
	defer transport.mu.Unlock()
	assert.Len(t, transport.events, 2)
}

// report_bug events have their own grouping — the source, the product and the
// normalized title (BugReportFingerprint) — and forwarding log records as
// grouped message events must leave it exactly as it was: a bug report is
// never regrouped by call site, and a log line that says the same words as a
// report title never lands in the report's issue.
func TestCaptureBugReport_GroupingIsIndependentOfLogEvents(t *testing.T) {
	reporter, transport := newCapturingReporter(t)
	report := sampleBugReport()

	reporter.CaptureBugReport(report)
	logged := reapLogEvent(chatID, nil)
	logged.Message = report.Title
	reporter.CaptureLogEvent(logged)

	transport.mu.Lock()
	events := append([]*sentry.Event(nil), transport.events...)
	transport.mu.Unlock()
	require.Len(t, events, 2)

	bugReport, logEvent := events[0], events[1]
	assert.Equal(t,
		[]string{"llm_bug_report", "reliant", "user message delivered to the model as content trimmed"},
		bugReport.Fingerprint, "a report still groups by product and normalized title")
	assert.Equal(t, BugReportFingerprint(report.Product, report.Title), bugReport.Fingerprint)
	assert.Equal(t, BugReportSource, bugReport.Logger)
	assert.Empty(t, bugReport.Threads, "a report carries no call-site stack")
	assert.Empty(t, bugReport.Transaction)

	assert.Equal(t, "slog", logEvent.Fingerprint[0])
	assert.NotEqual(t, bugReport.Fingerprint, logEvent.Fingerprint,
		"a log line worded like a report title is not that report's issue")
}
