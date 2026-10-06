// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Ollama 0.12.3 SSE captured live (research/LOCAL_MODELS.md §3): qwen3 puts
// reasoning inline in content, and usage arrives in a final chunk with
// choices: [] AFTER finish_reason.
const ollamaThinkStream = `data: {"id":"c1","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":"<think>"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":"\nThe user wants PO"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":"NG.\n</thi"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":"nk>\n\nPONG"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"stop"}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"qwen3:latest","choices":[],"usage":{"prompt_tokens":14,"completion_tokens":120,"total_tokens":134}}

data: [DONE]

`

const ollamaToolStream = `data: {"id":"c2","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_1zprp9v2","index":0,"type":"function","function":{"name":"get_secret_word","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"c2","object":"chat.completion.chunk","model":"qwen3:latest","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}

data: {"id":"c2","object":"chat.completion.chunk","model":"qwen3:latest","choices":[],"usage":{"prompt_tokens":210,"completion_tokens":31,"total_tokens":241}}

data: [DONE]

`

const reasoningFieldStream = `data: {"id":"c3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning":"Let me think. "},"finish_reason":null}]}

data: {"id":"c3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"More."},"finish_reason":null}]}

data: {"id":"c3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"4"},"finish_reason":"stop"}]}

data: {"id":"c3","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14,"completion_tokens_details":{"reasoning_tokens":7}}}

data: [DONE]

`

func sseServer(t *testing.T, body string, onRequest func(*http.Request, []byte)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if onRequest != nil {
			onRequest(r, raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(baseURL string, mutate func(*llm.DriverOptions)) *LocalClient {
	opts := llm.DriverOptions{
		BaseURL: baseURL + "/v1",
		Model:   models.Model{ID: "qwen3:latest", APIModel: "qwen3:latest"},
	}
	if mutate != nil {
		mutate(&opts)
	}
	return NewClient(opts)
}

func userTurn(text string) []message.Message {
	return []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}}
}

type streamed struct {
	content, thinking string
	thinkingEvents    int
	complete          *llm.DriverResponse
	err               error
	order             []llm.EventType
}

func collect(events <-chan llm.DriverEvent) streamed {
	var out streamed
	for ev := range events {
		out.order = append(out.order, ev.Type)
		switch ev.Type {
		case llm.EventContentDelta:
			out.content += ev.Content
		case llm.EventThinkingDelta:
			out.thinking += ev.Thinking
			out.thinkingEvents++
		case llm.EventComplete:
			out.complete = ev.Response
		case llm.EventError:
			out.err = ev.Error
		}
	}
	return out
}

// D10: <think> must not reach answer text.
func TestStreamSplitsInlineThinkFromContent(t *testing.T) {
	srv := sseServer(t, ollamaThinkStream, nil)
	got := collect(newTestClient(srv.URL, nil).StreamResponse(context.Background(), nil, userTurn("say PONG"), nil))

	if got.err != nil {
		t.Fatalf("stream error: %v", got.err)
	}
	if got.content != "PONG" {
		t.Errorf("content = %q, want PONG (think block must not leak into the answer)", got.content)
	}
	if strings.Contains(got.content, "think") {
		t.Errorf("content still carries think markup: %q", got.content)
	}
	if want := "The user wants PONG."; strings.TrimSpace(got.thinking) != want {
		t.Errorf("thinking = %q, want %q", got.thinking, want)
	}
	if got.thinkingEvents == 0 {
		t.Error("no EventThinkingDelta emitted")
	}
	if got.complete == nil || strings.TrimSpace(got.complete.Thinking) != "The user wants PONG." {
		t.Errorf("DriverResponse.Thinking = %+v", got.complete)
	}
	if got.complete.Content != "PONG" {
		t.Errorf("DriverResponse.Content = %q, want PONG", got.complete.Content)
	}
}

// D11: usage arrives in a trailing choices:[] chunk, after finish_reason.
func TestStreamCapturesTrailingUsageBeforeComplete(t *testing.T) {
	srv := sseServer(t, ollamaThinkStream, func(r *http.Request, body []byte) {
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Errorf("request lacks stream_options.include_usage: %s", body)
		}
	})
	got := collect(newTestClient(srv.URL, nil).StreamResponse(context.Background(), nil, userTurn("hi"), nil))

	if got.complete == nil {
		t.Fatal("no EventComplete")
	}
	u := got.complete.Usage
	if u.InputTokens != 14 || u.OutputTokens != 120 || u.TokenCount != 14 {
		t.Errorf("usage = %+v, want input 14 / output 120", u)
	}
	if got.order[len(got.order)-1] != llm.EventComplete {
		t.Errorf("EventComplete must be last, order = %v", got.order)
	}
}

