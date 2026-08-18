// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// ============================================================================
// CLAUDE CODE PROMPT HELPERS
// ============================================================================
//
// Claude Code (sk-ant-oat keys) sends a 4-block `system` array, followed by the
// caller's own system prompts. The four base blocks, in order, are:
//   [0] billing header      (NOT cached)
//   [1] identity            (NOT cached)   — "You are Claude Code, …"
//   [2] agent instructions  (CACHED: ephemeral, ttl 1h, scope:global)
//   [3] output instructions (CACHED: ephemeral, ttl 1h, no scope)
// Blocks [2]/[3] are model-tuned into a verbose (haiku/sonnet-5) or lean
// (opus-4.8/fable-5) variant; see claude_code_prompts_embed.go. The exact prompt
// text is embedded byte-for-byte from real claude-cli/2.1.204 captures.

// claudeCodeVersion is the CLI version reported in the billing header. It must
// match the User-Agent's claude-cli version family used by the spoof.
const claudeCodeVersion = "2.1.204.281"

// randomCCH returns a plausible 5-hex-character request hash for the billing
// header's `cch=` segment. Real Claude Code varies this per request.
func randomCCH() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000"
	}
	return hex.EncodeToString(b[:])[:5]
}

// claudeCodeBillingHeader builds block[0]: the (uncached) billing header.
// Format: `x-anthropic-billing-header: cc_version=<v>; cc_entrypoint=cli; cch=<5hex>; [cc_prev_req=<req_…>;]`
//
// TODO(prev-id-chaining): chain `cc_prev_req=<previous req_… id>;` (the "request-id"
// HTTP response header of the PRIOR turn). DEFERRED for the same reason as
// diagnostics.previous_message_id (see applyClaudeCodeExtras): the client is built
// fresh per turn, and the prior turn's req_ id is neither captured nor persisted.
// The req_ id is not on the SDK response body — it must be read via
// option.WithResponseInto(&resp) then resp.Header.Get("request-id") — then threaded
// back on llm.DriverResponse, persisted on message.Message, and read next turn. That
// plumbing lives outside internal/llm/drivers/anthropic; until it exists we omit just
// this segment rather than send a stale/fabricated req_ id.
func claudeCodeBillingHeader() string {
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s; cc_entrypoint=cli; cch=%s;",
		claudeCodeVersion, randomCCH())
}

// claudeCodeBaseSystemBlocks builds the 4 base Claude Code system blocks with the
// exact cache_control treatment observed in real traffic. Caller prompts are
// appended AFTER these by the driver.
//
// EVERY request — normal turns, compaction, title generation — sends the same 4
// base blocks; requests are specialized by their USER message, not by varying the
// base system prompt. This keeps all Reliant→Anthropic traffic shaped like
// legitimate Claude Code (a 2-block request would be an anomalous fingerprint).
func claudeCodeBaseSystemBlocks(apiModel string, disableCache bool) []anthropic.TextBlockParam {
	blocks := []anthropic.TextBlockParam{
		{Text: claudeCodeBillingHeader()}, // block[0] — not cached
		{Text: ccIdentityPrompt},          // block[1] — not cached
	}

	agent, output := claudeCodeAgentOutputBlocks(apiModel)
	agentBlock := anthropic.TextBlockParam{Text: agent}
	outputBlock := anthropic.TextBlockParam{Text: output}

	if !disableCache {
		// block[2]: ephemeral, ttl 1h, scope:global (scope via SetExtraFields).
		agentCC := anthropic.CacheControlEphemeralParam{
			Type: "ephemeral",
			TTL:  anthropic.CacheControlEphemeralTTLTTL1h,
		}
		agentCC.SetExtraFields(map[string]any{"scope": "global"})
		agentBlock.CacheControl = agentCC

		// block[3]: ephemeral, ttl 1h, no scope.
		outputBlock.CacheControl = anthropic.CacheControlEphemeralParam{
			Type: "ephemeral",
			TTL:  anthropic.CacheControlEphemeralTTLTTL1h,
		}
	}

	blocks = append(blocks, agentBlock, outputBlock)
	return blocks
}
