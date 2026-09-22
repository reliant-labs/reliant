// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// opus55Opts is the driver options shape for Claude Opus 5.5. The catalog entry
// (id claude-5.5-opus) is owned elsewhere; the driver keys off api_model. The
// capture sends max_tokens 128000 and effort medium.
func opus55Opts() llm.DriverOptions {
	return llm.DriverOptions{
		Model:           models.Model{APIModel: apiModelOpus55, ThinkingMode: "adaptive", CanReason: true},
		ReasoningEffort: "medium",
		MaxTokens:       128000,
	}
}

// TestClaudeCodeBetaHeader_Opus55 pins the 2.1.280 beta string byte-exactly and
// asserts it differs from the 2.1.261 fable-5.1 string in exactly the ways the
// two captures show. Order is load-bearing.
func TestClaudeCodeBetaHeader_Opus55(t *testing.T) {
	const wantOpus55 = "claude-code-20250219,oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,mid-conversation-system-clear-at-2026-08-21,effort-2025-11-24,thinking-binding-controls-2026-08-01,thinking-display-updates-2026-08-18,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07"

	got := claudeCodeBetaHeader(apiModelOpus55)
	if got != wantOpus55 {
		t.Fatalf("beta header mismatch\n got: %s\nwant: %s", got, wantOpus55)
	}

	// Adding opus-5.5 must not have moved any other model's captured string.
	for _, tc := range []struct{ apiModel, want string }{
		{"claude-fable-5-1", betaFable51},
		{"claude-fable-5", betaFable5},
		{"claude-opus-5", betaOpus5},
		{"claude-opus-4-8", betaOpus48},
		{"claude-sonnet-5", betaSonnet5},
		{"claude-haiku-4-5-20251001", betaHaiku45},
		{"claude-opus-4-5", betaDefault},
	} {
		if got := claudeCodeBetaHeader(tc.apiModel); got != tc.want {
			t.Errorf("%s beta header moved:\n got: %s\nwant: %s", tc.apiModel, got, tc.want)
		}
	}

	// opus-5.5 is opus-class, so the 1M context beta returns; the server-side
	// fallback pair and afk-mode are gone, matching a capture with no
	// `fallbacks` field.
	for _, present := range []string{
		"context-1m-2025-08-07",
		"mid-conversation-tool-changes-2026-07-01",
		"mid-conversation-system-clear-at-2026-08-21",
		"thinking-binding-controls-2026-08-01",
		"thinking-display-updates-2026-08-18",
	} {
		if !strings.Contains(got, present) {
			t.Errorf("opus-5.5 beta header should contain %q", present)
		}
	}
	for _, absent := range []string{
		"redact-thinking-2026-02-12",
		"server-side-fallback-2026-06-01",
		"server-side-fallback-2026-07-01",
		"fallback-credit-2026-06-01",
		"afk-mode-2026-01-31",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("opus-5.5 beta header should not contain %q", absent)
		}
	}
}

// TestClaudeCodeBaseSystemBlocks_Opus55Shape pins opus-5.5 back at the 4-block
// shape. 2.1.280 does NOT send the reporting-outcomes block that 2.1.261 added —
// that content moved into the release's mid-conversation system turn, which
// Reliant does not send. Emitting 5 blocks here would be a fingerprint no real
// 2.1.280 client produces.
func TestClaudeCodeBaseSystemBlocks_Opus55Shape(t *testing.T) {
	blocks := marshalSystemBlocks(t, apiModelOpus55, false)

	if len(blocks) != 4 {
		t.Fatalf("block count = %d, want 4", len(blocks))
	}
	for i, b := range blocks {
		if strings.HasPrefix(b.Text, "# Reporting outcomes") {
			t.Errorf("opus-5.5 must not send a reporting-outcomes block (found at %d)", i)
		}
	}

	if !strings.HasPrefix(blocks[0].Text, "x-anthropic-billing-header: cc_version=2.1.280.790;") {
		t.Errorf("block[0] = %q, want the 2.1.280 billing header", blocks[0].Text)
	}
	if blocks[0].CacheControl != nil || blocks[1].CacheControl != nil {
		t.Error("blocks[0] and [1] must not be cached")
	}
	if !strings.HasPrefix(blocks[1].Text, "You are Claude Code") {
		t.Errorf("block[1] is not the identity block: %q", blocks[1].Text)
	}

	// The opus-5.5 agent block is distinguished from every other variant by its
	// pasted_content guidance, and the output block by its leading line.
	if !strings.Contains(blocks[2].Text, "<pasted_content>") {
		t.Error("block[2] is not the opus-5.5 agent variant")
	}
	if !strings.HasPrefix(blocks[3].Text, "Write code that reads like the surrounding code") {
		t.Errorf("block[3] is not the opus-5.5 output variant: %.60q", blocks[3].Text)
	}

	// agent: ephemeral/1h/global. output: ephemeral/1h, no scope.
	agent, output := blocks[2].CacheControl, blocks[3].CacheControl
	if agent == nil || agent.Type != "ephemeral" || agent.TTL != "1h" || agent.Scope != "global" {
		t.Errorf("agent cache_control = %+v, want ephemeral/1h/global", agent)
	}
	if output == nil || output.Type != "ephemeral" || output.TTL != "1h" || output.Scope != "" {
		t.Errorf("output cache_control = %+v, want ephemeral/1h with no scope", output)
	}
}

