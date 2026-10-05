# Model + settings verification: shared briefing

Goal (user ask): "make sure all models work, and all the given settings."
Static findings: `research/MODEL_SETTINGS_AUDIT.md` (14 defects, file:line). Read it
for the defect you own; do not re-derive what it already establishes.

## Settled facts (verified; do not re-check)

- `ModelSelector` (proto/reliant/v1/workflow_v2.proto:91) carries ONLY id, tags,
  providers. The model value the UI writes is `{tags|id, thinking_level?,
  temperature?, compaction_threshold?}`; `toModelSelector`
  (internal/workflow/cel/resolver.go:492) drops everything but id/tags/providers.
  Only `agent.yaml:196-200` digs the extras out by hand, with `0.0` / `0` / `''`
  sentinels; `call_llm.go:907` treats temperature 0.0 as unset.
- Request construction for every workflow LLM call is ONE path:
  `handlers/llm_request.go` `resolveLLMCall` -> `drivers.GetDriver`
  (`internal/llm/drivers/resolver.go` `defaultGetDriver`).
- Credentials come from the DB via `drivers.BuildAvailableDrivers`
  (`internal/llm/drivers/helpers.go`). OAuth refresh persists rotated tokens with
  compare-and-swap (`*_token_refresh.go`). Refreshing WITHOUT persisting can burn
  a rotating refresh token (Claude, Codex) — never do that.
- Live checks already run (2026-10-04, OpenRouter key from `$OPENROUTER_API_KEY`):
  - `anthropic/claude-opus-5-5` with `temperature: 0.5` via OpenRouter -> 200 PONG
    (OpenRouter likely strips it; says nothing about direct Anthropic/Vertex).
  - `openai/gpt-6-sol` with `reasoning.effort: "max"` via OpenRouter -> 200 PONG
    (accepted; whether honored is unknown).
  - `$RELIANT_SEED_API_KEY` (Anthropic) is INVALID (401). Don't use it.
- Dev DB: `postgres://postgres:postgres@localhost:5434/reliant` — REAL data.
  Read-only except the OAuth CAS refresh production itself performs. Never
  drop/mutate anything else. Owner user `a6e15ec0-d1be-40c2-9c17-8d775613c904`
  has: claude OAuth (valid until 2026-10-04 21:50Z), codex (JWT exp
  2026-10-09), antigravity (expired 2026-09-18, has refresh token). No openai,
  gemini, vertex, copilot, xai credentials exist anywhere.
- Dev LiteLLM: `localhost:4000` (healthy). Dev admin-server (reliant gateway
  proxy) listens on `:8090`; the reliant driver base URL is
  `RELIANT_API_BASE_URL` (dev: `http://127.0.0.1:<admin_server_port>/v1`).
- Proto regen: `make proto-generate` (BSR; may rate-limit — retry, don't
  hand-edit gen/). `make generate-go` for the rest.
- Never run git commands that change state (no stash/checkout/commit/reset/add).
  Other agents are editing this worktree concurrently.

## Decisions (made; implement, don't relitigate)

1. **The model value is the one home for per-call model settings.**
   `ModelSelector` gains `string thinking_level`, `optional double temperature`,
   `optional int32 compaction_threshold`. Precedence, highest first:
   explicit call_llm node arg (author pinned) > model value > user's Settings
   tag preference (`model.tag_config.<tag>`) > tier/model default.
   Temperature presence comes from `optional`, so 0 is a real value. Remove the
   hand extraction and sentinels from agent.yaml and every builtin workflow;
   remove separate top-level `temperature`/`thinking_level`/`compaction_threshold`
   inputs that duplicate the model value. No backwards compatibility.
2. **Temperature is shown only where it is honored.** Adaptive-thinking Claude
   models get `temperature_mode: omit` in models.yaml (enforced by a registry
   test). `ModelInfo` gains a per model@driver `supports_temperature`; the UI
   hides the slider when false and shows "Default" (not "1.0") when unset.
3. **A provider is available iff the user has a credential AND its driver is
   registered.** Enforce in `BuildAvailableDrivers` so the picker and resolution
   agree (grok-4/xai currently reaches the picker and fails every call).
   Whether to implement an xai driver or delete grok is a USER decision — do
   neither.
4. **Settings tag preferences resolve server-side** in `resolveLLMCall`, so
   spawns, existing chats, mobile and scheduled runs honor them; ListModels
   tiers reflect them; the client-side merge in `ChatInput.tsx:611-686` is
   removed.
5. **The live probe goes through production's request path**, not a copy of it.

## Ownership (disjoint; do not touch another agent's files)

