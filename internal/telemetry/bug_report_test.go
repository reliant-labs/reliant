// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureTransport records what the client would have sent. Events reach it
// after BeforeSend, so a test reads exactly the payload Sentry would get.
type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *captureTransport) Flush(time.Duration) bool              { return true }
func (t *captureTransport) FlushWithContext(context.Context) bool { return true }
func (t *captureTransport) Configure(sentry.ClientOptions)        {}
func (t *captureTransport) Close()                                {}
func (t *captureTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *captureTransport) only(tb testing.TB) *sentry.Event {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	require.Len(tb, t.events, 1, "exactly one event should have been sent")
	return t.events[0]
}

// newCapturingReporter is a live SentryReporter wired through the production
// client options — BeforeSend included — onto a transport the test can read.
func newCapturingReporter(t *testing.T) (*SentryReporter, *captureTransport) {
	t.Helper()
	transport := &captureTransport{}
	opts := clientOptions(SentryConfig{Enabled: true, DSN: "https://public@example.ingest.sentry.io/1"})
	opts.Transport = transport
	client, err := sentry.NewClient(opts)
	require.NoError(t, err)
	return &SentryReporter{initialized: true, hub: sentry.NewHub(client, sentry.NewScope())}, transport
}

func sampleBugReport() BugReport {
	return BugReport{
		Product:  "reliant",
		Severity: "high",
		Title:    "User message delivered to the model as [content trimmed]",
		Summary:  "The newest user message reached the model as the literal [content trimmed].",
		Expected: "The model receives the user's message intact.",
		Actual:   "The model replied that the message was truncated.",
		Evidence: "chat 5ffd6bb4: messages.token_count alternates 520k / 696k\nmodel window is 272000",

		ChatID:     chatID,
		ThreadID:   threadID,
		ToolCallID: toolCallID,
		ProjectID:  "d70701a6-8aa9-4fb8-8d13-9c41b708201d",
		UserID:     userID,
		WorktreeID: "1ddeaaf3-ffa8-43e0-a99e-08f219670a2c",
		Workflow:   "builtin://agent",
		Model:      "gpt-5.6-terra",
		DaemonID:   "daemon-2aab1465",
		DaemonType: "managed",
		Pod:        "ws-ws-2aab1465",

		ReliantVersion: "v0.9.3",
		ForgeVersion:   "v0.1.28-0.20261009201500-abcdef123456",
	}
}

func TestCaptureBugReport_BuildsATaggedGroupedEvent(t *testing.T) {
	reporter, transport := newCapturingReporter(t)

	eventID := reporter.CaptureBugReport(sampleBugReport())

	event := transport.only(t)
	require.NotEmpty(t, eventID, "the model is handed the event id")
	assert.Equal(t, string(event.EventID), eventID)
	assert.Equal(t, sentry.LevelError, event.Level, "high severity is an error")
	assert.Equal(t, "[reliant] User message delivered to the model as [content trimmed]", event.Message)
	assert.Equal(t,
		[]string{"llm_bug_report", "reliant", "user message delivered to the model as content trimmed"},
		event.Fingerprint, "repeats of one defect group into one issue")

	for key, want := range map[string]string{
		"source":       "llm_bug_report",
		"product":      "reliant",
		"severity":     "high",
		"chat_id":      chatID,
		"thread_id":    threadID,
		"tool_call_id": toolCallID,
		"project_id":   "d70701a6-8aa9-4fb8-8d13-9c41b708201d",
		"user_id":      userID,
		"workflow":     "builtin://agent",
		"model":        "gpt-5.6-terra",
		"daemon_id":    "daemon-2aab1465",
		"daemon_type":  "managed",
		// The machine, and the build, are what make a report actionable:
		// without them a workspace defect cannot be tied to a pod.
		"worktree_id":     "1ddeaaf3-ffa8-43e0-a99e-08f219670a2c",
		"pod":             "ws-ws-2aab1465",
		"reliant_version": "v0.9.3",
		"forge_version":   "v0.1.28-0.20261009201500-abcdef123456",
	} {
		assert.Equal(t, want, event.Tags[key], "tag %s", key)
	}
	assert.Equal(t, userID, event.User.ID)

	report := event.Contexts[bugReportContext]
	require.NotNil(t, report, "the authored report is the point of the event and must survive BeforeSend")
	assert.Equal(t, "User message delivered to the model as [content trimmed]", report["title"])
	assert.Equal(t, "The newest user message reached the model as the literal [content trimmed].", report["summary"])
	assert.Equal(t, "The model receives the user's message intact.", report["expected"])
	assert.Equal(t, "The model replied that the message was truncated.", report["actual"])
	assert.Equal(t, "chat 5ffd6bb4: messages.token_count alternates 520k / 696k\nmodel window is 272000", report["evidence"],
		"evidence keeps every line: unlike an error string, it was written to be read")
}