// TestClaudeCodeBillingHeader_Opus55 pins the 2.1.280 billing header: version
// 2.1.280.790, a fresh cc_prompt_id per request, and the cc_turn_origin segment
// no earlier release sends.
func TestClaudeCodeBillingHeader_Opus55(t *testing.T) {
	profile := claudeCodeProfileFor(apiModelOpus55)

	if profile.cliVersion != "2.1.280" {
		t.Errorf("cliVersion = %q, want 2.1.280", profile.cliVersion)
	}
	if profile.stainlessVersion != "0.112.1" {
		t.Errorf("stainlessVersion = %q, want 0.112.1", profile.stainlessVersion)
	}
	if !strings.HasPrefix(profile.billingVersion, "2.1.280.") {
		t.Errorf("billingVersion %q is not on cli release 2.1.280", profile.billingVersion)
	}

	header := claudeCodeBillingHeader(profile)
	for _, want := range []string{
		"cc_version=2.1.280.790;",
		"cc_entrypoint=cli;",
		"cc_prompt_id=",
		"cc_turn_origin=human;",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q missing %q", header, want)
		}
	}
	// cc_prev_req chaining is still deferred; never emit a fabricated one.
	if strings.Contains(header, "cc_prev_req=") {
		t.Errorf("header %q emits cc_prev_req, which is still deferred", header)
	}
	if promptID(t, header) == promptID(t, claudeCodeBillingHeader(profile)) {
		t.Errorf("cc_prompt_id repeated across requests: %q", header)
	}

	// cc_turn_origin is 2.1.280-only — no earlier profile may grow it.
	for _, apiModel := range []string{"claude-fable-5-1", "claude-opus-5", "claude-sonnet-5"} {
		if h := claudeCodeBillingHeader(claudeCodeProfileFor(apiModel)); strings.Contains(h, "cc_turn_origin") {
			t.Errorf("%s billing header emits cc_turn_origin: %q", apiModel, h)
		}
	}
}

// TestClaudeCodeThinkingConfig_Opus55 verifies opus-5.5 carries
// display:"updates" like fable-5.1, while the 2.1.204 models keep sending a bare
// adaptive config. The display value postdates the SDK's declared constants, so
// the marshal check matters.
func TestClaudeCodeThinkingConfig_Opus55(t *testing.T) {
	client := NewClaudeCodeClient(opus55Opts())
	raw, err := json.Marshal(client.claudeCodeThinkingConfig())
	if err != nil {
		t.Fatalf("marshal thinking: %v", err)
	}
	if !jsonEquivalent(t, raw, []byte(`{"type":"adaptive","display":"updates"}`)) {
		t.Fatalf("thinking = %s, want {\"type\":\"adaptive\",\"display\":\"updates\"}", raw)
	}
}

