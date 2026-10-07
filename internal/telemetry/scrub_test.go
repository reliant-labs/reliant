// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Values a real event carries that must never reach Sentry. Each is distinct
// enough that finding it anywhere in the marshalled event is unambiguous.
const (
	userPrompt      = "Please refactor the payment reconciliation module"
	toolOutputLine  = "STRIPE_SECRET=sk_live_toolOutputLeak"
	fileContentLine = "func chargeCustomer(amount int) error"
	shellCommand    = "cat ~/.aws/credentials"
	anthropicKey    = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	reliantToken    = "rlat_0123456789abcdefghij"
	oauthCode       = "OAUTHCODE123"
	userEmail       = "founder@example.com"
	userIP          = "203.0.113.9"
	diffBody        = "+++ b/internal/billing/charge.go"
	frameSecret     = "sk-live-frameVarSecretValue0000"

	chatID     = "3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b"
	workflowID = "wf-3f2a8c1e-9b7d-4e2f"
	toolCallID = "toolu_01AbCdEfGhIjKlMn"
	threadID   = "thr_7c9e6679"
	userID     = "user-8d7f"
)

// realisticErrorEvent is the shape logging.sentryHandler and the activity
// error reporter actually produce: a wrapped error whose message embeds tool
// output, slog attributes split into short tags and long "extra" values, plus
// the request, breadcrumb, user and attachment fields the SDK can populate.
func realisticErrorEvent() *sentry.Event {
	stack := &sentry.Stacktrace{Frames: []sentry.Frame{{
		Function: "executeTool",
		Module:   "github.com/reliant-labs/reliant/internal/llm/tools",
		Lineno:   42,
		Vars:     map[string]interface{}{"apiKey": frameSecret},
	}}}
	return &sentry.Event{
		Level: sentry.LevelError,
		Exception: []sentry.Exception{
			{
				Type:       "*fmt.wrapError",
				Value:      "executing tool bash: exit status 1: " + toolOutputLine + "\n" + fileContentLine + "\n" + diffBody,
				Stacktrace: stack,
				Mechanism:  &sentry.Mechanism{Type: "generic", Description: userPrompt, Data: map[string]any{"prompt": userPrompt}},
			},
			{
				Type:  "*errors.errorString",
				Value: "anthropic: 401 Unauthorized: Authorization: Bearer " + anthropicKey,
			},
		},
		Tags: map[string]string{
			"component":     "backend",
			"chat_id":       chatID,
			"workflow_id":   workflowID,
			"activity_type": "execute_tool",
			"procedure":     "/reliant.v1.ChatService/SendMessage",
			// Short slog attributes become tags verbatim in sentryHandler.
			"command":   shellCommand,
			"file_path": "/Users/alice/acme/internal/billing/charge.go",
			"query":     userPrompt,
		},
		Contexts: map[string]sentry.Context{
			"runtime": {"name": "go", "version": "go1.27.0"},
			"extra": {
				"log_message":    "Tool execution failed\n" + toolOutputLine,
				"error":          "exit status 1: " + toolOutputLine,
				"tool_call_id":   toolCallID,
				"thread_id":      threadID,
				"attempt_number": 2,
				"tool_input":     `{"command":"` + shellCommand + `"}`,
				"tool_output":    toolOutputLine,
				"content":        userPrompt,
				"diff":           diffBody,
				"env":            map[string]string{"DATABASE_URL": "postgres://u:p@db/reliant"},
			},
		},
		Request: &sentry.Request{
			URL:         "https://api.reliantapi.com/oauth/callback?code=" + oauthCode,
			Method:      "POST",
			Data:        `{"content":"` + userPrompt + `"}`,
			QueryString: "code=" + oauthCode,
			Cookies:     "sb-access-token=" + reliantToken,
			Headers:     map[string]string{"Authorization": "Bearer " + reliantToken},
			Env:         map[string]string{"REMOTE_ADDR": userIP},
		},
		Breadcrumbs: []*sentry.Breadcrumb{{
			Type:      "default",
			Category:  "log",
			Level:     sentry.LevelInfo,
			Message:   "user said: " + userPrompt,
			Data:      map[string]interface{}{"content": userPrompt},
			Timestamp: time.Unix(1700000000, 0),
		}},
		User:        sentry.User{ID: userID, Email: userEmail, IPAddress: userIP, Username: "alice"},
		Attachments: []*sentry.Attachment{{Filename: "charge.patch", Payload: []byte(diffBody)}},
		Threads:     []sentry.Thread{{ID: "1", Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Function: "main", Vars: map[string]interface{}{"secret": frameSecret}}}}}},
	}
}

