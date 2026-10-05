// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// captureClaudeWireBody sends a built request through makeClaudeRequest to an
// httptest server and returns the JSON body exactly as Vertex would receive it.
func captureClaudeWireBody(t *testing.T, c *VertexAIClient) map[string]any {
	t.Helper()
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","content":[],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	req := c.buildClaudeRequest([]string{"sys"}, msgs, nil, false)
	if _, err := c.makeClaudeRequest(context.Background(), srv.URL, req, "tok"); err != nil {
		t.Fatalf("makeClaudeRequest: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("decode wire body: %v", err)
	}
	return body
}

func TestVertexClaudeThinkingOnTheWire(t *testing.T) {
	half := 0.5
	const maxTokens = 64000

	type want struct {
		thinking map[string]any // nil => key absent
		effort   string         // "" => output_config absent
		temp     *float64       // nil => key absent
	}

	cases := []struct {
		name   string
		mode   string
		effort string
		temp   *float64
		want   want
	}{}

	adaptive := func(effort string, t *float64, w want) {
		cases = append(cases, struct {
			name   string
			mode   string
			effort string
			temp   *float64
			want   want
		}{"adaptive/" + effort + tempName(t), "adaptive", effort, t, w})
	}
	budget := func(effort string, t *float64, w want) {
		cases = append(cases, struct {
			name   string
			mode   string
			effort string
			temp   *float64
			want   want
		}{"budget/" + effort + tempName(t), "budget", effort, t, w})
	}

	for _, tp := range []*float64{nil, &half} {
		// Adaptive models never get temperature, whatever the level.
		adaptiveOn := map[string]any{"type": "adaptive"}
		adaptive("", tp, want{adaptiveOn, "high", nil})
		adaptive("low", tp, want{adaptiveOn, "low", nil})
		adaptive("medium", tp, want{adaptiveOn, "medium", nil})
		adaptive("high", tp, want{adaptiveOn, "high", nil})
		adaptive("xhigh", tp, want{adaptiveOn, "xhigh", nil})
		adaptive("max", tp, want{adaptiveOn, "max", nil})
		adaptive("disabled", tp, want{nil, "", nil})

		// Budget models: thinking on => temperature omitted; off => honored.
		on := func(b float64) map[string]any {
			return map[string]any{"type": "enabled", "budget_tokens": b}
		}
		budget("", tp, want{nil, "", tp})
		budget("low", tp, want{on(1024), "", nil})
		budget("medium", tp, want{on(16000), "", nil})
		budget("high", tp, want{on(31999), "", nil})
		budget("xhigh", tp, want{on(16000), "", nil})
		budget("max", tp, want{on(16000), "", nil})
		budget("disabled", tp, want{nil, "", tp})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &VertexAIClient{options: llm.DriverOptions{
				Model: models.Model{
					ID: "m", APIModel: "claude-x", CanReason: true, ThinkingMode: tc.mode,
				},
				MaxTokens:       maxTokens,
				Temperature:     tc.temp,
				ReasoningEffort: tc.effort,
			}}
			body := captureClaudeWireBody(t, c)

			if got := body["max_tokens"]; got != float64(maxTokens) {
				t.Errorf("max_tokens = %v, want %d", got, maxTokens)
			}
			gotThinking, _ := body["thinking"].(map[string]any)
			if !jsonEqual(gotThinking, tc.want.thinking) {
				t.Errorf("thinking = %v, want %v", body["thinking"], tc.want.thinking)
			}
			oc, hasOC := body["output_config"].(map[string]any)
			if tc.want.effort == "" {
				if hasOC {
					t.Errorf("output_config = %v, want absent", oc)
				}
			} else if oc["effort"] != tc.want.effort {
				t.Errorf("output_config = %v, want effort %q", body["output_config"], tc.want.effort)
			}
			gotTemp, hasTemp := body["temperature"]
			switch {
			case tc.want.temp == nil && hasTemp:
				t.Errorf("temperature = %v, want absent", gotTemp)
			case tc.want.temp != nil && gotTemp != *tc.want.temp:
				t.Errorf("temperature = %v, want %v", gotTemp, *tc.want.temp)
			}
		})
	}
}