// TestApplyClaudeCodeExtras_Opus55NoFallbacks pins the absence of `fallbacks`.
// The 2.1.280 capture has no such field, consistent with its beta header
// dropping both server-side-fallback and fallback-credit. Sending fable-5.1's
// "default" here would be a shape no real opus-5.5 request carries.
func TestApplyClaudeCodeExtras_Opus55NoFallbacks(t *testing.T) {
	client := NewClaudeCodeClient(opus55Opts())
	params := &anthropic.MessageNewParams{}
	client.applyClaudeCodeExtras(params)

	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var got struct {
		Fallbacks     json.RawMessage `json:"fallbacks"`
		ContextManage json.RawMessage `json:"context_management"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got.Fallbacks != nil {
		t.Errorf("fallbacks = %s, want absent", got.Fallbacks)
	}
	if got.ContextManage == nil {
		t.Error("context_management is absent; every capture sends it")
	}
}

// TestClaudeCodeHeaders_Opus55DispatchAndRequestClass pins the two headers only
// 2.1.280 sends, and their absence everywhere else. They are set through the
// lowercase-header map so Go does not canonicalize them.
func TestClaudeCodeHeaders_Opus55DispatchAndRequestClass(t *testing.T) {
	opus55 := claudeCodeProfileFor(apiModelOpus55)
	if opus55.dispatchID != "v2d" {
		t.Errorf("dispatchID = %q, want v2d", opus55.dispatchID)
	}
	if opus55.requestClass != "main" {
		t.Errorf("requestClass = %q, want main", opus55.requestClass)
	}

	for _, apiModel := range []string{"claude-fable-5-1", "claude-opus-5", "claude-sonnet-5", "claude-opus-4-5"} {
		p := claudeCodeProfileFor(apiModel)
		if p.dispatchID != "" || p.requestClass != "" {
			t.Errorf("%s carries 2.1.280-only headers: dispatch=%q class=%q",
				apiModel, p.dispatchID, p.requestClass)
		}
	}
}

// TestPreparedMessages_Opus55MatchesCaptureShape marshals a full opus-5.5
// request and compares its STRUCTURE against the captured body. Per-session
// values (ids, billing-header randoms, message content, tools) legitimately
// differ and are not asserted; the top-level key set and the system array's
// shape are the contract.
func TestPreparedMessages_Opus55MatchesCaptureShape(t *testing.T) {
	capturePath := filepath.Join("..", "..", "..", "..", ".dev", "claude", "opus-5.5.json")
	captureRaw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Skipf("capture not available at %s: %v", capturePath, err)
	}

	type body struct {
		Model         string          `json:"model"`
		System        []systemBlock   `json:"system"`
		MaxTokens     int64           `json:"max_tokens"`
		Thinking      json.RawMessage `json:"thinking"`
		Fallbacks     json.RawMessage `json:"fallbacks"`
		OutputConfig  json.RawMessage `json:"output_config"`
		ContextManage json.RawMessage `json:"context_management"`
	}

	var capture body
	if err := json.Unmarshal(captureRaw, &capture); err != nil {
		t.Fatalf("unmarshal capture: %v", err)
	}

	client := NewClaudeCodeClient(opus55Opts())
	params := client.preparedMessages(nil, []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
	}, nil)

	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal prepared messages: %v", err)
	}
	var got body
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal prepared messages: %v", err)
	}

	if got.Model != capture.Model {
		t.Errorf("model = %q, want %q", got.Model, capture.Model)
	}
	if got.MaxTokens != capture.MaxTokens {
		t.Errorf("max_tokens = %d, want %d", got.MaxTokens, capture.MaxTokens)
	}
	if !jsonEquivalent(t, got.Thinking, capture.Thinking) {
		t.Errorf("thinking = %s, want %s", got.Thinking, capture.Thinking)
	}
	if !jsonEquivalent(t, got.OutputConfig, capture.OutputConfig) {
		t.Errorf("output_config = %s, want %s", got.OutputConfig, capture.OutputConfig)
	}
	if !jsonEquivalent(t, got.ContextManage, capture.ContextManage) {
		t.Errorf("context_management = %s, want %s", got.ContextManage, capture.ContextManage)
	}
	if capture.Fallbacks != nil {
		t.Errorf("capture unexpectedly has fallbacks = %s", capture.Fallbacks)
	}
	if got.Fallbacks != nil {
		t.Errorf("fallbacks = %s, want absent", got.Fallbacks)
	}

	if len(got.System) != len(capture.System) {
		t.Fatalf("system block count = %d, want %d", len(got.System), len(capture.System))
	}
	for i := range capture.System {
		wantCC, gotCC := capture.System[i].CacheControl, got.System[i].CacheControl
		switch {
		case wantCC == nil && gotCC != nil:
			t.Errorf("system[%d] is cached but the capture's is not", i)
		case wantCC != nil && gotCC == nil:
			t.Errorf("system[%d] is not cached but the capture's is %+v", i, *wantCC)
		case wantCC != nil && gotCC != nil && *wantCC != *gotCC:
			t.Errorf("system[%d] cache_control = %+v, want %+v", i, *gotCC, *wantCC)
		}

		if i == 0 {
			// Billing header: version fixed, cch/cc_prompt_id per-request.
			if !strings.HasPrefix(got.System[0].Text, "x-anthropic-billing-header: cc_version=2.1.280.790;") {
				t.Errorf("system[0] = %q, want the 2.1.280 billing header", got.System[0].Text)
			}
			continue
		}

		// Prompt bodies are byte-faithful to the capture EXCEPT for the one
		// sanitization applied when they were extracted (the memory directory's
		// home path). That only ever shortens a body and never touches its
		// opening, so compare a generous prefix rather than the whole block.
		const prefixLen = 256
		wantText, gotText := capture.System[i].Text, got.System[i].Text
		n := min(prefixLen, min(len(wantText), len(gotText)))
		if wantText[:n] != gotText[:n] {
			t.Errorf("system[%d] prompt body does not match the capture\n got: %q\nwant: %q",
				i, gotText[:n], wantText[:n])
		}
		if len(gotText) > len(wantText) {
			t.Errorf("system[%d] is %d bytes, longer than the capture's %d — sanitization only removes",
				i, len(gotText), len(wantText))
		}
	}
}