func TestStreamToolCallRoundTrip(t *testing.T) {
	srv := sseServer(t, ollamaToolStream, nil)
	tool := llmtools.NewSchemaOnlyTool("get_secret_word", "stub", map[string]interface{}{"type": "object"})
	got := collect(newTestClient(srv.URL, nil).StreamResponse(context.Background(), nil, userTurn("go"), []llmtools.Tool{tool}))

	if got.complete == nil || len(got.complete.ToolCalls) != 1 {
		t.Fatalf("complete = %+v", got.complete)
	}
	tc := got.complete.ToolCalls[0]
	// The id is ours, never the server's (see tool_call_id_test.go).
	if !mintedToolCallID.MatchString(tc.ID) || tc.Name != "get_secret_word" || !tc.Finished {
		t.Errorf("tool call = %+v", tc)
	}
	if got.complete.FinishReason != message.FinishReasonToolUse {
		t.Errorf("finish = %v", got.complete.FinishReason)
	}
	if got.complete.Usage.InputTokens != 210 || got.complete.Usage.OutputTokens != 31 {
		t.Errorf("usage = %+v", got.complete.Usage)
	}
}

func TestStreamReadsReasoningFields(t *testing.T) {
	srv := sseServer(t, reasoningFieldStream, nil)
	got := collect(newTestClient(srv.URL, nil).StreamResponse(context.Background(), nil, userTurn("2+2"), nil))

	if got.content != "4" {
		t.Errorf("content = %q", got.content)
	}
	if got.thinking != "Let me think. More." {
		t.Errorf("thinking = %q", got.thinking)
	}
	if got.complete.Usage.ReasoningTokens != 7 {
		t.Errorf("reasoning tokens = %d", got.complete.Usage.ReasoningTokens)
	}
}

func TestSendMessagesSplitsThinkAndReportsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"<think>\nhmm\n</think>\n\nPONG"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":40,"total_tokens":49}}`)
	}))
	defer srv.Close()
	resp, err := newTestClient(srv.URL, nil).SendMessages(context.Background(), nil, userTurn("hi"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "PONG" || resp.Thinking != "hmm" {
		t.Errorf("content=%q thinking=%q", resp.Content, resp.Thinking)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 40 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestThinkSplitterEdges(t *testing.T) {
	cases := []struct {
		name, in, wantContent, wantThinking string
	}{
		{"no think", "hello world", "hello world", ""},
		{"empty think", "<think></think>four", "four", ""},
		{"mentions tag later", "use <think> tags like so", "use <think> tags like so", ""},
		{"unterminated", "<think>never closes", "", "never closes"},
		{"partial open held then answer", "<thi", "<thi", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s thinkSplitter
			c1, t1 := s.push(tc.in)
			c2, t2 := s.flush()
			if got := c1 + c2; got != tc.wantContent {
				t.Errorf("content = %q, want %q", got, tc.wantContent)
			}
			if got := t1 + t2; got != tc.wantThinking {
				t.Errorf("thinking = %q, want %q", got, tc.wantThinking)
			}
		})
	}
}

// D6/D9: transport honored; the base URL is never dialed.
type recordingTransport struct {
	calls atomic.Int32
	paths []string
	inner http.RoundTripper
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls.Add(1)
	r.paths = append(r.paths, req.URL.Path)
	return r.inner.RoundTrip(req)
}

func TestDriverHonorsTransport(t *testing.T) {
	srv := sseServer(t, ollamaThinkStream, nil)
	rt := &recordingTransport{inner: http.DefaultTransport}
	// The placeholder host does not resolve: only the transport can succeed,
	// and it rewrites to the real test server.
	client := NewClient(llm.DriverOptions{
		BaseURL: "http://local-model.invalid/v1",
		Model:   models.Model{ID: "qwen3:latest", APIModel: "qwen3:latest"},
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme, clone.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
			return rt.RoundTrip(clone)
		}),
	})
	got := collect(client.StreamResponse(context.Background(), nil, userTurn("hi"), nil))
	if got.err != nil || got.content != "PONG" {
		t.Fatalf("content=%q err=%v", got.content, got.err)
	}
	if rt.calls.Load() != 1 || rt.paths[0] != "/v1/chat/completions" {
		t.Errorf("transport calls=%d paths=%v", rt.calls.Load(), rt.paths)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// D6: relay failures and refused connections fail immediately, not after the
// 1s+2s+4s backoff the old driver ran for any non-API error.
func TestFailsFastOnTransportError(t *testing.T) {
	var calls atomic.Int32
	relayErr := errors.New("local model relay via daemon d1 failed: connection refused")
	client := NewClient(llm.DriverOptions{
		BaseURL: "http://local-model.invalid/v1",
		Model:   models.Model{ID: "qwen3:latest", APIModel: "qwen3:latest"},
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, relayErr
		}),
	})

	start := time.Now()
	got := collect(client.StreamResponse(context.Background(), nil, userTurn("hi"), nil))
	if got.err == nil || !strings.Contains(got.err.Error(), "connection refused") {
		t.Fatalf("err = %v", got.err)
	}
	if calls.Load() != 1 {
		t.Errorf("transport called %d times, want exactly 1 (no retry)", calls.Load())
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v; must fail fast", elapsed)
	}

	start = time.Now()
	_, err := client.SendMessages(context.Background(), nil, userTurn("hi"), nil)
	if err == nil || calls.Load() != 2 || time.Since(start) > 500*time.Millisecond {
		t.Errorf("SendMessages err=%v calls=%d took=%v", err, calls.Load(), time.Since(start))
	}
}

func TestRefusedConnectionFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	start := time.Now()
	got := collect(newTestClient(url, nil).StreamResponse(context.Background(), nil, userTurn("hi"), nil))
	if got.err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %v; the old driver backed off for ~7s", time.Since(start))
	}
}

func TestRetriesTransientServerStatus(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"busy"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, reasoningFieldStream)
	}))
	defer srv.Close()
	got := collect(newTestClient(srv.URL, nil).StreamResponse(context.Background(), nil, userTurn("hi"), nil))
	if got.err != nil || got.content != "4" || calls.Load() != 2 {
		t.Errorf("content=%q err=%v calls=%d", got.content, got.err, calls.Load())
	}
}