func TestCaptureBugReport_LevelFollowsSeverity(t *testing.T) {
	for severity, level := range map[string]sentry.Level{
		"low":      sentry.LevelInfo,
		"medium":   sentry.LevelWarning,
		"high":     sentry.LevelError,
		"critical": sentry.LevelFatal,
	} {
		t.Run(severity, func(t *testing.T) {
			reporter, transport := newCapturingReporter(t)
			report := sampleBugReport()
			report.Severity = severity
			reporter.CaptureBugReport(report)
			assert.Equal(t, level, transport.only(t).Level)
		})
	}
}

func TestCaptureBugReport_RedactsCredentialsAndBoundsText(t *testing.T) {
	reporter, transport := newCapturingReporter(t)
	report := sampleBugReport()
	report.Title = "forge env deploy leaks " + anthropicKey
	report.Evidence = strings.Join([]string{
		"$ curl -H 'Authorization: Bearer " + reliantToken + "' https://api.reliantapi.com/x",
		"DATABASE_URL=postgres://reliant:hunter2pass@10.0.0.4:5432/reliant_cp",
		"ANTHROPIC_API_KEY=" + anthropicKey,
		"error: context deadline exceeded",
	}, "\n")
	// Prose, not one long run: a 40+ character token is an opaque blob and
	// is redacted outright, which is a different rule.
	report.Summary = strings.Repeat("the daemon lost its mount ", maxBugReportFieldRunes/10)

	reporter.CaptureBugReport(report)

	event := transport.only(t)
	wire := marshalEvent(t, event)
	for _, secret := range []string{anthropicKey, reliantToken, "hunter2pass"} {
		assert.NotContains(t, wire, secret, "a credential in an authored report reached Sentry")
	}

	fields := event.Contexts[bugReportContext]
	require.NotNil(t, fields)
	evidence, _ := fields["evidence"].(string)
	assert.Contains(t, evidence, "error: context deadline exceeded", "the diagnostic text itself is kept")
	assert.Contains(t, evidence, "https://api.reliantapi.com/x")
	assert.Equal(t, 4, strings.Count(evidence, "\n")+1, "every line of evidence survives redaction")

	summary, _ := fields["summary"].(string)
	assert.True(t, strings.HasSuffix(summary, truncatedMarker), "an overlong field is cut and says so")
	assert.LessOrEqual(t, len([]rune(summary)), maxBugReportFieldRunes+len(truncatedMarker))
}

// The bug-report exception to the scrub policy is keyed on the event being a
// bug report. An ordinary error event cannot smuggle free text out by naming
// its context the same way, or by setting the report's tag keys.
func TestScrubEvent_BugReportExceptionIsNarrow(t *testing.T) {
	event := &sentry.Event{
		Level:   sentry.LevelError,
		Message: "activity failed",
		Tags:    map[string]string{"product": "reliant", "severity": "high", "workflow": "builtin://agent"},
		Contexts: map[string]sentry.Context{
			bugReportContext: {"summary": userPrompt, "evidence": fileContentLine},
		},
	}

	got := clientOptions(SentryConfig{Enabled: true}).BeforeSend(event, &sentry.EventHint{})
	require.NotNil(t, got)

	wire := marshalEvent(t, got)
	assert.NotContains(t, wire, userPrompt)
	assert.NotContains(t, wire, fileContentLine)
	for _, key := range []string{"product", "severity", "workflow"} {
		assert.NotContains(t, got.Tags, key, "bug-report tag %s is not part of the general allowlist", key)
	}
}

func TestBugReportFingerprint_GroupsRewordingsOfOneDefect(t *testing.T) {
	first := BugReportFingerprint("reliant", "Message truncated to [content trimmed] in chat 5ffd6bb4")
	again := BugReportFingerprint("reliant", "  message TRUNCATED to `content trimmed` in chat d221a691!  ")
	assert.Equal(t, first, again, "case, punctuation and ids do not split one defect into two issues")
	assert.Equal(t, []string{"llm_bug_report", "reliant", "message truncated to content trimmed in chat"}, first)

	assert.NotEqual(t, first, BugReportFingerprint("forge", "Message truncated to [content trimmed] in chat 5ffd6bb4"),
		"the same words about another product are another issue")
	assert.NotEqual(t, first, BugReportFingerprint("reliant", "Workspace volume detached"))
}

func TestCaptureBugReport_GlobalNoopReporterIsNotLive(t *testing.T) {
	previous := GetReporter()
	t.Cleanup(func() { SetReporter(previous) })
	SetReporter(NewNoopReporter())

	eventID, live := CaptureBugReport(sampleBugReport())
	assert.False(t, live, "with Sentry off the caller must know the report went nowhere")
	assert.Empty(t, eventID)
}

func TestCaptureBugReport_GlobalSentryReporterIsLive(t *testing.T) {
	previous := GetReporter()
	t.Cleanup(func() { SetReporter(previous) })
	reporter, transport := newCapturingReporter(t)
	SetReporter(reporter)

	eventID, live := CaptureBugReport(sampleBugReport())
	assert.True(t, live)
	assert.Equal(t, string(transport.only(t).EventID), eventID)
}
