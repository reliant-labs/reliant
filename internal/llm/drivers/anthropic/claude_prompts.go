// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"
)

// ============================================================================
// CLAUDE CODE PROMPT HELPERS
// ============================================================================
//
// Claude Code sends a base `system` array, followed by the caller's own system
// prompts. The base array's shape depends on which claude-cli release the model
// is fingerprinted against (see claudeCodeProfile).
//
// 2.1.204 (every model except fable-5.1) — 4 blocks:
//   [0] billing header      (NOT cached)
//   [1] identity            (NOT cached)   — "You are Claude Code, …"
//   [2] agent instructions  (CACHED: ephemeral, ttl 1h, scope:global)
//   [3] output instructions (CACHED: ephemeral, ttl 1h, no scope)
//
// 2.1.261 (fable-5.1) — 5 blocks: an uncached "# Reporting outcomes" block is
// inserted at index 2, pushing agent/output to 3/4 with their cache treatment
// unchanged.
//
// Blocks are model-tuned into verbose, lean or fable-5.1 variants; see
// claude_code_prompts_embed.go. The exact prompt text is embedded byte-for-byte
// from real captures.

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
//
// Format, 2.1.261: `x-anthropic-billing-header: cc_version=<v>; cc_entrypoint=cli; cch=<5hex>; cc_prompt_id=<uuid>;`
// Format, 2.1.204: `x-anthropic-billing-header: cc_version=<v>; cc_entrypoint=cli; cch=<5hex>; [cc_prev_req=<req_…>;]`
//
// cc_prompt_id is a fresh per-request uuid in the 2.1.261 capture, so we mint one
// the same way. It replaced the older cc_prev_req segment.
//
// TODO(prev-id-chaining): on the 2.1.204 profiles, chain `cc_prev_req=<previous
// req_… id>;` (the "request-id" HTTP response header of the PRIOR turn). DEFERRED
// for the same reason as diagnostics.previous_message_id (see
// applyClaudeCodeExtras): the client is built fresh per turn, and the prior turn's
// req_ id is neither captured nor persisted. The req_ id is not on the SDK
// response body — it must be read via option.WithResponseInto(&resp) then
// resp.Header.Get("request-id") — then threaded back on llm.DriverResponse,
// persisted on message.Message, and read next turn. That plumbing lives outside
// internal/llm/drivers/anthropic; until it exists we omit just this segment rather
// than send a stale/fabricated req_ id.
func claudeCodeBillingHeader(profile claudeCodeProfile) string {
	header := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s; cc_entrypoint=cli; cch=%s;",
		profile.billingVersion, randomCCH())
	if profile.billingPromptID {
		header += fmt.Sprintf(" cc_prompt_id=%s;", uuid.New().String())
	}
	return header
}

// claudeCodeBaseSystemBlocks builds the base Claude Code system blocks with the
// exact cache_control treatment observed in real traffic. Caller prompts are
// appended AFTER these by the driver.
//
// EVERY request — normal turns, compaction, title generation — sends the same base
// blocks; requests are specialized by their USER message, not by varying the base
// system prompt. This keeps all Reliant→Anthropic traffic shaped like legitimate
// Claude Code (a 2-block request would be an anomalous fingerprint).
func claudeCodeBaseSystemBlocks(apiModel string, disableCache bool) []anthropic.TextBlockParam {
	profile := claudeCodeProfileFor(apiModel)

	blocks := []anthropic.TextBlockParam{
		{Text: claudeCodeBillingHeader(profile)}, // not cached
		{Text: ccIdentityPrompt},                 // not cached
	}

	// 2.1.261 only: an extra uncached block before the cached pair.
	if profile.reportingOutcomes != "" {
		blocks = append(blocks, anthropic.TextBlockParam{Text: profile.reportingOutcomes})
	}

	agentBlock := anthropic.TextBlockParam{Text: profile.agent}
	outputBlock := anthropic.TextBlockParam{Text: profile.output}

	if !disableCache {
		// agent block: ephemeral, ttl 1h, scope:global (scope via SetExtraFields).
		agentCC := anthropic.CacheControlEphemeralParam{
			Type: "ephemeral",
			TTL:  anthropic.CacheControlEphemeralTTLTTL1h,
		}
		agentCC.SetExtraFields(map[string]any{"scope": "global"})
		agentBlock.CacheControl = agentCC

		// output block: ephemeral, ttl 1h, no scope.
		outputBlock.CacheControl = anthropic.CacheControlEphemeralParam{
			Type: "ephemeral",
			TTL:  anthropic.CacheControlEphemeralTTLTTL1h,
		}
	}

	blocks = append(blocks, agentBlock, outputBlock)
	return blocks
}
