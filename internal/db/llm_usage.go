// Copyright (c) 2025 Reliant Labs
package db

import (
	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/store"
)

// LLMCallUsage is one model call's counts: tokens, model, provider, auth mode,
// outcome and latency. It has no field for prompt, reply, tool I/O or error
// text, and must not gain one (TestLLMUsageTablesHoldNoContent).
type LLMCallUsage = store.LLMCallUsageRecord

// LLMUsageDay is one rollup row: a UTC day's calls of one user, org, provider,
// model and auth mode.
type LLMUsageDay = store.LlmCallUsageDaily

// LLMCallUsageRetention is how long the per-call rows live. The daily rollup
// is kept; docs/help/privacy.mdx states this number.
const LLMCallUsageRetention = llmusage.Retention

// LLMUsageDetail is the user's per-call rows summed per UTC day and every
// recorded dimension, funding and account included.
type LLMUsageDetail = store.LLMUsageDetail

// LLMOrgUsageDay is one row of an org's rollup of ORG-FUNDED calls.
type LLMOrgUsageDay = store.LlmOrgUsageDaily

// OrgKeyModelUsage is the tokens one model used through an org's own API keys.
type OrgKeyModelUsage = store.OrgKeyModelUsage

// OrgChatCall is one of an org's live chats' latest chat-turn model call.
type OrgChatCall = store.OrgChatCall
