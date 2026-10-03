# Prompt-cache retention: the longest TTL each provider supports

Researched from public docs. **[V]** means I verified it in an official source during this pass. **[I]** means it is inferred or comes from a secondary source.

## 1. Anthropic Messages API — SUPPORTED: `"cache_control": {"type":"ephemeral","ttl":"1h"}`
- [V] Uses the wire shape above. If you omit `ttl`, it defaults to 5m. Top-level automatic caching accepts the same `{"type":"ephemeral","ttl":"1h"}`.
- [V] Beta header: the current prompt-caching page documents `ttl` with no mention of `extended-cache-ttl-2025-04-11`, so treat 1h TTL as GA. Sending the old header is harmless as far as we know [I].
- [V] Pricing: a 5m cache write costs 1.25× base input. A 1h write costs 2× base input. Reads cost 0.1×.
- [V] Ordering: when one request mixes TTLs, the longer TTL must come before the shorter one in prompt order (tools → system → messages), so every 1h breakpoint must precede any 5m breakpoint. Billing is split at three positions: A (cache read), B (1h write), C (5m write).
- [V] Gotcha: if automatic caching is on and the last block also has an explicit `cache_control` with a *different* TTL, the API returns a 400.
- [I] Model support: the docs list no model exclusions on the first-party API for current models.
- URL: https://platform.claude.com/docs/en/build-with-claude/prompt-caching (Markdown version: append `.md`)

## 2. Claude on Google Vertex AI — SUPPORTED: same `cache_control.ttl:"1h"`
- [V] Vertex docs: "You can extend … `"ttl": "1h"` within the cache_control object." 1h TTL is **not supported** on Claude 3.7 Sonnet, 3.5 Sonnet v2, 3.5 Sonnet, or 3 Opus. The docs don't say what happens on those models (error vs. silently ignored) [UNKNOWN].
- [V] The Anthropic docs also list Google Cloud as a platform with 1h TTL. Neither source mentions a beta header [I: none needed].
- URL: https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/prompt-caching

## 3. Claude on AWS Bedrock (InvokeModel with an Anthropic body) — SUPPORTED: `cache_control.ttl:"1h"` on newer models
- [V] InvokeModel supports explicit caching (cache checkpoints) on `system`, `messages` and `tools`, with up to 4 checkpoints. Bedrock's model table lists the supported TTLs:
  - **5m + 1h:** Opus 4.5 / 4.6 / 4.7 / 4.8 / 5 / 5.5, Sonnet 4.5 / 4.6 / 5 / 5.5, Haiku 4.5, Fable 5 / 5.1, Mythos 5 / 5.1 (gated).
  - **5m only:** Claude 3.7 Sonnet and 3.5 Sonnet v2.
- [V] Minimum tokens per checkpoint is 512 for the 5.x models, 1,024 for Sonnet 4.x/5 and Opus 4.8, and 4,096 for Opus 4.5–4.7 and Haiku 4.5.
- [V] Not supported with batch inference.
- [UNKNOWN] Behaviour when you send 1h to a 5m-only model.
- [I] The Claude Code source exposes a separate `ENABLE_PROMPT_CACHING_1H_BEDROCK` flag, which suggests Bedrock was gated separately at some point.
- URL: https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html

## 4. OpenRouter — SUPPORTED for Claude: `cache_control.ttl:"1h"`; NOT SUPPORTED as a TTL passthrough for OpenAI models
- [V] OpenRouter supports `"ttl":"1h"` for Claude on the Chat Completions and Anthropic Messages endpoints, across all three Claude providers (Anthropic, Bedrock, Vertex). A 1h write costs 2× base.
- [V] **Gotcha:** the Responses API endpoint does *not* expose per-block `cache_control`. `prompt_cache_breakpoint` carries no TTL, so it maps to a 5m write.
- [V] OpenAI models: a `cache_control` `ttl` is dropped. The only OpenAI-side knob is `prompt_cache_options: {"ttl":"30m"}` (GPT-5.6+, explicit mode).
- [UNKNOWN] The page says nothing about passing `prompt_cache_retention` through.
- [V] OpenRouter uses `prompt_cache_key` as a sticky-routing key.
- URL: https://openrouter.ai/docs/features/prompt-caching

