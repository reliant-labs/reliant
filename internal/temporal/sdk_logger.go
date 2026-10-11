// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
)

// NewSDKLogger is the logger the Temporal SDK writes through — its own lines
// and every workflow.GetLogger / activity.GetLogger line — built on logger. It
// is slog's, with one correction: see activityFailureHandler.
func NewSDKLogger(logger *slog.Logger) log.Logger {
	return log.NewStructuredLogger(slog.New(&activityFailureHandler{inner: logger.Handler()}))
}

// selfReportingActivities holds the activity types declared with
// DeclareSelfReportingActivity.
var selfReportingActivities sync.Map // activity type -> struct{}

// DeclareSelfReportingActivity records that reliant logs every error the named
// activity returns, at the level it chose for that error, so the SDK's own
// per-attempt echo of it is a duplicate. The workflow runtime's activity
// registry declares every activity it wraps.
func DeclareSelfReportingActivity(activityType string) {
	selfReportingActivities.Store(activityType, struct{}{})
}

// sdkActivityFailureMessage is the line the SDK writes for every failed
// attempt of every activity, at ERROR unless the error is benign, with the
// error under "Error" (activityTaskHandlerImpl.Execute).
const sdkActivityFailureMessage = "Activity error."

// sdkFunctionPrefix marks the SDK's own code.
const sdkFunctionPrefix = "go.temporal.io/sdk/"

// activityFailureHandler writes the SDK's activity-failure line at WARN when
// reliant has already reported that failure itself. The line is kept; it
// just does not page.
//
// The workflow runtime's activity wrapper logs every error an activity it
// registered returns: at WARN when Temporal will retry it, at ERROR — and as
// a Sentry exception — when it is terminal. The SDK then logs the same error
// again at ERROR whichever it was, so a retryable failure reliant kept out of
// Sentry on purpose reached it anyway, as one issue for every activity type
// (ELECTRON-CK: a CallLLM attempt the wrapper had logged at WARN).
//
// Narrowly: the record must be the SDK's own line — its PC inside the SDK, so
// reliant code writing the same words is untouched — for a declared activity
// type, carrying an error the wrapper can have returned: an ApplicationError
// or a cancellation. Everything else stays at ERROR, and with it the failures
// this line is the only report of: activities registered outside the wrapper
// (the trigger activities), and failures the SDK raises itself before or
// after the wrapper runs — an input it cannot decode, a result it cannot
// encode — which are plain errors.
type activityFailureHandler struct {
	inner slog.Handler
}

func (h *activityFailureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *activityFailureHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && reportedByReliant(r) {
		r.Level = slog.LevelWarn
		// The SDK asked Enabled about the level it wrote.
		if !h.inner.Enabled(ctx, r.Level) {
			return nil
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *activityFailureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &activityFailureHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *activityFailureHandler) WithGroup(name string) slog.Handler {
	return &activityFailureHandler{inner: h.inner.WithGroup(name)}
}

// reportedByReliant reports whether r is the SDK's activity-failure line for a
// failure the activity wrapper has already logged.
func reportedByReliant(r slog.Record) bool {
	if r.Message != sdkActivityFailureMessage || !loggedBySDK(r.PC) {
		return false
	}
	var activityType string
	var err error
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "ActivityType":
			activityType = a.Value.String()
		case "Error":
			err, _ = a.Value.Any().(error)
		}
		return true
	})
	if _, declared := selfReportingActivities.Load(activityType); !declared || err == nil {
		return false
	}
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) || temporal.IsCanceledError(err) || errors.Is(err, context.Canceled)
}

// loggedBySDK reports whether pc, a record's call site, is in the SDK.
func loggedBySDK(pc uintptr) bool {
	if pc == 0 {
		return false
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return strings.HasPrefix(frame.Function, sdkFunctionPrefix)
}