var forbidden = []string{
	userPrompt, toolOutputLine, fileContentLine, shellCommand, anthropicKey,
	reliantToken, oauthCode, userEmail, userIP, diffBody, frameSecret,
	"sk_live_toolOutputLeak", "/Users/alice/acme", "postgres://u:p@db",
}

func marshalEvent(t *testing.T, event *sentry.Event) string {
	t.Helper()
	b, err := json.Marshal(event)
	require.NoError(t, err)
	return string(b)
}

func TestBeforeSendStripsUserContent(t *testing.T) {
	opts := clientOptions(SentryConfig{Enabled: true})
	require.NotNil(t, opts.BeforeSend)

	got := opts.BeforeSend(realisticErrorEvent(), &sentry.EventHint{OriginalException: errors.New("x")})
	require.NotNil(t, got, "scrubbing must not drop the event: the error itself is still reported")

	wire := marshalEvent(t, got)
	for _, secret := range forbidden {
		assert.NotContains(t, wire, secret, "user content or credential reached the Sentry payload")
	}
	assert.Empty(t, got.Attachments, "attachments can carry arbitrary bytes")
	for _, frame := range got.Exception[0].Stacktrace.Frames {
		assert.Empty(t, frame.Vars, "frame locals can hold anything the function touched")
	}
}

func TestBeforeSendKeepsWhatDebuggingNeeds(t *testing.T) {
	got := clientOptions(SentryConfig{Enabled: true}).BeforeSend(realisticErrorEvent(), &sentry.EventHint{})
	require.NotNil(t, got)

	// Error type and stack trace.
	require.Len(t, got.Exception, 2)
	assert.Equal(t, "*fmt.wrapError", got.Exception[0].Type)
	assert.Equal(t, "executeTool", got.Exception[0].Stacktrace.Frames[0].Function)
	assert.Equal(t, 42, got.Exception[0].Stacktrace.Frames[0].Lineno)

	// The message survives as a bounded first-line prefix, credentials redacted.
	assert.True(t, strings.HasPrefix(got.Exception[0].Value, "executing tool bash: exit status 1:"), got.Exception[0].Value)
	assert.Contains(t, got.Exception[1].Value, "anthropic: 401 Unauthorized")

	// Identifiers and enum-like metadata.
	assert.Equal(t, chatID, got.Tags["chat_id"])
	assert.Equal(t, workflowID, got.Tags["workflow_id"])
	assert.Equal(t, "execute_tool", got.Tags["activity_type"])
	assert.Equal(t, "/reliant.v1.ChatService/SendMessage", got.Tags["procedure"])
	assert.Equal(t, "backend", got.Tags["component"])
	extra := got.Contexts["extra"]
	assert.Equal(t, toolCallID, extra["tool_call_id"])
	assert.Equal(t, threadID, extra["thread_id"])
	assert.Equal(t, 2, extra["attempt_number"])
	assert.Equal(t, userID, got.User.ID)

	// SDK-populated environment context is not user content.
	assert.Equal(t, "go1.27.0", got.Contexts["runtime"]["version"])

	// Request keeps method and path, nothing that carries data.
	require.NotNil(t, got.Request)
	assert.Equal(t, "POST", got.Request.Method)
	assert.Equal(t, "https://api.reliantapi.com/oauth/callback", got.Request.URL)

	// Breadcrumbs keep their shape for the timeline.
	require.Len(t, got.Breadcrumbs, 1)
	assert.Equal(t, "log", got.Breadcrumbs[0].Category)
	assert.Equal(t, sentry.LevelInfo, got.Breadcrumbs[0].Level)
}

