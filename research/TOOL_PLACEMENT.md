# Tool placement: where tools and MCP servers may execute

**Status:** design only. Nothing here is implemented. Read with `TRIGGERS.md`
and `DELEGATED_CREDENTIAL.md`.

**Requirement.** Some MCP servers need the user's machine: stdio processes,
filesystem access, local SQLite. Those must never be spun up on a Reliant
server. MCPs must never wake a daemon on their own. Only a registered
workflow's trigger wakes a daemon, and the trigger names which daemon. Later,
server-side integrations (trigger → email, trigger → GitHub PR) are a
legitimate server execution path. The api-server, worker and gateway never
touch a user's filesystem.

---

## 0. Executive summary

1. **Can a stdio MCP be spawned on the api-server today? Not in practice:
   the path is wired and armed, but nothing calls it.** The api-server builds
   a `LocalToolExecutor` backed by a real, in-process `mcp.Manager`
   (`internal/serverapi/run.go:226,356-358`). That manager *can* exec
   `npx`/`uvx`, and its fallback config source is the server's local
   filesystem plus a built-in stdio `chrome-devtools` server. However, the
   api-server's `RemoteExecutor` is only stored in `grpc.Server.toolExecutor`
   and `DaemonServer.toolExecutor` (`internal/grpc/server.go:49,493`,
   `daemon_server.go:35,111`). Those fields are assigned and **never read**.
   No api-server code path calls `ExecuteTool`. So this is a **latent defect**:
   a loaded gun with no trigger finger. One innocent call (say, a future
   "run this tool now" RPC) would spawn `npx chrome-devtools-mcp` on the
   api-server pod. Minimal fix in §4.1: delete that wiring.
2. **The worker cannot spawn MCP processes.** Its server executor binds
   `NewDaemonMCPContextBinder` (`internal/serverworker/run.go:284,314`), so
   every MCP operation becomes a NATS daemon command. There is no
   `mcp.Manager` in the worker process.
3. **A live defect, and the more important one: MCP traffic wakes the
   user's *default* daemon, blindly.** `daemonMCPRuntime` sends every command
   through `SendDaemonCommand` (`internal/toolexec/mcp_binder.go:74,125,150,172`).
   That resolves with a nil selector (`daemon_router_nats.go:112-113,557-571`)
   and, through the control plane, calls `ResumeDaemon` on a suspended daemon
   (`daemon_router_nats.go:129,264,334-349`). Two consequences:
   - **Every `call_llm` turn wakes a daemon.** It calls
     `EnsureProjectServersLoaded` unconditionally whenever a user id is in
     context (`activities/handlers/call_llm.go:1984-1996`). This happens even
     if the node declares no MCP tools, and even if the workflow is all
     server-placed. So the preflight's "no daemon needed" verdict is
     contradicted the moment the LLM node runs. A "trigger → email" workflow
     would still resume the user's laptop VM.
   - **MCP calls ignore the run's daemon.** Built-in daemon tools honour
     `req.DaemonSelector` (`remote_executor.go:331-332`). MCP tools go
     `loc == ""` → `executeOnServer` (`remote_executor.go:159-166,378-386`) →
     daemon MCP runtime → **default** daemon. So a trigger pinned to daemon B
     runs its built-in tools on B and its MCP tools on whichever daemon
     default resolution prefers (`local` first, `:148-154`).
4. **Preflight is blind to MCP tools.** `IsDaemonTool` is built from the
   built-in registry only (`activities/register.go:238-250`). An `mcp__*`
   name is not in that registry, so it reads as "not daemon". Today this is
   masked, because `call_llm` wakes the daemon anyway (finding 3). Once that
   is fixed, it becomes a real hole.
5. **Proposal:** one concept, `Placement` (`daemon` | `server` | `any`). It
   replaces `ToolLocation` and is attached to both built-in tool definitions
   and MCP server definitions. MCP tools inherit their server's placement.
   - stdio is *always* `daemon`, structurally.
   - `server` is allowed only for HTTP transport **and** a curated
     integration whose credentials are owned server-side (Connections).
     User-typed config can never claim `server`.
   - Enforcement lives in the transport constructor: a server-role `mcp`
     client factory has no stdio branch at all.
   - Daemon traffic for MCP is pinned to the run's daemon and never resumes.
     Waking happens only at trigger fire time, in the launcher.