func TestVertexClaudeBudgetClampedBelowMaxTokens(t *testing.T) {
	c := &VertexAIClient{options: llm.DriverOptions{
		Model:           models.Model{ID: "m", APIModel: "x", CanReason: true, ThinkingMode: "budget"},
		MaxTokens:       4096,
		ReasoningEffort: "high",
	}}
	th, _ := captureClaudeWireBody(t, c)["thinking"].(map[string]any)
	if th["budget_tokens"] != float64(4095) {
		t.Fatalf("budget_tokens = %v, want 4095 (max_tokens-1)", th["budget_tokens"])
	}
}

func TestVertexClaudeReplaysThinkingBlocks(t *testing.T) {
	assistant := message.Message{Role: message.Assistant, Parts: []message.ContentPart{
		message.ReasoningContent{Thinking: "pondering", Signature: "sig"},
		message.RedactedReasoningContent{Data: "sealed"},
		message.TextContent{Text: "answer"},
	}}
	for _, mode := range []string{"adaptive", "budget"} {
		c := &VertexAIClient{options: llm.DriverOptions{
			Model:           models.Model{CanReason: true, ThinkingMode: mode},
			MaxTokens:       8192,
			ReasoningEffort: "medium",
		}}
		out := c.convertMessagesToClaude([]message.Message{assistant})
		raw, _ := json.Marshal(out)
		var msgs []struct {
			Content []map[string]any `json:"content"`
		}
		_ = json.Unmarshal(raw, &msgs)
		blocks := msgs[0].Content
		if len(blocks) != 3 ||
			blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "pondering" || blocks[0]["signature"] != "sig" ||
			blocks[1]["type"] != "redacted_thinking" || blocks[1]["data"] != "sealed" ||
			blocks[2]["type"] != "text" {
			t.Errorf("%s: blocks = %s", mode, raw)
		}
	}

	// Thinking not requested => history thinking is not replayed.
	off := &VertexAIClient{options: llm.DriverOptions{
		Model: models.Model{CanReason: true, ThinkingMode: "budget"}, MaxTokens: 8192, ReasoningEffort: "disabled",
	}}
	raw, _ := json.Marshal(off.convertMessagesToClaude([]message.Message{assistant}))
	var msgs []struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(raw, &msgs)
	if len(msgs[0].Content) != 1 || msgs[0].Content[0]["type"] != "text" {
		t.Errorf("thinking off: blocks = %s", raw)
	}
}

func TestVertexClaudeCapturesThinkingFromResponse(t *testing.T) {
	c := &VertexAIClient{}
	resp := c.convertClaudeResponse(&claudeResponse{Content: []claudeContentBlock{
		{Type: "thinking", Thinking: "hmm", Signature: "s1"},
		{Type: "redacted_thinking", Data: "blob"},
		{Type: "text", Text: "ok"},
	}})
	if resp.Thinking != "hmm" || resp.ThinkingSignature != "s1" || resp.RedactedThinking != "blob" || resp.Content != "ok" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestVertexClaudeStreamCapturesThinking(t *testing.T) {
	sse := `{"type":"content_block_start","content_block":{"type":"thinking"}}
{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"ab"}}
{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"s"}}
{"type":"content_block_stop"}
{"type":"content_block_start","content_block":{"type":"redacted_thinking","data":"blob"}}
{"type":"content_block_stop"}
{"type":"message_stop"}
`
	c := &VertexAIClient{}
	ch := make(chan llm.DriverEvent, 32)
	c.processClaudeStream(context.Background(), strings.NewReader(sse), ch)
	close(ch)
	var final *llm.DriverResponse
	var deltas string
	for ev := range ch {
		if ev.Type == llm.EventThinkingDelta {
			deltas += ev.Thinking
		}
		if ev.Type == llm.EventComplete {
			final = ev.Response
		}
	}
	if final == nil || final.Thinking != "ab" || final.ThinkingSignature != "s" || final.RedactedThinking != "blob" || deltas != "ab" {
		t.Fatalf("final = %+v deltas=%q", final, deltas)
	}
}

func tempName(t *float64) string {
	if t == nil {
		return "/temp=nil"
	}
	return "/temp=0.5"
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if a == nil {
		x = []byte("null")
	}
	if b == nil {
		y = []byte("null")
	}
	return string(x) == string(y)
}
