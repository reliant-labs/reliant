// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

type captured struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureHandler struct {
	mu   *sync.Mutex
	recs *[]captured
	with []slog.Attr
}

func (captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h captureHandler) WithAttrs(a []slog.Attr) slog.Handler {
	h.with = append(append([]slog.Attr(nil), h.with...), a...)
	return h
}
func (h captureHandler) WithGroup(string) slog.Handler { return h }
func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]any{}
	for _, a := range h.with {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, captured{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

// activityInput is a v3 activity's input shape: the run's ids sit one object
// down, in runtime.
type activityInput struct {
	Runtime struct {
		ChatID     string `json:"chat_id"`
		Thread     string `json:"thread"`
		WorkflowID string `json:"workflow_id,omitempty"`
	} `json:"runtime"`
}

func runFailingActivity(t *testing.T, owners *telemetry.ChatOwnerCache, input activityInput) (records func() []captured) {
	t.Helper()
	var mu sync.Mutex
	var recs []captured
	// The production chain: the scope handler in front of the writer, and the SDK
	// logger built on it, exactly as the worker's client is.
	handler := logging.NewScopeHandler(captureHandler{mu: &mu, recs: &recs})
	logger := slog.New(handler)
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	var suite temporaltest.WorkflowTestSuite
	suite.SetLogger(rtemporal.NewSDKLogger(logger))
	env := suite.NewTestActivityEnvironment()
	env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{NewRunScopeWorkerInterceptor(owners)},
	}))
	fail := func(ctx context.Context, in activityInput) error {
		// Both ways a worker logs: through the process logger with the activity's
		// context, and through the Temporal activity logger.
		logging.ErrorContext(ctx, "[Test] activity failed", "error", errors.New("boom"))
		activity.GetLogger(ctx).Error("[Test] sdk logger line")
		return errors.New("boom")
	}
	env.RegisterActivityWithOptions(fail, activity.RegisterOptions{Name: "ScopeProbe"})
	_, err := env.ExecuteActivity("ScopeProbe", input)
	require.Error(t, err)
	return func() []captured {
		mu.Lock()
		defer mu.Unlock()
		return append([]captured(nil), recs...)
	}
}

func scopeProbeInput() activityInput {
	var in activityInput
	in.Runtime.ChatID, in.Runtime.Thread = "chat-42", "thread-7"
	return in
}

// An activity's error log carries the user and the chat it ran for. Before this,
// a worker's error records named neither: only 2 of ~17k carried a user id.
func TestActivityErrorLogCarriesTheUserAndTheChat(t *testing.T) {
	owners := telemetry.NewChatOwnerCache(func(_ context.Context, chat string) (string, error) {
		assert.Equal(t, "chat-42", chat)
		return "user-5", nil
	}, time.Hour, 10)
	recs := runFailingActivity(t, owners, scopeProbeInput())()

	var seen int
	for _, r := range recs {
		if r.msg != "[Test] activity failed" && r.msg != "[Test] sdk logger line" {
			continue
		}
		seen++
		assert.Equal(t, "user-5", r.attrs["user.id"], r.msg)
		assert.Equal(t, "chat-42", r.attrs["reliant.chat.id"], r.msg)
		assert.Equal(t, "thread-7", r.attrs["thread.id"], r.msg)
		assert.NotEmpty(t, r.attrs["workflow.id"], "%s: the workflow comes from the activity's own execution info", r.msg)
	}
	assert.Equal(t, 2, seen, "both the process logger line and the SDK logger line are stamped")
}

// The owner lookup failing must neither fail the activity nor drop the chat.
func TestActivityRunsAndStampsTheChatWhenTheOwnerCannotBeRead(t *testing.T) {
	owners := telemetry.NewChatOwnerCache(func(context.Context, string) (string, error) {
		return "", errors.New("database unavailable")
	}, time.Hour, 10)
	recs := runFailingActivity(t, owners, scopeProbeInput())()
	for _, r := range recs {
		if r.msg == "[Test] activity failed" {
			assert.Equal(t, "chat-42", r.attrs["reliant.chat.id"])
			assert.NotContains(t, r.attrs, "user.id")
			return
		}
	}
	t.Fatal("the activity's error line was not logged")
}

func TestRunScopeOfActivityArgs(t *testing.T) {
	flat := map[string]any{"chat_id": "c", "thread_id": "t", "workflow_id": "w"}
	assert.Equal(t, telemetry.RunScope{ChatID: "c", ThreadID: "t", WorkflowID: "w"}, RunScopeOfActivityArgs([]any{flat}))

	assert.Equal(t, telemetry.RunScope{ChatID: "chat-42", ThreadID: "thread-7"}, RunScopeOfActivityArgs([]any{scopeProbeInput()}))

	assert.Equal(t, telemetry.RunScope{}, RunScopeOfActivityArgs(nil))
	assert.Equal(t, telemetry.RunScope{}, RunScopeOfActivityArgs([]any{"not an object"}))
	assert.Equal(t, telemetry.RunScope{}, RunScopeOfActivityArgs([]any{make(chan int)}))
}