---

## 1. Current state, with evidence

### 1.1 The three processes and their MCP wiring

| Process | Tool executor | MCP binder | Can exec a process? |
|---|---|---|---|
| api-server (`serverapi.Run`) | `RemoteExecutor` + `LocalToolExecutor` (`run.go:244,356-358`) | **`NewLocalMCPContextBinder(mcp.NewManager())`** (`run.go:226,357`) | Yes, if `ExecuteTool` were called. It never is (§1.2). |
| worker (`serverworker.Run`) | `RemoteExecutor` + `LocalToolExecutor` (`serverworker/run.go:282-289`) | `NewDaemonMCPContextBinder(router)` (`:284`, and `:314` for `call_llm`) | No: no manager exists in the process. |
| daemon (`daemonruntime.Start`) | `LocalToolExecutor` (`daemonruntime/runtime.go:314-318`) | `NewLocalMCPContextBinder(mcpManager)` (`:315`) | Yes, and it should. This is the only legitimate host. |

`internal/grpc/services/mcp.go:66-71`: MCP *management* RPCs (list, add,
status) are proxied to the daemon (`NewDaemonMCPProxy`). That surface is
already daemon-only.

### 1.2 Why the api-server path is latent, not live

- `RemoteExecutor` is passed as `grpc.Config.ToolExecutor` (`serverapi/run.go:394`).
- `grpc/server.go:484-485` calls only `SetDaemonRouter` on it. Then it stores
  it in `Server.toolExecutor` (`:493`) and `DaemonServer.toolExecutor`
  (`daemon_server.go:111`).
- `rg 'toolExecutor' internal/grpc` shows only declarations and assignments.
  No method calls.
- All `ExecuteTool` callers are Temporal activities (`execute_tools.go:649`,
  `run_executor.go:113`, `register.go:168-210`). Those run in the worker,
  through `workersetup` (`setup.go:45,102`). The api-server never starts a
  worker. It only imports `workersetup` for `ChatWorkflowLookup`
  (`serverapi/run.go:231,234`).

