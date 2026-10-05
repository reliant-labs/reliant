// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSSE returns the `data: {...}` lines of the response stream recorded
// in a research/probe-runs/wire capture. A capture is a sequence of
// `<< HH:MM:SS.mmm N` (response) / `>> ...` (request) headers, each followed by
// exactly N raw bytes; the response body is HTTP/1.1 chunked, and SSE lines
// are split across both segment and chunk boundaries, so reassemble first.
func captureSSE(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var resp bytes.Buffer
	for pos := 0; pos < len(raw); {
		nl := bytes.IndexByte(raw[pos:], '\n')
		if nl < 0 {
			break
		}
		header := string(raw[pos : pos+nl])
		pos += nl + 1
		fields := strings.Fields(header)
		if len(fields) != 3 || (fields[0] != "<<" && fields[0] != ">>") {
			continue
		}
		n, err := strconv.Atoi(fields[2])
		require.NoError(t, err)
		end := pos + n
		if end > len(raw) {
			end = len(raw)
		}
		if fields[0] == "<<" {
			resp.Write(raw[pos:end])
		}
		pos = end
	}

	_, body, ok := bytes.Cut(resp.Bytes(), []byte("\r\n\r\n"))
	require.True(t, ok, "no header terminator in %s", path)
	decoded, err := io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(body)))
	require.NoError(t, err)

	var out []string
	for _, line := range strings.Split(string(decoded), "\n") {
		if strings.HasPrefix(line, "data: {") {
			out = append(out, strings.TrimRight(line, "\r"))
		}
	}
	require.NotEmpty(t, out, "capture %s has no SSE data lines", path)
	return out
}

func newTestClient(apiModel, effort, baseURL string) *Client {
	return &Client{OpenaiClient: &openai.OpenaiClient{Options: llm.DriverOptions{
		Model:           models.Model{APIModel: apiModel},
		BaseURL:         baseURL,
		ApiKey:          "k",
		MaxTokens:       4096,
		ReasoningEffort: effort,
	}}}
}

var userHello = []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}

func drain(t *testing.T, c *Client) (thinking string, final *llm.DriverResponse) {
	t.Helper()
	var sb strings.Builder
	for ev := range c.StreamResponse(context.Background(), nil, userHello, nil) {
		switch ev.Type {
		case llm.EventError:
			t.Fatalf("stream error: %v", ev.Error)
		case llm.EventThinkingDelta:
			sb.WriteString(ev.Thinking)
		case llm.EventComplete:
			final = ev.Response
		}
	}
	require.NotNil(t, final, "no EventComplete")
	return sb.String(), final
}

// D4: the Claude (cache-control) path never sent `reasoning`, so the level
// was a no-op (wire/claude-5.5-opus_openrouter bodies had no reasoning key).
func TestClaudeCachePath_SendsReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		effort    string
		wantSent  bool
		wantValue string
	}{
		{"low", true, "low"},
		{"medium", true, "medium"},
		{"high", true, "high"},
		{"xhigh", true, "xhigh"},
		{"disabled", false, ""},
		{"", false, ""},
	} {
		t.Run("effort="+tc.effort, func(t *testing.T) {
			var body map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"1\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()

			drain(t, newTestClient("anthropic/claude-opus-5.5", tc.effort, srv.URL))

			reasoning, sent := body["reasoning"].(map[string]interface{})
			assert.Equal(t, tc.wantSent, sent, "reasoning present; body=%v", body)
			if tc.wantSent {
				assert.Equal(t, tc.wantValue, reasoning["effort"])
				_, hasTemp := body["temperature"]
				assert.False(t, hasTemp, "temperature must be omitted when reasoning is on for Claude")
			}
		})
	}
}

func serveCapture(t *testing.T, lines []string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprint(w, l, "\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// D3: fixtures are the real streams from the probe wire captures.
func TestStreamReasoningText_FromWireCaptures(t *testing.T) {
	for _, tc := range []struct {
		name, model, capture string
		wantSignature        bool
	}{
		{"claude", "anthropic/claude-opus-5.5", "../../../../research/probe-runs/wire/claude-5.5-opus_openrouter/001-openrouter.ai.log", true},
		{"gemini", "google/gemini-3.5-flash", "../../../../research/probe-runs/wire/gemini-3.5-flash_openrouter/002-openrouter.ai.log", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveCapture(t, captureSSE(t, tc.capture))
			thinking, final := drain(t, newTestClient(tc.model, "high", srv.URL))

			assert.NotEmpty(t, thinking, "reasoning text must stream as EventThinkingDelta.Thinking")
			assert.Equal(t, thinking, final.Thinking, "DriverResponse.Thinking carries the accumulated text")
			assert.Equal(t, tc.wantSignature, final.ThinkingSignature != "", "signature capture")
			assert.Greater(t, final.Usage.ReasoningTokens, int64(0), "completion_tokens_details.reasoning_tokens")
			assert.GreaterOrEqual(t, final.Usage.OutputTokens, final.Usage.ReasoningTokens)
		})
	}
}

// reasoning and reasoning_details carry the same text; it must not be doubled.
func TestReasoningAccumulator_NoDoubleCount(t *testing.T) {
	var acc reasoningAccumulator
	got := acc.consume(map[string]interface{}{
		"reasoning": "abc",
		"reasoning_details": []interface{}{
			map[string]interface{}{"type": "reasoning.text", "text": "abc"},
		},
	})
	assert.Equal(t, "abc", got)
	got = acc.consume(map[string]interface{}{
		"reasoning_details": []interface{}{
			map[string]interface{}{"type": "reasoning.summary", "summary": "def"},
		},
	})
	assert.Equal(t, "def", got)
	assert.Equal(t, "abcdef", acc.text.String())
}