| Agent | Owns |
|---|---|
| probe | `tools/reliant-dev/models_probe*.go`, `internal/workflow/runtime/activities/handlers/llm_probe.go` (new), `research/MODEL_PROBE_RESULTS.md` |
| vertex | `internal/llm/drivers/vertexai/claude*.go` (+ tests) |
| reliant-gw | `internal/llm/drivers/reliant/*` |
| selector | `proto/reliant/v1/workflow_v2.proto`, `internal/workflow/cel/resolver.go`, `internal/workflow/yaml/*`, `call_llm.go` (arg/selector precedence only), `compact.go`, `internal/workflow/builtin/*.yaml` + presets + their tests/scenarios |
| temp-honesty | `internal/llm/models/definitions/models.yaml` (temperature_mode only), `internal/llm/models/*_test.go` (new test), `proto/reliant/v1/catalog.proto`, `internal/grpc/services/catalog.go` (supports_temperature only), `internal/llm/drivers/helpers.go`, web `ModelSettingsPage.tsx`, `ModelPreferences.tsx`, `MobileModelPreferences.tsx`, `WorkflowBuilderChat.tsx` |
| tag-prefs (wave 2) | `handlers/llm_request.go`, catalog tiers in `catalog.go`, `ChatInput.tsx` merge removal |

Generated code (`gen/`, `web/src/gen/`) is shared: regenerate with make, never hand-edit.

## Wave 3: Copilot + local models (2026-10-04, after the user signed in to GitHub)

- The owner now has Copilot (`copilot_auth_tokens`, tier individual, `gho_` token,
  2026-10-04 19:50Z). Probe: 15/15 copilot cells pass (claude-4.5-haiku,
  claude-5-sonnet, gpt-5-mini; incl. tool round-trips). Reasoning pass:
  `gpt-5-mini@copilot` output tokens 395/587/779 at low/default/high (honored);
  `claude-5-sonnet@copilot` 159/173/165 at low/default/xhigh with sig=true
  (looks IGNORED — unverified, one sample).
- Copilot `/models` for this account (fetched with the driver's own headers from
  `copilot/driver.go:30-33`; a vscode integration id returns only gpt-4o-era
  models) reports policy=enabled for, among catalog models we do NOT map to
  copilot: claude-sonnet-5.5, gemini-3.7-flash, gemini-3.8-flash, gpt-5.4,
  gpt-5.4-mini, gpt-5.6-luna, gpt-5.6-terra, gpt-6-luna; gpt-5.3-codex has no
  policy block (= unrestricted). policy=disabled: claude-opus-5.5/5/4.8,
  claude-fable-5.1/5, gpt-5.5, gpt-5.6-sol, gpt-6-sol, gpt-6.1-sol, gpt-6-astra.
  Copilot also serves models not in our catalog at all (grok-4.5/4.6/4.7,
  kimi-k3, mai-code-1.1-flash, gpt-5.4-nano) — catalog expansion is a USER
  decision, not in scope. Per-model reasoning levels Copilot reports: Claude
  low..max; gpt-5.4*/5.6*/6* none..xhigh/max; gemini low/medium/high.
- Local models: Ollama is installed (`/opt/homebrew/bin/ollama`, models
  `qwen3:latest` ~4.9GB, `nomic-embed-text`), NOT running; nothing listens on
  11434/1234/8000/8080. Local provider config today comes ONLY from project
  config `models.providers.local.base_url`, applied per call in `call_llm.go`
  ~900-910 by mutating process-global state (`models.InitGlobalRegistryWithDiscovery`,
  `local.SetLocalConfig`) — a multi-tenant leak in a shared worker, and in
  distributed mode the worker cannot reach the user's localhost (only the
  daemon runs on the user's machine). api-server/worker boot with
  `local.SetLocalConfig(nil)`. The Settings provider list has a "Local Models"
  card (`CombinedGeneralSettings.tsx:191`, keyFormat "N/A").

## Wave 5: user decisions (2026-10-04, evening) — implement, don't relitigate

- **Drop xAI/grok entirely**:
  - delete the catalog entries (grok-4, grok-3-mini-beta) and every tag
    entry naming them;
  - delete `internal/llm/drivers/xai`, plus groq/azure/bedrock if they are
    likewise model-lists-only with no client;
  - delete the xAI key form and any UI/analytics/constants references.
- **Copilot**: add the catalog-missing models Copilot serves —
  grok-4.5/4.6/4.7, kimi-k3, mai-code-1.1-flash, gpt-5.4-nano. Grok here is a
  COPILOT-served model, and that is fine (it is the xai DRIVER being dropped).
  Copilot resolution must respect per-account policy=disabled, not just the
  picker. Make Copilot's model availability dynamic from its `/models` (it
  already is for the picker via `ModelLister`).
