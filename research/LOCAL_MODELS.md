# Local models (Ollama / LM Studio / llama.cpp / vLLM) in Reliant

Date: 2026-10-04. Repo: `reliant/` in worktree `small-polish-b5b8ca5e`. No source edited.

Tags: **[code]** = read from source, **[tested]** = observed live in this session,
**[log]** = observed in the running dev stack's logs.
Builds on `research/MODEL_SETTINGS_PLAN.md` "Wave 3", which is not re-derived here.

**Process left running:** `ollama serve` (Ollama 0.12.3), **PID 89398**, started by
this investigation on 127.0.0.1:11434 (port was free). Its log is `/tmp/ollama-serve.log`.
Stop it with `kill 89398` when you no longer need it.

## Executive summary

- **Do local models work end to end today? Partly, and only by accident.**
  If a `models.providers.local.base_url` is set, and the worker is on the same
  machine as Ollama, a workflow that names a model by id (`local-qwen3-latest`)
  or by the `local` tag gets a real answer, and tool round-trips work **[tested]**.
  That is the whole working surface.
- **Local models never appear in the model picker.** The api-server, which serves
  `ListModels`, never loads any local config or discovers anything **[code]**.
- **The Settings "Local Models" card is never rendered.** `local` is not in
  `VISIBLE_PROVIDERS` **[code]**.
- **The implementation is unsafe in a shared worker.** One project's local config
  is installed into process globals on every `CallLLM`. It then leaks to every
  other user's calls in that worker, and is never cleared **[tested]**.
- **Quality defects, all live-reproduced:**
  - context window is assumed to be 200k; Ollama silently truncated a 50k-token
    prompt to 4096, and the model returned a wrong answer
  - thinking arrives as raw `<think>…</think>` inside the answer text
  - token usage is always 0
  - an embedding model is offered as a chat model and wins tag resolution, which
    then fails with a 400
- **Distributed mode:** the worker calls `base_url` from its own host. Against a
  user's `localhost` this cannot work, and nothing in the system detects that.
- **Recommendation:** a **daemon-side LLM HTTP relay** over the existing NATS
  daemon-command channel, with a new *streaming* command type. The endpoint is
  configured **per daemon (per machine)**, auto-detected on the daemon. Discovered
  models are published with the daemon's registration/heartbeat, so the catalog
  can list them per user. All process-global local state is deleted. See §4–§6.

---

## 1. As-built path, config to wire

### 1.1 Where `base_url` can be set **[code]**

- **Type:** `models.LocalProviderConfig{BaseURL}` (`internal/llm/models/types.go:856-873`),
  under `UserModelsConfig.Providers.Local` (`types.go:845`).
- **Daemon side:** `configloader` loads global `~/.reliant/config.yaml`, project
  `.reliant/config.yaml` and project-local `.reliant.local/config.yaml`
  (`internal/daemon/configloader/loader.go:67-86`). It merges them with
  `mergeModelsConfig`, which **returns only the user (global) scope**
  (`loader.go:521-532`). The comment there says this is deliberate: "Local model
  servers (Ollama, etc.) are machine-specific, not project-specific."
- **Shipped to the server:** the daemon reads the three YAML files raw
  (`internal/toolexec/daemonruntime/runtime.go:1661-1667`) and pushes them as
  `user_config_yaml` / `project_config_yaml` / `local_config_yaml` blobs into
  `project_configs`.
- **Worker side:** `StoredConfigProvider.GetProjectConfig` (`internal/config/provider.go:67`)
  → `mergeStoredConfigRecord` merges user < project < local (`provider.go:118`).
  `mergeConfigInto` **replaces** `Models` wholesale when a later scope sets it
  (`provider.go:198-200`).

**Inconsistency:** the worker honors project and project-local `models:` blocks,
but the daemon's own loader ignores them. A `.reliant/config.yaml` checked into a
repo can therefore set the local `base_url` the worker uses. The daemon's view
says otherwise.

**[data, read-only]** `localhost:5434/reliant`: 26 of 38 `project_configs` rows
contain a `base_url`, spanning 6 distinct users. The owner's
`~/.reliant/config.yaml` has `models.providers.local.base_url: http://localhost:11434/v1`.
So this path is live in dev for every one of those projects.

### 1.2 Per-call application in the worker **[code]**

`internal/workflow/runtime/activities/handlers/call_llm.go:896-907`, on every `CallLLM` activity:

```go
projectCfg, err := a.getProjectConfig(ctx, project)
models.InitGlobalRegistryWithDiscovery(projectCfg.Models, local.DiscoverModels) // replaces GLOBAL registry
if projectCfg.Models != nil && projectCfg.Models.Providers.Local != nil {
    local.SetLocalConfig(projectCfg.Models.Providers.Local)                     // sets GLOBAL local config
    local.DiscoverAndRegisterModels(BaseURL)                                    // APPENDS to GLOBAL DriverMapping
}
```

- `InitGlobalRegistryWithDiscovery` (`internal/llm/models/registry_v2.go:138-154`):
  - with `cfg == nil` it just returns the existing global registry, **not** a
    clean one (`:139-143`)
  - otherwise it re-parses the embedded `models.yaml`, clones it, discovers, and
    calls `SetGlobalRegistry` (`user_config.go:208-228`, `registry_v2.go:152`)
- `discoverLocalModels` (`user_config.go:96-119`) adds each discovered model
  unless its id already exists, and tags it `local`.
- `RegisterDriverModels` **appends** with no dedupe (`internal/llm/models/mapping.go:84-88`).
- Boot: api-server and worker both call `InitGlobalRegistryWithUserConfig(nil)`
  and `local.SetLocalConfig(nil)` (`internal/serverapi/run.go:176-179`,
  `internal/serverworker/run.go:134-137`).

### 1.3 Discovery **[code]** (+ **[tested]** where marked)

`internal/llm/drivers/local/models.go`:

- `DiscoverModels` (`:86-111`) first tries LM Studio's beta `GET /api/v0/models`
  (`:26, :98`). That 404s on Ollama **[tested]**. It then tries `GET /v1/models`
  (`:100`).
  - The URL is built by overwriting `u.Path` (`:93`), so any path prefix in
    `base_url` is discarded. A reverse-proxied endpoint such as
    `https://host/ollama/v1` is broken.
- `listLocalModels` (`:140-189`):
  - uses `http.Get` on the default client, which has **no timeout**
  - swallows every error into an empty list (`:142-168`)
  - filters only on LM Studio's `type == "llm"` (`:172-183`). An Ollama embedding
    model passes through **[tested]**.
- `convertLocalModelToDefinition` (`:197-224`):
  - id is `"local-" + sanitize(id)` (`:207`)
  - context is `loaded_context_length`, else `max_context_length` (both LM
    Studio-only fields), else `DefaultContextWindow = 200000` (`:194, :198-204`)
  - `MaxOutputTokens = contextWindow` (`:212`)
  - `SupportsTools`, `SupportsStreaming` and `SupportsAttachments` are hard-coded
    `true` (`:213-215`)
  - `CanReason` is left `false`
- `DiscoverAndRegisterModels` (`:61-81`) runs discovery a **second** time for the
  same call. That makes up to four HTTP GETs per LLM call, before the LLM request.

### 1.4 `BuildAvailableDrivers` local branch **[code]**

`internal/llm/drivers/helpers.go:214-224`: if `local.GetLocalConfig()` (the
process global) has a `BaseURL`, then `local` is added to **every user's**
available drivers, with no user or project check. `GetAvailableDrivers`
(`api_key_provider.go:66-88`) uses this for model resolution
(`llm_request.go:146`) and for `ListModels`.

### 1.5 The local driver (`internal/llm/drivers/local/driver.go`) **[code]** + **[tested]**

- **Transport:** openai-go Chat Completions client pointed at `BaseURL`, with a
  placeholder key `"local"` (`:46-74`).
- **Tools:** converted to function tools (`:165-199`). `tool_choice` is pinned
  only when the named tool is present (`:235-241`). A round-trip works **[tested]**.
- **Streaming:**
  - only `delta.content` and `delta.tool_calls` are read (`:337-362`)
  - `EventComplete` is emitted on the chunk carrying `finish_reason` (`:365-382`)
  - usage is read from later chunks (`:387-389`), but with `include_usage` Ollama
    sends usage in a **separate final chunk with `choices: []`**, after
    `finish_reason` **[tested]**, so the completion is always sent with zero usage
  - even when usage is captured, only `PromptTokens` → `TokenCount` is copied;
    output tokens are never set (`:388, :489-493`)
- **Thinking/reasoning:**
  - not sent: `CanReason=false` means `ReconcileThinkingLevel` drops any level to
    `""` (`internal/llm/models/thinking_policy.go:94-97`, `llm_request.go:198-201`)
  - not parsed either way: no `reasoning`/`reasoning_content` field, no `<think>`
    splitting. Any `EventThinkingDelta` is impossible from this driver.
