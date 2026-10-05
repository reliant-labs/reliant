# Live model probe results

Run: 2026-10-04 ~12:45–12:56 EDT, dev stack. Text matrix 325 cells, images 4 cells.
Built from `tools/reliant-dev/models_probe*.go`; requests go through
`handlers.ProbeLLMCall` (`handlers/llm_probe.go`) -> `resolveLLMCall` ->
`drivers.GetDriver` -> `prepareHistoryForLLM` -> `driver.StreamResponse`, i.e. production's path.
Raw output: `research/probe-runs/2026-10-04-text-matrix-run1.json`, `research/probe-runs/2026-10-04-images-run1.json`.

## Provider set

- **Probed (credential present):** `anthropic` (Claude OAuth), `codex`, `antigravity` (expired token, refreshed by the driver's own CAS refresh), `openrouter` (`OPENROUTER_API_KEY`).
- **Not testable (no credential):** `openai`, `gemini`, `vertexai`, `copilot`, `xai`, `mock`; every `vertex-claude-*` and `grok-*` model, and the vertex/gemini-direct/openai-direct mappings of shared models.
- **`reliant` gateway: not testable.** The owner user has no `reliant` key. The 16 `rlnt_` keys in `api_keys` belong to other users and all return **401** against the dev admin-server (`127.0.0.1:8090/v1/chat/completions`) — stale or unrecognized by the running control-plane. So audit #3 (reliant drops `reasoning_effort`) is **not confirmed or refuted** live. The probe accepts a valid key via `RELIANT_PROBE_API_KEY`.

## Counts

| Run | Cells | Pass | Fail |
|---|---|---|---|
| Text (basic default+levels, temp 0 / 0.7, tool round-trip, 7 tags) | 325 | 310 | 15 |
| Images (`--images`, 1 image each) | 4 | 4 | 0 |

Failures by class (corrected classifier): catalog-mapping 13 cells (F1 ×6, F2 ×7), driver-bug 2 (F3), credential 0, provider-side 0. (The first run's classifier labelled F2 "driver-bug"; a bad model id is now classified catalog-mapping.)

## Failures

| # | Class | Cells | Error excerpt | Suspected location |
|---|---|---|---|---|
| F1 | catalog-mapping | `gemini-3-pro-preview@openrouter` ×6 (default, low, high, temp 0, temp 0.7, tool) | `404 No endpoints found for google/gemini-3-pro-preview` | `internal/llm/models/definitions/models.yaml` openrouter `api_model` for gemini-3-pro-preview (OpenRouter no longer serves it; use the 3.1-pro-preview id, which passes) |
| F2 | catalog-mapping | `gpt-6-terra@openrouter` ×7 (default + low…max, tool) | `400 openai/gpt-6-terra is not a valid model ID` (POST openrouter.ai/api/v1/responses) | models.yaml openrouter mapping for gpt-6-terra; `gpt-6-sol/luna/astra` map fine on OpenRouter, terra does not exist there |
| F3 | driver-bug (catalog-level) | `gpt-5.6-sol@codex level=ultra`, `gpt-5.6-terra@codex level=ultra` | `400 Invalid value: 'ultra'. Supported values are: none, minimal, low, medium, high, xhigh, max` (param `reasoning.effort`) | Catalog advertises `ultra` for these models; Codex rejects it. `codex/envelope_gpt56.go:129-141` passes `ultra` through; either map ultra→max for codex or drop `ultra` from these models' `thinking_levels`. (`gpt-5.6-luna` and `gpt-6-astra@codex` don't list ultra and pass at `max`.) |

No credential or provider-side (429/5xx/timeout) failures occurred.

## Everything else that passed — and what it says about the audit

- **Tool round-trip + history replay: 49/51 pass** (the two fails are F1/F2 above). This includes all Anthropic thinking models with signed thinking replayed, and every Gemini 3.x model on OpenRouter/antigravity, where `ThoughtSignature` was present on the tool call (**toolsig** observed on `gemini-2.5-flash-lite`, `3-flash`, `3.1-pro`, `3.5/3.6/3.7/3.8-flash`, antigravity `3.8-flash`) and replay succeeded. No thought-signature / tool-schema / replay bug reproduced in the probed providers.
- **Tags** (resolved for providers anthropic/antigravity/codex/openrouter), all pass: powerful→`claude-5.1-fable@anthropic` xhigh; flagship→`claude-5.5-opus@anthropic` xhigh; moderate→`claude-5.5-sonnet@anthropic` medium; fast→`gemini-3.5-flash@openrouter` low; cheap and meta→`claude-4.5-haiku@anthropic` (no level); reasoning→`claude-5.5-opus@anthropic` xhigh.
- **Temperature 0 and 0.7 pass** on 40 of 42 temperature cells (the 2 failures are F1). Whether the driver actually *sent* the value is not observable at this layer ("unknown"); adaptive Claude (4.8/5.x) models have no temp cells because the catalog already marks them `temperature_mode: omit` for the probe (see audit #6). Audit #2 (vertex Claude adaptive + temperature → 400) is untestable (no vertex credential).
- **Audit #13 (OpenRouter `max`)**: `gpt-6-sol/luna/astra@openrouter` at `max` all **pass**. For astra/luna output tokens rise with effort (astra 6→19→24 at high→xhigh→max, luna 6/16/6/17/19), so `max` is accepted and appears honored; `gpt-6-sol` stays at 6 tokens at every level (no observable effect on a PONG prompt — unknown).
- **Audit #3 (reliant effort ignored)**: not testable, see above.
- **Audit #9 (grok-4@xai)**: not testable (no xai credential); with a key it would be reached only via the picker, per the audit.
- **Effort effects on direct Anthropic**: claude-4.5-sonnet output tokens vary 50–80 across levels (budget thinking enabled); adaptive models (4.8+/5.x) emit the same 5 tokens at all levels — a PONG prompt does not reveal effort differences there. **No cell returned readable thinking text** (all 0 chars), including Claude with thinking enabled; signed-thinking presence was only observed for `claude-4.5/4.6` on the tool cells (Anthropic returns summarized/omitted thinking text). Treat thinking-level verification as "accepted by the API", not "demonstrably changes reasoning".
- **Codex usage reporting**: every `@codex` cell reports `output_tokens = 0` although text came back — the driver's usage is not populated on the streaming path (cost/telemetry gap, not an answer failure). Suspect `internal/llm/drivers/codex` usage mapping.

## Images (separate run, `--images`)

| Cell | Result |
|---|---|
| `gpt-image-2@codex` | pass (14.2s) |
| `gpt-image-2.5-sunburst@codex` | pass (14.1s) |
| `gpt-image-2.5-flare@codex` | pass (15.7s) |
| `gemini-3.1-flash-image@antigravity` | pass (7.9s) |

Image models behind openrouter/openai/gemini-direct were not probed (no catalog image mapping on a credentialed provider).

## Thinking levels: do they work?

Run 2026-10-04 ~13:10 EDT, `--reasoning` pass, 132 cells (every reasoning-capable model x credentialed provider at default / lowest / highest declared level), all calls succeeded. Prompt (`reasoningPrompt`, `models_probe.go`): sum of integers 1..1000 divisible by exactly one of 7 or 11, reply with only the number; answer 104104. Visible output is ~6 tokens, so any larger output-token count is hidden reasoning. Raw: `research/probe-runs/2026-10-04-reasoning-run1.json`. Wire captures (plaintext of the actual provider streams, credentials scrubbed) for one model per driver: `research/probe-runs/wire/<model>_<provider>/` plus `wire-*.json`. Single sample per cell (no retries), so single-model deltas are noisy; the medians below are across models.

### Per-provider verdict

| Provider | Level changes reasoning? (high vs low, median across models) | Thinking TEXT surfaced to us? | Signature / redacted | Why (evidence) |
|---|---|---|---|---|
| anthropic (Claude OAuth) | **Yes.** Output tokens x1.44, latency x1.27; higher in 11/11 models. e.g. claude-5.5-opus 137 -> 191, 4.6-opus 194 -> 412 | **No.** 0 of 33 cells | signature on 29/33 (absent on 5.1-fable default+low, 5.5-sonnet default+low: no thinking block returned at low effort); no redacted blocks | Wire: every thinking block is `thinking_delta` with `"thinking":""` (+ `estimated_tokens` on 5.5-opus), then `signature_delta`. `output_tokens_details.thinking_tokens` is 129 of 133 output tokens, so the server DID reason; it just sends no text. See D1 |
| codex | **Yes.** x1.52 tokens, x1.48 latency; 5/5 models. Wire `reasoning_tokens`: gpt-5.6-sol low 94 / medium 142 / max 191 | **No** (0/15), although gpt-5.5 sends it on the wire | encrypted reasoning item returned on wire (not surfaced as signature to the probe) | gpt-5.5 requests `summary:"concise"` and the stream carries `response.reasoning_summary_text.delta` (e.g. "**Calculating final sum**") but the driver emits nothing. gpt-5.6/6-astra send `summary:null` by design (`envelope_gpt56.go:114-120`), so no summary exists. See D2 |
| openrouter / gpt-* | **Yes.** x2.02 tokens, 10/10 models up (e.g. gpt-6-luna 112 -> 203, gpt-5.4-mini 117 -> 397) | **No** (0) | none | Request carries `reasoning:{effort,summary:"concise"}`; response `reasoning_tokens:169`. Goes through the openai Responses parser, same drop as codex. See D2 |
| openrouter / gemini-* | **Yes.** x2.21 tokens, x1.90 latency; 5/6 up (3.8-flash low was WRONG at 348 tokens, 3.1-pro 788 -> 1759) | **No** (0), though OpenRouter streams `reasoning` text and `reasoning_details` | none (thought signatures only handled for tool calls) | Wire: `reasoning:"**Summing Multiples**..."` deltas, `reasoning_tokens:2003`. Driver reads `reasoning_details` only for signatures. See D3 |
| openrouter / claude-* | **No.** x1.00 tokens, x0.98 latency; higher in only 4/11 | **No** (0) | none | Wire: the request body has NO `reasoning` / `thinking` field at all, for any level. The Anthropic-via-OpenRouter path (`streamWithCacheControl`) never sends effort; the thinking that happens is OpenRouter's default. The setting is a no-op. See D4 |
| antigravity (gemini-3.8-flash) | **No / not visibly.** Only 1 model; tokens x1.00, latency x1.60. Wire `thoughtsTokenCount`: -low 1474, -medium 1280, -high 1353 | **Yes.** 522-748 chars in 3/3 cells, streamed as `thought:true` parts and parsed | `thoughtSignature` present 3/3 | The model-id suffix (`-low/-medium/-high`) did change per level on the wire, but thought tokens did not order with it on this puzzle. See D5 for the usage defect |
| reliant, openai, gemini, vertexai, copilot, xai | not testable (no credential) | | | |

Correctness did not track level (anthropic 32/33 correct, openrouter 73/81, codex 14/15, antigravity 3/3); the 10 wrong answers are scattered (4.5-opus@openrouter returns a bare 6-token guess at every level; 5.5-sonnet@openrouter wrong at all three), consistent with the puzzle being easy for most models and noise otherwise. Level effect is therefore measured through tokens and latency, not accuracy.

### Is thinking text surfaced at all?

Only from antigravity. For the production logs' `thinking_len=0 has_signature=true` on Claude OAuth turns: **summaries do not come through, and our parser is not what drops them.** Captured stream (`wire/claude-5.5-opus_anthropic/002-*.log`, decoded):

```
content_block_start  {"type":"thinking","thinking":"","signature":""}
content_block_delta  {"type":"thinking_delta","thinking":"","estimated_tokens":50}
content_block_delta  {"type":"thinking_delta","thinking":"","estimated_tokens":null}
content_block_delta  {"type":"signature_delta","signature":"CAQSuAYK..."}
...
message_delta usage  {"output_tokens":133,"output_tokens_details":{"thinking_tokens":129}}
```

The request carried `"thinking":{"display":"updates","type":"adaptive"}` and the `thinking-display-updates-2026-08-18` beta, as `claude_code.go:697-716` intends. With `display:"updates"` Anthropic sends token-count updates (`estimated_tokens`), not text. The non-adaptive path (claude-4.5-sonnet, `thinking:{type:"enabled",budget_tokens:1024..16000}`) sends the `redact-thinking-2026-02-12` beta (`claude_code_betas.go:14,47` `betaSonnet5`/`betaDefault`) and likewise returns empty `thinking_delta`s with `thinking_tokens:595`. The parser at `claude_code.go:634` skips empty `thinking_delta`s, which is correct since there is no text to lose. `estimated_tokens` is ignored (no struct field in the SDK delta). So the signature persists, the text is empty because Anthropic withheld it. Whether `display:"summarized"` (the SDK's own constant, `anthropic-sdk-go message.go:9207`) would return text under OAuth was **not tested**; it needs a driver change, which I did not make.

### Driver-side defects (reported, not fixed)

- **D1 Claude OAuth: no thinking text is requested or received.** `drivers/anthropic/claude_code.go:708-714` sets `display:"updates"` (token counts only); non-adaptive models send `redact-thinking-2026-02-12` (`claude_code_betas.go:14,47`). Net: users never see Claude reasoning, and the UI `thinking_len=0` blocks are expected. If visible summaries are wanted, try `display:"summarized"` and drop `redact-thinking` and measure; this departs from the Claude Code fingerprint.
- **D2 Codex and OpenAI Responses reasoning summaries are dropped.** `drivers/codex/driver.go:824-827` handles only `ResponseReasoningSummaryPartAddedEvent`, whose `Part.Text` is `""` on the wire (`{"type":"summary_text","text":""}`), and the text arrives in `response.reasoning_summary_text.delta` events that are never handled (`ResponseReasoningSummaryTextDeltaEvent` appears nowhere in `internal/llm`). Additionally the emitted event sets `Content:` not `Thinking:` (`codex/driver.go:826`), and `handleThinkingDelta` reads `event.Thinking` (`call_llm.go:2665-2668`), so even a non-empty part would be dropped. Same pattern in `drivers/openai/driver.go:972-975`. The probe confirmed: gpt-5.5@codex had `thinking_start/stop` events, 4 summary deltas on the wire, `thinking_chars=0`.
- **D3 OpenRouter Gemini/Claude reasoning text dropped.** `drivers/openrouter/streaming.go:472-487,530,571` reads `reasoning_details` only for `reasoning.encrypted` signatures; the plain `reasoning` delta string and `reasoning.text` details are never emitted as thinking.
- **D4 OpenRouter Anthropic path ignores thinking level.** `drivers/openrouter/streaming.go:22-296` (`streamWithCacheControl`) has no reasoning config; only `streamWithGeminiSupport` (`:339-345`) does. Captured request bodies for `claude-5.5-opus@openrouter` at default / low / xhigh contain no `reasoning` or `thinking` key; measured effect x1.00.
- **D5 Antigravity usage excludes thought tokens.** `drivers/antigravity/driver.go:457-459` maps `OutputTokens: CandidatesTokenCount`, but the wire reports `candidatesTokenCount:6, thoughtsTokenCount:1280` (`totalTokenCount:1343`). Thinking is billed but invisible to us (probe saw `output_tokens=6` while 1280 thought tokens were spent). Same class as the codex usage mapping already being fixed.
- **D6 Antigravity level control is unproven.** `antigravity/driver.go:361-366` sends `thinkingBudget:-1` for every effort and varies only the model-id suffix; thought tokens on the wire were 1474 (low), 1280 (medium), 1353 (high), i.e. not monotonic. Needs more than one sample before it is called a defect.
- Codex also reports `reasoning_tokens` on the wire (`envelope_gpt56.go:284` logs it) but `llm.TokenUsage` has no reasoning-token field, so no driver surfaces it; the probe could only infer reasoning from output tokens, which for codex cells was 0 before the usage fix and is now populated.

### Re-run

```
cd reliant
go build -o /tmp/scratchpad/reliant-dev ./tools/reliant-dev
/tmp/scratchpad/reliant-dev models probe --skip-text --reasoning --concurrency 4 \
  --json research/probe-runs/<date>-reasoning.json          # 132 cells, ~4 min
/tmp/scratchpad/reliant-dev models probe --skip-text --reasoning --filter '^gpt-5.5@codex$' \
  --wire-dump research/probe-runs/wire/gpt-5.5_codex       # raw provider bytes, one dir per run
```

`--wire-dump` replaces TLS dialing on the process-wide shared transport so net/http falls back to HTTP/1.1 and each connection is logged in plaintext. Authorization / x-api-key / chatgpt-account-id / cookie headers are redacted, and so are account fingerprints in request bodies (device_id, account_uuid, organization_uuid, account_email, installation_id), in both escaped and plain JSON form (`scrubWire`, pinned by `TestScrubWireRedactsCredentialsAndFingerprints`). The captures under `research/probe-runs/wire/` predate that body redaction and were scrubbed after the fact; in the anthropic captures the scrub removed the fingerprint keys along with their values from `metadata.user_id`. Anthropic bodies are gzip: decode by joining the `<<` segments, de-chunking, and `zlib.decompressobj(16+zlib.MAX_WBITS)`.

## Final state (2026-10-04, after every fix in this change)

- **Text matrix:** 310/310 pass (`probe-runs/2026-10-04-text-matrix-final2.json`). Compared with run 1 (310/325), the only difference is the 15 cells whose catalog entries were removed as unservable: `gpt-6-terra@openrouter`, `gemini-3-pro-preview@openrouter`, and `ultra` on `gpt-5.6-sol` / `gpt-5.6-terra` @codex. No cell regressed.
- **OpenRouter Claude ids:** the corrected dotted ids (`anthropic/claude-opus-5.5`, `claude-sonnet-5.5`, `claude-fable-5.1`) pass live, including tool round-trips with reasoning now enabled.
- **Codex usage:** cells reporting output tokens went from 0/36 to 34/34.
- **Thinking fixes (D2–D5):** fixed and re-probed (`probe-runs/2026-10-04-reasoning-after-fixes.json`).
  - Thinking text now arrives from openrouter claude and gemini, codex gpt-5.5, and antigravity.
  - OpenRouter Claude now responds to the level setting: 137 / 149 / 183 output tokens at low / default / xhigh, single sample.
- **Still open:**
  - **D1:** Claude OAuth sends `display:"updates"`, and Anthropic returns no thinking text for it. The model does reason: thinking tokens are spent and signatures arrive.
  - **D6:** antigravity's level ordering is unproven.
- **Never exercised live (no credential):** openai, gemini, vertexai, copilot, xai, and the reliant gateway via admin-server. The vertexai and reliant changes are verified at the wire by httptest only. The reliant change is also verified live directly against dev LiteLLM.

## Re-run

```
cd reliant
go build -o /tmp/scratchpad/reliant-dev ./tools/reliant-dev
/tmp/scratchpad/reliant-dev models probe --dry-run                       # print matrix
/tmp/scratchpad/reliant-dev models probe --concurrency 4 --json out.json # full text matrix (~5 min)
/tmp/scratchpad/reliant-dev models probe --filter '^gpt-6-.*@openrouter' # subset
/tmp/scratchpad/reliant-dev models probe --images --skip-text --json img.json
```

Env: `OPENROUTER_API_KEY` (and optionally `OPENAI_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `RELIANT_PROBE_API_KEY` with a valid `rlat_`/`rlnt_` key) add providers; `--db-url`/`--user` override the dev DB and user.

## GitHub Copilot (2026-10-04, individual plan, `gho_` credential)

### Thinking on Copilot Claude: root cause and fix

Wire dump (`research/probe-runs/wire/copilot_claude_before/`) of `claude-5-sonnet@copilot` at low/default/xhigh: every request carried `"thinking":{"type":"adaptive"}` and **no `output_config`**. The plain `anthropic` client only sets `output_config.effort` on the Claude Code (OAuth) path (`claude_code.go:735`), so Copilot never saw the level. Output tokens were flat (159/173/165).

GitHub's own client sends the level as `output_config:{effort}` next to `thinking:{type:"adaptive"}` (microsoft/vscode-copilot-chat `src/platform/endpoint/node/messagesApi.ts`, `createMessagesRequestBody`: `...(effort ? { output_config: { effort } } : {})`, gated on the model's `supports.reasoning_effort` in `/models`). Copilot `/models` lists `reasoning_effort: low..max` and `adaptive_thinking: true` for sonnet-5 / 5.5, and no reasoning_effort for haiku-4.5 (budget tier).

Fix: `copilot/driver.go` `newAnthropicDialectAt` injects `output_config.effort` for `thinking_mode: adaptive` models only (`copilotClaudeEffort`). Budget-tier haiku-4.5 sends none. Wire captures after the fix show `output_config":{"effort":"xhigh"|"medium"|"low"}`.

Output tokens, `claude-5-sonnet@copilot`, three runs (low / default(medium) / xhigh):

| | low | default | xhigh |
|---|---|---|---|
| before | 159 | 173 | 165 |
| after run 1 | 148 | 187 | 312 |
| after run 2 | 167 | 176 | 408 |
| after run 3 | 162 | 176 | 207 |
| after run 4 | 152 | 173 | 256 |

`claude-5.5-sonnet@copilot`: 173 / 173 / 210 and 173 / 173 / 264 (low is not below default here; xhigh is above both). Claude thinking text is withheld by Copilot, as with OAuth (`think_events=0`, signature present), so output tokens are the only signal. Effect is real but modest on this puzzle and noisy at one sample per cell.

### Mappings added (all `policy=enabled`, or no policy block for gpt-5.3-codex, with `model_picker_enabled=true`)

`claude-5.5-sonnet`→`claude-sonnet-5.5`, `gemini-3.7-flash`, `gemini-3.8-flash`, `gpt-5.3-codex`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-6-luna` (api_model spellings equal the catalog ids; confirmed against live `/models`). Not mapped (policy=disabled on this plan): claude-opus-5.5/5/4.8, claude-fable-5.1/5, gpt-5.5, gpt-5.6-sol, gpt-6-sol, gpt-6.1-sol, gpt-6-astra.

Every declared `thinking_levels` is within what Copilot reports for that model, so no clamp was needed (pinned by `TestCopilotMappingsAreServableOnTheProbedAccount`). Copilot serves gpt-5.3-codex, gpt-5.4-mini, gpt-5.6-*, gpt-6-* on `/responses` only, so the copilot OpenAI dialect now keeps the catalog `responses` preference for `gpt-*` and forces chat_completions for everything else (`copilotOpenAIEndpoint`).

### Live matrix

Text: 74/74 pass (`probe-runs/2026-10-04-copilot-text.json`): haiku-4.5, claude-5-sonnet, claude-5.5-sonnet, gemini-3.7/3.8-flash, gpt-5-mini, gpt-5.3-codex, gpt-5.4, gpt-5.4-mini, gpt-5.6-luna/terra (incl. `max`), gpt-6-luna (incl. `max`), each at every declared level, temperature cells where sent, and a tool round-trip.
Reasoning: 33/33 pass (`probe-runs/2026-10-04-copilot-reasoning.json`). Output tokens low/default/top:

| cell | low | default | top |
|---|---|---|---|
| claude-5-sonnet | 152 | 173 | 256 (xhigh) |
| claude-5.5-sonnet | 173 | 173 | 210 (xhigh) |
| gemini-3.7-flash | 426 | 669 | 1296 (high) |
| gemini-3.8-flash | 1368 | 1292 | 1337 (high), flat |
| gpt-5-mini | 471 | 681 | 713 (high) |
| gpt-5.3-codex | 181 | 259 | 311 (xhigh) |
| gpt-5.4 | 197 | 179 | 271 (xhigh) |
| gpt-5.4-mini | 180 | 219 | 495 (xhigh) |
| gpt-5.6-luna | 102 | 131 | 190 (max) |
| gpt-5.6-terra | 93 | 95 | 167 (max) |
| gpt-6-luna | 114 | 184 | 228 (max) |

`gemini-3.8-flash@copilot` is flat across levels (single sample); not diagnosed. gpt-5.6-* report no reasoning-token detail (0 THINK) but output scales with level.

### Per-account availability

`registry.AvailableModelsFor` -> `CopilotClient.GetAvailableModels` sets `Enabled` from `/models` policy (cached 5 min per token). `CatalogService.ListModels` (`catalog.go:69-107`) hides a `{model, driver}` whose `Enabled` is false; `ListAvailableModels` RPC returns the flag. Live against this account all 12 mapped models are `enabled=true`; the 10 policy=disabled models are unmapped so cannot appear. Gap (not fixed, outside my files): `resolveLLMCall` / tag resolution do not consult `Enabled`; a tag can resolve to a model the account later disables (it would 400 upstream). That only occurs if a user disables a mapped model in GitHub settings.

## Wave 5 follow-ups (2026-10-04): Claude OAuth thinking text, gemini-3.8-flash levels

### Part A: Claude OAuth `thinking.display:"summarized"` (fixes D1)

Live over the OAuth path with `--wire-dump` (`wire/claude-5.5-{sonnet,opus}_anthropic_summarized`). Request differs from before ONLY in `thinking.display` (`updates` -> `summarized`); headers, betas, system blocks and metadata untouched.

| Check | claude-5.5-opus (low / default / xhigh) | claude-5.5-sonnet (low / default / xhigh) |
|---|---|---|
| HTTP status | 200 x3 | 200 x3 |
| Readable thinking text | **Yes**: think_events 16 / 20 / 50 (was 0 of 33 cells) | Only when it thinks: xhigh had a signed thinking block with 0 text events; low/default return no thinking block (unchanged from before: sonnet skips thinking at low effort) |
| Signature present | yes x3 | xhigh yes; low/default no (same as before) |
| Output tokens | 137 / 149 / 201 | 173 / 173 / 205 |
| Rate-limit / policy headers | `unified-status: allowed`, `representative-claim: five_hour`, `overage-status: rejected (out_of_credits)`, `fallback-percentage: 0.5`, identical to the `display:"updates"` capture | same |

Decision: ship `summarized` for the profiles that send `display` (fable-5.1, opus-5.5). No 4xx, no header or policy change, text now arrives with signatures intact. Open caveat: sonnet-5.5 at xhigh produced a signature but no summary text in the one sample, so text is not guaranteed on every turn.

Replay: the unit tests that pin thinking-block replay pass (`thinking_replay_test.go`, `redacted_thinking_replay_test.go`). The live probe tool-roundtrip cell passed on claude-5.5-opus, claude-5.5-sonnet, claude-5.1-fable and claude-4.6-opus, BUT the probe's tool prompt is trivial and none of those models emitted a thinking block before the tool call (wire: assistant turn contains only `tool_use`, or `text`+`tool_use`), so a live replay of a thinking block on turn 2 was NOT exercised. Not verified live.

Code: `claude_code.go` uses `anthropic.ThinkingConfigAdaptiveDisplaySummarized`; `thinkingDisplayUpdates` removed; `claude_code_opus55_test.go` and `claude_code_fable51_test.go` now assert `"display":"summarized"` (failed before the change).

### Part B: gemini-3.8-flash flat across levels

Samples are probe cells; columns are output tokens (including thoughts where reported) / thought tokens.

**Probe bug found first.** Every earlier `gemini-3.8-flash@copilot` cell was actually served by antigravity: `copilot/models.go` registered a hard-coded 3-model allowlist, so `CanDriverUseModel(copilot, gemini-3.8-flash)` was false and the resolver logged `explicit driver does not support model` and fell back. The earlier "Copilot flat 1368/1292/1337" numbers were antigravity numbers. Fixed by `RegisterCatalogDriver(Family)` in `copilot/models.go` (the catalog is the source of truth). Test `TestCopilotServesEveryCatalogMapping` fails before (lists claude-5.5-sonnet, claude-5-sonnet, gpt-6-luna, gpt-5.6-terra, ... as unservable) and passes after.

**Antigravity**, hard prompt (digit-sum/prime/not-square count over 1..3000), thought tokens:

| Variant sent | low (3-5 samples) | medium | high |
|---|---|---|---|
| BEFORE: suffix id + `thinkingBudget:-1` | 4127, 2965, 3213 | 2822, 4170, 3929 | 4426, 2606, 2101 |
| suffix id, budget removed | 1195, 774, 1193, 1385, 1328 | 2112, 1209, 961, 1147, 1657 | 1593, 1692, 1469, 1155, 674 |
| base id + `thinkingLevel` | 404 Not Found (base id not served; suffix ids are real) | | |
| suffix id + `thinkingLevel`, no budget | 675, 935, 762, 1059, 205 | 1356, 1185, 1290, 1336, 1244 | 4031, 3607, 2183, 5212, 5585 |
| AFTER fix (driver, hard prompt) | 1059, 853, 934 | 2169, 1183, 1118 | 7360, 3574, 3664 |

Finding: the `-low/-medium/-high` model ids are real, but `thinkingBudget:-1` (dynamic) overrides the level, which is why levels were flat. Sending `thinkingConfig.thinkingLevel` with no budget makes thought tokens order low < medium < high (medians about 900 / 1250 / 4000).

**Copilot** (`/chat/completions`, fixed routing; wire: `reasoning_effort` is `low|medium|high`, correct per level). Thought tokens are NOT reported (`completion_tokens` 6, or 2 on the hard prompt), so latency is the signal:

| Prompt | low | medium | high |
|---|---|---|---|
| easy (3 samples) | 3.4s, 3.0s, 2.7s | 4.7s, 4.7s, 3.5s | 7.8s, 8.3s, 8.5s |
| hard (4 samples) | 12.9, 15.0, 10.9, 10.5s | 16.4, 14.2, 19.4, 15.0s | 26.7, 36.4, 26.5, 29.6s |

Decision: Copilot honors `reasoning_effort` (latency rises about 2.5x low to high); nothing to change in the driver or catalog. The only Copilot defect was the allowlist. Antigravity fix: `agywire.ThinkingConfig` now carries `thinkingLevel` and an optional `thinkingBudget`; for models with effort variants (gemini-3.8-flash) the level is sent and the budget omitted, other antigravity models keep the capture's `-1`. Test `TestBuildEnvelopeThinkingLevelReplacesBudget` failed before, passes after. Catalog unchanged; no per-provider levels needed.
