// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tlog "go.temporal.io/sdk/log"
)

// These pin which function a forwarded ERROR is attributed to — the issue's
// culprit and the call-site half of its fingerprint — for every front end
// that logs into the process handler chain.
//
// The Temporal SDK's slog adapter builds the record itself and calls the
// handler directly, so no slog frame is on the stack. Finding the call site as
// "the first frame past slog" then found none, skipped this package's handlers
// as logger wrappers, and named the next frame: forge's
// errorClassHandler.Handle, the outermost handler. Every ERROR logged through
// workflow.GetLogger, activity.GetLogger or by the SDK itself was filed under
// it (ELECTRON-CM, CK, CE), and two call sites logging the same words merged.

const historyEndsMessage = "[CallLLM] History ends with an assistant message after the mailbox drain"

// Each front end logs from a function of its own: the call site the event
// must name.

func logThroughSlog(logger *slog.Logger) {
	logger.Error(historyEndsMessage, "chatID", "chat-slog")
}

func logThroughTemporal(logger tlog.Logger) {
	logger.Error(historyEndsMessage, "chatID", "chat-temporal")
}

func logThroughPackageError() {
	Error(historyEndsMessage, "chatID", "chat-package")
}

// outerHandler stands for whatever handler wraps the process chain next: one
// more frame between the call site and this bridge.
type outerHandler struct{ inner slog.Handler }

func (h outerHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}
func (h outerHandler) Handle(ctx context.Context, r slog.Record) error { return h.inner.Handle(ctx, r) }
func (h outerHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return outerHandler{h.inner.WithAttrs(attrs)}
}
func (h outerHandler) WithGroup(name string) slog.Handler {
	return outerHandler{h.inner.WithGroup(name)}
}

func processChain() slog.Handler { return withReporting(slog.NewTextHandler(io.Discard, nil)) }

// useAsDefault installs h as the process logger for the test, as install does.
func useAsDefault(t *testing.T, h slog.Handler) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

func TestSentryHandler_AttributesEveryFrontEndToItsCaller(t *testing.T) {
	for _, tc := range []struct {
		name     string
		log      func(t *testing.T)
		callSite string
	}{
		{
			name:     "slog.Logger",
			log:      func(*testing.T) { logThroughSlog(slog.New(processChain())) },
			callSite: "logThroughSlog",
		},
		{
			name:     "this package's Error",
			log:      func(t *testing.T) { useAsDefault(t, processChain()); logThroughPackageError() },
			callSite: "logThroughPackageError",
		},
		{
			name:     "Temporal adapter",
			log:      func(*testing.T) { logThroughTemporal(tlog.NewStructuredLogger(slog.New(processChain()))) },
			callSite: "logThroughTemporal",
		},
		{
			// activity.GetLogger: the worker's adapter, With the activity's tags.
			name: "Temporal activity logger",
			log: func(*testing.T) {
				worker := tlog.NewStructuredLogger(slog.New(processChain()))
				logThroughTemporal(tlog.With(worker, "ActivityType", "CallLLM", "Attempt", 1))
			},
			callSite: "logThroughTemporal",
		},
		{
			name: "Temporal adapter behind another handler",
			log: func(*testing.T) {
				logThroughTemporal(tlog.NewStructuredLogger(slog.New(outerHandler{processChain()})))
			},
			callSite: "logThroughTemporal",
		},
		{
			name:     "slog.Logger behind another handler",
			log:      func(*testing.T) { logThroughSlog(slog.New(outerHandler{processChain()})) },
			callSite: "logThroughSlog",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := installSentryTransport(t)
			tc.log(t)

			event := transport.await(t, 1)[0]
			assert.Equal(t, "logging."+tc.callSite, event.Transaction,
				"the culprit is the function that logged, not a handler between it and Sentry")
			require.Len(t, event.Fingerprint, 3)
			assert.True(t, strings.HasSuffix(event.Fingerprint[1], "/internal/logging."+tc.callSite),
				"the fingerprint's call site is the function that logged: %q", event.Fingerprint[1])
			require.Len(t, event.Threads, 1)
			frames := event.Threads[0].Stacktrace.Frames
			require.NotEmpty(t, frames)
			assert.Equal(t, tc.callSite, frames[len(frames)-1].Function, "the innermost frame is the call site")
		})
	}
}

