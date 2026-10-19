// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const invalidSignatureBody = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`

func thinkingParams() anthropic.MessageNewParams {
	return anthropic.MessageNewParams{Model: "m", MaxTokens: 10, Messages: []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		anthropic.NewAssistantMessage(
			anthropic.NewThinkingBlock("sig", "pondering"),
			anthropic.NewRedactedThinkingBlock("opaque"),
			anthropic.NewTextBlock("answer"),
		),
		anthropic.NewUserMessage(anthropic.NewTextBlock("more")),
	}}
}

type bodyRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *bodyRecorder) add(req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, string(b))
}

func TestStreamRetriesOnceWithoutThinkingBlocksOnInvalidSignature(t *testing.T) {
	rec := &bodyRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if len(rec.bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(invalidSignatureBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(okSSE))
	}))
	defer srv.Close()

	params := thinkingParams()
	events := collect(t, testBaseClient(srv.URL).streamResponseInternal(context.Background(), params))

	require.Len(t, rec.bodies, 2)
	assert.Contains(t, rec.bodies[0], `"thinking"`)
	assert.Contains(t, rec.bodies[0], `"redacted_thinking"`)
	assert.NotContains(t, rec.bodies[1], `"type":"thinking"`)
	assert.NotContains(t, rec.bodies[1], `"redacted_thinking"`)
	assert.Contains(t, rec.bodies[1], "answer", "text survives")
	assert.Equal(t, llm.EventComplete, events[len(events)-1].Type)
	for _, ev := range events {
		assert.NotEqual(t, llm.EventError, ev.Type)
	}
	assert.NotNil(t, params.Messages[1].Content[0].OfThinking, "the caller's request is not mutated")
}

func TestStreamSignatureRecoveryIsAtMostOneRetry(t *testing.T) {
	rec := &bodyRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(invalidSignatureBody))
	}))
	defer srv.Close()

	events := collect(t, testBaseClient(srv.URL).streamResponseInternal(context.Background(), thinkingParams()))

	assert.Len(t, rec.bodies, 2, "one original request, one recovery, no loop")
	var sawErr bool
	for _, ev := range events {
		sawErr = sawErr || ev.Type == llm.EventError
	}
	assert.True(t, sawErr, "a second failure surfaces")
}

func TestStreamDoesNotRecoverFromOtherBadRequests(t *testing.T) {
	rec := &bodyRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens too large"}}`))
	}))
	defer srv.Close()

	collect(t, testBaseClient(srv.URL).streamResponseInternal(context.Background(), thinkingParams()))
	assert.Len(t, rec.bodies, 1)
}

func TestSendMessagesRetriesOnceWithoutThinkingBlocks(t *testing.T) {
	rec := &bodyRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		if len(rec.bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(invalidSignatureBody))
			return
		}
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	msg, err := testBaseClient(srv.URL).newMessage(context.Background(), thinkingParams())
	require.NoError(t, err)
	require.NotNil(t, msg)
	require.Len(t, rec.bodies, 2)
	assert.False(t, strings.Contains(rec.bodies[1], `"type":"thinking"`))
}

func TestWithoutThinkingBlocksDropsEmptiedMessages(t *testing.T) {
	p := anthropic.MessageNewParams{Messages: []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		anthropic.NewAssistantMessage(anthropic.NewThinkingBlock("s", "t")),
	}}
	got, n := withoutThinkingBlocks(p)
	assert.Equal(t, 1, n)
	assert.Len(t, got.Messages, 1)
	assert.Len(t, p.Messages, 2, "input untouched")

	_, n = withoutThinkingBlocks(anthropic.MessageNewParams{Messages: p.Messages[:1]})
	assert.Zero(t, n)
}
