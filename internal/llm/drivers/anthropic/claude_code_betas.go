// Copyright (c) 2025 Reliant Labs
package anthropic

// Per-model anthropic-beta header strings, captured verbatim from real
// claude-cli/2.1.204 traffic. Order and content are load-bearing for fidelity, so
// each string is stored exactly as captured rather than composed from feature
// flags. Keep these byte-identical to the captures.
const (
	// haiku-4.5 (api_model: claude-haiku-4-5-20251001)
	betaHaiku45 = "oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,server-side-fallback-2026-06-01,fallback-credit-2026-06-01,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07"

	// sonnet-5 (api_model: claude-sonnet-5)
	betaSonnet5 = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24,server-side-fallback-2026-06-01,fallback-credit-2026-06-01,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07"

	// opus-4.8 (api_model: claude-opus-4-8) — same as sonnet-5 plus context-1m-2025-08-07 in 3rd position
	betaOpus48 = "claude-code-20250219,oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24,server-side-fallback-2026-06-01,fallback-credit-2026-06-01,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07"

	// fable-5 (api_model: claude-fable-5) — inferred (no curl captured); assumed identical to sonnet-5.
	betaFable5 = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24,server-side-fallback-2026-06-01,fallback-credit-2026-06-01,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07"

	// opus-5 (api_model: claude-opus-5) — opus-class, so identical to opus-4.8
	// (includes context-1m-2025-08-07). A 2.1.219 capture drops
	// server-side-fallback-2026-06-01, but we keep the whole Claude Code spoof on
	// the 2.1.204 fingerprint (User-Agent + billing version + prompt blocks all
	// 2.1.204), so opus-5 reuses the 2.1.204 opus-4.8 header verbatim.
	betaOpus5 = betaOpus48

	// betaDefault is retained for OLDER models not present in the captures
	// (opus-4.5/4.6, sonnet-4.5/4.6). They still work with this set.
	betaDefault = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24"
)

// claudeCodeBetaHeader returns the exact anthropic-beta header string for the given
// api_model, falling back to betaDefault for models not seen in the captures.
func claudeCodeBetaHeader(apiModel string) string {
	switch apiModel {
	case "claude-haiku-4-5-20251001":
		return betaHaiku45
	case "claude-sonnet-5":
		return betaSonnet5
	case "claude-opus-4-8":
		return betaOpus48
	case "claude-opus-5":
		return betaOpus5
	case "claude-fable-5":
		return betaFable5
	default:
		return betaDefault
	}
}