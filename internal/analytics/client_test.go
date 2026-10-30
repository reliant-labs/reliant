// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/version"
)

// consent is a PrivacyChecker answering from a map; a user it does not know
// is a read failure.
type consent map[string]bool

func (c consent) GetAnalyticsEnabled(_ context.Context, userID string) (bool, error) {
	enabled, ok := c[userID]
	if !ok {
		return false, errors.New("settings unavailable")
	}
	return enabled, nil
}

// jsonClient is a Client writing JSON lines to the returned buffer: the exact
// shape a server writes to stdout, which the node agent's json_parser turns
// into ClickStack LogAttributes.
func jsonClient(t *testing.T, c consent) (*Client, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewClient(c, slog.New(slog.NewJSONHandler(&buf, nil))), &buf
}

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &rec), sc.Text())
		out = append(out, rec)
	}
	return out
}

func TestEmitsProductRecordWithUserAndTypedFields(t *testing.T) {
	c, buf := jsonClient(t, consent{"user-1": true})

	c.ForUser(context.Background(), "user-1").TrackMessageSent(MessageSentMetrics{
		MessageID:     "msg-1",
		ChatID:        "chat-1",
		ProjectID:     "proj-1",
		ContentLength: 42,
		IsFirstInChat: true,
		WorkflowName:  "default",
	})

	recs := records(t, buf)
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "product.message_sent", rec["msg"])
	assert.Equal(t, "product.message_sent", rec[AttrEventName])
	assert.Equal(t, "user-1", rec[AttrUserID])
	assert.Equal(t, version.Version, rec[AttrAppVersion])
	assert.Equal(t, "backend", rec[AttrAppClient])
	assert.Equal(t, "msg-1", rec["product.message_id"])
	assert.Equal(t, "chat-1", rec["product.chat_id"])
	assert.Equal(t, "proj-1", rec["product.project_id"])
	assert.Equal(t, float64(42), rec["product.content_length"])
	assert.Equal(t, true, rec["product.is_first_in_chat"])
	assert.Equal(t, false, rec["product.has_attachments"])
	assert.Equal(t, "default", rec["product.workflow_name"])
	assert.NotContains(t, rec, "product.workflow_id", "an unset optional field is omitted, not written empty")
}

func TestListFieldsAreJSONArrays(t *testing.T) {
	c, buf := jsonClient(t, consent{"user-1": true})

	c.ForUser(context.Background(), "user-1").TrackWorkflowEnded(WorkflowMetrics{
		WorkflowID: "wf-1",
		Providers:  []string{"anthropic", "openai"},
		Duration:   1500 * time.Millisecond,
		Success:    true,
	})

	recs := records(t, buf)
	require.Len(t, recs, 1)
	assert.Equal(t, "product.workflow_ended", recs[0][AttrEventName])
	assert.Equal(t, []any{"anthropic", "openai"}, recs[0]["product.providers"])
	assert.Equal(t, float64(1500), recs[0]["product.duration_ms"])
}

func TestOptOutRecordsNothing(t *testing.T) {
	c, buf := jsonClient(t, consent{"opted-out": false})
	ctx := context.Background()

	opted := c.ForUser(ctx, "opted-out")
	opted.TrackSessionStart()
	opted.TrackMessageSent(MessageSentMetrics{MessageID: "m"})
	opted.TrackLLMCallCompleted(LLMCallMetrics{Provider: "anthropic"})
	// A consent read that fails records nothing either: no event is written
	// for a user whose consent was not confirmed.
	c.ForUser(ctx, "unknown-user").TrackSessionStart()

	assert.Empty(t, buf.String())
}

func TestNoConsentSourceRecordsNoUserEvents(t *testing.T) {
	var buf bytes.Buffer
	c := NewClient(nil, slog.New(slog.NewJSONHandler(&buf, nil)))

	c.ForUser(context.Background(), "user-1").TrackSessionStart()
	assert.Empty(t, buf.String())

	// An event about nobody needs no consent.
	c.ForUser(context.Background(), "").TrackPageVisited(PageVisitedMetrics{PageName: "chat"})
	recs := records(t, &buf)
	require.Len(t, recs, 1)
	assert.NotContains(t, recs[0], AttrUserID)
}

