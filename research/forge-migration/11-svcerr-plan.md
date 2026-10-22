# 11 — P7a: hand-built Connect errors → forge `svcerr`

Status: **plan, not implemented.** Measured on reliant `origin/main` `444afb7b`
(forge pin `2472900c2b41`), 2026-10-22. Every count below is reproducible with the
commands in §8.

## 0. Summary

- reliant has **1,242** hand-written `connect.NewError` calls in non-test,
  non-generated Go, in 73 files. **1,213** are under `internal/handlers/**`, and 29
  are in daemon plumbing and interceptors. Another **323** are in the generated
  `internal/handlers/mocks/*_mock_gen.go` (forge's proto-mock template, §2.6). The
  1,290 figure in PLAN.md included part of those.
- `svcerr.Interceptor` already runs innermost on both chains, the user chain
  (`internal/grpc/user_chain.go:116`) and the daemon chain
  (`internal/grpc/daemon_listener.go:93`), since the forge `19e73851` pin (#1220). So
  a handler can **already** return a bare `svcerr.X(...)`. Nothing on the server
  side has to change first.
- About three quarters of the sites (**~935**) build a fixed message with an
  explicit code. Converting them changes nothing on the wire, so a codemod can do it
  (§3).
- The other **~300** sites wrap or discard an underlying error under a guessed code.
  That is where the bugs are:
  - **386 sites discard `err` and pick a code.** The `ListProjects` bug is one of
    them (`project.go:744`: every error, including `deadline_exceeded`, becomes
    `internal`). 185 more map *any* error to `NotFound`, so a database outage reads
    as "chat not found".
  - **246 sites hand `err` to `connect.NewError`**, which **publishes `err.Error()`
    to the client**. 163 of them are `CodeInternal`, and they ship repo/driver text,
    encoder errors and daemon transport strings. Forge's svcerr redaction exists to
    stop exactly this, and the interceptor can't help because a `*connect.Error`
    passes through it untouched.
- `forgeconv-no-handler-error-mapping` reports **11** findings today, not 9 (§1.4).
  It is also silenced by any `svcerr` import in the file, so the codemod alone would
  make it go quiet without fixing anything. That is a forge lint defect (F-P7a-2).
- Forge work comes first and is small:
  1. `svcerr.WrapOr`, the classified-or-fallback primitive that `pkg/crud` already
     uses privately.
  2. `tdd.AssertConnectError` should read `svcerr.Code`.
  3. The proto-mock template should use svcerr.
  4. A `forge fix connect-errors` codemod.
  5. The lint fix.
- Risk: **medium**. The wire-identical half is low risk. The semantic half
  deliberately changes codes (Internal → NotFound / DeadlineExceeded / Canceled, and
  NotFound → Internal on real outages). The web keys on codes in about 40 places and
  on **message markers** in about 10 (§2.4), and those must survive.

## 1. Taxonomy of current sites

Method: each `connect.NewError(` call was parsed (balanced parens), its argument
shape classified, and the 4–8 lines before it inspected for `errors.Is/As`,
sentinel comparisons and `err != nil` guards. The heuristic is a census, not a type
check, and the boundaries between categories can be off by a few percent.

### 1.1 By category (hand-written, 1,242)

| Cat | Shape | Sites | Representative |
|---|---|---:|---|
| **A** | Explicit code + a message the handler wrote; no underlying error in scope | **498** | `run/run.go:27` `CodeInvalidArgument, fmt.Errorf("run_id is required")`; `workflow/workflow.go:463` `CodeAlreadyExists, fmt.Errorf("workflow name '%s' conflicts…", …)` |
| **B** | Wraps an underlying error with a guessed code (`fmt.Errorf("…: %w", err)` or bare `err`) | **246** | `worktree/worktree.go:2265` `CodeInternal, fmt.Errorf("failed to push: %w", err)`; `workflow/workflow.go:449` (repo); `daemon/daemon_proxy.go:41` (marshal); `worktree/worktree.go:420` `CodeInvalidArgument, err` |
| **C** | Branch of a switch/if-chain mapping domain sentinels or typed errors to codes | **81** | `catalog/rpc_list_input_options.go:115` `inputOptionsError`; `chat/chat_helpers.go:220` `launchErrorToConnect`; `model_endpoint/model_endpoint.go:50` `modelEndpointErr`; `daemonerr/daemonerr.go:95` `MachineStateConnectError` |
| **D** | Inside `if err != nil`, but `err` is **discarded** and the code is guessed (shape-identical to A) | **386** | `project/project.go:744` `ListProjects` → `CodeInternal, "failed to list projects"`; `worktree/worktree.go:189` *any* error → `CodeNotFound "worktree not found"`; `chat/chat_crud.go:261` |
| **E** | `nil` or a variable as the error | **29** | `daemon_registry/daemon_registry.go:41` `CodeUnauthenticated, nil` (21 `nil` sites: 17 Unauthenticated, 4 InvalidArgument); `connection/connection.go:41` `msg` |
| F | Already svcerr-shaped but forced into a `*connect.Error` | 2 | `tools_daemon/tools_daemon.go:234` `svcerr.Draining(…)`; `machinewake/machinewake.go:160` |

### 1.2 Category × code

| | Internal | InvalidArg | NotFound | FailedPrec | Unauthn | Unavailable | PermDenied | AlreadyEx | Unimpl | Deadline | Aborted | Canceled | ResExh | var |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 17 | 293 | 38 | 63 | 37 | 16 | 14 | 9 | 4 | 1 | 3 | 0 | 2 | 1 |
| B | 163 | 29 | 15 | 3 | 20 | 15 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 1 |
| C | 21 | 8 | 14 | 11 | 3 | 9 | 1 | 5 | 0 | 4 | 0 | 4 | 0 | 1 |
| D | **156** | 10 | **185** | 10 | 0 | 12 | 5 | 3 | 4 | 0 | 1 | 0 | 0 | 0 |
| E | 0 | 6 | 1 | 2 | 17 | 2 | 0 | 1 | 0 | 0 | 0 | 0 | 0 | 0 |

B, split by what produced the wrapped error: **daemon command** 62 (45 Internal),
**repo/store** 51 (42 Internal), **encode/marshal** 14, **other/local validation**
119 (62 Internal, 27 InvalidArgument where `err` came from a local validator).

### 1.3 Errors with details or metadata (keep their shape)

These are not conversions. They are the places svcerr's `WithDetail` / `WithReason`
must replace hand-built metadata:

- `connect.NewErrorDetail` + `AddDetail`: `forge/forge.go:943`, `forge/forge.go:1165`,
  `file_system/fs_proxy_checkout.go:106`, `machinewake/machinewake.go:161`
  (`DaemonWaking`, read by the web's `wakingDaemonId`).
- `Meta().Set(svcerr.ReasonHeader, …)`: `token/token_git.go:85` (`gitError`, read by
  the CLI's git credential helper) and `toolexec/bootstrap/foreign_daemon_id.go:38`.
  Both become `svcerr.WithReason(svcerr.X(msg), reason)`, which puts the same header
  on the wire.
- `Meta().Set("X-Reliant-Reason", …)`: `internal/grpc/forge_api_disabled.go:41`. This
  uses a **different header** that the web reads (`lib/forgeApiDisabled.ts`).
  **Leave it**: it is an interceptor (§5), and moving it to `x-forge-error-reason`
  is a web change.

### 1.4 The mapping helpers lint flags today (11)

`reliant forge lint --conventions` at the pin, built from forge `2472900c` (§8):

```
catalog/catalog_search.go:152        catalogSearchError      (5 sentinels)
catalog/rpc_list_input_options.go:115 inputOptionsError      (9)
chat/chat_crud.go:891                TerminateChat           (2)
chat/chat_crud.go:1820               loadMessagesError       (4)
chat/chat_helpers.go:220             launchErrorToConnect    (5)
chat/chat_interrupt.go:48            interruptThreadError    (3)
chat/chat_send_idempotency.go:68     asConnectError          (name)
connection/connection.go:20          connectionError         (3)
file_system/fs_proxy.go:201          validateWorkspacePath   (2)
model_endpoint/model_endpoint.go:50  modelEndpointErr        (5)
trigger/trigger_alerting.go:58       alertingError           (3)
```

`internal/daemonerr` holds the largest mapper (`DispatchConnectError`,
`MachineStateConnectError`). The rule never sees it because it scans only
`handlers/` directories. The 10 handler call sites that go through it are in
`background`, `daemon`, `file_system`, `forge`, `terminal`, `worktree`, and in
`daemoncmd` / `toolexec`.

### 1.5 In-process readers of a handler's `*connect.Error`

`connect.CodeOf(err)` returns `CodeUnknown` for anything that is not a
`*connect.Error` (connect v1.21.1 `code.go:221`). So every in-process reader of a
converted helper's result has to switch to `svcerr.Code(err)` **in the same change**:

- Non-test, inside handlers:
  - `forge/forge.go:422,428`
  - `account/account.go:152` (a control-plane client error, which really is a
    `*connect.Error`: keep it)
  - `connection/connection.go:25`
  - `project/project.go:1775`
  - `chat/chat_send.go:1536`
  - `chat/chat_send_idempotency.go:70`
  - `trigger/trigger_workflow.go:578`
- Non-test, outside handlers:
  - **`daemonerr.Unreachable`** (`connect.CodeOf(err) == CodeUnavailable &&
    contains "no daemon connected"`). The machine-wake path depends on it.
  - `daemonoffline/daemonoffline.go:69`
  - `toolexec/bootstrap/foreign_daemon_id.go:46`
  - `toolexec/daemon_router_nats.go`
- Tests: **326** `connect.CodeOf(` assertions in handler test files, which call
  `*Service` methods directly with no interceptor. Forge's own
  `tdd.AssertConnectError` (`forge/pkg/tdd/rpc.go:180`) has the same blind spot (§2.6).

## 2. Target per category

### 2.1 Decision table

| Cat | Target | Wire effect |
|---|---|---|
| A, `"<x> not found"` (187 of 203 const NotFound) | `svcerr.NotFound("<x>")` | identical |
| A, other NotFound text | `svcerr.WithMessage(svcerr.ErrNotFound, "<msg>")` | identical |
| A, every other code | `svcerr.<Code>("<msg>")`; `fmt.Errorf(f, args…)` → `svcerr.<Code>(fmt.Sprintf(f, args…))` | identical |
| A, value typed `*connect.Error` (helper returns `*connect.Error`) | `svcerr.ToConnect(svcerr.<Code>(…))`; the lane later retypes the helper to `error` | identical |
| E, `nil` | the bare sentinel `svcerr.ErrUnauthenticated` / `ErrInvalidArgument` | message `""` → `"unauthenticated"` (cosmetic) |
| D (err discarded) | `return nil, svcerr.WrapOr(err, svcerr.Internal("<same msg>"))` | **changes**: a classified err passes (NotFound / DeadlineExceeded / Canceled / PermissionDenied…); an unclassified one stays Internal with the same message and keeps `err` as the logged cause |
| D, NotFound guess | the same `WrapOr(err, svcerr.Internal("failed to get <x>"))`, with the store responsible for a classified not-found (L0) | **changes**: a real miss stays NotFound (from the store), an outage becomes Internal |
| D, guard pattern `err != nil \|\| x == nil` | split it: `err` → `WrapOr`; `x == nil` → `svcerr.NotFound("<x>")` | NotFound kept for the nil case |
| B, repo/store | `svcerr.WrapOr(err, svcerr.Internal("<summary>"))` | **changes**: error text is no longer published (a security fix); classified errors pass through |
| B, local validator (`CodeInvalidArgument, err`) | the validator returns `svcerr.InvalidArgument(msg)`; the handler does `return nil, err` (the interceptor wraps it) or `svcerr.Wrap(err)` | identical text, same code |
| B, encode/marshal | `WrapOr(err, svcerr.Internal("encode <x> failed"))` | text redacted |
| B / C, daemon command | `daemonerr.Error(doing, err)` (2.3) | codes and markers preserved |
| C (switch/if chains) | **move the mapping to the producer.** Its sentinels become svcerr kinds (`var ErrBadScope = svcerr.InvalidArgument("…")`), or its typed error implements the kind via `Unwrap() error { return svcerr.ErrX }`. Delete the helper; the handler does `svcerr.Wrap(err)` | identical by construction; tests pin it |
| context errors in C (`rpc_list_input_options.go:129,131`, `catalog_search.go:159`) | delete the branch: `svcerr.Wrap` maps `context.Canceled/DeadlineExceeded` itself | identical |
| details/reason (1.3) | `svcerr.WithDetail(svcerr.X(msg), detail)` / `svcerr.WithReason(svcerr.X(msg), reason)` | identical |
| F | return the svcerr value directly; drop the `connect.NewError` around it | identical |

`svcerr.WrapOr(err, fallback)` is new (F-P7a-1). Its semantics, which
`pkg/crud.mapClosureErr` already implements privately:

```go
// WrapOr is Wrap(err) when err is classified (IsClassified: a svcerr kind, a
// context cancellation/deadline, or a *connect.Error), and otherwise
// Wrap(WithCause(fallback, err)): the client reads fallback, the log reads err.
func WrapOr(err, fallback error) error
```

Why a forge primitive and not a reliant helper: about 600 reliant sites need exactly
this, control-plane needs it (182 `connect.NewError` sites remain there), and forge
already writes it twice inside `pkg/crud`. Without it, the only correct spelling is
two lines per site (`if svcerr.IsClassified(err) {…}; return WithCause(…)`), which is
the per-handler mapping the lint exists to stop.

Devil's advocate, `WrapOr` passing a `*connect.Error` through: a downstream
service's verdict would be forwarded verbatim. That is the leak `WithCause` exists
to prevent. In reliant the only remote Connect client a handler calls is
control-plane (`account.go:152`), and that site stays an explicit
`WithCause(svcerr.Unavailable(…), cpErr)`. The rule for lanes: **a call to a remote
Connect client never goes through `WrapOr`.** Excluding `*connect.Error` from
`WrapOr` would break mid-migration composition (reliant's own helpers still return
`*connect.Error` until their lane lands), so pass-through is the right default, and
forge's doc comment states it.

### 2.2 Where the domain sentinels live (L0)

Handlers can only `WrapOr`/`Wrap` once **the store says "not found" in svcerr's
language**. Today the store does not, consistently:

| Source | Shape today | Wrap sees |
|---|---|---|
| generated by-key gets (`store.GetWorktree`, `GetChatByID`, …) | `WithCause(svcerr.NotFound("<label>"), orm.ErrNoRows)` | NotFound ✅ |
| `internal/db/core` sentinels: `ErrChatNotFound`, `ErrWorktreeNotFound`, `ErrProjectNotFound`, `ErrTriggerNotFound`, `ErrTriggerEventNotFound`, `ErrTriggerRegistrationNotFound`, `ErrRunEventNotFound`, `ErrProviderAccountNotFound`, `ErrConnectionNotFound`, `ErrModelEndpointNotFound`, `ErrIntegrationWriteNotFound`, `ErrPlanNotFound` | `errors.New(…)` (unclassified; 64 uses in `internal/store`) | **Internal** ❌ |
| `core.*NameTaken` / `ErrWorkflowSlugTaken` / `ErrGenerationConflict` / `ErrDaemonOwnedByAnotherUser` / `ErrInvalidAccountOrder` / `ErrToolCallIDInAnotherChat` / `ErrSpawnReportSlotTaken` | `errors.New` | **Internal** ❌ |
| hand `_repo_ext.go` returning raw `sql.ErrNoRows` (115 mentions in `internal/store`), `repository_impl.go:1825–1901` matching `strings.Contains(err.Error(), "not found")` | unclassified | **Internal** ❌ |

L0 changes the sentinels at their declaration. Identity is preserved, because a
constructor returns a fixed pointer, so `errors.Is` keeps working everywhere:

```go
// internal/db/core/chat.go
var ErrChatNotFound = svcerr.NotFound("chat")                              // "chat not found"
// internal/db/core/project_worktree.go
var ErrProjectNotFound = svcerr.WithMessage(svcerr.ErrNotFound, "project not found or access denied")
var ErrWorktreeNameTaken = svcerr.AlreadyExists("a live worktree already has this name")
// internal/db/repository_impl.go
var ErrDaemonOwnedByAnotherUser = svcerr.PermissionDenied("daemon is owned by another user")
```

The store layer converts raw `sql.ErrNoRows` at the source, as forge's `orm` doc
prescribes ("ErrNoRows → svcerr.NotFound already flows"):
`svcerr.WithCause(svcerr.NotFound("<entity>"), orm.ErrNoRows)`. That keeps
`errors.Is(err, sql.ErrNoRows)` true for the 11 handler sites that branch on it
deliberately (absence as a default, e.g. the project config columns). Those branches
stay.

L0 adds one DB-backed invariant test in `internal/store`: for every by-key getter
a handler calls, a missing id gives `svcerr.IsNotFound(err)`.

`svcerr` must **not** learn `sql.ErrNoRows`. A raw ErrNoRows that reaches a handler
can be a row our own code requires, which is a server fault, so classifying it at
the store is the only place the meaning is known.

### 2.3 Daemon errors (`internal/daemonerr`) and the web's markers

`daemonerr` is reliant's one legitimate classifier: a daemon command's failure is
opaque text after NATS. It stays, but it **returns svcerr kinds instead of
`*connect.Error`**:

- `Error(doing string, err error) error` replaces `DispatchConnectError` and
  `MachineStateConnectError`, with the same decision tree and the same codes:
  - unreachable → `svcerr.WithClass(svcerr.Unavailable(msg), ClassUser)`
  - no daemon → `svcerr.FailedPrecondition(…)`
  - timeout → `svcerr.DeadlineExceeded(…)`
  - outdated → `svcerr.FailedPrecondition(daemonMsg)`
  - dir missing → `svcerr.NotFound`-kind with `WithMessage`
  - anything else → `svcerr.Unavailable(…)`
- `Unreachable` switches to `svcerr.Code(err)`.
- `toolexec.ErrDaemonPending` / `ErrNoDaemon` (today `WithClass(errors.New(…))`, which
  is unclassified, so a bare `Wrap` would make them **Internal "internal server
  error"** and erase the marker) become `svcerr.Unavailable("no daemon connected: …")`
  / `svcerr.FailedPrecondition("no daemon available")`, with their `WithClass` kept.
  `daemonoffline.go:28` likewise.

**Message markers the web reads, which must keep their exact text in the client
message** (svcerr publishes the *nearest* detail, so the marker must be in the
constructor's argument, not in an outer `fmt.Errorf`):

| Marker | Web reader | Server source |
|---|---|---|
| `no daemon connected` (+ `still starting` / `suspended`) | `lib/daemon-errors.ts:43,67` → `rpcErrorPolicy` "machine-wait", `useDaemonWait` | `toolexec/daemon_router.go:34`, `daemon_router_nats.go:463-465`, `daemonoffline.go:28` |
| `no daemon available` | `lib/daemon-errors.ts:53` | `toolexec/daemon_router.go:84`, `worktree/worktree_placement.go:53,59` |
| `chat has not started` (FailedPrecondition) | `lib/chatSendRouting.ts:39` | `chat/chat_send.go:686,1168` |
| `not suspended`, `resource_exhausted` / `compute limit` (FailedPrecondition) | `lib/daemon-resume.ts:28,69` | daemon_registry ResumeDaemon path / control-plane |
| `already exists` (fallback to AlreadyExists) | `ProjectPicker.tsx:96`, `ProjectPickerModal.tsx:119` | `project/project.go:391,566` |

The daemon's own reported text (`git push` rejected, a missing path, …) is today
published through `fmt.Errorf("failed to push: %w", err)`. It comes from **the
user's own machine to the same user** and is the actionable part of the toast. So
`daemonerr.Error` keeps it as the detail for daemon-reported failures and redacts
only router/transport text (NATS subjects, gateway hosts). This is an owner decision
(§7 Q1); the recommendation is to keep it.

A follow-up (P9-adjacent, **not** P7a): stamp `svcerr.WithReason` on each daemon
state (`daemon_pending`, `no_daemon`, `daemon_outdated`), switch the web from
message markers to `x-forge-error-reason`, and then retire the markers.

### 2.4 Behaviour changes, and who depends on them

Codes that **change by design**:

1. **Internal → classified** (D-Internal 156 + B-Internal 163). A repo deadline is
   now `DeadlineExceeded`, a cancel `Canceled`, a store not-found `NotFound`. Web:
   - `rpcErrorPolicy` stops filing these to Sentry: DeadlineExceeded → "deadline",
     NotFound / Canceled → "expected". That is the goal.
   - `queryRetry.ts:39-41` stops retrying NotFound.
   - The `ListProjects` bug is fixed here.
2. **NotFound → Internal on a real outage** (D-NotFound 185). Before, any DB error
   on a get read "not found". Web readers that currently take an outage as permanent
   absence:
   - `store/worktreeStore.ts` (marks the id unresolvable forever)
   - `components/runs/RunRouteLoader.tsx` (renders "not found")
   - `components/Projects/cloneTargets.ts`
   - `api/trigger-grpc.ts:633`
   - `hooks/dashboard-queries.ts`
   - `api/streaming-grpc.ts` (`isNotFound` stops a stream)
   - `components/inbox/InboxItem.tsx`

   After the change they get Internal, which is reported and retried. This is
   correct, but expect a **visible rise in Sentry `Internal`** for
   transient-DB-blip sites. It is real signal that was previously hidden.
3. **Message text** on B sites loses the `: <err>` suffix (redaction). The web never
   branches on those suffixes; only daemon text matters (2.3).
4. **`Unauthenticated` with nil** reads `"unauthenticated"` instead of `""`. Nothing
   reads it.

Codes that **must not change**, pinned by tests in their lane:

- Web:
  - AlreadyExists: `projectStore.ts`, `ProjectPicker*`, `useOnboardingComplete.ts`
  - Aborted: `ChatInput.tsx`, `chatSelection.ts` (selection version conflict →
    `chatselection.ErrVersionConflict`)
  - FailedPrecondition + marker / reason: `chatSendRouting`, `daemon-resume`,
    `accountRequired`, `forgeApiDisabled`, `api/fileSystem.ts`, `RepoSelector`,
    `GitHubConnectStep`, `forge-grpc.ts`
  - Unimplemented on `ProviderAccountService` (probe)
  - Unavailable + `no daemon connected`
- CLI (`cmd/reliant/cmd`):
  - `trigger.go:524` NotFound
  - `project.go:79,205` AlreadyExists on create
  - `workflow_supervise.go:58-73` NotFound / PermissionDenied / Unauthenticated /
    Unavailable / FailedPrecondition
  - `git_credential.go:92-98` FailedPrecondition / Unauthenticated /
    PermissionDenied, plus `token_git.go`'s reason header
  - `connection.go:324`
  - `daemon.go:609`, `daemon_instance.go:204` Unauthenticated
  - `internal/forgecred/forgecred.go:124,185-187` codes + reason

The CLI talks over the wire, so it sees only code changes, not the in-process type
change.

### 2.5 Logging

Most D sites log by hand (`logging.ErrorContext(ctx, "Failed to …", "error", err)`)
and then discard `err`. With `WrapOr`, the observe interceptor logs the cause, at
the class's level, with the trace id (`svcerr.Cause` in
`forge/pkg/observe/error_class.go:131`). Lanes **delete the manual log line next to
each converted return**: the api skill says "do not re-log it at the call site". As
a side effect, user-class errors stop logging at ERROR.

### 2.6 Gaps in forge (fix in forge, not reliant)

| # | Gap | Evidence | Fix | Needed by |
|---|---|---|---|---|
| **F-P7a-1a** | No exported classified-or-fallback primitive | `pkg/crud/crud.go:244-248` (`IsClassified` → pass, else `WithCause(Internal…)`), duplicated in `mapClosureErr` | `svcerr.WrapOr(err, fallback)`; `pkg/crud` calls it | D, B |
| **F-P7a-1b** | `tdd.AssertConnectError` uses `connect.CodeOf`, so a handler test calling a `*Service` method directly sees **Unknown** for a bare `svcerr.X(...)`, while the api skill says a bare return is fine ("the interceptor wraps it") | `forge/pkg/tdd/rpc.go:180` | assert `svcerr.Code(err)` (what the interceptor sends); same for `TableRPC`'s `WantErr` | every lane's tests |
| **F-P7a-1c** | The proto-service mock template emits `connect.NewError(connect.CodeUnimplemented, fmt.Errorf("handler for %s not yet implemented", …))` | `forge/internal/codegen/generator.go:452`, 323 sites in reliant's `internal/handlers/mocks` | `svcerr.Unimplemented("handler for X not yet implemented")` (not `ScaffoldStub`: an unprogrammed mock is not a scaffold) | cleanliness, and the gating rule below |
| F-P7a-1d (nit) | The generated store's NotFound label is the lowercased type name (`"accesstoken not found"`, `"chatswithactivity not found"`) | `internal/store/store_gen.go:36,594` | humanize the label (`"access token"`), or use the table's singular | nicer passthrough text |
| **F-P7a-2a** | `forgeconv-no-handler-error-mapping` is **suppressed for any file importing svcerr** (`importsSvcerr`), so a codemod adding the import silences the 11 findings without fixing them | `forge/internal/linter/forgeconv/no_handler_error_mapping.go:180` | drop the suppression; a real mapper beside `svcerr.Wrap` is still a mapper | gating |
| **F-P7a-2b** | Nothing flags the leak class: `connect.NewError(code, <an error value>)` in a handler publishes `err.Error()` and bypasses redaction | B = 246 sites | new rule `forgeconv-handler-connect-error` (warn → error): any `connect.NewError` in a `handlers/` tree, with the remediation table of §2.1 | gating |
| **F-P7a-2c** | No codemod | §3 | `forge fix connect-errors` | the A pass |

Not a gap: context errors (handled by `codeForRecognized`), sentinels for every code
(all 16), `WithMessage` for NotFound prose, `WithDetail` / `WithReason` / `WithClass`,
`Draining`, and the interceptor on serverkit chains.

## 3. Codemod vs hand conversion

**Recommendation: one forge codemod for the wire-identical half, and hand lanes,
following §2.1, for the semantic half.**

`forge fix` already hosts release codemods (`forge/internal/codemod`:
`composition-graph`, `operatorkit`, `tx-isolation`, plus `narrow-deps`), with
`--dry-run`, a clean-tree guard and a `Blocked` report. A `connect-errors` codemod
fits that shape, and **it serves every forge project**: control-plane still has 182
sites, 72 of them in handlers. It ships as forge PR F-P7a-2.

`forge fix connect-errors` spec (type-checked via `go/packages`, handler and
non-handler packages alike):

- **Rewrites** `connect.NewError(connect.Code<X>, E)` when `E` is one of:
  - `errors.New(<string>)`
  - `fmt.Errorf(<const format without %w>, args…)` where **no arg has type `error`**
    and no arg is a call to `.Error()`
  - `nil`

  The output follows §2.1's A/E rows:
  - NotFound `"<x> not found"` → `NotFound("<x>")`, other NotFound text →
    `WithMessage(ErrNotFound, …)`
  - `Errorf` → `Sprintf`
  - `nil` → the sentinel
  - an empty message is skipped
  - in a `*connect.Error`-typed context it wraps the result in `svcerr.ToConnect(…)`
  - imports are fixed up
- **Rewrites, in `_test.go` files of the packages it touched**, `connect.CodeOf(x)`
  → `svcerr.Code(x)`. This is safe: the two differ only for a non-nil,
  non-`*connect.Error`, non-svcerr error (Unknown vs Internal), and reliant asserts
  `CodeUnknown` **0** times in handler tests.
- **Reports, without rewriting**:
  - every non-test `connect.CodeOf` / `errors.As(…, *connect.Error)` in a touched
    package (§1.5)
  - every remaining `connect.NewError` with an error-valued argument (the B/C list,
    per file), so the lanes start from a worklist
- **Wire-identical by construction**: same code, and `Message()` equals the old
  text. The codemod's own test is a corpus of every shape with a
  `connect.Error`-vs-`svcerr.ToConnect` equality assertion.

In reliant it rewrites about **935 sites** (A 498 + D 386 + E-nil 21 + the C
branches with fixed text, minus the ~8 with error-valued args) and the 326 test
assertions. D sites become e.g. `svcerr.Internal("failed to list projects")`, still
discarding `err`. That is deliberate: semantics change only in reviewed lane PRs.

Why not a second, behaviour-changing codemod mode for D (`err` in scope → `WrapOr`):
D-NotFound is only correct once the callee's not-found is classified (L0), and the
nil-guard split (`err != nil || x == nil`) needs a reader. The lanes can script it
locally if their package turns out to be uniform. A forge mode that silently turns
404s into 500s is the wrong default.

Fallback if F-P7a-2 slips: the lanes do the A conversion by hand with the same
table. It is mechanical, but about 3× the review surface.

## 4. Lanes

Ordering and file ownership. "Owns" means a lane is the only one editing those files.

```
F-P7a-1 (forge pkg) ─┐
F-P7a-2 (forge fix + lint) ─┴─► forge release + reliant pin ─┐
R6 (in flight) ──────────────────────────────────────────────┼─► L0 ─► C0 (codemod PR) ─► L1 ∥ L2 ∥ L3 ∥ L4 ∥ L5 ─► Z (gate flip)
```

### Prerequisites (serial, one agent each)

**F-P7a-1 — forge `pkg/`.** Owns `pkg/svcerr`, `pkg/tdd`, `pkg/crud` (calls
`WrapOr`), `internal/codegen/generator.go:452` + its golden, and optionally the
store label (1d).
- Tests: `WrapOr` table (nil, every sentinel, context errors, `*connect.Error`
  passthrough, unclassified → fallback with `Cause` = original, fallback's reason
  and class honoured); `AssertConnectError` on a bare sentinel; the crud suite
  unchanged.
- Lint lane: `cd pkg && golangci-lint run ./svcerr/... ./tdd/... ./crud/...`.

**F-P7a-2 — forge `internal/codemod` + `internal/linter/forgeconv`.** Adds
`connect-errors`, removes `importsSvcerr`, and adds `forgeconv-handler-connect-error`
at warn.
- Tests: codemod corpus under `internal/codemod/testdata`; lint fixtures for both
  rules.
- e2e: a scaffold + `forge fix connect-errors --dry-run` smoke.

Then: forge release, and a reliant pin bump (one PR, `make test-affected`).

**L0 — reliant classification at the source.** Owns `internal/db/core/*.go`
(sentinel declarations only), `internal/store/*_repo_ext.go` (not-found returns
only), `internal/db/repository_impl.go:1094,1825-1901,3459`, `internal/daemonerr`,
`internal/daemonoffline`, `internal/daemoncmd`, `internal/daemonchoice`,
`internal/toolexec/daemon_router*.go`, `internal/toolexec/bootstrap/foreign_daemon_id.go`,
`internal/machinewake`, `internal/connectionrpc`.
- Work: §2.2 sentinels; §2.3 `daemonerr.Error` plus its callers, mechanically
  (`DispatchConnectError` → `Error` at the 10 call sites; this is the one place L0
  edits handler files, one line each); `svcerr.Code` in the four in-process readers.
- Verification:
  - `DATABASE_URL=<own db> REQUIRE_TEST_DB=1 make test-short PKGS="./internal/store/... ./internal/db/..."`
    plus the new invariant test
  - `make test-short PKGS="./internal/daemonerr/... ./internal/toolexec/... ./internal/machinewake/..."`
  - existing `daemonerr` table tests assert identical codes and messages
    (markers included)
  - `go test ./internal/handlers/{file_system,terminal,worktree,daemon,background,forge}/...`
    unchanged

**C0 — run the codemod.** One agent, one PR: `forge fix connect-errors` over
reliant, review, `go build ./...`, `make test-affected`, and a full
`go test ./internal/handlers/...` with an own `DATABASE_URL`. The diff must be
wire-identical, so **no test expectation may change** except the `CodeOf →
svcerr.Code` rewrite. Attach the codemod's report (the B/C worklist) to the PR; it
is the lanes' input.

### Semantic lanes (parallel, by handler package)

Sizes are B + C + D + E sites left after C0 (total hand-written in brackets).

| Lane | Owns `internal/handlers/…` | Sites | Notes |
|---|---|---:|---|
| **L1 chat** | `chat/` | 129 [198] | 5 of the 11 lint findings (`TerminateChat`, `loadMessagesError`, `launchErrorToConnect` → producer `launch`, `interruptThreadError` → `threads`, `asConnectError`); `chat has not started` marker; Aborted on `chatselection.ErrVersionConflict` |
| **L2 worktree/project** | `worktree/`, `project/`, `repo/` | 125 [214] | the `ListProjects` fix (`project.go:744`); `worktreeBelongsToUser` ownership → NotFound (keep: hides existence); `already exists` marker; worktree git ops through `daemonerr.Error` (daemon text kept, Q1) |
| **L3 workflow/run** | `workflow/`, `scenario/`, `run/`, `plan/`, `task/`, `approval/`, `question/`, `tool_call/`, `message/`, `streaming/` | 169 [264] | mostly D-Internal/NotFound on repo gets; `ErrWorkflowSlugTaken` → AlreadyExists via L0; `streaming/` NotFound feeds `streaming-grpc.ts` |
| **L4 triggers/integrations** | `trigger/`, `mcp/`, `connector/`, `connection/`, `catalog/`, `inbox/`, `model_endpoint/` | 138 [240] | 5 lint findings (`catalogSearchError`, `inputOptionsError` → producer `integrations/inputoptions`, `connectionError` → `connections`, `modelEndpointErr` → `modelendpoints` + L0's `db.ErrModelEndpoint*`, `alertingError`); `trigger_inbound.go` / `trigger_senders.go` **only after R6 merges** |
| **L5 daemon proxies + settings** | `file_system/`, `terminal/`, `background/`, `daemon/`, `tools_daemon/`, `daemon_registry/`, `package_commands/`, `settings/`, `preset/`, `token/`, `account/`, `attachment/`, `forge/`, `system/` | 170 [297] | `validateWorkspacePath` lint finding; details (`fs_proxy_checkout.go:106`, `forge.go:943,1165`) → `WithDetail`; `token_git.go` `gitError` → `WithReason` (CLI-visible); `account.go:152` stays `WithCause` (remote client); `tools_daemon.go:234` F-site; `daemon_registry` `not suspended` text kept |

The lanes are disjoint by directory. Shared helper files (`chat/chat_helpers.go`,
`worktree/worktree_placement.go`) belong to their package's lane. A producer package
a lane edits (`internal/launch`, `internal/threads`, `internal/integrations/inputoptions`,
`internal/connections`, `internal/modelendpoints`, `internal/chatselection`) belongs
to the lane that consumes it. None is shared between two lanes; check with
`rg -l '<pkg>\.' internal/handlers` before starting.

**Per-lane recipe:**
1. Take the C0 worklist for your packages.
2. Apply §2.1.
3. Delete the converted helpers and the manual log lines (§2.5).
4. Retype helpers that return `*connect.Error` to `error`.
5. Convert any remaining `connect.CodeOf` in your package to `svcerr.Code`.

**Per-lane verification (all of it in the PR body):**
- `go build ./...`; `make test-short PKGS=./internal/handlers/<pkg>/...`; then the
  full run, `DATABASE_URL=<own db> REQUIRE_TEST_DB=1 go test ./internal/handlers/<pkg>/...`
  (44 handler test files are DB-backed).
- **New rows** for every converted rpc *class* (not every rpc), via the harness
  `StubRepo` / `With<Svc>Deps`:
  - repo returns `svcerr.NotFound` → `CodeNotFound`
  - repo returns `context.DeadlineExceeded` → `CodeDeadlineExceeded` (the
    `ListProjects` regression row is mandatory in L2)
  - `context.Canceled` → `CodeCanceled`
  - an opaque `errors.New("dsn=postgres://u:secret@h")` → `CodeInternal`, and
    `svcerr.ToConnect(err).Message()` does **not** contain `secret`
- Fixture updates are expected and correct: about 21 handler-test stubs return raw
  `sql.ErrNoRows` and 7 return `core.Err*NotFound`. They now return the store's real
  shape (the L0 sentinels or `svcerr.NotFound`). A test that expected NotFound for
  an *opaque* stub error changes to Internal, with the reason in the commit message.
- One wire-level test per lane through a real Connect client (57 handler test
  files already build `httptest` + client): proves the interceptor path, the code and the
  reason header for the lane's marker/reason rpcs (§2.4 "must not change").
- `/path/to/forge lint --conventions --scope internal/handlers/<pkg>` (built from the
  pin, §8): zero `no-handler-error-mapping` and zero `handler-connect-error` in scope.
- `rg 'connect\.NewError\(' internal/handlers/<pkg> --glob '!*_test.go'` → only the
  approved remainder, each listed in the PR (expected: none).
- Web contract (no web change): `make test-affected LANES=web` is a no-op; instead,
  the wire test asserts the markers in §2.3 verbatim.
- The flake gate runs automatically (new or changed tests ×3).

**Z — gate flip** (after L1–L5): in `forge.yaml` set
`lint.rules: { forgeconv-no-handler-error-mapping: error, forgeconv-handler-connect-error: error }`.
`config.go:1453` accepts `error`. Delete `internal/daemonerr`'s
`connect` import, if L0 left one. Update PLAN.md's P7 line.

## 5. Conflicts with in-flight work, and ordering

**R6 (roots cleanup, `fm-w24-r6-roots-cleanup`; `internal/app`, `cmd`,
`internal/grpc`).** Its working tree edits **every handler's `service.go`** (Deps)
plus `trigger/trigger_inbound.go` (18 sites) and `trigger/trigger_senders.go` (5).
`service.go` files contain **zero** `connect.NewError`, so C0 and the lanes don't
touch them. Order: **R6 merges before C0.** If R6 slips, C0 excludes the two
trigger files, and L4 converts them after R6 lands.

**Interceptors** in `internal/grpc/interceptors/` (`auth.go` 5, `daemon_auth.go` 7,
`domain_whitelist.go` 1) and `forge_api_disabled.go` are **out of P7a**:
- they sit outside `svcerr.Interceptor`, so they must emit `*connect.Error` (or
  `svcerr.Wrap` themselves)
- the lint doesn't cover them
- `forge_api_disabled` carries the web's `x-reliant-reason` contract
- they overlap R6 / P3c's interceptor move into `observe.Chain`

Convert them there.

**P6 Repository split (`docs/p6-repo-split-plan`, plan in progress).** P6 edits:
- handler Deps (`service.go`)
- the receiver of repo calls (`s.deps.Repo.GetChat` → `s.deps.Chats.GetChat`), a
  line or two above the returns P7a edits
- `internal/db` interfaces

Git merges the two cleanly when an unchanged line (`if err != nil {`) separates
them, but **never run P6 and P7a on the same handler package concurrently**.
Recommended order:
1. **L0 before P6's `internal/db` work.** P6's role interfaces should document
   "missing → `svcerr.NotFound`" against classified sentinels, not freeze today's
   `errors.New` ones.
2. **C0 before P6's handler lanes.** It is a single, fast, mechanical PR.
3. **P7a lane N, then P6 lane N, per package.** P7a lanes are smaller and shrink the
   code P6 must reread. If P6 is already in a package, the P7a lane waits for it and
   rebases. The rebase is cheap because the edits are on different lines.

**Other P7 lint items.**
- `forgeconv-handler-file-size` (8, including `chat_crud.go` 1,304 lines and
  `chat_send.go` 1,116): splitting a file conflicts with any lane editing it, so
  **split after L1**. L1 shrinks those files by deleting log lines and helpers.
- `forgeconv-activity-declared` (191) edits protos only: no interaction.
- Tracing (106): done except where already gated, and no interaction. Deleting
  manual `logging.ErrorContext` lines (§2.5) removes call sites that rule cares
  about, which only helps.
- `forgeconv-no-getenv` (4, `internal/cliauth` etc.): no interaction.

## 6. Recovery (roll forward)

- C0 is wire-identical, so a regression there is a codemod bug. Fix it forward in
  forge and re-run, or revert the one PR.
- Each lane is one PR per package group, so a code regression the web or CLI notices
  is a one-line fix forward (`WrapOr` → an explicit constructor). The §2.4 "must not
  change" list is the checklist.
- Expected and correct after the lanes: fewer Sentry `Internal` events from
  deadline/cancel/not-found, and **more** from real outages on D-NotFound sites. Watch
  the `[gRPC] Request error` volume for a week after L1–L3. A spike of
  `NotFound → Internal` on one rpc means its store getter was missed in L0: fix the
  store, not the handler.
- No schema or migration changes. Nothing touches prod config.

## 7. Open questions (owner)

1. **Daemon-reported text on the wire.** Keep publishing the daemon's own failure
   text (git rejections, missing paths) to the same user through `daemonerr.Error`,
   redacting only router/transport text? This is the recommendation: the text is the
   user's own data and the actionable part of the toast. The alternative is full
   redaction, which loses that UX.
2. **`svcerr.WrapOr` in forge** (recommended) vs a reliant-local helper. The local
   helper would be the per-project mapper the lint forbids, and control-plane needs
   the same primitive.
3. **Markers → reason header** for daemon states. A follow-up in P9 (web), after P7a
   (recommended), or folded into L0 + a web PR now.

## 8. Reproducing the numbers

```bash
cd reliant   # origin/main 444afb7b
F='-t go --glob !*_test.go --glob !**/gen/** --glob !*.pb.go'
rg -c 'connect\.NewError\(' $F . | awk -F: '{s+=$2} END{print s}'                  # 1565
rg -c 'connect\.NewError\(' $F --glob '!*_gen.go' . | awk -F: '{s+=$2} END{print s}' # 1242
rg -c 'connect\.NewError\(' internal/handlers/mocks | awk -F: '{s+=$2} END{print s}' # 323
rg -c 'connect\.CodeOf\(' internal/handlers --glob '*_test.go' | awk -F: '{s+=$2} END{print s}' # 326
# lint at the pin: the `reliant forge` on PATH is older than the pin and rejects
# reliant's forge.yaml (unknown keys lint.tracing, database.orm), so build forge at
# go.mod's pin (2472900c2b41) and run that:
# (in a forge worktree of your own at that commit — never check out a shared checkout)
(cd <forge-worktree-at-2472900c2b41> && go build -o /tmp/forge-pin ./cmd/forge)
/tmp/forge-pin lint --conventions --no-fix | grep 'no-handler-error-mapping\]'      # 11
```

The category split (§1.1–1.2) comes from a throwaway script: it parses each call's
arguments, classifies the error argument, and scans the 4–8 preceding lines for
`errors.Is/As`, `err != nil` and sentinel comparisons. It is a census heuristic: the
codemod's type-checked report in C0 replaces it as the authoritative worklist.
