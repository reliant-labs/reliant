package reliant

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

func captureBody(t *testing.T, model models.Model, effort string, temp *float64) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"PONG"}}],"usage":{}}`))
	}))
	defer srv.Close()
	opts := llm.DriverOptions{Model: model, BaseURL: srv.URL, ApiKey: "k", MaxTokens: 1024, Temperature: temp}
	llm.WithReasoningEffort(effort)(&opts)
	c := NewClient(opts)
	c.Options.Model.APIModel = model.APIModel
	_, err := c.SendMessages(t.Context(), nil, []message.Message{message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}, nil)
	if err != nil {
		t.Fatalf("SendMessages: %v", err)
	}
	return got
}

func TestReasoningEffortOnWire(t *testing.T) {
	temp := 0.7
	claude := models.Model{ID: "claude-5.5-opus", APIModel: "claude-opus-5-5", CanReason: true, ThinkingMode: "adaptive"}
	claudeBudget := models.Model{ID: "claude-4.5-haiku", APIModel: "claude-haiku-4-5", CanReason: true, ThinkingMode: "budget"}
	gemini := models.Model{ID: "gemini-3.8-flash", APIModel: "gemini-3.8-flash", CanReason: true}
	noReason := models.Model{ID: "img", APIModel: "gemini-image", CanReason: false}

	cases := []struct {
		name     string
		model    models.Model
		effort   string
		want     any
		wantTemp bool
	}{
		{"adaptive low", claude, "low", "low", false},
		{"adaptive xhigh", claude, "xhigh", "xhigh", false},
		{"adaptive max", claude, "max", "max", false},
		{"adaptive disabled", claude, "disabled", nil, true},
		{"budget high", claudeBudget, "high", "high", false},
		{"budget xhigh clamps", claudeBudget, "xhigh", "high", false},
		{"gemini low", gemini, "low", "low", true},
		{"gemini high", gemini, "high", "high", true},
		{"gemini xhigh clamps", gemini, "xhigh", "high", true},
		{"gemini disabled", gemini, "disabled", nil, true},
		{"non-reasoning model", noReason, "high", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := captureBody(t, tc.model, tc.effort, &temp)
			if got := body["reasoning_effort"]; got != tc.want {
				t.Errorf("reasoning_effort = %v, want %v", got, tc.want)
			}
			if _, has := body["temperature"]; has != tc.wantTemp {
				t.Errorf("temperature present = %v, want %v", has, tc.wantTemp)
			}
		})
	}
}