// buildRecordHere builds an ERROR record the way a front end does, its PC from
// runtime.Callers, naming this function.
func buildRecordHere() slog.Record {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return slog.NewRecord(time.Now(), slog.LevelError, historyEndsMessage, pcs[0])
}

// A record handled on a goroutine other than the one that built it — as by a
// handler that queues records — is still attributed to its PC: the handling
// goroutine's stack says nothing about who logged.
func TestSentryHandler_RecordFromAnotherGoroutineKeepsItsCallSite(t *testing.T) {
	transport := installSentryTransport(t)
	records := make(chan slog.Record)
	go func() { records <- buildRecordHere() }()
	record := <-records

	require.NoError(t, processChain().Handle(context.Background(), record))

	event := transport.await(t, 1)[0]
	assert.Equal(t, "logging.buildRecordHere", event.Transaction)
}

// The Temporal SDK logs its errors under "Error". Only "error"/"err" used to
// be read, so its "Activity error." line reached Sentry with no error at all
// (ELECTRON-CK) and forge's policy classified one error while Sentry showed
// none.
func TestSentryHandler_CarriesTheErrorWhateverItsKey(t *testing.T) {
	transport := installSentryTransport(t)
	logger := tlog.NewStructuredLogger(slog.New(processChain()))

	err := fmt.Errorf("failed to stream LLM response: %w", &timeoutError{seconds: 30})
	logger.Error("Activity error.", "ActivityType", "CallLLM", "Attempt", 1, "Error", err)

	event := transport.await(t, 1)[0]
	assert.Equal(t, "logging.timeoutError", event.Tags["error_type"], "the innermost error's type names what failed")
	assert.Contains(t, event.Contexts["extra"]["error"], "failed to stream LLM response: timed out after 30s")
}

func TestSentryHandler_WhichAttributeIsTheError(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	for _, tc := range []struct {
		name   string
		logger []slog.Attr
		attrs  []slog.Attr
		want   string
	}{
		{name: "an error value under any key", attrs: []slog.Attr{slog.Any("Error", first)}, want: "first"},
		{name: "the first error value", attrs: []slog.Attr{slog.Any("cause", first), slog.Any("error", second)}, want: "first"},
		{name: "an error value over an error string", attrs: []slog.Attr{slog.String("error", "text"), slog.Any("cause", first)}, want: "first"},
		{name: "an error string under error", attrs: []slog.Attr{slog.String("error", "text")}, want: "text"},
		{name: "an error string under err", attrs: []slog.Attr{slog.String("err", "text")}, want: "text"},
		{name: "a string under another key is not an error", attrs: []slog.Attr{slog.String("Error", "text")}, want: ""},
		{name: "the record's error over the logger's", logger: []slog.Attr{slog.Any("error", second)}, attrs: []slog.Attr{slog.Any("Error", first)}, want: "first"},
		{name: "the logger's error when the record has none", logger: []slog.Attr{slog.Any("error", second)}, want: "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reporter := installCapturingReporter(t)
			handler := newSentryHandler(slog.NewTextHandler(io.Discard, nil))
			if tc.logger != nil {
				handler = handler.WithAttrs(tc.logger).(*sentryHandler)
			}
			reportRecord(handler, tc.attrs...)

			if tc.want == "" {
				assert.NoError(t, reporter.err)
				return
			}
			require.Error(t, reporter.err)
			assert.Equal(t, tc.want, reporter.err.Error())
		})
	}
}
