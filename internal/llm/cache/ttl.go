// Copyright (c) 2025 Reliant Labs
package cache

import "strings"

// ExtendedTTL is the cache_control `ttl` every Anthropic-dialect breakpoint
// carries: the Anthropic API, Claude on Vertex AI, OpenRouter's Claude
// passthrough, and the LiteLLM gateway in front of Vertex.
//
// WHY ONE HOUR, NOT THE 5-MINUTE DEFAULT. An agent turn routinely outlives five
// minutes: a long xhigh-thinking generation, a slow tool, a user reading the
// reply. When it does, the next request's prefix has already expired and the
// whole conversation is written to cache again from scratch. Measured on our
// own claude-code traffic, gaps of 5m–1h were 5% of requests but produced 55% of
// all cache-write tokens, at a 41% miss rate. A 1h write costs 2x base input
// against 1.25x for 5m, and replaying that traffic with every write at the 1h
// price still came out ~8% cheaper on input, before counting the latency of
// re-prefilling a 400k-token prefix.
//
// It must be uniform within a request, not just preferred. Anthropic requires
// every longer-TTL breakpoint to precede every shorter one (tools -> system ->
// messages), so mixing 1h system blocks with a 5m message breakpoint is only
// legal in that one order, and a stray 5m breakpoint on a tool would 400 the
// request. Sending 1h everywhere makes the ordering rule unreachable.
//
// No beta header is needed: 1h TTL is GA on the Anthropic API, Vertex AI and
// Bedrock. Real Claude Code sends ttl:"1h" on its message breakpoint and its
// cached system blocks (.dev/claude/*.json captures), so the claude-code driver
// matches its fingerprint by using this too.
//
// Not applied to GitHub Copilot's Anthropic endpoint: its official client sends
// a bare {type:"ephemeral"}, and nothing documents that the endpoint accepts a
// ttl. See research/PROMPT_CACHE_TTL_BY_PROVIDER.md.
const ExtendedTTL = "1h"

// OpenAIExtendedRetention is the prompt_cache_retention value that keeps an
// OpenAI prompt-cache prefix for up to 24 hours instead of the in-memory
// default of 5–10 minutes (up to an hour off-peak).
const OpenAIExtendedRetention = "24h"

// openAIExtendedRetentionModels is OpenAI's documented list of models that
// accept prompt_cache_retention:"24h", verbatim from
// https://platform.openai.com/docs/guides/prompt-caching.
//
// It is an allowlist on purpose. OpenAI documents no behavior for the field on
// an unlisted model, and its neighbors show both outcomes are live: gpt-5.5
// rejects "in_memory" outright, and GPT-5.6+ replaced the field with
// prompt_cache_options. A request that 400s costs far more than a cache that
// expires early, so an unlisted model gets the provider default.
//
// Notably absent, per the docs: every -pro except gpt-5.5-pro, gpt-5.2-codex,
// gpt-5.3-codex, gpt-5.4-mini, and the GPT-5.6/GPT-6 families (whose only
// knob, prompt_cache_options.ttl, accepts nothing longer than its "30m"
// default, so there is nothing to extend).
var openAIExtendedRetentionModels = map[string]struct{}{
	"gpt-5.5":             {},
	"gpt-5.5-pro":         {},
	"gpt-5.4":             {},
	"gpt-5.2":             {},
	"gpt-5.1-codex-max":   {},
	"gpt-5.1":             {},
	"gpt-5.1-codex":       {},
	"gpt-5.1-codex-mini":  {},
	"gpt-5.1-chat-latest": {},
	"gpt-5":               {},
	"gpt-5-codex":         {},
	"gpt-4.1":             {},
}

// SupportsOpenAIExtendedRetention reports whether apiModel — the identifier
// sent to the OpenAI platform API — accepts prompt_cache_retention:"24h".
//
// For an organization without Zero Data Retention, "24h" is already the
// default on these models, so sending it changes nothing there. It is sent
// anyway so the retention does not silently depend on account settings: a ZDR
// organization defaults to in-memory, and an explicit value states what this
// client wants rather than inheriting whatever the account was provisioned with.
func SupportsOpenAIExtendedRetention(apiModel string) bool {
	_, ok := openAIExtendedRetentionModels[strings.ToLower(strings.TrimSpace(apiModel))]
	return ok
}
