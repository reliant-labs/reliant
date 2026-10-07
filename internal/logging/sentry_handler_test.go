// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingReporter records the tags and extra of the one event it is sent.
type capturingReporter struct {
	telemetry.NoopReporter
	err   error
	tags  map[string]string
	extra map[string]interface{}
}

func (r *capturingReporter) CaptureExceptionWithContext(err error, tags map[string]string, extra map[string]interface{}) string {
	r.err, r.tags, r.extra = err, tags, extra
	return "event"
}

func installCapturingReporter(t *testing.T) *capturingReporter {
	t.Helper()
	previous := telemetry.GetReporter()
	reporter := &capturingReporter{}
	telemetry.SetReporter(reporter)
	t.Cleanup(func() { telemetry.SetReporter(previous) })
	return reporter
}

// reportRecord runs one ERROR record through the Sentry bridge synchronously.
// Handle reports from a goroutine; the test calls the reporting step directly
// so it can read the result without waiting on one.
func reportRecord(handler *sentryHandler, attrs ...slog.Attr) {
	record := slog.NewRecord(time.Time{}, slog.LevelError, "tool execution failed", 0)
	record.AddAttrs(attrs...)
	handler.reportToSentry(record)
}

// Short content is still content. Before the allowlist, every attribute of 64
// characters or fewer became a Sentry tag, so a short shell command, file path
// or prompt left the process as a searchable, indexed tag. Only identifiers and
// small enums may be promoted; the rest stays in the log line.
func TestSentryHandler_ContentKeysAreNotPromoted(t *testing.T) {
	reporter := installCapturingReporter(t)
	handler := newSentryHandler(slog.NewTextHandler(io.Discard, nil))

	content := []slog.Attr{
		slog.String("command", "cat ~/.aws/credentials"),
		slog.String("file_path", "/home/founder/billing/charge.go"),
		slog.String("path", "/Users/founder/.ssh/id_ed25519"),
		slog.String("prompt", "refactor the payment module"),
		slog.String("user_prompt", "fix the login bug"),
		slog.String("query", "select * from users"),
		slog.String("content", "func chargeCustomer() error"),
		slog.String("output", "permission denied"),
		slog.String("stderr", "fatal: not a git repository"),
	}
	reportRecord(handler, append(content, slog.Any("error", errors.New("exit status 1")))...)

	require.NotNil(t, reporter.err, "the error itself must still be reported")
	for _, attr := range content {
		assert.NotContains(t, reporter.tags, attr.Key, "%q must not become a Sentry tag", attr.Key)
		assert.NotContains(t, reporter.extra, attr.Key, "%q must not reach Sentry at all", attr.Key)
		for key, value := range reporter.tags {
			assert.NotEqual(t, attr.Value.String(), value, "value of %q leaked under tag %q", attr.Key, key)
		}
	}
}

// Identifiers and short enums are what Sentry is for: they find the run.
func TestSentryHandler_IdentifiersAndEnumsArePromoted(t *testing.T) {
	reporter := installCapturingReporter(t)
	handler := newSentryHandler(slog.NewTextHandler(io.Discard, nil)).
		WithAttrs([]slog.Attr{slog.String("service", "reliant-worker")}).(*sentryHandler)

	reportRecord(handler,
		slog.String("chat_id", "3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b"),
		slog.String("workflow_id", "wf-3f2a8c1e"),
		slog.String("run_id", "run_42"),
		slog.String("thread_id", "thr_7c9e6679"),
		slog.String("tool_call_id", "toolu_01AbCdEfGhIjKlMn"),
		slog.String("daemon_id", "dmn_123"),
		slog.String("user_id", "user-8d7f"),
		slog.String("org_id", "org_9"),
		slog.String("component", "toolexec"),
		slog.String("phase", "execute"),
		slog.String("status", "failed"),
		slog.String("kind", "shell"),
		slog.String("provider", "anthropic"),
		slog.String("model", "claude-sonnet-5"),
		slog.Int("attempt", 3),
		slog.Group("req", slog.String("chat_id", "chat-in-group"), slog.String("prompt", "grouped prompt")),
	)

	for key, want := range map[string]string{
		"service":      "reliant-worker",
		"chat_id":      "3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b",
		"workflow_id":  "wf-3f2a8c1e",
		"run_id":       "run_42",
		"thread_id":    "thr_7c9e6679",
		"tool_call_id": "toolu_01AbCdEfGhIjKlMn",
		"daemon_id":    "dmn_123",
		"user_id":      "user-8d7f",
		"org_id":       "org_9",
		"component":    "toolexec",
		"phase":        "execute",
		"status":       "failed",
		"kind":         "shell",
		"provider":     "anthropic",
		"model":        "claude-sonnet-5",
		"attempt":      "3",
		"req.chat_id":  "chat-in-group",
	} {
		assert.Equal(t, want, reporter.tags[key], "tag %q", key)
	}
	assert.NotContains(t, reporter.tags, "req.prompt")
	assert.NotContains(t, reporter.extra, "req.prompt")
}

// An allowlisted key does not vouch for its value. A status that is really a
// sentence, or an id that carries whitespace, is not an identifier.
func TestSentryHandler_AllowlistedKeyWithContentValueIsNotPromoted(t *testing.T) {
	reporter := installCapturingReporter(t)
	handler := newSentryHandler(slog.NewTextHandler(io.Discard, nil))

	reportRecord(handler,
		slog.String("status", "failed because the user typed rm -rf"),
		slog.String("chat_id", "chat id with spaces"),
	)

	assert.NotContains(t, reporter.tags, "status")
	assert.NotContains(t, reporter.tags, "chat_id")
}

// Numbers and flags cannot carry content, so a non-allowlisted one still
// travels as extra context.
func TestSentryHandler_NumbersGoToExtra(t *testing.T) {
	reporter := installCapturingReporter(t)
	handler := newSentryHandler(slog.NewTextHandler(io.Discard, nil))

	reportRecord(handler, slog.Int("exit_code", 1), slog.Bool("retried", true), slog.Int64("bytes_written", 2048))

	assert.EqualValues(t, 1, reporter.extra["exit_code"])
	assert.Equal(t, true, reporter.extra["retried"])
	assert.EqualValues(t, 2048, reporter.extra["bytes_written"])
	assert.NotContains(t, reporter.tags, "exit_code")
}

// The Handle path the process actually uses must still forward the inner line
// and report ERRORs asynchronously.
func TestSentryHandler_HandleStillReports(t *testing.T) {
	reporter := &waitingReporter{done: make(chan struct{})}
	previous := telemetry.GetReporter()
	telemetry.SetReporter(reporter)
	t.Cleanup(func() { telemetry.SetReporter(previous) })

	handler := newSentryHandler(slog.NewTextHandler(io.Discard, nil))
	record := slog.NewRecord(time.Time{}, slog.LevelError, "boom", 0)
	record.AddAttrs(slog.String("chat_id", "chat-1"), slog.String("command", "ls"))
	require.NoError(t, handler.Handle(context.Background(), record))

	select {
	case <-reporter.done:
	case <-time.After(5 * time.Second):
		t.Fatal("ERROR record was never reported")
	}
	assert.Equal(t, "chat-1", reporter.tags["chat_id"])
	assert.NotContains(t, reporter.tags, "command")
}

type waitingReporter struct {
	telemetry.NoopReporter
	tags map[string]string
	done chan struct{}
}

func (r *waitingReporter) CaptureExceptionWithContext(_ error, tags map[string]string, _ map[string]interface{}) string {
	r.tags = tags
	close(r.done)
	return "event"
}