**If** it were invoked, the path is:
`RemoteExecutor.ExecuteTool` → (`mcp__*` has `loc==""`) `executeOnServer`
(`remote_executor.go:164-166`) → `LocalToolExecutor.executeTool` →
`mcpBinder.Bind` → local `Manager` → `EnsureProjectServersLoaded`
(`local_executor.go:170-175`) → `loadProjectServersFromConfig`. With no
resolver set (the api-server sets none), that call reads
`configloader.LoadMCPServersFromProjectScopes(projectPath)` from the **server's**
disk, then merges `builtinMCPServers()` (`mcp/manager.go:1433-1470`). The
built-ins include stdio `npx chrome-devtools-mcp` (`manager.go:1357-1420`).
Next: `AddServer` → `client.go:85-146` → `exec.Command(resolvedCmd…)` +
`mcp.CommandTransport`. **There is no guard anywhere on that path that knows
it is running on a server.** The comment at `manager.go:1441-1442` ("This
path is used by the daemon runtime where filesystem access is expected") is
the entire guard.

### 1.3 Routing in `RemoteExecutor`

`remote_executor.go:155-170`, `:376-386`:

- `toolRunsOn(name)` looks in the **built-in** registry only.
- `daemon` → `executeOnDaemon` (honours `DaemonSelector`, `:331`).
- `server` / `any` / **unknown ("")** → `executeOnServer`.

Every MCP tool is "unknown", so it executes "on the server". On the worker,
that means "on the server, which then proxies to the default daemon over a
side channel". The comment at `:164` ("unknown tools (e.g. MCP) execute on
the server") states this as intent. It is the root of findings 3 and 4.

### 1.4 `ToolLocation` today

`internal/llm/tools/registry.go:119-134`. Note the comment on
`ToolRunsOnDaemon`: "In distributed mode, it runs on the server but calls
daemon for FS/exec ops". That is stale. `RemoteExecutor` actually ships
`daemon` tools to the daemon whole. Consumers:

- `RemoteExecutor` routing (above).
- Preflight `IsDaemonTool` (`activities/register.go:238-250`, `preflight.go:150-170`).
- `ListAvailableToolsForLocation` (`factory.go:546-552`), which is how the
  daemon advertises capabilities (`daemonruntime/runtime.go:311`).

### 1.5 MCP config

`config.MCPServer` (`internal/config/config.go:29-…`): Command / Args / Env /
Type (`stdio|sse|http`, `:19-25`) / URL / Headers / Dir / DirScoped / Enabled.
It has no placement field. `recommended-mcps.yaml` is almost all `type: stdio`
(npx/uvx/bash). Supabase is the one `type: http` (`:98-101`). The built-in
`reliant-docs` is HTTP (`manager.go:1359-1363`). HTTP headers are attached
via `headerTransport` (`client.go:152-171`). Their values come from user
config, which is resolved on the daemon.

---

## 2. Proposed model: one concept, `Placement`

### 2.1 The type

Rename `tools.ToolLocation` to `Placement` (it is pre-launch, so there is no
alias). It lives in a small leaf package, e.g. `internal/placement`, so that
`config`, `mcp`, `tools` and `toolexec` can all import it:

```go
type Placement string
const (
    Daemon Placement = "daemon" // needs the user's machine: process, FS, local state
    Server Placement = "server" // runs in the worker; needs no daemon, ever
    Any    Placement = "any"    // pure network/compute; runs where the run already is
)
```

`any` keeps its current meaning: "no requirement". It executes where it is
cheapest, which is server-side, and it never *causes* a daemon to be needed.

### 2.2 Where placement is declared

| Thing | Declared by | Rule |
|---|---|---|
| Built-in tool | `ToolDefinition.RunsOn` (renamed `Placement`) in the registry | Unchanged values. |
| MCP server | **derived**, not user-declared (see 2.3) | stdio ⇒ `daemon`. |
| MCP tool `mcp__s__t` | inherited from server `s` | Never per-tool. A server is one process with one trust boundary. |

### 2.3 Fail-safe defaults: who may say `server`

`placement` is **not a field users write in `.mcp.json` / project config.**
It is computed:

```
placementOf(server):
  if server.Type == stdio (or Command != "")       -> Daemon   // always, no override
  if server is a curated server integration        -> Server   // from the server-side catalog
  otherwise (user-config http/sse)                  -> Daemon
```

Why user HTTP MCPs are still `daemon`:

- Their URL may be `localhost` or a LAN host that only the laptop can reach.
- Their headers carry the user's secrets, which today live in daemon-side
  config.
- Calling an arbitrary user-supplied URL from our worker is SSRF from inside
  our network.

`server` is earned by being a **curated integration**: an entry in a
server-owned catalog (`internal/mcp/catalog` already exists and is the
natural home). That entry has a fixed URL (or a host allowlist) and gets its
credentials from Connections (§5). The initial set could be the hosted Gmail,
GitHub and similar integrations the trigger roadmap needs. Built-ins like
`reliant-docs` (public, credential-free HTTP) can be `Any`.

An optional user-facing escape hatch, "this remote HTTP MCP is safe to call
from Reliant's servers", is deliberately left out of v1. See open
questions.

### 2.4 Composition: one concept

- `toolRunsOn` becomes `placementOf(toolName)`. It consults the built-in
  registry first, then for `mcp__<server>__*` the resolved server placement.
  It **never** returns `""`. An unknown tool is an error, not "server".
- Preflight's `IsDaemonTool` becomes `PlacementOf(name) == Daemon` and covers
  MCP tools. Static analysis cannot see which MCP servers a user will have,
  so a literal `mcp__*` glob in a filter counts as `Daemon` unless every
  matched server is a curated server integration. The CEL case stays
  conservative (`preflight.go:150-153`).
- The daemon still advertises `ListAvailableToolsForLocation(Daemon)`.

---

## 3. How placement drives behaviour

### 3.1 Routing (`RemoteExecutor.ExecuteTool`)

```
switch placementOf(tool):
  Daemon       -> executeOnDaemon(req)            // MCP too: one "mcp.call_tool" via the run's DaemonSelector
  Server, Any  -> executeOnServer(req)            // worker in-process; server MCP runtime (§4) only
  unknown      -> error (not a silent server run)
```

Daemon MCP calls move off the side channel (`daemonMCPRuntime` +
`SendDaemonCommand`) onto the same pinned path built-in daemon tools use.
That means the selector or the run's recorded daemon id
(`SendDaemonCommandToDaemon`, `daemon_router_nats.go:579`). An MCP call from
a trigger run lands on the trigger's daemon.

### 3.2 Discovery (`call_llm`)

`call_llm` must stop asking the daemon on every turn:

- Server-placed MCP tools are listed from the server-side catalog runtime.
  No daemon is involved.
- Daemon MCP tools are listed from the **run's** daemon. That happens only
  if the node's tool filter can reach `mcp__*` daemon tools, and only if the
  daemon is already attached. A run that the preflight judged daemon-free
  must never send `mcp.ensure_loaded`.

### 3.3 "MCPs never wake daemons"

There are two resolution modes, and the rule becomes structural:

- **Wake-capable:** `resolveDaemonID` with the control-plane `ResumeDaemon`
  call. It is callable only from the launcher at trigger fire time / chat
  start, for the daemon the trigger names (`TRIGGERS.md`). It is authorised
  by the `daemon:resume` scope in `DELEGATED_CREDENTIAL.md`.
- **Non-waking:** everything at tool time, built-in and MCP. It targets the
  run's pinned daemon id. If that daemon is not attached, return
  `ErrDaemonPending` / `FailedPrecondition`. Never resume.

Implementation shape: split the router's API so that tool-time callers hold
an interface without a resume method. Do not add a `wake bool` parameter
that someone can flip.

### 3.4 Preflight

`RequiresDaemon` (`preflight.go:53`) is true iff some reachable tool is
`Daemon`, or there is a `run` node, or there is an explicit `daemon`. With
MCP covered (§2.4) and `call_llm` fixed (§3.2), a "trigger → email" workflow
whose tools are all `Server`/`Any` gets `RequiresDaemon == false`. The
trigger then needs no daemon field, the launcher wakes nothing, and no tool
can wake anything.

---

## 4. Enforcement point: make the stdio-on-server bug unconstructible

### 4.1 Minimal fix now (independent of the rest)

In `internal/serverapi/run.go`:

- delete `mcpManager := mcp.NewManager()` (`:226`) and its `Close` (`:534-538`)
- delete `serverExecutor` + `SetServerExecutor` + `SetDaemonClientFactory` (`:355-361`)
- if `grpc.Config.ToolExecutor` remains unused, delete it and both
  `toolExecutor` fields (`grpc/server.go:49,75,484-485,493`,
  `daemon_server.go:35,45,111`).

After this, the api-server has no MCP manager. Add a test
(`serverapi` or a build-graph check) asserting that `serverapi` does not
transitively construct `mcp.Manager`. A cheap version: a
`go list -deps` check that `internal/serverapi` does not import
`internal/mcp`, or does so only through the `catalog` types.

### 4.2 Structural guard

Split the client factory by host role:

- `mcp.NewDaemonClient(cfg)`: today's `NewClient`, with all transports. It
  is constructed only by `daemonruntime`.
- `mcp.NewServerClient(entry catalog.ServerIntegration, creds)`: **accepts
  no `config.MCPServer` at all.** It takes a catalog entry (fixed URL / host
  allowlist) and builds only `StreamableClientTransport`. The package that
  exports it does not import `os/exec`. Make this its own package,
  `internal/mcp/serverclient`, and add a depguard / `go list` test that
  forbids `os/exec` in it.

Why the type, and not a runtime `if Type == stdio { return err }`: a runtime
check is one forgotten branch away from the current state. A constructor
whose inputs cannot express a command is not. Keep a belt-and-braces runtime
check as well. `Manager` gets a `Role` set at construction, and `AddServer`
refuses stdio when `Role == Server`. This covers anyone who reaches for
`mcp.NewManager()` in a server process again.

### 4.3 Config sourcing

`loadProjectServersFromConfig`'s nil-resolver fallback to the local
filesystem (`manager.go:1440-1446`) is only correct on the daemon. With a
`Role`, the server role has no filesystem fallback and no
`builtinMCPServers()` stdio entries.

---

## 5. Credentials for server-placed MCPs (brief)

Today a remote HTTP MCP's `Headers`/`Env` come from user config, resolved
and held on the daemon. A server-placed integration cannot depend on that:
the daemon may be asleep, and the secret would have to transit to the
worker.

- Server-placed integrations get credentials from a server-side
  **Connections** store: per-user OAuth grants or tokens, encrypted at rest,
  resolved by the worker at call time, scoped to (user, integration). That
  store is the future credential vault and is out of scope here.
- Never copy daemon-side MCP headers to the server to "make it work". If a
  user's HTTP MCP needs a secret, it stays `daemon`.
- Trigger runs act as the user via the delegated `rlat_`
  (`DELEGATED_CREDENTIAL.md`). Integration calls should use a
  Connections-issued token, not that `rlat_`.

---

## 6. Phased plan

| Phase | Change | Files | Tests |
|---|---|---|---|
| **0 (now, tiny)** | Remove the armed-but-unused api-server MCP manager and server executor (§4.1). | `internal/serverapi/run.go`, `internal/grpc/server.go`, `internal/grpc/daemon_server.go` | Build; an import/construct guard test. |
| **1** | Stop blind wakes. MCP daemon calls go through the pinned run daemon (selector / recorded id) with **no resume**. `call_llm` skips `EnsureProjectServersLoaded` when the node cannot reach `mcp__*` daemon tools or no daemon is attached. | `internal/toolexec/mcp_binder.go`, `daemon_router_nats.go` (non-waking API), `remote_executor.go`, `activities/handlers/call_llm.go:1984-2000` | Test that `call_llm` on a server-only node sends zero daemon commands. Test that MCP calls with `DaemonSelector{B}` hit B. Test that a suspended daemon yields `ErrDaemonPending`, and that `ResumeDaemon` is not called. Write each test to fail first. |
| **2** | `Placement` type; rename `ToolLocation`; `placementOf` covers MCP; unknown → error; preflight covers MCP. | `internal/llm/tools/registry.go`, `factory.go`, `toolexec/remote_executor.go`, `workflow/runtime/preflight.go`, `activities/register.go`, `daemonruntime/runtime.go:311` | `remote_executor_test.go`, `preflight_test.go`, `preflight_network_tools_test.go`, `registry_network_location_test.go` (rename + MCP cases). |
| **3** | Role-split MCP clients (§4.2/4.3): `serverclient` package, `Manager.Role`, no FS fallback for the server role. | `internal/mcp/client.go`, `manager.go`, new `internal/mcp/serverclient/` | No-`os/exec` dep test. Test that `AddServer(stdio)` with the server role errors. |
| **4** | Curated server integrations: catalog entries carry `Server`. Worker gets a server MCP runtime fed by Connections. | `internal/mcp/catalog`, `serverworker/run.go`, Connections (separate design) | E2E: a trigger → email workflow fires with every daemon suspended, completes, and no `ResumeDaemon` is called. |

**Risks.**

- *Phase 1 can surface missing tools.* Workflows that silently relied on
  `call_llm` waking the default daemon will now see daemon MCP tools only
  when a daemon is attached or pinned. This is the intended behaviour, but
  the UI must show `ErrDaemonPending` clearly.
- *Default-daemon semantics for interactive chats.* An interactive chat
  without an explicit daemon still needs *a* daemon id recorded at
  `StartChat`. The launcher resolves it once (and may wake it, since the user
  is present). Tools then pin to it. Confirm that the launcher records it.
- *Preflight becomes stricter* for `mcp__*` filters: more workflows will be
  marked `RequiresDaemon`. That is correct, and it only changes the outcome
  for workflows that currently claim to be daemon-free.
- *Rename churn* `ToolLocation` → `Placement`: mechanical, roughly 40
  registry lines plus about 6 consumers.

---

## 7. Open questions

1. Should a user be able to mark their own remote HTTP MCP as server-safe?
   That would need an SSRF guard (deny private ranges), moving the
   credentials to Connections, and an explicit consent UX. The proposal is
   no for v1.
2. Is `any` worth keeping distinct from `server`? It differs only in intent
   ("would also be fine on the daemon"). Keep it if the daemon will ever run
   network tools locally for latency or egress reasons. Otherwise collapse
   it.
3. For interactive chats, should MCP calls ever resume a daemon? This doc
   says no: only `StartChat`/trigger launch wakes a daemon. A suspended
   mid-chat daemon would then surface "daemon asleep" instead of
   auto-resuming. Product call.
4. Where does the per-run daemon id live for tool-time pinning: on the run /
   chat row (`ActiveDaemonID`) or in `ToolRequest.DaemonSelector` derived
   from the workflow? Align with `TRIGGERS.md` launcher output.
5. Does the gateway process build any tool executor? A quick check found
   none, but it should be re-verified when phase 0 lands.
