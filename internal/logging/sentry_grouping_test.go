// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// These run ERROR records through the real path — slog, the process handler
// chain (withReporting: forge's error-class policy, the metrics bridge, this
// bridge), the live SentryReporter and the Sentry client — onto a transport
// the test reads, so they pin what Sentry receives, not what this package
// hands telemetry. The chain matters: every handler between slog and this one
// is a frame between the call site and the stack this bridge reads.
//
// Before, every forwarded record arrived as an exception built from
// errors.New(message) (or the record's wrapped error): Sentry titled the issue
// "errors.errorString", found no stack, and grouped on the exception value —
// one issue per id an error string happened to embed, while unrelated call
// sites that shared an error type merged.

// fakeTransport records what the Sentry client would have sent.
type fakeTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
	sent   chan struct{}
}

func (f *fakeTransport) Flush(time.Duration) bool              { return true }
func (f *fakeTransport) FlushWithContext(context.Context) bool { return true }
func (f *fakeTransport) Configure(sentry.ClientOptions)        {}
func (f *fakeTransport) Close()                                {}
func (f *fakeTransport) SendEvent(event *sentry.Event) {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
	f.sent <- struct{}{}
}

// await waits for n events. The handler reports from its own goroutine.
func (f *fakeTransport) await(t *testing.T, n int) []*sentry.Event {
	t.Helper()
	for range n {
		select {
		case <-f.sent:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected %d Sentry events", n)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*sentry.Event(nil), f.events...)
}

// installSentryTransport makes the process reporter a live SentryReporter
// whose events land on the returned transport.
func installSentryTransport(t *testing.T) *fakeTransport {
	t.Helper()
	hub := sentry.CurrentHub()
	previousClient := hub.Client()
	previousReporter := telemetry.GetReporter()
	t.Cleanup(func() {
		telemetry.SetReporter(previousReporter)
		hub.BindClient(previousClient)
	})

	reporter, err := telemetry.NewSentryReporter(telemetry.SentryConfig{
		Enabled: true,
		DSN:     "https://public@example.ingest.sentry.io/1",
	})
	require.NoError(t, err)
	require.NotNil(t, reporter)

	transport := &fakeTransport{sent: make(chan struct{}, 16)}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.ingest.sentry.io/1",
		Transport: transport,
	})
	require.NoError(t, err)
	hub.BindClient(client)
	telemetry.SetReporter(reporter)
	return transport
}

const reapedThreadsMessage = "[Reconciler] Reaped orphaned threads — a terminal workflow did not cascade to its thread"

// logReapedThreads and logReapedThreadsElsewhere are two call sites logging
// the same line.
func logReapedThreads(logger *slog.Logger, chatID string) {
	logger.Error(reapedThreadsMessage, "chatID", chatID, "rows", 1)
}

func logReapedThreadsElsewhere(logger *slog.Logger, chatID string) {
	logger.Error(reapedThreadsMessage, "chatID", chatID, "rows", 1)
}

func eventsByChat(events []*sentry.Event) map[string]*sentry.Event {
	out := map[string]*sentry.Event{}
	for _, event := range events {
		out[event.Tags["chatID"]] = event
	}
	return out
}

func TestSentryHandler_GroupsByMessageAndCallSite(t *testing.T) {
	transport := installSentryTransport(t)
	logger := slog.New(withReporting(slog.NewTextHandler(io.Discard, nil)))

	const firstChat, secondChat, otherSiteChat = "66a045ce-5872-4efe-a510-910f63fcb8a6", "3f03dc31-95a6-4945-8422-052575b549e0", "chat-elsewhere"
	logReapedThreads(logger, firstChat)
	logReapedThreads(logger, secondChat)
	logReapedThreadsElsewhere(logger, otherSiteChat)

	byChat := eventsByChat(transport.await(t, 3))
	require.Len(t, byChat, 3)
	for chat, event := range byChat {
		assert.Empty(t, event.Exception, "%s: a log line is a message event, not an errors.errorString exception", chat)
		assert.Equal(t, reapedThreadsMessage, event.Message, "%s: the log message is the issue title", chat)
		assert.Equal(t, sentry.LevelError, event.Level)
	}

	first := byChat[firstChat].Fingerprint
	require.Len(t, first, 3, "grouped by an explicit fingerprint, not by whatever the exception value held")
	assert.Equal(t, "slog", first[0])
	assert.True(t, strings.HasSuffix(first[1], "/internal/logging.logReapedThreads"),
		"the call site is the function that logged, not slog or this package's wrappers: %q", first[1])
	assert.Equal(t, reapedThreadsMessage, first[2])

	assert.Equal(t, first, byChat[secondChat].Fingerprint,
		"one line from one place is one issue, whatever ids each occurrence carries")
	assert.NotEqual(t, first, byChat[otherSiteChat].Fingerprint,
		"the same words logged from another function are another issue")

	thread := byChat[firstChat].Threads
	require.Len(t, thread, 1, "the event is located by the logging call's stack")
	frames := thread[0].Stacktrace.Frames
	require.NotEmpty(t, frames)
	assert.Equal(t, "logReapedThreads", frames[len(frames)-1].Function,
		"the innermost frame is the call site")
}

// A record's error is context on the event, not its exception: its text and
// innermost type travel as context and a tag, and two different errors from
// one log line stay one issue.
func TestSentryHandler_ErrorAttributeIsContextNotTheException(t *testing.T) {
	transport := installSentryTransport(t)
	logger := slog.New(withReporting(slog.NewTextHandler(io.Discard, nil)))

	for i, chat := range []string{"chat-a", "chat-b"} {
		err := fmt.Errorf("failed to reap orphaned threads: %w", &timeoutError{seconds: 30 + i})
		logger.Error("[Reconciler] Failed to reap orphaned threads", "error", err, "chatID", chat)
	}

	byChat := eventsByChat(transport.await(t, 2))
	require.Len(t, byChat, 2)
	for chat, event := range byChat {
		assert.Empty(t, event.Exception, "%s: the error must not become the exception", chat)
		assert.Equal(t, "[Reconciler] Failed to reap orphaned threads", event.Message)
		assert.Equal(t, "logging.timeoutError", event.Tags["error_type"],
			"%s: the innermost error's type names what failed", chat)
		assert.Contains(t, event.Contexts["extra"]["error"], "failed to reap orphaned threads: timed out after")
	}
	assert.Equal(t, byChat["chat-a"].Fingerprint, byChat["chat-b"].Fingerprint,
		"different error text from one log line is one issue")
}

type timeoutError struct{ seconds int }

func (e *timeoutError) Error() string { return fmt.Sprintf("timed out after %ds", e.seconds) }
