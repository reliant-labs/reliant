// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// fable51Model is the driver options shape for Claude Fable 5.1. The catalog
// entry (id claude-5.1-fable) is owned elsewhere; the driver keys off api_model.
func fable51Opts() llm.DriverOptions {
	return llm.DriverOptions{
		Model:           models.Model{APIModel: "claude-fable-5-1", ThinkingMode: "adaptive", CanReason: true},
		ReasoningEffort: "xhigh",
		MaxTokens:       64000,
	}
}

// TestClaudeCodeBetaHeader_Fable51 pins the 2.1.261 beta string for fable-5.1 and
// asserts no other model's captured string moved. These are byte-exact captures;
// order is load-bearing.
func TestClaudeCodeBetaHeader_Fable51(t *testing.T) {
	const wantFable51 = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,per-turn-control-2026-07-01,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24,server-side-fallback-2026-07-01,fallback-credit-2026-06-01,thinking-display-updates-2026-08-18,afk-mode-2026-01-31,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07"

	tests := []struct {
		apiModel string
		want     string
	}{
		{"claude-fable-5-1", wantFable51},
		{"claude-fable-5", betaFable5},
		{"claude-opus-5", betaOpus5},
		{"claude-opus-4-8", betaOpus48},
		{"claude-sonnet-5", betaSonnet5},
		{"claude-haiku-4-5-20251001", betaHaiku45},
		{"claude-opus-4-5", betaDefault},
	}

	for _, tc := range tests {
		t.Run(tc.apiModel, func(t *testing.T) {
			if got := claudeCodeBetaHeader(tc.apiModel); got != tc.want {
				t.Fatalf("beta header mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}

	// fable-5.1 must differ from fable-5 in exactly the ways the capture shows.
	got := claudeCodeBetaHeader("claude-fable-5-1")
	for _, absent := range []string{"redact-thinking-2026-02-12", "server-side-fallback-2026-06-01", "context-1m-2025-08-07"} {
		if strings.Contains(got, absent) {
			t.Errorf("fable-5.1 beta header should not contain %q", absent)
		}
	}
	for _, present := range []string{"per-turn-control-2026-07-01", "thinking-display-updates-2026-08-18", "afk-mode-2026-01-31", "server-side-fallback-2026-07-01"} {
		if !strings.Contains(got, present) {
			t.Errorf("fable-5.1 beta header should contain %q", present)
		}
	}
}

// jsonEquivalent reports whether two JSON documents are semantically equal.
// Object key order and whitespace are not part of the wire contract — Go's
// marshaler orders keys differently from the captured body, and the capture on
// disk is pretty-printed — so raw byte comparison would fail on differences that
// do not exist on the wire.
func jsonEquivalent(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(av, bv)
}

// systemBlock is the decoded wire shape of one system block, enough to assert
// block count and cache treatment without depending on SDK internals.
type systemBlock struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	CacheControl *struct {
		Type  string `json:"type"`
		TTL   string `json:"ttl"`
		Scope string `json:"scope"`
	} `json:"cache_control"`
}

// marshalSystemBlocks round-trips the base blocks through JSON so assertions see
// the actual wire shape rather than the param structs.
func marshalSystemBlocks(t *testing.T, apiModel string, disableCache bool) []systemBlock {
	t.Helper()
	raw, err := json.Marshal(claudeCodeBaseSystemBlocks(apiModel, disableCache))
	if err != nil {
		t.Fatalf("marshal system blocks: %v", err)
	}
	var blocks []systemBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("unmarshal system blocks: %v", err)
	}
	return blocks
}

// TestClaudeCodeBaseSystemBlocks_Shape pins the block count and cache treatment
// per model. fable-5.1 sends 5 blocks (an uncached "# Reporting outcomes" block
// at index 2); every other model keeps the 4-block shape.
func TestClaudeCodeBaseSystemBlocks_Shape(t *testing.T) {
	tests := []struct {
		name       string
		apiModel   string
		wantBlocks int
		// agentIdx/outputIdx are the indices of the two cached blocks.
		agentIdx, outputIdx int
		wantReportingAt     int // -1 when the model sends no reporting block
	}{
		{"fable-5.1 sends 5 blocks", "claude-fable-5-1", 5, 3, 4, 2},
		{"fable-5 stays at 4", "claude-fable-5", 4, 2, 3, -1},
		{"opus-5 stays at 4", "claude-opus-5", 4, 2, 3, -1},
		{"opus-4.8 stays at 4", "claude-opus-4-8", 4, 2, 3, -1},
		{"sonnet-5 stays at 4", "claude-sonnet-5", 4, 2, 3, -1},
		{"haiku-4.5 stays at 4", "claude-haiku-4-5-20251001", 4, 2, 3, -1},
		{"unknown model stays at 4", "claude-opus-4-5", 4, 2, 3, -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := marshalSystemBlocks(t, tc.apiModel, false)

			if len(blocks) != tc.wantBlocks {
				t.Fatalf("block count = %d, want %d", len(blocks), tc.wantBlocks)
			}

			// [0] billing header, uncached.
			if !strings.HasPrefix(blocks[0].Text, "x-anthropic-billing-header: cc_version=") {
				t.Errorf("block[0] is not the billing header: %q", blocks[0].Text)
			}
			if blocks[0].CacheControl != nil {
				t.Errorf("block[0] must not be cached")
			}

			// [1] identity, uncached.
			if !strings.HasPrefix(blocks[1].Text, "You are Claude Code") {
				t.Errorf("block[1] is not the identity block: %q", blocks[1].Text)
			}
			if blocks[1].CacheControl != nil {
				t.Errorf("block[1] must not be cached")
			}

			// [2] reporting outcomes, uncached — fable-5.1 only.
			if tc.wantReportingAt >= 0 {
				rb := blocks[tc.wantReportingAt]
				if !strings.HasPrefix(rb.Text, "# Reporting outcomes") {
					t.Errorf("block[%d] is not the reporting-outcomes block: %q", tc.wantReportingAt, rb.Text)
				}
				if rb.CacheControl != nil {
					t.Errorf("block[%d] (reporting outcomes) must not be cached", tc.wantReportingAt)
				}
			} else {
				for i, b := range blocks {
					if strings.HasPrefix(b.Text, "# Reporting outcomes") {
						t.Errorf("model %s must not send a reporting-outcomes block (found at %d)", tc.apiModel, i)
					}
				}
			}

			// agent block: ephemeral, ttl 1h, scope global.
			agent := blocks[tc.agentIdx]
			if agent.CacheControl == nil {
				t.Fatalf("agent block[%d] is not cached", tc.agentIdx)
			}
			if agent.CacheControl.Type != "ephemeral" || agent.CacheControl.TTL != "1h" {
				t.Errorf("agent cache_control = %+v, want ephemeral/1h", *agent.CacheControl)
			}
			if agent.CacheControl.Scope != "global" {
				t.Errorf("agent cache_control scope = %q, want global", agent.CacheControl.Scope)
			}

			// output block: ephemeral, ttl 1h, NO scope.
			output := blocks[tc.outputIdx]
			if output.CacheControl == nil {
				t.Fatalf("output block[%d] is not cached", tc.outputIdx)
			}
			if output.CacheControl.Type != "ephemeral" || output.CacheControl.TTL != "1h" {
				t.Errorf("output cache_control = %+v, want ephemeral/1h", *output.CacheControl)
			}
			if output.CacheControl.Scope != "" {
				t.Errorf("output cache_control scope = %q, want none", output.CacheControl.Scope)
			}
		})
	}
}

// TestClaudeCodeBaseSystemBlocks_DisableCache verifies the block COUNT is
// independent of caching — disabling the cache drops cache_control but must not
// change the 5-block fable-5.1 shape.
func TestClaudeCodeBaseSystemBlocks_DisableCache(t *testing.T) {
	blocks := marshalSystemBlocks(t, "claude-fable-5-1", true)
	if len(blocks) != 5 {
		t.Fatalf("block count with cache disabled = %d, want 5", len(blocks))
	}
	for i, b := range blocks {
		if b.CacheControl != nil {
			t.Errorf("block[%d] carries cache_control with DisableCache set", i)
		}
	}
}

// TestClaudeCodeBillingHeader_PerProfile pins the per-release billing header:
// fable-5.1 reports cc_version=2.1.261.a78 and carries a fresh cc_prompt_id uuid,
// while every other model stays on 2.1.204.281 with no cc_prompt_id.
func TestClaudeCodeBillingHeader_PerProfile(t *testing.T) {
	tests := []struct {
		apiModel      string
		wantVersion   string
		wantPromptID  bool
		wantCLI       string
		wantStainless string
	}{
		{"claude-fable-5-1", "cc_version=2.1.261.a78;", true, "2.1.261", "0.112.1"},
		{"claude-fable-5", "cc_version=2.1.204.281;", false, "2.1.204", "0.94.0"},
		{"claude-opus-5", "cc_version=2.1.204.281;", false, "2.1.204", "0.94.0"},
		{"claude-sonnet-5", "cc_version=2.1.204.281;", false, "2.1.204", "0.94.0"},
		{"claude-opus-4-5", "cc_version=2.1.204.281;", false, "2.1.204", "0.94.0"},
	}

	for _, tc := range tests {
		t.Run(tc.apiModel, func(t *testing.T) {
			profile := claudeCodeProfileFor(tc.apiModel)

			// The whole fingerprint must name ONE cli release.
			if profile.cliVersion != tc.wantCLI {
				t.Errorf("cliVersion = %q, want %q", profile.cliVersion, tc.wantCLI)
			}
			if profile.stainlessVersion != tc.wantStainless {
				t.Errorf("stainlessVersion = %q, want %q", profile.stainlessVersion, tc.wantStainless)
			}
			if !strings.HasPrefix(profile.billingVersion, tc.wantCLI+".") {
				t.Errorf("billingVersion %q is not on cli release %s", profile.billingVersion, tc.wantCLI)
			}

			header := claudeCodeBillingHeader(profile)
			if !strings.Contains(header, tc.wantVersion) {
				t.Errorf("header %q does not contain %q", header, tc.wantVersion)
			}
			if !strings.Contains(header, "cc_entrypoint=cli;") {
				t.Errorf("header %q missing cc_entrypoint", header)
			}
			if got := strings.Contains(header, "cc_prompt_id="); got != tc.wantPromptID {
				t.Errorf("cc_prompt_id present = %v, want %v (header %q)", got, tc.wantPromptID, header)
			}
			// cc_prev_req chaining is still deferred; never emit a fabricated one.
			if strings.Contains(header, "cc_prev_req=") {
				t.Errorf("header %q emits cc_prev_req, which is still deferred", header)
			}

			// cc_prompt_id must be a FRESH uuid per request.
			if tc.wantPromptID {
				second := claudeCodeBillingHeader(profile)
				if promptID(t, header) == promptID(t, second) {
					t.Errorf("cc_prompt_id repeated across requests: %q", header)
				}
			}
		})
	}
}

// promptID extracts the cc_prompt_id value from a billing header.
func promptID(t *testing.T, header string) string {
	t.Helper()
	_, rest, ok := strings.Cut(header, "cc_prompt_id=")
	if !ok {
		t.Fatalf("no cc_prompt_id in %q", header)
	}
	id, _, _ := strings.Cut(rest, ";")
	if len(id) != 36 {
		t.Fatalf("cc_prompt_id %q is not a uuid", id)
	}
	return id
}

// TestClaudeCodeThinkingConfig_DisplayUpdates verifies display:"updates" is
// emitted for fable-5.1 only, and that it marshals to the JSON the capture shows.
// The value postdates the SDK's declared constants, so the marshal check matters.
func TestClaudeCodeThinkingConfig_DisplayUpdates(t *testing.T) {
	tests := []struct {
		name     string
		opts     llm.DriverOptions
		wantJSON string
	}{
		{
			name:     "fable-5.1 sends display updates",
			opts:     fable51Opts(),
			wantJSON: `{"type":"adaptive","display":"updates"}`,
		},
		{
			name: "fable-5 sends bare adaptive",
			opts: llm.DriverOptions{
				Model:     models.Model{APIModel: "claude-fable-5", ThinkingMode: "adaptive"},
				MaxTokens: 64000,
			},
			wantJSON: `{"type":"adaptive"}`,
		},
		{
			name: "opus-5 sends bare adaptive",
			opts: llm.DriverOptions{
				Model:     models.Model{APIModel: "claude-opus-5", ThinkingMode: "adaptive"},
				MaxTokens: 64000,
			},
			wantJSON: `{"type":"adaptive"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClaudeCodeClient(tc.opts)
			raw, err := json.Marshal(client.claudeCodeThinkingConfig())
			if err != nil {
				t.Fatalf("marshal thinking: %v", err)
			}
			if !jsonEquivalent(t, raw, []byte(tc.wantJSON)) {
				t.Fatalf("thinking = %s, want %s", raw, tc.wantJSON)
			}
		})
	}
}

// TestClaudeCodeThinkingConfig_BudgetModelUnaffected ensures the fable-5.1
// display override never touches the budget tier, which has no adaptive config.
func TestClaudeCodeThinkingConfig_BudgetModelUnaffected(t *testing.T) {
	client := NewClaudeCodeClient(llm.DriverOptions{
		Model:           models.Model{APIModel: "claude-haiku-4-5-20251001", ThinkingMode: "budget", CanReason: true},
		ReasoningEffort: "medium",
		MaxTokens:       8192,
	})
	thinking := client.claudeCodeThinkingConfig()
	if thinking.OfAdaptive != nil {
		t.Fatalf("budget model produced an adaptive thinking config")
	}
	if thinking.OfEnabled == nil {
		t.Fatalf("budget model produced no enabled thinking config")
	}
}

// TestApplyClaudeCodeExtras_FallbackShapes pins the two non-interchangeable
// fallbacks shapes: fable-5 names a model in an array, fable-5.1 sends the plain
// string "default", and nothing else sends fallbacks at all.
func TestApplyClaudeCodeExtras_FallbackShapes(t *testing.T) {
	type extras struct {
		Fallbacks json.RawMessage `json:"fallbacks"`
	}

	tests := []struct {
		apiModel string
		want     string // "" means the field must be absent
	}{
		{"claude-fable-5-1", `"default"`},
		{"claude-fable-5", `[{"model":"claude-opus-4-8"}]`},
		{"claude-opus-5", ""},
		{"claude-sonnet-5", ""},
	}

	for _, tc := range tests {
		t.Run(tc.apiModel, func(t *testing.T) {
			client := NewClaudeCodeClient(llm.DriverOptions{
				Model:     models.Model{APIModel: tc.apiModel, ThinkingMode: "adaptive"},
				MaxTokens: 64000,
			})
			params := &anthropic.MessageNewParams{}
			client.applyClaudeCodeExtras(params)
			raw, err := params.MarshalJSON()
			if err != nil {
				t.Fatalf("marshal params: %v", err)
			}
			var got extras
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
			if tc.want == "" {
				if got.Fallbacks != nil {
					t.Fatalf("fallbacks = %s, want absent", got.Fallbacks)
				}
				return
			}
			if string(got.Fallbacks) != tc.want {
				t.Fatalf("fallbacks = %s, want %s", got.Fallbacks, tc.want)
			}
		})
	}
}

// TestGetOutputConfig_EffortLevels covers the xhigh downgrade bug: several models
// declare xhigh and the captures send effort:"xhigh", but the switch previously
// fell through to high.
func TestGetOutputConfig_EffortLevels(t *testing.T) {
	tests := []struct {
		effort string
		want   anthropic.OutputConfigEffort
	}{
		{"low", anthropic.OutputConfigEffortLow},
		{"medium", anthropic.OutputConfigEffortMedium},
		{"high", anthropic.OutputConfigEffortHigh},
		{"xhigh", anthropic.OutputConfigEffortXhigh},
		{"max", anthropic.OutputConfigEffortMax},
		{"", anthropic.OutputConfigEffortHigh},
		{"nonsense", anthropic.OutputConfigEffortHigh},
	}

	for _, tc := range tests {
		t.Run("effort="+tc.effort, func(t *testing.T) {
			client := NewClaudeCodeClient(llm.DriverOptions{
				Model:           models.Model{APIModel: "claude-fable-5-1", ThinkingMode: "adaptive"},
				ReasoningEffort: tc.effort,
				MaxTokens:       64000,
			})
			oc, ok := client.getOutputConfig()
			if !ok {
				t.Fatalf("adaptive model produced no output_config")
			}
			if oc.Effort != tc.want {
				t.Fatalf("effort = %q, want %q", oc.Effort, tc.want)
			}
		})
	}

	// Budget-tier models must not send output_config at all.
	budget := NewClaudeCodeClient(llm.DriverOptions{
		Model:           models.Model{APIModel: "claude-haiku-4-5-20251001", ThinkingMode: "budget", CanReason: true},
		ReasoningEffort: "xhigh",
		MaxTokens:       8192,
	})
	if _, ok := budget.getOutputConfig(); ok {
		t.Fatalf("budget model emitted output_config")
	}
}

// TestPreparedMessages_MatchesCaptureShape marshals a full fable-5.1 request and
// compares its STRUCTURE against the captured body. Per-session values (ids,
// billing-header randoms, message content, tools) legitimately differ and are not
// asserted; the top-level key set and the system array's shape are the contract.
func TestPreparedMessages_MatchesCaptureShape(t *testing.T) {
	capturePath := filepath.Join("..", "..", "..", "..", ".dev", "claude", "fable-5.1.json")
	captureRaw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Skipf("capture not available at %s: %v", capturePath, err)
	}

	var capture struct {
		Model         string          `json:"model"`
		System        []systemBlock   `json:"system"`
		MaxTokens     int64           `json:"max_tokens"`
		Thinking      json.RawMessage `json:"thinking"`
		Fallbacks     json.RawMessage `json:"fallbacks"`
		OutputConfig  json.RawMessage `json:"output_config"`
		ContextManage json.RawMessage `json:"context_management"`
	}
	if err := json.Unmarshal(captureRaw, &capture); err != nil {
		t.Fatalf("unmarshal capture: %v", err)
	}

	client := NewClaudeCodeClient(fable51Opts())
	params := client.preparedMessages(nil, []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
	}, nil)

	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal prepared messages: %v", err)
	}

	var got struct {
		Model         string          `json:"model"`
		System        []systemBlock   `json:"system"`
		MaxTokens     int64           `json:"max_tokens"`
		Thinking      json.RawMessage `json:"thinking"`
		Fallbacks     json.RawMessage `json:"fallbacks"`
		OutputConfig  json.RawMessage `json:"output_config"`
		ContextManage json.RawMessage `json:"context_management"`
	}
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
	if !jsonEquivalent(t, got.Fallbacks, capture.Fallbacks) {
		t.Errorf("fallbacks = %s, want %s", got.Fallbacks, capture.Fallbacks)
	}
	if !jsonEquivalent(t, got.OutputConfig, capture.OutputConfig) {
		t.Errorf("output_config = %s, want %s", got.OutputConfig, capture.OutputConfig)
	}
	if !jsonEquivalent(t, got.ContextManage, capture.ContextManage) {
		t.Errorf("context_management = %s, want %s", got.ContextManage, capture.ContextManage)
	}

	// The system array: same block count, same cache treatment per index, and
	// the same prompt bodies for every block but the billing header (whose cch
	// and cc_prompt_id are per-request randoms).
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
			if !strings.HasPrefix(got.System[0].Text, "x-anthropic-billing-header: cc_version=2.1.261.a78;") {
				t.Errorf("system[0] = %q, want the 2.1.261 billing header", got.System[0].Text)
			}
			continue
		}

		// Prompt bodies are byte-faithful to the capture EXCEPT for the four
		// sanitizations applied when they were extracted (home directory, working
		// directory, scratchpad path, git user name). Those only ever shorten a
		// body and never touch its opening, so compare a generous prefix rather
		// than the whole block: it pins that the right variant is embedded at the
		// right index without re-asserting the sanitization.
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