- **Temperature:** passed through when set (`:245-247`). Sent and accepted **[tested]**.
- **Max tokens:** `max_tokens` only when explicitly set (`:251-253`).
- **Context window:** nothing tells the server what context to allocate. There is
  no Ollama `num_ctx` / `options` and no LM Studio context hint.
- **Retries:** any non-API error, including connection refused, is retried with
  backoff up to `MaxRetries` (`:430-438`). A dead endpoint therefore costs
  several seconds of backoff before it fails.

### 1.6 Catalog / picker **[code]** + **[log]**

`internal/grpc/services/catalog.go:79-100` lists `registry.GetUserVisibleModels()`
and shows any model with a `local` provider without a key check ("Local models
don't need API keys - they're available if they exist in the registry").

The api-server process never discovers. It boots with the base registry and
`SetLocalConfig(nil)` (`serverapi/run.go:176-179`), and no code path in the
api-server calls `InitGlobalRegistryWithDiscovery`. Only `call_llm.go:900`
does, which runs in the worker. `internal/grpc/server.go:140-148` confirms the
api-server is a separate process ("pre-split monolith days; it isn't anymore").

**So a local model can never appear in the picker today, in any deployment.**
- **[log]** `grep -c "[ListModels] Found local model" reliant-api-server.log` → 0.
- **[log]** The same worker log has 119 `"Registered local models with driver"` lines.
- **Electron:** it supervises only the daemon (`electron/src/backend-auth.js:30`),
  so it has no in-process catalog either.

The only ways to reach a local model:
- a workflow/preset that names `local-<id>` (or `local-<id>@local`)
- the `local` tag
- leakage via tag resolution once `local` is in the provider set

### 1.7 Settings "Local Models" card **[code]**

`web/src/components/Settings/CombinedGeneralSettings.tsx:191-197` defines
`local: { name: "Local Models", keyFormat: "N/A", … }`. However, the "add provider"
list filters by `VISIBLE_PROVIDERS` (`:69-79`, `:526-530`), and `local` is not in
that list.

So the card is **never rendered**. Even if it were, it would only offer the
generic API-key save (`api.settings.updateProvider`), which has no field for a
URL. Nothing in the UI reads or writes `models.providers.local`.

---

## 2. Defects

| # | Defect | Evidence |
|---|---|---|
| D1 | **Cross-tenant leakage of local config.** `SetLocalConfig` is process-global and set from whichever project last ran `CallLLM`. It is never reset when a later project has no local config (`call_llm.go:903` only sets, never clears), so `BuildAvailableDrivers` hands `local` + that `BaseURL` to every user. | **[tested]** `TestLocalRace`: tenant A sets `http://tenant-a.invalid:1/v1`; tenant B (no local config) saw that URL in 200/200 reads of `GetAvailableDrivers(…,"tenant-b").Drivers["local"].BaseURL`. Also a fake user id with no keys got `local` with the dev URL. |
| D2 | **Cross-tenant leakage of the model registry.** `SetGlobalRegistry` per call. A project with `Models == nil` gets the previous caller's registry (`registry_v2.go:139-143`), including their custom models, tag overrides and discovered models. User B's `tags:` override can therefore change user A's model resolution. | **[code]** |
| D3 | **Races.** Concurrent activities interleave `SetGlobalRegistry` / `SetLocalConfig` / resolution. Activity X can install its registry, then Y replaces it, then X resolves against Y's tags and Y's `base_url`. Each global is individually locked, but the sequence is not atomic. X's LLM request can go to Y's endpoint, which leaks X's prompt to whatever Y configured. | **[code]**; the D1 test shows the visibility |
| D4 | **Registry re-init and re-discovery on every call.** Re-parses the embedded YAML and does up to four HTTP GETs per LLM call. `DriverMapping[local]` grows without bound. | **[tested]** 5 calls → 10 entries for 2 models. **[log]** dev worker: 119 registrations in ~10 min. |
| D5 | **Distributed reachability.** The worker dials `base_url` from its own network namespace. A user's `localhost` is the worker pod. Nothing validates this or reports a useful error. Dev hides it because the worker is a host process on the same Mac. | **[code]** + architecture constraints |
| D6 | **Discovery failure is silent.** Errors are swallowed (`models.go:142-168`), registry init returns nil, and `SetLocalConfig` still marks `local` available. The user then gets a generic "failed to resolve model … check your API key configuration". There is no timeout, so a blackholed host hangs the activity. A path prefix in `base_url` is dropped (`models.go:93`). | **[tested]** unreachable URL: `DiscoverAndRegisterModels` err=nil; `InitGlobalRegistryWithDiscovery` err=nil, model absent |
| D7 | **ID collisions.** The `local-` prefix prevents clashes with catalog ids. But sanitizing is lossy: `qwen3:latest` and `qwen3-latest` both become `local-qwen3-latest`, and the second is silently skipped (`user_config.go:111-113`). Two machines/endpoints with the same model name are indistinguishable, so there is no notion of which endpoint a model belongs to. | **[code]** |
| D8 | **Capabilities are fabricated.** Tools, attachments and streaming are always true. Context is 200k. Max output equals the context window. Reasoning is false. Ollama reports the truth at `POST /api/show` (`capabilities: [completion, tools, thinking]`, `qwen3.context_length: 40960` **[tested]**). Effects: (a) compaction thresholds are derived from 200k, so they never fire before the server truncates; (b) the embedding model is offered; (c) thinking is never requested; (d) image attachments are sent to text-only models. | **[tested]** see §3 |
| D9 | **Silent context truncation.** Ollama's default `OLLAMA_CONTEXT_LENGTH` is 4096, and the driver never sends `num_ctx`. Reliant's system prompt plus tools alone is about 20k tokens (comment at `models.go:191-193`), so every real agent turn on Ollama is truncated to its last 4096 tokens. | **[tested]** server log: `truncating input prompt limit=4096 prompt=49924`. The needle answer was `2999` (wrong; correct is 7341). |
| D10 | **Thinking leaks into the answer text.** qwen3 via Ollama's OpenAI endpoint returns `<think>…</think>` inline in `content`. The driver streams it as answer text. | **[tested]** every qwen3 response's `text` begins `<think>\n…`. `ThinkingEvents=0`. |
| D11 | **Usage always 0.** See §1.5. Breaks context accounting and compaction triggers. | **[tested]** `usage={TokenCount:0 …}` on every call |
| D12 | **`local` tag resolves to an embedding model.** Discovered ids are appended in server order, and `nomic-embed-text` sorts first. | **[tested]** `Tags:["local"]` → `local-nomic-embed-text-latest` → 400 "does not support chat" |
| D13 | **Config scope disagreement.** Daemon: user-only. Worker: user < project < local (§1.1). A repo can point every collaborator's worker at an arbitrary URL. That is an SSRF primitive from the shared worker into its own network (cluster-internal services, metadata endpoints). | **[code]** |
| D14 | **Dead UI.** The Settings card is defined but unreachable, and has no URL field (§1.7). | **[code]** |

---

## 3. Live test

### 3.1 Direct against Ollama 0.12.3 **[tested]**

**Models and capabilities**
- `GET /v1/models` → `nomic-embed-text:latest`, `qwen3:latest` (no context or
  capability fields).
- `GET /api/v0/models` → 404.
- `POST /api/show {"model":"qwen3:latest"}` → `capabilities ['completion','tools','thinking']`,
  `qwen3.context_length 40960`.
- Server config: `OLLAMA_CONTEXT_LENGTH:4096`, `OLLAMA_NUM_PARALLEL:1`.

**Chat completions** (`POST /v1/chat/completions`)
- **Basic:** OK. 25s cold, including model load. `content` starts with
  `<think>\n…`. Usage `{prompt 14, completion 1149}`, finish `stop`.
- **Tools, non-stream:** `tool_calls=[{id:call_1zprp9v2, function:{name:get_secret_word, arguments:"{}"}}]`,
  finish `tool_calls`. No `reasoning` field.
- **Streaming:** `delta.content` chunks, starting `"<think>"`. Final chunks:
  `choices: [] , usage {prompt 14, completion 120}` then `[DONE]`.
- **Streaming tools:** a single chunk carries the full `tool_calls` (id, name,
  args), then a chunk with `finish_reason: tool_calls`.
- **`reasoning_effort`:**
  - `"none"` → 400 `invalid think value: "none" (must be "high","medium","low",true,false)`
  - `"high"` → 400 `think value "high" is not supported for this model`
  - So Ollama maps `reasoning_effort` to its `think` parameter, and the levels
    are per-model. Blindly forwarding Reliant levels would break.
- **Native `POST /api/chat {"think":true}`:** `content='4'`,
  `thinking='Okay, the user asked …'`. The native API separates thinking; the
  OpenAI-compat endpoint in this version does not.
- **`/no_think` in the prompt:** `'<think>\n\n</think>\n\nfour'`.

### 3.2 Through Reliant's production path **[tested]**

**Harness**
- A throwaway test file at `/tmp/localprobe/local_probe_test.go`, injected with
  `go test -overlay` into package `handlers` (nothing written into the worktree).
- It performs exactly the `call_llm.go:900-907` sequence, then
  `handlers.ProbeLLMCall` (resolveLLMCall → drivers.GetDriver → prepareHistoryForLLM →
  StreamResponse).
- Credentials repo: `5434/reliant`, opened read-only.
- Rerun:
  `go test -overlay /tmp/localprobe/overlay.json -run TestLocal -v -count=1 ./internal/workflow/runtime/activities/handlers/`

**Registry after discovery**
- `local-qwen3-latest` "Qwen3" and `local-nomic-embed-text-latest` "Nomic Embed".
- Both have `CanReason:false SupportsTools:true SupportsAttachments:true MaxContextWindow:200000 MaxOutputTokens:200000`.

| Case | Result |
|---|---|
| basic (`local-qwen3-latest`) | OK, resolved `local-qwen3-latest@local`, api `qwen3:latest`. Text = `<think>…</think>\n\nPONG`. thinking="" usage 0. 2.6s |
| basic `@local` | same, 1.3s |
| `Tags:["local"]` | **FAIL**: resolved `local-nomic-embed-text-latest` → `400 "nomic-embed-text:latest" does not support chat` |
| embed model by id | **FAIL**, same 400 |
| tool round-trip | turn 1: `get_secret_word` call `call_oqglh1rc`, finish `tool_use`. Turn 2 (with tool result `pineapple`): text with `<think>` + answer, finish `end_turn`. **Works.** |
| thinking `low` / `high` | effective thinking `""` (dropped by reconcile). Model thought anyway inline (713 / 304 content deltas). 0 thinking events. |
| temperature 0 / 1.5 | temp passed (`temp=0`, `temp=1.5`), accepted. Outputs `Apple` / `Banana`, both with an empty `<think></think>` prefix. |
| long context (~50k tokens, needle at line 1500) | **WRONG ANSWER** `2999`, no error. Ollama log: `truncating input prompt limit=4096 prompt=49924`. |
| usage (all cases) | `TokenCount:0 InputTokens:0 OutputTokens:0` |

Not tested: LM Studio, llama.cpp and vLLM (none installed). Their behavior is
inferred from documentation only.

---

## 4. Design options

### 4a. Where a local-model request executes

Existing pieces worth reusing **[code]**:

- **Daemon command channel.**
  - `DaemonCommandRequest{command_type, payload, timeout_ms}` / `DaemonCommandResponse`
    (`proto/reliant/v1/tools_daemon.proto:409-463`)
  - sent from worker/api over NATS by `NATSDaemonRouter.SendDaemonCommand[ToDaemon]`
    (`internal/toolexec/daemon_router_nats.go:649, :671`)
  - with selector routing (`daemon_router_select.go:35-50`) and chunking up to
    32 MB (`nats_chunked_request.go:29`)
  - daemon handlers registered via `RegisterCommand("…")` in
    `internal/toolexec/daemonruntime/`, about 90 commands today
- **Precedent for "worker asks daemon to perform I/O on the user's machine":**
  - `mcp.call_tool` (`internal/toolexec/mcp_binder.go:130`)
  - `fs.*` (`internal/daemon/remote.go`)
  - `auth.start_oauth`, which binds localhost on the user's machine
    (`daemonruntime/cmd_auth.go:16`)
- **Precedent for daemon → server streaming over NATS:**
  - process output (`ProcessOutputChunkMessage`, `tools_daemon.proto:533-540`;
    `SubscribeProcessOutput`, `daemon_router_nats.go:1063`)
  - terminal output (subscribe-then-publish ordering, `daemon_router_nats.go:1030-1060`)
- **Ports:** `DaemonHeartbeat.detected_ports` (`tools_daemon.proto:121-126`)
  piggybacks per-daemon facts on the heartbeat. It is empty on macOS/local
  daemons today. Preview (`cmd/reliant/commands/preview.go`,
  `RELIANT_PREVIEW_URL_TEMPLATE`) is an **inbound HTTP** proxy to managed
  workspace pods, built by the control-plane workspace proxy. It is not usable
  for a user's laptop, and it is the wrong direction for this purpose.

| Option | How | Trade-offs | Failure modes | Size |
|---|---|---|---|---|
| **A1. Worker calls URL directly** (today) | Driver in the worker dials `base_url`. | Zero new transport. Correct only when the endpoint is reachable from the worker: dev, self-host, or a public/tunnelled URL (Cursor's model). | User localhost is unreachable in cloud. SSRF from a shared worker into the cluster (D13). | S (already exists). Still needs D1–D4 fixed. |
| **A2. Daemon relays LLM HTTP** (recommended) | New streaming daemon command, e.g. `llm.http`. The worker's `local` driver serializes the OpenAI request; the daemon POSTs to its configured endpoint and streams SSE chunks back over NATS; the driver parses the chunks exactly as now. | Works wherever the daemon runs: laptop, Electron, cloud workspace with a GPU sidecar, or a LAN box. Reuses NATS, auth, daemon selection, chunking, and the streaming-subscription pattern. The SSRF surface moves to the user's own machine, where they already have shell. The worker never needs network reach. | Daemon offline: fail fast with "your machine X is offline". Daemon busy/slow: the per-request in-flight limit (`nats_bridge.go:90-110`) must not starve tool calls. Long generations exceed the 330s NATS tool timeout, so a streaming protocol with idle-timeout, not request-timeout, semantics is required. Cancellation must propagate (`tool_cancel`-like). Adds a hop of NATS latency per chunk (small next to local token rates). | M–L. Proto: new streaming message pair (or reuse `ProcessOutputChunk`-style with a request id). Daemon handler (~200 LOC). Router method + driver transport (`option.WithHTTPClient` with a `RoundTripper` that tunnels through NATS, so the driver is unchanged). Cancellation and timeouts. |
| **A3. Electron-only direct** | The renderer or Electron main calls Ollama. | No server change for desktop. | Breaks the architecture: the LLM loop runs in the worker (Temporal activity), not the client. Would need a second agent loop. No web or CLI support. | XL. Reject. |
| **A4. Generic daemon TCP/HTTP port-forward** | Expose `localhost:<port>` on the daemon's machine as an address the worker can dial (a reverse tunnel, like ngrok/frp, over NATS or WebSocket). | Generic: also serves future "worker hits user's dev server" needs. The driver is unchanged (just a URL). | A tunnel is a much larger security object (arbitrary TCP into the user's machine from the cloud). Per-connection lifecycle. Harder to scope to "LLM endpoint only". | L–XL |
| **A5. User-provided public URL** | Document tunnels (Cloudflare/ngrok) plus an API key. | No work. This is how Cursor does it. | Burdens the user. Exposes their GPU to the internet. | XS, and should be supported anyway as "remote OpenAI-compatible endpoint" on A1. |

A2's transport detail: implement a `http.RoundTripper` in the worker, passed to
`llm.NewOpenAISDKClient` via `option.WithHTTPClient`. Its `RoundTrip`:

1. sends `{method, path, headers, body}` to the selected daemon
2. returns an `http.Response` whose `Body` is a pipe fed by NATS chunk messages

The local driver and its SSE parsing then stay identical, and the same tunnel
serves `/v1/models` and `/api/show` for discovery and the test-connection
button. Allow-list the daemon side to the configured endpoint's origin plus the
`/v1/*`, `/api/show`, `/api/tags` and `/api/v0/models` paths, so this is not a
generic proxy.

### 4b. Where the config lives

| Option | Trade-offs |
|---|---|
| **B1. Project YAML** (today, worker side) | Wrong scope. The endpoint is a property of a machine, not a repo. Collaborators share a repo but not a GPU. It is also the SSRF vector (D13). Remove it. |
| **B2. Per-user setting (DB)** | Easy UI. But a user with a laptop and a GPU box has two endpoints, and "localhost" means a different thing on each daemon. On its own it still needs a "which machine" field, which turns it into B3. |
| **B3. Per-daemon** (recommended) | The daemon owns its endpoint list. Source: `~/.reliant/config.yaml` `models.providers.local` (already user-scoped by `loader.go:528`), plus auto-detection of well-known ports on the daemon's host (11434 Ollama, 1234 LM Studio, 8080 llama.cpp, 8000 vLLM) via `GET /v1/models`. The daemon publishes `{endpoint, kind, models[{id, ctx, caps}]}` with `DaemonRegister.capabilities`/`labels` or a new register/heartbeat field, the same piggyback as `detected_ports`. The server stores it per daemon row. The catalog lists `local` models for the user's online daemons. A UI edit writes the daemon's config via a daemon command (the same as other config edits). |
| **B4. Hybrid** | B3 for local endpoints, plus A1-style "remote OpenAI-compatible endpoint + key" stored per user in the existing provider-key table (a URL + key, for vLLM on a server or Ollama Turbo). This covers the remote GPU without a daemon. |

Model identity under B3: `local-<daemonShort>-<sanitized>` is ugly and unstable.
Better: keep the model id as `<sanitized>@local` and carry the **daemon id in the
selector/driver config**, with a default of "the chat's worktree daemon, else the
user's preferred local daemon". The picker groups local models by machine name.

### 4c. UI needs

1. **Settings → Models → "Local models" section** (replaces the dead card), one
   row per daemon/machine (name, online state from the existing daemon status
   hooks):
   - detected endpoints with kind badges, a "+ Add endpoint" (URL) control, and
     **Test connection**. Test sends a daemon command that hits
     `/v1/models` + `/api/show`, then returns latency, the model list and any
     errors.
   - per-model toggles (hide embeddings by default) and overrides: context
     window, supports tools/vision/thinking, default temperature. Store these in
     the daemon config `models.custom` or a per-user override table.
   - a warning when the server's context is smaller than Reliant's prompt
     (Ollama `OLLAMA_CONTEXT_LENGTH` 4096). Offer a fix: send `num_ctx` per
     request, or show the env var.
2. **Picker:** a "Local · <machine>" group; disabled with a tooltip when that
   daemon is offline.
3. **Errors in chat:** a typed error ("Local model on *Sean's MacBook* is
   unreachable: daemon offline / connection refused at localhost:11434 / model
   not pulled"), not the current generic API-key message.
4. **Onboarding (optional):** if the daemon auto-detects Ollama, offer it.

### Recommendation and devil's advocate

Recommend **A2 + B3 (+ B4 for remote endpoints)**.

Devil's advocate:

- *"A2 is a big build for a niche feature; just do A1 and tell cloud users to tunnel."*
  A1 is not actually cheap. It still needs D1–D4 fixed, it ships an SSRF
  primitive in a multi-tenant worker, and it fails silently for the default
  user (desktop app plus cloud worker) who has Ollama on localhost. Most of A2's
  machinery (routing, chunking, the streaming subscription) exists already.
- *"Streaming tokens over NATS through the daemon adds latency and load."*
  Local generation runs at about 20–100 tok/s. Batching chunks every ~50 ms
  keeps the message rate trivial next to terminal output, which already flows
  this way.
- *"Per-daemon config means models disappear when the laptop sleeps."*
  That is the truth: the model is unusable then. The UI must say so, and
  workflows must fail fast with a clear error rather than retrying for a minute
  (`driver.go:430-438`).
- *"Which daemon runs the LLM vs the tools?"* They can differ. A chat's tools run
  on the worktree daemon, but its local model may live on the GPU box. That is
  why the daemon id must be explicit in the model selection (B3 identity), not
  inferred.
- *"Remote GPU with no daemon?"* That is B4 (URL + key, worker-direct). It should
  be gated to non-private address ranges in hosted deployments, to avoid SSRF.
- **Risk:** the daemon's in-flight limit and the 330s request timeout were sized
  for tool calls. The LLM relay needs its own subject, limit and idle-timeout,
  not the command path as-is.

---

## 5. Prior art (web, 2026-10-04)

- **Zed** (zed.dev/docs/ai/use-a-local-model):
  - first-class Ollama / LM Studio / llama.cpp providers that **auto-discover**
    models, with `auto_discover: false` plus a manual `available_models` list
    per model (`max_tokens`, `supports_tools`, `supports_thinking`, `supports_images`)
  - for Ollama, sends **`num_ctx`** per request (default 4096, configurable via
    `context_window`)
  - llama.cpp context is read from `/props`
  - runs client-side, so localhost is always the user's machine
  - **Copy:** per-model capability overrides, sending `num_ctx`, discovery with
    a manual fallback.
- **Continue** (docs.continue.dev, Ollama provider): capabilities are
  auto-detected (from Ollama model info), with explicit `capabilities: [tool_use, image_input]`
  overrides for custom names. It also runs client-side.
  **Copy:** read capabilities from `/api/show` rather than assuming them.
- **OpenCode** (opencode.ai/docs/providers): a custom provider via
  `@ai-sdk/openai-compatible` with `options.baseURL` and an explicit `models`
  map, including per-model `limit.context`/`output`. It runs locally.
  **Copy:** an explicit per-model context/output limit in config.
- **Cline:** a VS Code extension, client-side Ollama/LM Studio providers with a
  base URL and model picker. It warns about small default context and recommends
  raising `num_ctx` (based on general knowledge; the docs URL fetch 404'd).
- **Cursor** (forum.cursor.com, multiple guides): requests are proxied through
  Cursor's cloud, so **localhost does not work**. Users must expose Ollama via
  an ngrok/Cloudflare tunnel and set "Override OpenAI Base URL". This is exactly
  our distributed-mode problem; Cursor chose A5. Native local support "without
  tunneling" is an open request.

**Takeaway:** every tool that "just works" locally does so because its LLM
client runs on the user's machine. Reliant's daemon *is* the component on the
user's machine, so A2 gives us Zed/Continue ergonomics with a cloud agent loop.
Cursor shows what happens without it.

---

## 6. Recommended design and ordered implementation outline

**Design.** The local endpoint is a property of a daemon. The daemon:
- auto-detects and reads the user-scoped config
- probes `/v1/models` + `/api/show` (or `/props`, or `/api/v0/models`) for real
  context and capabilities
- publishes `{endpoint, models+caps}` per daemon

The server stores this per daemon. The catalog lists those models for the
user's online daemons. The worker's `local` driver sends requests through a NATS
`llm.http` streaming relay to the selected daemon. The driver:
- parses reasoning
- sends `num_ctx` for Ollama
- reports usage

No process-global local state remains. Remote OpenAI-compatible URL + key (B4)
is a per-user provider credential, called worker-direct, with private ranges
blocked in hosted mode.

Ordered steps (each independently shippable):

1. **Stop the bleeding (S).**
   - Delete `call_llm.go:900-907` and the `SetLocalConfig`/`GetLocalConfig`
     globals, along with the `helpers.go:214-224` branch.
   - Make `InitGlobalRegistryWithDiscovery` non-global, or remove it.
   - Remove `models` from the worker's project/local YAML merge, so it matches
     `loader.go:528`.
   - This fixes D1–D4 and D13. Local models stop working in dev until step 4,
     which is acceptable: no launch, no backwards compatibility.
2. **Driver correctness (S–M)**, testable against Ollama now with the
   `/tmp/localprobe` harness:
   - split `<think>` / read `reasoning`/`reasoning_content` into `EventThinkingDelta`
   - capture the trailing usage chunk (input + output) before emitting `EventComplete`
   - send `num_ctx` (Ollama `options` via extra body) equal to the model's
     configured context
   - map thinking levels only to levels the model reports (Ollama
     `think: true/false` or low/med/high)
   - give discovery HTTP a timeout, keep the `base_url` path prefix, and fail
     fast on connection refused
   - fixes D6, D9–D11
3. **Real capabilities (S):**
   - read Ollama `/api/show` (capabilities, `*.context_length`), llama.cpp
     `/props`, and LM Studio `/api/v0/models`
   - drop non-`completion` models
   - default unknowns conservatively (tools=false, ctx=8192)
   - support per-model overrides in config
   - fixes D7, D8, D12
4. **Daemon relay (M–L):**
   - proto streaming request/chunk/cancel messages
   - daemon `llm.http` handler with an origin + path allow-list
   - `NATSDaemonRouter` streaming method
   - a `RoundTripper` for the openai-go client
   - its own NATS subject, in-flight limit and idle timeout
   - add a worker/daemon fixture test that exercises it locally
5. **Per-daemon publication (M):**
   - daemon auto-detect plus config read
   - publish via register/heartbeat
   - persist on the daemon row
   - `ListModels` adds local models per online daemon
   - the selector carries the daemon id
   - typed "daemon offline / endpoint unreachable" errors
6. **UI (M):**
   - a Settings "Local models" section per machine (detect, add URL, test
     connection, per-model toggles and overrides, context warning)
   - picker group with offline state
   - delete the dead `providerConfigs.local` entry
7. **Remote endpoint provider (S–M, B4):** URL + optional key as a user
   credential, worker-direct, SSRF guard in hosted mode.
8. **Probe coverage:** extend `reliant-dev models probe` with a `--local`
   matrix (basic, tools, thinking, temperature, long context) so regressions
   show up like the other providers.
