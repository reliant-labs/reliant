// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A stream that reports usage and then fails for good (a mid-stream error is
// not retried here) drops its accumulator on the way out. What the proxy billed
// still goes out, after the error, as a final event holding only that usage,
// so the usage ledger records the request's tokens instead of zero.
func TestStreamResponse_TerminalFailureStillReportsItsUsage(t *testing.T) {
	const chunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m",%s}`
	for name, tc := range map[string]struct {
		body      string
		wantError string
	}{
		"error after usage": {
			body: fmt.Sprintf("data: "+chunk+"\n\n", `"choices":[{"index":0,"delta":{"content":"par"}}]`) +
				fmt.Sprintf("data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620,"cost":0.25}`) +
				"data: {\"error\":{\"message\":\"upstream reset\",\"type\":\"server_error\"}}\n\n",
			wantError: "upstream reset",
		},
		"usage but no choices": {
			body:      fmt.Sprintf("data: "+chunk+"\n\n", `"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":120,"total_tokens":620,"cost":0.25}`) + "data: [DONE]\n\n",
			wantError: "empty response",
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			client := NewClient(llm.DriverOptions{ApiKey: "k", BaseURL: srv.URL, Model: models.Model{ID: "m", APIModel: "m"}, MaxTokens: 100})
			msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var events []llm.DriverEvent
			for ev := range client.StreamResponse(ctx, []string{"p"}, msgs, nil) {
				events = append(events, ev)
			}

			require.GreaterOrEqual(t, len(events), 2, "%+v", events)
			errEv, last := events[len(events)-2], events[len(events)-1]
			require.Equal(t, llm.EventError, errEv.Type, "the error comes before the usage: %+v", events)
			assert.ErrorContains(t, errEv.Error, tc.wantError)
			require.Equal(t, llm.EventComplete, last.Type, "the failed request's usage follows the error: %+v", events)
			require.NotNil(t, last.Response)
			u := last.Response.Usage
			assert.Equal(t, [2]int64{500, 120}, [2]int64{u.InputTokens, u.OutputTokens})
			assert.InDelta(t, 0.25, u.Cost, 1e-9, "the proxy's reported cost is kept too")
			assert.Empty(t, last.Response.Content, "the usage-only event carries no answer")
		})
	}
}
