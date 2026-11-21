// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Both Copilot dialects open through a shared-ladder client (the Anthropic
// one's is covered in that package). On the OpenAI dialect a 429 retries
// after its Retry-After, a 400 does not, and a wait past the ladder's cap ends
// the turn as a usage limit that names Copilot, not OpenAI.
func TestOpenAIDialect_RetriesThroughTheLadder(t *testing.T) {
	user := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	opts := llm.DriverOptions{Model: models.Model{APIModel: "gpt-5.4"}, MaxTokens: 100}
	answer := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}
	refuse := func(w http.ResponseWriter, status int, retryAfter string) {
		w.Header().Set("Retry-After", retryAfter)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"refused","type":"x"}}`)
	}

	t.Run("429 then 200", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				refuse(w, http.StatusTooManyRequests, "0")
				return
			}
			answer(w)
		}))
		defer srv.Close()
		resp, err := newOpenAIDialectAt(srv.URL, opts, "gho_test", nil).SendMessages(context.Background(), nil, user, nil)
		require.NoError(t, err)
		assert.Equal(t, "pong", resp.Content)
		assert.EqualValues(t, 2, hits.Load())
	})

	t.Run("400 is not retried", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			refuse(w, http.StatusBadRequest, "0")
		}))
		defer srv.Close()
		_, err := newOpenAIDialectAt(srv.URL, opts, "gho_test", nil).SendMessages(context.Background(), nil, user, nil)
		require.Error(t, err)
		assert.EqualValues(t, 1, hits.Load())
	})

	t.Run("a long wait is a Copilot usage limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			refuse(w, http.StatusTooManyRequests, "86400")
		}))
		defer srv.Close()
		_, err := newOpenAIDialectAt(srv.URL, opts, "gho_test", nil).SendMessages(context.Background(), nil, user, nil)
		var ule *ratelimit.UsageLimitError
		require.ErrorAs(t, err, &ule)
		assert.Equal(t, "GitHub Copilot", ule.Provider)
	})
}