// Thinking levels: only low/medium/high ever go on the wire.
func TestReasoningEffortOnTheWire(t *testing.T) {
	cases := []struct {
		level string
		want  string // "" = field absent
	}{
		{"low", `"reasoning_effort":"low"`},
		{"high", `"reasoning_effort":"high"`},
		{"xhigh", ""},
		{"max", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run("level="+tc.level, func(t *testing.T) {
			var body string
			srv := sseServer(t, reasoningFieldStream, func(_ *http.Request, raw []byte) { body = string(raw) })
			collect(newTestClient(srv.URL, func(o *llm.DriverOptions) { o.ReasoningEffort = tc.level }).
				StreamResponse(context.Background(), nil, userTurn("hi"), nil))
			if tc.want == "" && strings.Contains(body, "reasoning_effort") {
				t.Errorf("reasoning_effort must be omitted for %q: %s", tc.level, body)
			}
			if tc.want != "" && !strings.Contains(body, tc.want) {
				t.Errorf("body lacks %s: %s", tc.want, body)
			}
		})
	}
}

func TestTemperatureIsSent(t *testing.T) {
	var body string
	srv := sseServer(t, reasoningFieldStream, func(_ *http.Request, raw []byte) { body = string(raw) })
	temp := 0.0
	collect(newTestClient(srv.URL, func(o *llm.DriverOptions) { o.Temperature = &temp }).
		StreamResponse(context.Background(), nil, userTurn("hi"), nil))
	if !strings.Contains(body, `"temperature":0`) {
		t.Errorf("temperature 0 not sent: %s", body)
	}
}

// Live, straight at Ollama (no relay). RELIANT_TEST_OLLAMA=1.
func TestLiveOllama(t *testing.T) {
	if os.Getenv("RELIANT_TEST_OLLAMA") != "1" {
		t.Skip("set RELIANT_TEST_OLLAMA=1 (ollama on :11434 with qwen3:latest)")
	}
	base := os.Getenv("RELIANT_TEST_OLLAMA_URL")
	if base == "" {
		base = "http://localhost:11434/v1"
	}
	newLive := func() *LocalClient {
		return NewClient(llm.DriverOptions{BaseURL: base, Model: models.Model{ID: "qwen3:latest", APIModel: "qwen3:latest"}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	t.Run("stream thinking and usage", func(t *testing.T) {
		got := collect(newLive().StreamResponse(ctx, nil, userTurn("Reply with exactly: PONG"), nil))
		if got.err != nil {
			t.Fatal(got.err)
		}
		if strings.Contains(got.content, "<think>") || strings.Contains(got.content, "</think>") {
			t.Errorf("think markup leaked into content: %q", got.content)
		}
		if got.thinkingEvents == 0 || strings.TrimSpace(got.thinking) == "" {
			t.Errorf("no thinking captured (events=%d)", got.thinkingEvents)
		}
		if !strings.Contains(strings.ToUpper(got.content), "PONG") {
			t.Errorf("content = %q", got.content)
		}
		if got.complete == nil || got.complete.Usage.InputTokens == 0 || got.complete.Usage.OutputTokens == 0 {
			t.Errorf("usage = %+v", got.complete)
		}
	})

	t.Run("tool round trip", func(t *testing.T) {
		tool := llmtools.NewSchemaOnlyTool("get_secret_word", "Returns the secret word. Call it when asked for the secret word.", map[string]interface{}{"type": "object"})
		first := collect(newLive().StreamResponse(ctx, nil, userTurn("Call the get_secret_word tool, then tell me the word."), []llmtools.Tool{tool}))
		if first.err != nil || first.complete == nil || len(first.complete.ToolCalls) == 0 {
			t.Fatalf("turn 1: err=%v complete=%+v", first.err, first.complete)
		}
		history := append(userTurn("Call the get_secret_word tool, then tell me the word."),
			message.Message{Role: message.Assistant, Parts: []message.ContentPart{first.complete.ToolCalls[0]}},
			message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: first.complete.ToolCalls[0].ID, Name: "get_secret_word", Content: "pineapple"}}},
		)
		second := collect(newLive().StreamResponse(ctx, nil, history, []llmtools.Tool{tool}))
		if second.err != nil || !strings.Contains(strings.ToLower(second.content), "pineapple") {
			t.Errorf("turn 2: err=%v content=%q", second.err, second.content)
		}
	})

	t.Run("unsupported thinking level is omitted not 400", func(t *testing.T) {
		c := NewClient(llm.DriverOptions{BaseURL: base, ReasoningEffort: "xhigh", Model: models.Model{ID: "qwen3:latest", APIModel: "qwen3:latest"}})
		got := collect(c.StreamResponse(ctx, nil, userTurn("Reply with exactly: PONG"), nil))
		if got.err != nil {
			t.Errorf("xhigh must be dropped, got %v", got.err)
		}
	})

	t.Run("connection refused fails fast", func(t *testing.T) {
		c := NewClient(llm.DriverOptions{BaseURL: "http://127.0.0.1:1/v1", Model: models.Model{ID: "x", APIModel: "x"}})
		start := time.Now()
		got := collect(c.StreamResponse(ctx, nil, userTurn("hi"), nil))
		if got.err == nil || time.Since(start) > 2*time.Second {
			t.Errorf("err=%v took=%v", got.err, time.Since(start))
		}
		_ = fmt.Sprint(got)
	})
}
