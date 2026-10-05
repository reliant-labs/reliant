// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const sseDone = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"x\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// captureAnthropicBody sends one request through the copilot Anthropic dialect
// and returns the JSON body that reached the wire.
func captureAnthropicBody(t *testing.T, mode, effort string) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseDone)
	}))
	defer srv.Close()

	opts := llm.DriverOptions{
		Model:           models.Model{APIModel: "claude-sonnet-5", CanReason: true, ThinkingMode: mode},
		MaxTokens:       4096,
		ReasoningEffort: effort,
	}
	client := newAnthropicDialectAt(srv.URL, opts, "gho_test", map[string]string{})
	if _, err := client.SendMessages(context.Background(), nil, nil, nil); err != nil {
		// An empty message list is fine for the stub; only the body matters.
		t.Logf("send: %v", err)
	}
	return body
}

func TestAnthropicDialectSendsEffortForAdaptiveModels(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		body := captureAnthropicBody(t, "adaptive", level)
		oc, _ := body["output_config"].(map[string]any)
		if oc["effort"] != level {
			t.Errorf("level %q: output_config = %v, want effort=%q (body=%v)", level, body["output_config"], level, body)
		}
		th, _ := body["thinking"].(map[string]any)
		if th["type"] != "adaptive" {
			t.Errorf("level %q: thinking = %v, want adaptive", level, body["thinking"])
		}
	}
}

func TestAnthropicDialectOmitsEffortForBudgetModels(t *testing.T) {
	body := captureAnthropicBody(t, "", "high")
	if _, ok := body["output_config"]; ok {
		t.Errorf("budget-tier model must not send output_config: %v", body)
	}
}

// Fallback routing when /models is unavailable: vendor prefix plus the
// catalog's preferred_endpoint.
func TestWireFallbackWithoutModelsCatalog(t *testing.T) {
	cases := []struct {
		apiModel, preferred string
		want                copilotWire
	}{
		{"claude-sonnet-5", "", wireMessages},
		{"gpt-5.4-mini", "responses", wireResponses},
		{"grok-4.7", "responses", wireResponses},
		{"mai-code-1.1-flash", "responses", wireResponses},
		{"gemini-3.8-flash", "responses", wireChat},
		{"kimi-k3", "", wireChat},
	}
	for _, c := range cases {
		m := models.Model{APIModel: c.apiModel, PreferredEndpoint: c.preferred}
		if got := copilotWireFor(m, nil); got != c.want {
			t.Errorf("%s (preferred %q): wire = %v, want %v", c.apiModel, c.preferred, got, c.want)
		}
	}
}

// Routing follows the account's recorded supported_endpoints. Copilot answers a
// call on an unlisted endpoint with 400 unsupported_api_for_model, which is what
// the name-prefix rule did to grok-* and mai-code-* (no catalog preference was
// consulted, so they went to /chat/completions).
func TestWireFollowsRecordedSupportedEndpoints(t *testing.T) {
	body, err := os.ReadFile("testdata/copilot_models.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _, endpoints, err := parseModels(body)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]copilotWire{
		"grok-4.5":           wireResponses,
		"grok-4.7":           wireResponses,
		"mai-code-1.1-flash": wireResponses,
		"gpt-5.4":            wireResponses,
		"claude-sonnet-5.5":  wireMessages,
		"gemini-3.8-flash":   wireChat,
		"kimi-k3":            wireChat,
	}
	for apiModel, want := range cases {
		eps := endpoints[apiModel]
		if len(eps) == 0 {
			t.Fatalf("%s: no supported_endpoints in recorded /models", apiModel)
		}
		// Deliberately pass NO catalog preference: the account list alone decides.
		if got := copilotWireFor(models.Model{APIModel: apiModel}, eps); got != want {
			t.Errorf("%s (%v): wire = %v, want %v", apiModel, eps, got, want)
		}
	}
}

// NewClient consults the warmed availability cache, so a model the catalog does
// not mark preferred_endpoint still lands on the endpoint Copilot lists.
func TestNewClientRoutesByCachedEndpoints(t *testing.T) {
	token := "gho_test_route"
	availabilityMu.Lock()
	availabilityCache[tokenKey(token)] = availabilityEntry{
		enabled:   map[string]bool{"grok-4.9": true},
		endpoints: map[string][]string{"grok-4.9": {"/responses"}},
		fetchedAt: time.Now(),
	}
	availabilityMu.Unlock()
	defer EvictAvailabilityCache(token)

	if got := cachedEndpoints(token, "grok-4.9"); len(got) != 1 || got[0] != "/responses" {
		t.Fatalf("cachedEndpoints = %v", got)
	}
	if cachedEndpoints("gho_other", "grok-4.9") != nil {
		t.Error("an unwarmed token must yield nil (fall back), not fetch")
	}
}
