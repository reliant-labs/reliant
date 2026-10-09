// Copyright (c) 2025 Reliant Labs
package cache

import "strings"

// Anthropic-dialect breakpoints carry no ttl (the API's 5m default): measured
// on prod, the 1h TTL cost more in write premium than it saved in reads.
// Anthropic requires longer-TTL breakpoints to precede shorter ones
// (tools -> system -> messages); any future per-section TTL must keep that order.

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