## 5. OpenAI (Chat Completions + Responses) — SUPPORTED, but the shape depends on the model
- [V] **Models up to GPT-5.5:** `"prompt_cache_retention": "24h"` (or `"in_memory"`). Extended retention is listed for `gpt-5.5`, `gpt-5.5-pro`, `gpt-5.4`, `gpt-5.2`, `gpt-5.1-codex-max`, `gpt-5.1`, `gpt-5.1-codex`, `gpt-5.1-codex-mini`, `gpt-5.1-chat-latest`, `gpt-5`, `gpt-5-codex` and `gpt-4.1`.
  - GPT-5.5 and 5.5 Pro accept **`"24h"` only**.
  - **`gpt-5.4-pro` is not on the list.**
- [V] **Defaults:** on models that support both values, organizations *without* Zero Data Retention already default to `24h`. ZDR organizations default to `in_memory`. So for a non-ZDR org, sending `24h` changes nothing.
- [V] **GPT-5.6 and later (all `gpt-5.6-*`): `prompt_cache_retention` is replaced.** The new knob is `"prompt_cache_options": {"ttl": "30m"}`. `30m` is the only value and also the default.
  - The migration guide says: "Replace `prompt_cache_retention` with `prompt_cache_options.ttl`."
  - Cache writes on 5.6+ cost **1.25×** input. Before 5.6 there was no write surcharge, and the retention setting carries no price difference.
- [UNKNOWN] Whether an unsupported model returns a 400 or silently ignores the field. The docs don't say, so test it or gate the field per model.
- URL: https://platform.openai.com/docs/guides/prompt-caching (Markdown version: append `.md`)

## 6. Azure OpenAI — SUPPORTED [I]: `prompt_cache_retention`
- [V] Microsoft Learn: you set "prompt_cache_retention on your Responses or Chat Completions request … in-memory or extended retention policies", and extended retention is available only on some models.
- [UNKNOWN] The exact model list.
- URL: https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/prompt-caching

## 7. Codex CLI (ChatGPT backend) — NOT SENT by codex-rs
- [V] `codex-rs/core/src/client.rs` sends `prompt_cache_key` (via `CodexResponsesMetadata`, with an override available), and `codex-rs/codex-api/src/common.rs` serializes it.
- [V] A GitHub code search of openai/codex for `prompt_cache_retention` finds only a bundled docs sample about migrating *away* from it. No request code sends it.
- **Do not add the field** if we are mirroring codex-tui.
- Source: https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/client.rs

## 8. Claude Code CLI — 1h is opt-in via env, not the universal default [I]
- [I] Secondary sources (claude-howto README, quoting the v2.1.108 changelog) say `ENABLE_PROMPT_CACHING_1H=1` switches to a 1h prompt-cache TTL instead of the 5m default. Decompiled sources also show `ENABLE_PROMPT_CACHING_1H_BEDROCK`.
- [V] The official CHANGELOG mentions "1-hour prompt cache writes" in a gateway billing fix, which confirms Claude Code issues 1h writes.
- [UNKNOWN] I found no authoritative capture of message-breakpoint TTL vs. system-block TTL, or of subscription-tier defaults. To settle it, capture traffic with Proxyman.
- URL: https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md

## 9. GitHub Copilot Anthropic endpoint — UNKNOWN
No public documentation found. Test it empirically: send `ttl:"1h"` and check the usage response for `ephemeral_1h_input_tokens`.

## 10. Gemini API / Vertex Gemini — NOT SUPPORTED (no per-request knob for implicit caching)
- [V] Implicit caching is on by default for Gemini 2.5+ and has no TTL parameter.
- [V] A TTL exists only on explicit `cachedContents` resources (`ttl` / `expire_time`), which are separate objects with storage billing.
- URL: https://ai.google.dev/gemini-api/docs/caching