func TestDisabledOrUninstalledRecordsNothing(t *testing.T) {
	t.Cleanup(func() { installed.Store(nil) })

	var nilClient *Client
	nilClient.ForUser(context.Background(), "user-1").TrackSessionStart()
	Tracker{}.TrackSessionStart()

	Start(Settings{Disabled: true}, consent{"user-1": true})
	assert.Nil(t, installed.Load())
	assert.Equal(t, Tracker{}, ForUser(context.Background(), "user-1"))

	Start(Settings{}, consent{"user-1": true})
	assert.NotEqual(t, Tracker{}, ForUser(context.Background(), "user-1"))
}

func TestDuplicateWithinWindowIsDroppedPerUser(t *testing.T) {
	c, buf := jsonClient(t, consent{"a": true, "b": true})
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	page := PageVisitedMetrics{PageName: "settings"}

	c.ForUser(ctx, "a").TrackPageVisited(page)
	c.ForUser(ctx, "a").TrackPageVisited(page) // double-fired: dropped
	c.ForUser(ctx, "b").TrackPageVisited(page) // another user: kept
	now = now.Add(dedupWindow)
	c.ForUser(ctx, "a").TrackPageVisited(page) // after the window: kept

	var users []any
	for _, rec := range records(t, buf) {
		users = append(users, rec[AttrUserID])
	}
	assert.Equal(t, []any{"a", "b", "a"}, users)
}

func TestWorkflowEndedRetryIsOneEvent(t *testing.T) {
	c, buf := jsonClient(t, consent{"a": true})
	tr := c.ForUser(context.Background(), "a")

	// A retried activity reports the same run with a different duration.
	tr.TrackWorkflowEnded(WorkflowMetrics{WorkflowID: "wf", Success: true, Duration: time.Second})
	tr.TrackWorkflowEnded(WorkflowMetrics{WorkflowID: "wf", Success: true, Duration: 2 * time.Second})

	assert.Len(t, records(t, buf), 1)
}

func TestLLMCallCompletedCarriesCountsAndMetadataOnly(t *testing.T) {
	c, buf := jsonClient(t, consent{"user-1": true})

	c.ForUser(context.Background(), "user-1").TrackLLMCallCompleted(LLMCallMetrics{
		Provider:         "anthropic",
		Model:            "claude-sonnet",
		InputTokens:      1200,
		OutputTokens:     340,
		CacheReadTokens:  900,
		CacheWriteTokens: 50,
		LatencyMs:        2100,
		Success:          false,
		ErrorType:        "rate_limit",
		IsStreaming:      true,
		WorkflowID:       "wf-1",
		ChatID:           "chat-1",
		StepName:         "agent",
	})

	recs := records(t, buf)
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.Equal(t, "product.llm_call_completed", rec[AttrEventName])
	assert.Equal(t, "user-1", rec[AttrUserID])
	assert.Equal(t, "backend", rec[AttrAppClient])
	assert.Equal(t, "anthropic", rec["product.provider"])
	assert.Equal(t, "claude-sonnet", rec["product.model"])
	assert.Equal(t, float64(1200), rec["product.input_tokens"])
	assert.Equal(t, float64(340), rec["product.output_tokens"])
	assert.Equal(t, float64(900), rec["product.cache_read_tokens"])
	assert.Equal(t, float64(50), rec["product.cache_write_tokens"])
	assert.Equal(t, float64(2100), rec["product.latency_ms"])
	assert.Equal(t, false, rec["product.success"])
	assert.Equal(t, "rate_limit", rec["product.error_type"])
	assert.Equal(t, true, rec["product.is_streaming"])
	assert.Equal(t, "wf-1", rec["product.workflow_id"])
	assert.Equal(t, "chat-1", rec["product.chat_id"])
	assert.Equal(t, "agent", rec["product.step_name"])

	// The contract is counts and metadata: no field may carry text.
	for key := range rec {
		assert.NotContains(t, key, "content")
		assert.NotContains(t, key, "prompt")
		assert.NotContains(t, key, "message")
	}
}

func TestLLMCallCompletedOmitsUnsetOptionalFields(t *testing.T) {
	c, buf := jsonClient(t, consent{"user-1": true})

	c.ForUser(context.Background(), "user-1").TrackLLMCallCompleted(LLMCallMetrics{Provider: "openai", Model: "m", Success: true})

	rec := records(t, buf)[0]
	for _, key := range []string{"error_type", "workflow_id", "chat_id", "step_name", "cache_read_tokens", "cache_write_tokens"} {
		assert.NotContains(t, rec, "product."+key)
	}
	assert.Equal(t, true, rec["product.success"])
}
