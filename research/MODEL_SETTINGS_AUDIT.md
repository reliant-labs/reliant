# Model settings audit: UI → wire, per driver

Static audit. No source code was edited. "Code read" means verified by reading code. "Test" means verified by running a test. Tests run (all pass, which also means none of them cover the defects below):
`go test ./internal/llm/drivers/ -run 'Temperature|CatalogProviderMapping'`, `go test ./internal/llm/models/`, and `go test` on the openai, anthropic, codex, openrouter, gemini and vertexai driver packages.

## Summary of defects

| # | Sev | Setting | Driver / surface | Evidence | One-line fix |
|---|-----|---------|------------------|----------|--------------|
| 1 | **High** | thinking_level (every level) | **vertexai Claude** (all `vertex-claude-*`, plus `claude-5.5-opus` / `claude-5.1-fable` @vertexai) | `drivers/vertexai/claude.go:91-100` (`claudeRequest` has no `thinking` or `output_config` field); `buildClaudeRequest` at `:208-260` never reads `ReasoningEffort` | Port anthropic `getThinkingConfig`/`getOutputConfig` (adaptive vs budget) into the vertex Claude request |
| 2 | **High** | temperature | **vertexai Claude, adaptive models** | `vertexai/claude.go:216-217` sends any `Temperature`. The anthropic driver says adaptive models 400 when temperature is sent at all (`anthropic/base.go:63-66`). Adaptive models have no `temperature_mode: omit` in models.yaml, so the resolver lets it through (`resolver.go:286-289`) | Add `temperature_mode: omit` to adaptive Claude models, or drop temperature in vertex claude.go |
| 3 | **High** | thinking_level (every level) | **reliant gateway** (25 provider mappings) | `drivers/reliant/driver.go:444-475` (`preparedParams` sets only temperature + MaxTokens). There is no `reasoning_effort` and the comment says "LiteLLM handles reasoning". `grep -i effort` in `drivers/reliant` finds nothing | Forward `reasoning_effort` (or `thinking`) in the chat-completions params |
| 4 | **High** | model.temperature / model.thinking_level / model.compaction_threshold (model-page settings) | **Every builtin workflow except agent, auditing (main node), parallel-compete (implementer)** | `implement-review.yaml:63-74,103-104`, `structured-agent.yaml:25-44,183-185`, `gsd.yaml:59-65,…`, `bmad-lite`, `migrate`, `discovery-relay`, `one-ring` use top-level `inputs.temperature`/`inputs.thinking_level`. Proto `ModelSelector` holds only id/tags/providers (`proto/reliant/v1/workflow_v2.proto:91-98`), so the model-page overrides are discarded (`call_llm.go:845-851`) | Change those nodes to `has(inputs.model.X) ? inputs.model.X : inputs.X`, like agent.yaml:197-200 |
| 5 | Med | temperature = 0 | all drivers, agent.yaml path | `call_llm.go:907-910` (`!= 0.0` ⇒ unset). `agent.yaml:197` also uses `0.0` as the "absent" sentinel. The UI slider allows 0 (`ModelSettingsPage.tsx:469-481`) | Use a pointer/`has()` sentinel (e.g. pass -1 or omit the arg) instead of 0.0 |
| 6 | Med | temperature | anthropic (API key + Claude OAuth) and copilot-Claude (anthropic dialect) | `anthropic/base.go:63-66`: never sent; `grep Temperature drivers/anthropic` finds no non-test hits. UI shows the slider for every model (`ModelSettingsPage.tsx:463-490`), with no capability check | Hide/disable the slider when the selected model+driver ignores temperature (expose temperature_mode in ModelInfo) |
| 7 | Med | temperature | omit-mode models (all GPT-5.x/6, codex) | `resolver.go:290-291` drops it silently. The UI still offers the slider (same lines) | Same as #6 |
| 8 | Med | default temperature 1.0 forced | implement-review, structured-agent, gsd, bmad-lite, migrate, discovery-relay, one-ring | `implement-review.yaml:63-67`: `default: 1.0`. This explicit 1.0 bypasses the model default (`llm_request.go` `resolveLLMCall` uses `DefaultTemperature` only when nil) and hits #2 on vertex adaptive Claude | Default to 0/unset and let the model default apply |
| 9 | Med | model `grok-4` (visibility: user) and `grok-3-mini-beta`, tags powerful/flagship/moderate/fast/cheap tail | **xai** | No blank import of `drivers/xai` in `resolver.go:20-29`. The package has only `models.go`, with no client. The test exemption says so explicitly: `catalog_agreement_test.go:37`. The UI offers an xAI key form (`CombinedGeneralSettings.tsx:184-189`), so an xai user sees grok-4 in the picker (ListModels gates only on key presence, `grpc/services/catalog.go:78-104`) and every call fails | Implement/register an xai driver, or hide grok + the xAI key form |
| 10 | Low | thinking_level `xhigh`/`max` | anthropic **budget** mode | `anthropic/base.go:793-802`: anything other than low/medium/high → 16000 (medium). It is unreachable today because budget models declare only [low,medium,high], and Reconcile keeps it unreachable. Latent | Add xhigh/max cases |
| 11 | Low | thinking_level | gemini / vertexai Gemini 3 **Flash Preview** | `gemini/driver.go:1032-1037`, `vertexai/gemini.go:518-520`: thinkingConfig deliberately skipped, so the level is ignored. Affects `gemini-3-flash-preview` (no thinking_levels declared, so the UI offers only Auto: consistent) | none needed; documented workaround |
| 12 | Low | thinking_level | antigravity | `antigravity/envelope.go:88-90`: only `gemini-3.8-flash` has effort suffixes. Any other model sends the base id + budget -1, so the level is ignored. The only text model on antigravity is gemini-3.8-flash, so this is OK today | Add variants when new antigravity models land |
| 13 | Low | thinking_level | openrouter | `openrouter/http_client.go:394-399`, `streaming.go:339-344` forward the raw string as `reasoning.effort`. OpenRouter documents `minimal/low/medium/high` (and newer `xhigh`). The catalog allows `max` for gpt-6-astra/sol/terra/luna @openrouter, which is passed verbatim and may be rejected or ignored by OpenRouter. **Not verified against the live API**; the live probe should cover `gpt-6-*@openrouter` at `max` | Clamp to OpenRouter's accepted set in the driver, or drop `openrouter` effort `max` |
| 14 | Low | model_tag_config prefs | client only | `ChatInput.tsx:614-681`: applied only when `!chatId \|\| isPendingChat`, only when the model value has `tags[0]`, only when the field is unset, only once per workflow (`tagPrefsApplied`). Not applied to existing chats, explicit-id models, multi-tag selectors beyond `tags[0]`, spawns/presets resolved server-side, or any backend-originated run | Resolve tag_config server-side in resolveLLMCall |