## 11. xAI Grok / Groq — NOT SUPPORTED (automatic only)
- [V] Groq: caching is automatic, "cached data automatically expires after 2 hours without use", and there is no knob. https://console.groq.com/docs/prompt-caching
- [I] xAI: caching is automatic and only cached-token pricing is published. No retention parameter was found. https://docs.x.ai

## Summary

| Provider | Verdict | Wire shape | Notes |
|---|---|---|---|
| Anthropic API | SUPPORTED [V] | `cache_control:{type:"ephemeral",ttl:"1h"}` | GA, no beta header; 2× write vs 1.25×; 1h breakpoints must precede 5m |
| Vertex Claude | SUPPORTED [V] | same | not on Claude 3.x |
| Bedrock InvokeModel | SUPPORTED [V] | same | Claude 4.5+ only; 3.7 / 3.5v2 are 5m only |
| OpenRouter → Claude | SUPPORTED [V] | same (Chat / Messages endpoints only) | the Responses endpoint can't set a TTL |
| OpenRouter → OpenAI | ttl dropped [V] | `prompt_cache_options.ttl:"30m"` (5.6+) | |
| OpenAI ≤ 5.5 | SUPPORTED [V] | `prompt_cache_retention:"24h"` | already the default for non-ZDR orgs; gpt-5.4-pro not listed |
| OpenAI 5.6+ | different knob [V] | `prompt_cache_options:{ttl:"30m"}` | `prompt_cache_retention` is superseded; 1.25× write |
| Azure OpenAI | SUPPORTED [I] | `prompt_cache_retention` | model list unknown |
| Codex backend | NOT SENT [V] | — | codex-rs sends only `prompt_cache_key` |
| Claude Code | 1h opt-in [I] | env `ENABLE_PROMPT_CACHING_1H` | per-breakpoint split unknown |
| Copilot | UNKNOWN | — | test empirically |
| Gemini implicit | NOT SUPPORTED [V] | — | TTL only on explicit cachedContents |
| Groq / xAI | NOT SUPPORTED [V] / [I] | — | automatic; Groq ~2h |

---

# Follow-up: deciding which requests get the field

## A. OpenAI `prompt_cache_retention`

**A1. Exact model list and ZDR wording** — both [V], from https://developers.openai.com/api/docs/guides/prompt-caching.md
- Model list, quoted: "Extended retention is supported by `gpt-5.5`, `gpt-5.5-pro`, `gpt-5.4`, `gpt-5.2`, `gpt-5.1-codex-max`, `gpt-5.1`, `gpt-5.1-codex`, `gpt-5.1-codex-mini`, `gpt-5.1-chat-latest`, `gpt-5`, `gpt-5-codex`, and `gpt-4.1`." The summary table adds that GPT-5.5 and GPT-5.5 Pro accept **`"24h"` only**.
- ZDR, quoted: "For models that support both `in_memory` and `24h`, the default depends on your organization's data retention policy: Organizations *without* Zero Data Retention enabled default to `24h`. Organizations *with* Zero Data Retention enabled default to `in_memory`."
- From https://developers.openai.com/api/docs/guides/your-data.md: "When Zero Data Retention is not enabled for an organization, all queries use extended prompt caching for all supported models." The same page says "For `gpt-5.5` and `gpt-5.5-pro`, setting `prompt_cache_retention` to `in_memory` returns an error." That is the only documented error case.
- That page also says prompt caching "may store encrypted key/value tensors in GPU-local storage as application state… not retained after the 24-hour expiration."
- What a ZDR org gets if it explicitly sends `"24h"` is **not documented** [UNKNOWN]. The docs say only that the default changes. My inference [I]: the request is allowed, since gpt-5.5 *only* accepts 24h and ZDR orgs can use gpt-5.5, and the result is data held as "application state".