- **Codex dynamic models**: verified 2026-10-04.
  - `GET https://chatgpt.com/backend-api/codex/models?client_version=0.153.4`
    with the codex driver's headers returns 200 and `{"models":[{slug,
    display_name, supported_reasoning_levels[{effort}], default_reasoning_level,
    context_window, visibility:"list"|"hide", supported_in_api}]}`.
  - This account lists 7 models: gpt-6-astra, gpt-reserve (hide), gpt-5.6-sol,
    gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, codex-auto-review (hide).
  - CAUTION: it advertises `ultra` ("automatic task delegation", a Codex-
    CLIENT feature), yet the API 400s on `ultra` (live probe). Dynamic levels
    must be intersected with what the API accepts, never trusted raw.
- **Claude OAuth thinking text**: try `display:"summarized"` (carefully —
  the request mimics Claude Code; keep the fingerprint stable).
- **Model preferences can pin a local model**: the tag config needs a
  provider/daemon field.
- **gemini-3.8-flash flat across levels**: investigate (antigravity
  id-suffix mapping; Copilot).
- **Custom model endpoints** (`proto/reliant/v1/model_endpoint.proto`, DONE,
  generated; migration `20261004224757_add_model_endpoints.sql`):
  - It unifies "remote endpoint" and the daemon relay: route DIRECT or
    VIA_DAEMON per endpoint, plus per-model params (context, max output, caps,
    temperature, top_p, extra_body_json).
  - Auto-detected Ollama etc. stay in each daemon's inventory.
  - **Secrets live ONLY in origin/main's sealed `connections` store**
    (`auth_kind='api_key'` + `connection_secrets`, vault-sealed, #440/#443/
    #444). This branch is 10 commits BEHIND main (a fast-forward), so
    endpoint storage that touches credentials is blocked on the user
    approving a sync with main. Build everything else now.
- **Video generation tool** (new):
  - Google's docs (2026-10-04) say: use **Gemini Omni Flash
    (`gemini-omni-1.1-flash`)** as the default — multimodal in, conversational
    multi-turn editing, extension, interpolation, 1080p upscaled. It uses the
    NEW Interactions API, which genai v1.71 does NOT have.
  - **Veo 3.1 (`veo-3.1-generate-preview`, plus `-fast-` and `-lite-` previews)**
    is for cinematic quality: 720p/1080p/4k, 8s, native audio, up to 3
    reference images, extension. It uses `predictLongRunning` + operation
    polling, which genai v1.71 HAS (`Models.GenerateVideos`).
  - The managed LiteLLM gateway has no video route, so video calls Google
    directly (a user `gemini` key, and/or platform Vertex creds).
- Items 2 (review/commit), 4 (canary), 6 (live gaps): NOT doing.

## Wave 4: local models — the contract (written by the orchestrator; DONE, builds)

Design (from `research/LOCAL_MODELS.md` §4–6, decided): **the local endpoint is
a property of a DAEMON.** The daemon detects/configures endpoints on its own
machine, probes them for real capabilities, publishes an inventory, and RELAYS
the worker's OpenAI-compatible HTTP to them over the existing daemon channel.
The worker never dials a local URL. No process-global local state remains.
Remote GPU servers without a daemon (URL + key, worker-direct) are a later,
separate step — not in this wave.

Already in the tree (do not redo; build on it):
- `proto/reliant/v1/tools_daemon.proto`:
  - `LocalModelInventory` / `LocalModelEndpoint` / `LocalModelInfo` messages.
  - `LocalModelHTTPRequest` / `LocalModelHTTPCancel` / `LocalModelHTTPChunk` /
    `LocalModelRefresh` messages.
  - `DaemonMessage.local_model_http_chunk = 14` and `local_model_inventory = 15`.
  - `ServerMessage.local_model_http_request = 17`, `local_model_http_cancel = 18`
    and `local_model_refresh = 19`.
  - Read the comments there; they are the spec: allow-list, done/error semantics,
    endpoint ids, and Ollama `context_window` = the num_ctx the daemon requests.
- `proto/reliant/v1/daemon_registry.proto`:
  - `DaemonInfo.local_models = 21`.
  - rpc `RefreshLocalModels` and rpc `SetLocalModelEndpoints`, plus their
    request/response messages. They have no handlers yet; the service embeds
    Unimplemented.
- `proto/reliant/v1/catalog.proto`: `ModelInfo.local = 17` (`LocalModelSource`:
  daemon_id, machine_name, endpoint_id, endpoint_kind, online).
- DB:
  - migration `20261004201053_add_local_models_to_daemons.sql` adds
    `daemons.local_models TEXT` (protojson LocalModelInventory; it outlives
    disconnect).
  - `Repo.SetDaemonLocalModels(ctx, daemonID, json)` and
    `Repo.ListDaemonLocalModels(ctx, userID) map[daemonID]json` exist (on the
    `db.Repository` interface).
  - `schema.sql` is NOT yet regenerated (`make generate-schema`); the store agent
    owns that.
- `llm.DriverOptions.Transport http.RoundTripper` and `llm.WithTransport(rt)`:
  when set, a driver must send all HTTP through it (openai-go:
  `option.WithHTTPClient(&http.Client{Transport: rt})`).
- gen/ and web/src/gen/ regenerated. **Tooling hazard:**
  `make proto-generate-controlplane` does `rm -rf gen/controlplane` BEFORE the
  BSR call, so a BSR cancel leaves the tree unbuildable. Run
  `make proto-generate-go` (or the reliant-only buf template) for reliant-only
  proto changes. If gen/controlplane vanishes, restore each deleted file with
  `git show HEAD:<path> > <path>` (sources in proto-vendor are untouched).
  NEVER use git checkout/stash.

Model identity (decided):
- A local model's catalog id is `<server model name>@local`, e.g.
  `qwen3:latest@local`.
- The DAEMON is carried separately: `ModelInfo.local.daemon_id` in the
  catalog, and `ModelSelector.providers` = `["local:<daemonID>"]` when the user
  picks one. The provider string `local:<daemonID>` is the one encoding of
  "this local model on that machine" across selector → resolution → driver.
- If no daemon is given, use the user's online daemon that serves the model,
  preferring the chat's worktree daemon.
- Local models are never in models.yaml. They are synthesized per request from
  the stored inventory of the user's ONLINE daemons — never registered into the
  global registry.

## Wave 1 outcome (landed in the worktree, uncommitted)

- Probe: `reliant-dev models probe` (`tools/reliant-dev/models_probe*.go`,
  `handlers/llm_probe.go` `ProbeLLMCall`). First run: 310/325 text cells, 4/4
  image cells. Results: `research/MODEL_PROBE_RESULTS.md`. Providers probed:
  anthropic (Claude OAuth), codex, antigravity, openrouter.
- Live failures (all reproducible):
  - `gpt-6-terra@openrouter`: 400 "openai/gpt-6-terra is not a valid model ID".
    OpenRouter's /models (fetched 2026-10-04) lists gpt-6-sol, gpt-6-luna,
    gpt-6-astra (+ -pro, :batch) and NO gpt-6-terra.
  - `gemini-3-pro-preview@openrouter`: 404 "No endpoints found". OpenRouter lists
    gemini-3.1-pro-preview but NOT gemini-3-pro-preview.
  - `gpt-5.6-sol` / `gpt-5.6-terra` @codex at `ultra`: Codex 400 "Invalid value:
    'ultra'. Supported values are: none, minimal, low, medium, high, xhigh, max".
  - Every @codex cell reports 0 output tokens: `codex/driver.go:738-741` and
    `:932-935` copy only `TotalTokens` into `llm.TokenUsage`.
- vertexai Claude: thinking/output_config now on the wire, temperature only when
  thinking is off on a non-adaptive model. Not live-verifiable (no creds).
- reliant gateway: `reasoning_effort` now forwarded; live via dev LiteLLM
  (master key): gemini-3.8-flash low=2 vs high=69 output tokens (was ~85 flat).
- The 16 `reliant`-provider keys in the dev DB are legacy `rlnt_` keys; the
  gateway proxy accepts only `rlat_` (`control-plane llmproxy/proxy.go:369`), so
  every one 401s, yet `BuildAvailableDrivers` still counts them as configured.
- Adaptive Claude models now carry `temperature_mode: omit`;
  `models.SupportsTemperature(def, driverID)` exists; `ModelInfo.supports_temperature = 16`;
  `BuildAvailableDrivers` skips providers with no registered driver factory.
- `ModelSelector` now has `thinking_level = 4`, `optional double temperature = 5`,
  `optional int32 compaction_threshold = 6`; `call_llm.go`
  `explicitSamplingOverrides` = node arg > model value. Builtin duplicates of
  those inputs are removed. Leftover: `structured-agent.yaml` keeps
  `temperature` (default -1 = "unpinned") and `thinking_level` pin inputs so
  get-it-right / pitch-deck can pin — an in-band sentinel to be removed in wave 2.
- Concurrent work: another agent in THIS worktree is editing many web files
  (tooltips etc.), including `web/src/components/Chat/ChatInput.tsx`. Re-read a
  file immediately before editing it, make surgical edits only, never rewrite it.