## Q1. Per setting: does it reach the wire?

Pipeline (code read): `agent.yaml:196-200` → `call_llm.go:901-932` → `llm_request.go:103+` (`resolveLLMCall`) → `drivers/resolver.go:284-303` → driver.

- **Tag selection**: `call_llm.go:845-851` copies `tags` into `ModelSelector`. The registry resolves the first servable entry and returns its `thinking_level`. That level is used only if the caller gave none (`llm_request.go` ~line 155, "precedence"). OK.
- **Explicit id / `id@driver`**: `registry_v2.go:386` and `resolver.go:144` split on the last `@`. ListModels emits `model@driver` ids (`catalog.go:111`). OK.
- **thinking_level**: a literal string is passed through (`call_llm.go:912-915`). An unknown level returns an error (`llm_request.go`, "invalid thinking_level"). A known level the model doesn't support goes through `ReconcileThinkingLevel` (`models/thinking_policy.go:94-104`), which falls back to the model **default** (typically medium), not to the closest level. This happens silently, with no log or UI signal. ModelSettingsPage already filters the levels it offers to `supportedThinkingLevels` (`ModelSettingsPage.tsx:193-198`) and clears the level when the model changes (`:212-227`), so this mostly affects stale chats and presets. Then `WithReasoningEffort` → driver (`resolver.go:303`). Per-driver results are in Q2.
- **temperature**: dropped when it equals 0 (#5). Otherwise it is gated by `TemperatureMode` (`resolver.go:286-294`). Per driver:
  - Sent by openai (`openai/driver.go:324,842`), codex (`codex/driver.go:640`), openrouter, gemini, vertexai (Gemini and Claude), antigravity (`antigravity/driver.go:355`) and reliant (`reliant/driver.go:467`).
  - Never sent by anthropic, or by copilot-Claude, which wraps the anthropic client (`copilot/driver.go:123-135`).
- **compaction_threshold**: honored when it is a literal > 0 (`call_llm.go:173-191, 964-976`). 0 means model-derived, which is intentional. It is not a wire setting; it only drives compaction. It is dropped in the workflows listed under #4.

## Q2. Per-driver thinking mapping

| Driver | low | medium | high | xhigh | max | ultra | Evidence |
|---|---|---|---|---|---|---|---|
| anthropic adaptive | effort low | medium | high | xhigh | max | → high (default) | `anthropic/base.go:833-848`. thinking `{type:adaptive}` is always on (`:784-787`) |
| anthropic budget | 1024 | 16000 | 31999 | →16000 | →16000 | →16000 | `base.go:793-802` (catalog never allows >high here) |
| copilot (Claude) | same as anthropic | | | | | | `copilot/driver.go:132-134` |
| copilot (gpt) | openai chat path | | | | | | `copilot/driver.go:141-145`; default "medium" `:67-69` |
| openai | low/medium/high | | | passthrough | passthrough | passthrough | `openai/reasoning_effort.go:17-30` |
| codex | same as openai | | | | | | `codex/envelope_gpt56.go:129-141` |
| openrouter | raw string as `reasoning.effort` | | | | `max` sent raw (#13) | n/a | `openrouter/http_client.go:394` |
| gemini / vertex Gemini 3 Pro | LOW | HIGH | HIGH | (unreachable) | | | `gemini/driver.go:1050-1059`; the catalog gives Pro only [low,high] |
| gemini / vertex Gemini 3 Flash | LOW | MEDIUM | HIGH | default LOW | | | `gemini/driver.go:1066-1075` |
| gemini / vertex 2.5 | budget 1024 | 8192 | 16384 | | | | `gemini/driver.go:1080-1094` |
| antigravity | model-id suffix `-low/-medium/-high`, budget -1 | | | | | | `antigravity/envelope.go:88-111`, `driver.go:362-367` |
| vertexai Claude | **ignored** (#1) | | | | | | `vertexai/claude.go:91-100` |
| reliant | **ignored** (#3) | | | | | | `reliant/driver.go:444-475` |

Note: gemini flash `default → LOW` means an unexpected value drops to LOW, not medium. Reconcile makes that unreachable.

## Q3. UI offers settings the backend ignores

- Temperature slider is shown for every model (`ModelSettingsPage.tsx:463-490`). It is ignored on anthropic, Claude OAuth and copilot-Claude (#6) and on omit-mode GPT/codex models (#7), and it can cause a 400 on vertex adaptive Claude (#2). An unset slider shows "1.0", which is not necessarily the model default.
- Thinking: only supported levels are offered. Models with no `thinking_levels` show "Auto" only. That is consistent. On vertex Claude and the reliant gateway the offered levels have no effect (#1, #3).
- grok-4 is selectable for an xai-key user but unservable (#9).

## Q4. Other builtin workflows

| Workflow | model-page temp / thinking / compaction honored? |
|---|---|
| agent.yaml | yes (`:196-200`); only temp==0 is lost (#5) |
| auditing-agent.yaml | no: top-level `inputs.temperature/thinking_level/compaction_threshold` (`:150-152`); auditor node passes only model (`:167`) |
| structured-agent.yaml | no (`:183-185`; defaults temp 1.0, thinking **low**) |
| implement-review.yaml | no (`:103-104,137-138`; defaults 1.0 / medium) |
| gsd.yaml, bmad-lite.yaml, migrate.yaml, discovery-relay.yaml, one-ring.yaml (`:396-397`) | no: top-level inputs |
| parallel-compete.yaml | implementer sub-object honored (`:178-184`); other call_llm nodes (`:110,239,369`) pass model only |
| get-it-right.yaml | model only; hardcodes `thinking_level: high` at `:445` |
| pitch-deck.yaml | hardcodes `temperature 0.7/0.2`, `thinking_level: high` (`:660-661,1225-1226`) |
| env-setup, markdown-checklist, ralph-wiggum, blog-content-pipeline | model only: thinking/temperature from tag/model default; model-page overrides dropped |

All verified by code read (`rg` over `internal/workflow/builtin/*.yaml`).

## Q5. Presets

- These presets carry only a tag (no override): code_reviewer, debug, documentation, forge, forge_implementer, general, git, implementer, planner, refactor, tester.
- These presets pin values inside `model`:
  - `migrate.yaml:9` sets `temperature: 0.4`.
  - `researcher.yaml:15` sets `thinking_level: low`.
  - `ux.yaml:36` sets `thinking_level: high`.
  - `workflow_builder.yaml:10-11` sets `temperature: 1` and `thinking_level: high`.
- Those pins sit in the model value, so they flow through agent.yaml's `inputs.model.X`. As a result, selecting the preset overrides the tier's thinking. The tag_config merge in ChatInput (`:654-665`) never replaces a preset value: it applies only when the field is unset. Whether a user's model-page edit after picking a preset wins depends on the params store, which was not traced (unverified).

## Q6. model.tag_config not applied

All of these are verified by reading `ChatInput.tsx:614-681`:
- existing (non-pending) chats (`:616`)
- an explicit model id, because there is no `tags` (`:636-637`)
- only `tags[0]` is consulted
- applied once per workflow mount (`tagPrefsApplied`)
- nothing server-side, so spawned sub-agents, presets resolved by the backend, titles/compaction, scheduled or API runs, and every other client that isn't ChatInput never see it

Mobile: `MobileModelPreferences.tsx` only reads and writes the same key. No mobile chat-input path that applies it was found (rg `loadTagModelConfigs` shows only ChatInput as the applier). Mobile chats probably ignore it (likely, not fully traced).

Even when it is applied, the values land in `model.*` and are dropped by every workflow in Q4 except agent.

## Q7. Registry level

- grok-4 (user-visible) and grok-3-mini-beta are served only by `xai`, which is unregistered (#9). The tag tails `powerful`, `flagship`, `moderate`, `fast` and `cheap` include grok "for xai-only users". Those entries can never resolve to a working driver.
- Tag levels are clamped (`ClampThinkingLevel`, `thinking_policy.go:124+`). Example: `powerful` asks grok-4 for `high`, which grok-4 supports. Tag entries are validated at parse time (see the models.yaml header). `go test ./internal/llm/models/` passes. No tag pointing at an unsupported level was found unclamped.
- ListModels (`catalog.go:78-104`) lists a model@driver whenever the user has a key for that driver. It does not check that the driver is registered in the resolver, which is how #9 reaches the picker.

## Notes for the live probe

Priority cells that static reading cannot settle:
- `vertex-claude-*` at every level, and with temperature set (#1, #2)
- `*@reliant` at low vs xhigh: check that thinking tokens differ (#3)
- `gpt-6-*@openrouter` at `max` (#13)
- `grok-4@xai` (expected failure, #9)
- agent workflow with temperature 0 (#5)