**A2. Per-model verdict for our catalog.** The model pages (developers.openai.com/api/docs/models/<id>.md) list pricing including "Cache writes" but no retention field, so the guide's list is the authority.

| Model | Extended 24h listed? | Recommendation |
|---|---|---|
| gpt-5.2 | yes [V] | SEND `"24h"` (no-op for a non-ZDR org) |
| gpt-5.2-codex | not listed | DON'T SEND |
| gpt-5.2-pro | not listed | DON'T SEND |
| gpt-5.3-codex | not on OpenAI's list; **is** on Azure's list [V] | DON'T SEND on OpenAI; SEND on Azure |
| gpt-5.4 | yes [V] | SEND |
| gpt-5.4-mini | not listed | DON'T SEND |
| gpt-5.4-pro | not listed | DON'T SEND |
| gpt-5.5 | yes, 24h only [V] | SEND (or omit; it's already effectively 24h) |
| gpt-6-astra / luna / sol / terra | not applicable | DON'T SEND `prompt_cache_retention` (see below) |

- **gpt-6-\* family:** the guide's "GPT-5.6 and later" column covers later families, and the gpt-6-astra migration guide in the openai/codex repo says to "replace `prompt_cache_retention` with `prompt_cache_options.ttl` set to `"30m"`" [V].
  - `prompt_cache_options.ttl` has one value, `"30m"`, and it is also the default [V]. There is no 1h or 24h value.
  - Sending `ttl:"30m"` therefore buys nothing; **DON'T SEND**.
  - The gpt-6-sol page shows a cache-write price of $2.50/M against $2/M input, i.e. 1.25× [V].
  - The gpt-6-terra model page returned "Not found" [V], so check the model ID.
- Azure states that `prompt_cache_retention` is "deprecated" and "doesn't apply" on 5.6+ [V]. It also says sending `prompt_cache_options` / `prompt_cache_breakpoint` to **pre-5.6** models returns a **400** [V].

**A3. What an unsupported model returns.** I found no public evidence either way [UNKNOWN]. The only documented error is `in_memory` on gpt-5.5 / 5.5-pro. Since the reverse field (`prompt_cache_options` on a pre-5.6 model) is documented to return a 400, assume a 400 is possible and gate the field per model [I].

## B. GitHub Copilot Anthropic `/v1/messages` — DON'T SEND `ttl`
- [V] The official client, https://raw.githubusercontent.com/microsoft/vscode-copilot-chat/main/src/platform/endpoint/node/messagesApi.ts, sets `cache_control = { type: 'ephemeral' }` with **no `ttl`** anywhere: on the last tool, the last system block, tool_result blocks and the last message block (lines ~322/419/474/535/542). There is no `ttl` string in the file, and it respects the max of 4 breakpoints.
- I found no evidence on whether the endpoint accepts or rejects `ttl:"1h"` [UNKNOWN]. The Copilot CLI is not open source. Mirror the official client: 5m only.

## C. Azure OpenAI — SEND `"24h"` on the listed models
- [V] Quoted from https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/prompt-caching: "Extended prompt cache retention is available for the following models: gpt-5.5 gpt-5.4 gpt-5.3-codex gpt-5.2 gpt-5.1-codex-max gpt-5.1 gpt-5.1-codex gpt-5.1-codex-mini gpt-5.1-chat gpt-5 gpt-5-codex gpt-4.1"
- [V] On defaults, quoted: "For gpt-5.4 and older models, if you don't specify a retention policy, the default is in_memory. Allowed values are in_memory and 24h. For gpt-5.5, extended retention is enabled by default."
  - Unlike OpenAI's platform, Azure's default for ≤5.4 is **in_memory**, so sending `24h` there is a real gain.
- [V] Pricing, quoted: "Prompt cache pricing is the same for both retention policies."
- The page states **no required api-version** [UNKNOWN]. Its examples use the v1 Responses API shape.
