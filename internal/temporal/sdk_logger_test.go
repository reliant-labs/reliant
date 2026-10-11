// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// recordingHandler keeps every record it is handed, at every level.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) withMessage(t *testing.T, message string) slog.Record {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == message {
			return r
		}
	}
	t.Fatalf("no %q line was written", message)
	return slog.Record{}
}

// sdkActivityFailureLine runs one failing attempt of activityType through the
// SDK's real activity task handler, logging through NewSDKLogger, and returns
// the line the SDK wrote about it.
func sdkActivityFailureLine(t *testing.T, activityType string, fn any, args ...any) slog.Record {
	t.Helper()
	lines := &recordingHandler{}
	var suite temporaltest.WorkflowTestSuite
	suite.SetLogger(NewSDKLogger(slog.New(lines)))
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: activityType})

	_, err := env.ExecuteActivity(activityType, args...)
	require.Error(t, err, "the attempt must fail")
	return lines.withMessage(t, sdkActivityFailureMessage)
}

func activityTypeFor(t *testing.T) string {
	return "SDKLoggerTest_" + strings.ReplaceAll(t.Name(), "/", "_")
}

// The SDK writes "Activity error." at ERROR for every failed attempt. For an
// activity the workflow runtime wraps, that is a second report of a failure
// the wrapper already logged at the level it chose — WARN for a retryable one
// — so a retry reliant kept out of Sentry reached it anyway (ELECTRON-CK).
func TestSDKLogger_ActivityFailureTheWrapperReportedIsAWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"retryable", temporal.NewApplicationError("provider overloaded", "ProviderOverloaded")},
		{"terminal", temporal.NewNonRetryableApplicationError("bad request", "TerminalError", nil)},
		{"canceled", temporal.NewCanceledError()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			activityType := activityTypeFor(t)
			DeclareSelfReportingActivity(activityType)

			line := sdkActivityFailureLine(t, activityType, func(context.Context) error { return tc.err })
			assert.Equal(t, slog.LevelWarn.String(), line.Level.String(), "the line is kept, but does not page")
		})
	}
}

// Everything the wrapper did not report stays an ERROR: for those, the SDK's
// line is the only report there is.
func TestSDKLogger_ActivityFailureOnlyTheSDKReportedStaysAnError(t *testing.T) {
	appErr := temporal.NewNonRetryableApplicationError("trigger is misconfigured", "NonRetryableFireError", nil)

	t.Run("an activity registered outside the wrapper", func(t *testing.T) {
		line := sdkActivityFailureLine(t, activityTypeFor(t), func(context.Context) error { return appErr })
		assert.Equal(t, slog.LevelError.String(), line.Level.String())
	})

	t.Run("an error the wrapper cannot have returned", func(t *testing.T) {
		activityType := activityTypeFor(t)
		DeclareSelfReportingActivity(activityType)
		line := sdkActivityFailureLine(t, activityType, func(context.Context) error { return errors.New("plain") })
		assert.Equal(t, slog.LevelError.String(), line.Level.String())
	})

	t.Run("an input the SDK cannot decode, before the wrapper runs", func(t *testing.T) {
		activityType := activityTypeFor(t)
		DeclareSelfReportingActivity(activityType)
		line := sdkActivityFailureLine(t, activityType, func(context.Context, int) error { return nil }, "not a number")
		assert.Equal(t, slog.LevelError.String(), line.Level.String())
	})

	t.Run("reliant code writing the same words", func(t *testing.T) {
		activityType := activityTypeFor(t)
		DeclareSelfReportingActivity(activityType)
		lines := &recordingHandler{}
		NewSDKLogger(slog.New(lines)).Error(sdkActivityFailureMessage, "ActivityType", activityType, "Error", appErr)
		assert.Equal(t, slog.LevelError.String(), lines.withMessage(t, sdkActivityFailureMessage).Level.String())
	})
}