func TestBoundMessage(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty stays empty", "", ""},
		{"short single line kept", "anthropic: 529 overloaded", "anthropic: 529 overloaded"},
		{"only the first line survives", "exit status 1\n" + toolOutputLine, "exit status 1" + truncatedMarker},
		{"authorization header", "401: Authorization: Bearer " + reliantToken, "401: Authorization: [redacted]"},
		{"bare bearer token", "sent Bearer " + reliantToken, "sent Bearer [redacted]"},
		{"anthropic key", "invalid key " + anthropicKey, "invalid key [redacted]"},
		{"key=value", "dial failed: password=hunter2 host=db", "dial failed: password=[redacted] host=db"},
		{"json credential", `{"refresh_token":"abc.def"}`, `{"refresh_token":"[redacted]"}`},
		{"url userinfo", "connect postgres://admin:s3cret@db:5432/x", "connect postgres://[redacted]@db:5432/x"},
		{"jwt", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.c2lnbmF0dXJl rejected", "token [redacted] rejected"},
		{"uuid is an identifier, not a secret", "chat " + chatID + " not found", "chat " + chatID + " not found"},
		{"prompt-is-too-long survives", "prompt is too long: 212345 tokens > 200000 maximum", "prompt is too long: 212345 tokens > 200000 maximum"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, boundMessage(tt.in))
		})
	}

	long := boundMessage(strings.Repeat("x ", 500))
	assert.True(t, strings.HasSuffix(long, truncatedMarker))
	assert.LessOrEqual(t, len([]rune(long)), maxMessageRunes+len(truncatedMarker))
}

func TestScrubFieldsKeepsOnlyIdentifierShapedValues(t *testing.T) {
	got := scrubFields(map[string]interface{}{
		"chatId":        chatID,
		"durationMs":    1234,
		"baseUrl":       "https://api.reliantapi.com/path?sig=abc",
		"serverName":    "My MCP",                // not an identifier key
		"provider":      "anthropic",             // enum metadata
		"code":          "x := charge(customer)", // metadata key, but reads as code
		"session_ids":   "s1",
		"error":         "boom\n" + toolOutputLine,
		"nested":        map[string]interface{}{"chat_id": chatID},
		"tool_call_ids": []string{toolCallID},
	})
	assert.Equal(t, map[string]interface{}{
		"chatId":      chatID,
		"durationMs":  1234,
		"baseUrl":     "https://api.reliantapi.com/path",
		"provider":    "anthropic",
		"session_ids": "s1",
		"error":       "boom" + truncatedMarker,
	}, got)
}

func TestBeforeSendTransactionStripsUserContent(t *testing.T) {
	opts := clientOptions(SentryConfig{Enabled: true})
	require.NotNil(t, opts.BeforeSendTransaction, "transactions are sampled in production and need the same scrubbing")

	txn := &sentry.Event{
		Type:        "transaction",
		Transaction: "/reliant.v1.ChatService/SendMessage",
		Request:     &sentry.Request{URL: "https://api.reliantapi.com/x?token=" + reliantToken, Data: userPrompt},
		Spans: []*sentry.Span{{
			Op:          "db.query",
			Description: "INSERT INTO messages VALUES ('" + userPrompt + "')\n" + toolOutputLine,
			Data:        map[string]interface{}{"db.statement": userPrompt, "chat_id": chatID},
			Tags:        map[string]string{"file_path": "/Users/alice/acme/main.go", "thread_id": threadID},
		}},
		Contexts: map[string]sentry.Context{"trace": {"op": "http.server"}},
	}
	got := opts.BeforeSendTransaction(txn, &sentry.EventHint{})
	require.NotNil(t, got)

	wire := marshalEvent(t, got)
	for _, secret := range forbidden {
		assert.NotContains(t, wire, secret)
	}
	require.Len(t, got.Spans, 1)
	assert.Equal(t, "db.query", got.Spans[0].Op)
	assert.Equal(t, chatID, got.Spans[0].Data["chat_id"])
	assert.Equal(t, threadID, got.Spans[0].Tags["thread_id"])
}

// TagValue is the allowlist logging's slog bridge uses to pick tags at the
// source. Message keys are kept by the event scrubber as bounded text, but
// they are prose, so they are never tags.
func TestTagValue(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		want       bool
	}{
		{"chat_id", chatID, true},
		{"toolCallId", toolCallID, true},
		{"provider", "anthropic", true},
		{"status", "failed", true},
		{"log_group", "worker.activity", true},
		{"url", "https://api.example.com/v1?code=" + oauthCode, true},
		{"command", "ls", false},
		{"file_path", "/tmp/x.go", false},
		{"prompt", "hi", false},
		{"error", "boom", false},
		{"message", "boom", false},
		{"status", "failed because of " + shellCommand, false},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			got, ok := TagValue(tc.key, tc.value)
			assert.Equal(t, tc.want, ok)
			if ok {
				assert.NotContains(t, got, oauthCode, "a kept URL loses its query string")
			}
		})
	}
}
