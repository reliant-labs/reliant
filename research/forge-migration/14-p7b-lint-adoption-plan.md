# 14 — P7b: the remaining warn-level forge lint rules go to gating

Status: **plan, not implemented.** Shared context: `PLAN.md` §4 P7, `11-svcerr-plan.md`
(P7a), `12-p8-devtooling-plan.md`, `13-p9-frontend-plan.md`.

Every count below was measured on reliant `origin/main` at **`11c8b6e4`**, using the forge that
reliant **pins**: `v0.1.47-0.20261022153127-5264250d49dd`. I re-ran it at **`ec2fca8b`** (#1319,
pin `f1e8f2ebdbb6`) and got the identical finding set. That is the version in both `go.mod`
and `forge.yaml forge_version`, built with `GOWORK=off go build github.com/reliant-labs/forge/cmd/forge`
from reliant's module, so it is not PATH's forge. control-plane was measured at `origin/main`
**`97499c94`**, with its own pin `v0.1.47-0.20261022115939-1be0d294d2af`. The commands are in §11.
Both runs pass `--skip-frontends`, because the frontend lane is P9's (§6).

---

## 0. Summary

1. **Tracing is already done in reliant.** The brief expected about 106 tracing findings. That
   figure is the count at the pin that introduced forge #993's rules (61 log-without-ctx,
   18 raw-http-client, 18 contract-uninstrumented, 9 raw-sql-open). Every one has since been fixed.
   All four `forge-tracing-*` rules run at their default **error** and report **0** findings. The
   only suppressions left are 4 reasoned `forge:lint-disable-next-line forge-tracing-raw-sql-open`
   on test-only provisioning pools. The stale `forge.yaml` comment block above `lint.rules` is
   the only tracing work that remains: rewrite it.
2. **What remains at warn in reliant: 215 findings across 5 rules.**

   | Rule | Findings | Why it is a warning today | How it becomes gating |
   |---|---:|---|---|
   | `forgeconv-activity-declared` | **191** | `forge.yaml lint.rules: warn` (the only override left) | declare all 191, then delete the entry |
   | `forge-config-deps` | **11** | lane is advisory by construction (`gates: false`) | fix the 11; **forge fix F1** lets `lint.rules` grade it |
   | `forgeconv-handler-file-size` | **7** | rule default warn | split the 7 files, then `lint.rules: error` |
   | `forgeconv-no-getenv` | **4** | rule default warn (error only under `--strict`) | fix the 4 (one needs **F2**), then `lint.rules: error` |
   | `forge-optional-deps-guard` | **2** | lane is advisory by construction | fix the 2; **F1**, then `lint.rules: error` |

   The other warn-level lanes (`--tests`, `--proto-markers`, `--proto-options`,
   `--time-bucketing`, `--config-reach`, `--go-markers`) report **0** for reliant. The opt-in
   `--slow-tests` lane reports 107 errors (§4, F7). That lane belongs to P8, not P7b.
3. **`forgeconv-activity-declared` is the bulk of the work, and today's wiring would record the
   wrong actor.** reliant's user chain already runs forge's default Activity slot (a log writer).
   The 12 rpcs that are CRUD-shaped already resolve to an implied event. But
   `activity.DefaultActor`/`DefaultScope` read `authn.ClaimsFromContext`, while reliant's auth
   interceptor stashes `auth.WithUser`. So every event the interceptor writes today, and every one
   P7b adds, is logged with actor `system:anonymous` and an empty scope (§2.1.3). Lane A3 fixes
   that before the rule flips.
4. **Draft classification of the 191:** 107 record an event, 68 declare `none` with a reason,
   and 16 are pure reads, marked `idempotency_level = NO_SIDE_EFFECTS` (§2.1.2). That splits into
   three proto lanes with no file in common.
5. **The 7 oversized handler files are pure moves**, apart from `tools_daemon.go`. Its 2,258 LOC are
   a daemon connection hub with one rpc in it, not a handler file (§2.2).
6. **`forgeconv-no-getenv`: 2 of the 4 are a real defect, not noise.** `internal/handlers/mcp`
   runs in the API server, and it expands `${VAR}` in MCP configs from the **server's**
   environment. The `//nolint` reason ("from the user's environment, by design") assumes the
   single-machine architecture that no longer exists. The expansion belongs on the daemon. It
   should be fixed as its own change, ahead of P7b (D2).
7. **The interceptor sites (§3):** 12 of the 14 already use `svcerr.ToConnect`, and that is the
   correct end state for code that runs *outside* `svcerr.Interceptor`. Two sites
   (`auth.go:216`, `daemon_auth.go:64`) still use `connect.NewError(CodeUnavailable,
   fmt.Errorf("…: %w", err))`, which publishes the store or JWKS error text to the client. They
   become `svcerr.ToConnect(svcerr.WithCause(svcerr.Unavailable(…), err))`: fixed text for the
   client, and the cause stays server-side.
8. **control-plane (§8):** activity and tracing are done there too. The brief's "activity ~142 /
   tracing ~378" are historical counts: today they are **0 and 0**, with 151 declarations and a
   store-backed interceptor. Its warn-level debt is 277 findings, led by
   `forgeconv-handler-connect-error` **72** (cp never set it to error) plus a
   `forge fix connect-errors` worklist of 135 rewrites and 47 manual sites.

Risk: **low to medium.** The proto lanes change no wire behaviour: method options, and
idempotency on pure reads. The operator-visible change is new `activity.event` INFO log lines
with a correct actor (§7). The file splits are moves. Only the MCP `${VAR}` fix changes
behaviour.

---

## 1. What runs at warn today

`forge.yaml lint.rules` (reliant, at `11c8b6e4`; unchanged at `ec2fca8b`):

| Entry | Severity | State |
|---|---|---|
| `forgeconv-activity-declared` | `warn` | **the P7b target** (191 findings) |
| `forgeconv-no-handler-error-mapping` | `error` | P7a, 0 findings |
| `forgeconv-handler-connect-error` | `error` | P7a, 0 findings |
| (tracing rules) | — | no entries: back at their default error, 0 findings |

`forge lint --show-suppressed` lists 335 suppressions. One of them is a `lint.rules` downgrade,
the activity entry. The rest are line- or package-scoped: 165 `forge:exclude-contract`,
32 contract-lane `lint-disable`, 4 tracing `lint-disable`, 2 `tx-lock-under-serializable`, and
the golangci `nolint`s. None of those is P7b's business.

`forge lint --json --skip-frontends` (pinned forge): `errors 0, warnings 215, ok: true`.

---

## 2. Rule by rule

### 2.1 `forgeconv-activity-declared` (191)

**What it requires** (`forge skill load activity`):

- Every **mutating** rpc declares `(forge.v1.method).activity`. An rpc counts as mutating unless
  its name starts with `Get/List/Watch/Search/Query/Count/Describe/BatchGet` or it carries
  `option idempotency_level = NO_SIDE_EFFECTS;`.
- A declaration is either an event (`verb`, `target {kind, id, label}`, optional `env`, `scope`,
  `detail`, `writer`) or `activity: { none: "<why>" }`.
- CRUD-shaped `Create<E>/Update<E>/Delete<E>` rpcs are implied. That is why 12 of reliant's 54
  `Create/Update/Delete*` rpcs are not flagged: `CreateProject`, `CreateWorktree`, `CreateTask`,
  `CreateTrigger`, `DeleteTrigger`, `CreatePlan`, `CreateScenario`, `CreateConnector`,
  `CreateSetting`, `CreateModelEndpoint`, `DeleteModelEndpoint`, `DeleteConnection`.
- The lint reads `gen/forge_descriptor.json`, so `forge generate` must run first. A declared
  path that does not resolve is an error even while the rule is at warn.
- A streaming rpc must use `writer: ACTIVITY_WRITER_HANDLER` or `none`.

#### 2.1.1 Findings by file

34 of reliant's 40 service protos have at least one finding. The largest are `chat.proto` 22,
`settings.proto` 20, `worktree.proto` 15, `project.proto` 12, `workflow.proto` 9,
`trigger.proto` 8 and `system.proto` 8. Only 3 of the 40 import `forge/v1/forge.proto` today,
so most files also gain the import.

By name prefix: Update 19, Delete 14, Set 11, Create 9, Start 8, then a long tail of 80 distinct
verbs (Approve, Branch, Commit, Push, Rotate, Fire, …). Four system rpcs are flagged only because
of their names: `Health`, `Ready`, `Info`, `Version`.

#### 2.1.2 Draft classification

This is a starting point for the lane agents, not a decision. **E** means declare an event.
**N** means `activity: { none: "<why>" }`. **R** means pure read: add
`idempotency_level = NO_SIDE_EFFECTS`. `*` marks a batch rpc, which needs a handler-written
event or one event with a `count` detail. `?` marks a case to confirm by reading the handler.

The rule used for N vs E: an event is something a person would want in a project's history
("archived worktree feat-x", "rotated webhook token", "promoted release to prod"). Per-turn agent
traffic, UI preferences, analytics pings, OAuth handshakes still in flight, filesystem edits
through the daemon (git already records these, and the volume is high) and daemon transport are N.

The rule used for R vs N: `NO_SIDE_EFFECTS` lets connect-go **serve the method over HTTP GET**
(§7.3). It is reserved for rpcs that only read. Rpcs that "compute" by calling out (validating a
key against a provider, testing a connection, running a forge plan on a daemon) are N, not R.

| proto | # | E (event) | N (none) | R (NO_SIDE_EFFECTS) |
|---|---:|---|---|---|
| `account` | 2 | DeleteAccount | — | PreviewAccountDeletion |
| `approval` | 4 | Approve, Deny, BatchApprove*, BatchDeny* | — | — |
| `attachment` | 3 | — | UploadAttachment, CreateFileReference, DeleteAttachment | — |
| `background` | 2 | — | KillProcess | StreamProcessOutput |
| `chat` | 22 | StartChat, RunNow, AdoptChat, UnadoptChat, UpdateChat, DeleteChat, UpdateChatState, TerminateChat, PauseChat, ResumeChat, CompactChat, BranchChat, UpdateWorkflowParams, UpdateChatProviderPolicy, SetChatDaemon | SendMessage, SendAgentMessage, CancelQueuedAgentMessage, InterruptThread, RetryUsageLimitWait, DismissChat, MarkUnreadChat | — |
| `connection` | 6 | CreateApiKeyConnection, CompleteOAuth, RenameConnection, SetDefaultConnection | StartOAuth, TestConnection | — |
| `connector` | 3 | RevokeConnector, AuthorizeClient, RevokeClientAuthorization | — | — |
| `daemon` | 3 | — | StartOAuthFlow, OpenOAuthHelper, CloseOAuthHelper | — |
| `daemon_registry` | 5 | SetLocalModelEndpoints, ClearMachineCaches | ResumeDaemon, RefreshLocalModels | ResolveDaemon |
| `filesystem` | 6 | — | SaveFileContent, CreateFileOrFolder, DeleteFileOrFolder, CopyFile, ReplaceInFiles, CreateDirectory | — |
| `forge` | 6 | ApplyPromote, StartDeploy | VerifyEnv, PlanPromote, PlanDeploy, StartDeployPlan | — |
| `inbox` | 3 | CleanupStorage | DismissInboxItem, RestoreInboxItem | — |
| `mcp` | 6 | InstallServer, UninstallServer, UpdateServerConfig, SetServerEnabled, MoveServerScope | RestartServer | — |
| `model_endpoint` | 2 | UpdateModelEndpoint | TestModelEndpoint | — |
| `package_commands` | 3 | — | RunCommand, KillProcess, SetCommandFavorite | — |
| `plan` | 2 | UpdatePlan, DeletePlan | — | — |
| `preset` | 4 | CreatePreset, UpdatePreset, DeletePreset, SetDefaultPreset | — | — |
| `project` | 12 | UpdateProject, DeleteProject, UpdateProjectMetadata, InitializeProject, SaveProjectPrompts, InitializeGitRepo, CreateProjectFromRepo, PullProjectOnDaemon, RemoveProjectFromDaemon, RecloneProjectOnDaemon | TouchProject, MarkProjectInstalled | — |
| `provider_account` | 5 | UpdateProviderAccount, DeleteProviderAccount, AddProviderAPIKey | ReorderProviderAccounts, RefreshProviderAccountUsage | — |
| `question` | 1 | ResolveQuestion | — | — |
| `repo` | 3 | AddRepo, RemoveRepo | — | DiscoverRepos? |
| `run` | 7 | StartRun, PauseRun, ResumeRun, CancelRun | SignalRun, InterruptRun | LastRunPerWorkflow |
| `scenario` | 4 | DeleteScenario, UploadScenario | RunScenario | ExportScenario |
| `settings` | 20 | SavePrompts, CompleteCodexOAuth, CompleteClaudeOAuth, CompleteAntigravityOAuth, PollCopilotDeviceAuth*, UpdatePrivacySettings, InstallSkill, SetSkillEnabled, DeleteGlobalSkill | UpdateSetting, DeleteSetting, BatchUpsertSettings, UpdateShortcuts, UpdatePreferences, SetHiddenItem, ValidateProviderAPIKey, SyncReliantProvider, StartCopilotDeviceAuth, TrackPageVisited, TrackOnboardingEvent | — |
| `streaming` | 1 | — | — | StreamUserUpdates |
| `system` | 8 | — | StartOAuthSignIn, DevAuthLoad, DevAuthSave, DevAuthClear | Health, Ready, Info, Version |
| `task` | 4 | UpdateTask, DeleteTask, AddTaskDependency, RemoveTaskDependency | — | — |
| `terminal` | 2 | — | CloseSession, StreamTerminal | — |
| `token` | 4 | CreateToken, RevokeToken, UpdateToken | ExchangeToken | — |
| `tool_call` | 2 | — | CancelToolCall, ConvertToBackground | — |
| `tools_daemon` | 4 | — | ConnectDaemon, ConnectGateway, ReportToolResult | WhoAmI |
| `trigger` | 8 | UpdateTrigger, SetTriggerEnabled, FireTrigger, RotateWebhookToken, EnableWorkflow, DisableWorkflow | ResolveTriggerSenders | PreviewReplay |
| `workflow` | 9 | SaveWorkflow, DeleteWorkflow, ImportWorkflow, SetWorkflowVisibility, CopyWorkflow, CreateWorkflowDraft, SetWorkflowStatus | — | ValidateWorkflow, ExportWorkflow |
| `worktree` | 15 | UpdateWorktree, DeleteWorktree, ArchiveWorktree, UnarchiveWorktree, ImportWorktree, PruneWorktrees, RecreateWorktree, RevertFiles, CommitWorktree, PushWorktree, PullWorktree, CreateWorktreePR | StageFiles, UnstageFiles | DiscoverWorktrees? |
| **total** | **191** | **107** | **68** | **16** |

Two follow-ons for the lanes:

- `CreateSetting` is CRUD-implied today, so it records a `create setting` event. It should get an
  explicit `none`, consistent with the rest of `settings`.
- Every `none` reason should say *why*, the way control-plane's do ("per-user preference",
  "dry run: computes a plan, changes nothing").

#### 2.1.3 The interceptor records the wrong actor today

- `serverkit.NewChain` fills a nil `Activity` slot with
  `activity.Interceptor(activity.Deps{Logger: logger})`, a log writer (`chain.go:236`). reliant's
  `NewUserChain` (`internal/grpc/user_chain.go:116`) and `NewDaemonChain`
  (`internal/grpc/daemon_listener.go:93`) leave the slot nil, so both run it.
- `activity.DefaultActor` reads `authn.ClaimsFromContext`. It returns `System("anonymous")` when
  that is empty. `DefaultScope` returns the claims' `OrgID`, or `""`.
- reliant's `interceptors.AuthInterceptor` stashes the caller with `auth.WithUser`
  (`internal/auth/context.go:47`). `pkg/middleware/claims.go:22` bridges the other way, forge
  claims → `auth.WithUser`, and only the test harness uses it. Nothing on the serving path ever
  sets authn claims.
- Consequence: by code reading, every event the interceptor writes is
  `activity.event actor_id=anonymous actor_kind=system scope=""`. Today that means the 12
  implied-CRUD rpcs. After the declarations land, it means all 107. A3 pins this with a test
  before it fixes it.

The fix is control-plane's shape (`control-plane/internal/activitystream`), sized to reliant:

- `ActorFrom` reads `auth.GetUserIDFromContext` / `GetUserEmailFromContext`. A machine
  credential that acts for a user is that user.
- `ScopeFrom` returns the user id. reliant has no orgs; projects are the natural sub-scope later.
- Both live in `internal/grpc` and are passed in `NewUserChain`'s `ChainDeps.Activity`. The daemon
  chain gets `serverkit.NoActivity`, because its 4 rpcs are all N/R and daemon-audience.
- `ViaFrom` stays at the default: the `Forge-Via` header, or `api` when it is absent.

**Where the events go is D1.** The recommendation is the log writer only, for now (§9).

#### 2.1.4 Codemod

None exists. `forge fix` has `connect-errors`, `narrow-deps`, `tx-isolation` and others, but no
activity codemod. Deciding E vs N and choosing verb and target is judgment, and the lint already
resolves paths fast and exactly. So hand-writing the declarations with `forge generate && forge
lint --conventions` as the oracle works: control-plane wrote 151 that way (#1080).

A small codemod is still worth having for every future adopter (F3, not blocking). It would take
a TSV of `rpc → E|N|R, verb, kind, id-path, reason`, resolve each path against the request and
response descriptors, and write the option blocks and the `forge/v1/forge.proto` import.

### 2.2 `forgeconv-handler-file-size` (7)

**What it requires:** no non-test, non-generated `.go` file under `internal/handlers/<svc>/`
may exceed `lint.handler_file_max_loc` lines (default 1000). Comments and blank lines don't
count. The fix hint is "extract RPCs into sibling `rpc_<name>.go` files".

| File | LOC (rule) | `wc -l` | Exported `*Service` methods | Proposed split |
|---|---:|---:|---|---|
| `tools_daemon/tools_daemon.go` | **2258** | 3404 | 30, of which 1 rpc (`ReportToolResult`) | by concern: `connections.go` (Connect/Register/Close/listeners), `dispatch.go` (SendToolRequest*, SendDaemonCommand*), `terminal.go` (SendTerminal*, Subscribe*), `presence.go` (IsDaemonOnline, counts, Touch*), `rpc_report_tool_result.go` |
| `worktree/worktree.go` | **1950** | 2622 | 24 rpcs | `rpc_git_*.go` (Stage/Unstage/Revert/Commit/Push/Pull/PR, 8), `worktree_lifecycle.go` (Create/Update/Delete/Archive/Unarchive/Recreate), `worktree_discovery.go` (Import/Discover/Prune), reads stay |
| `mcp/mcp.go` | **1503** | 1832 | 10 rpcs | `mcp_config.go` (the `*ToInternal`/`convertTo*` converters + scope IO), `rpc_install_server.go`, `rpc_lifecycle.go` (Restart/SetEnabled/MoveScope/Uninstall) |
| `project/project.go` | **1321** | 1814 | 23 rpcs + `StartDaemonEventConsumer` | `project_daemon.go` (the 7 `*OnDaemon`/`*ForDaemon` rpcs + consumer), `project_git.go` (GitInfo/Branches/Changes/InitializeGitRepo), `project_init.go` |
| `chat/chat_crud.go` | **1253** | 1779 | 20 rpcs | `chat_lifecycle.go` (State/Terminate/Pause/Resume/Dismiss/MarkUnread/Compact), `chat_adopt.go` (Adopt/Unadopt/SetChatDaemon), the list/read rpcs stay |
| `workflow/workflow.go` | **1191** | 1526 | 11 rpcs | `workflow_io.go` (Import/Export/Validate), `workflow_draft.go` (Draft/Status/Visibility/Copy) |
| `chat/chat_send.go` | **1093** | 1866 | 4 rpcs | the send pipeline helpers move to `chat_send_pipeline.go`; the 4 rpcs stay |

- **Codemod:** none. The scaffold the hint names (`forge scaffold handler-file <svc> <name>`)
  only writes a package stub, so splitting is hand-done code motion. Same package, no API change.
  F4 (not blocking) proposes a `forge fix split-handler-file` that moves a named rpc together with
  the unexported helpers only it reaches.
- **`tools_daemon.go` is a smell the lint only half sees.** It is the daemon connection hub
  (stream registry, request/response correlation, terminal fan-out) living inside a handler
  package, and the gateway and the api-server reach it as a dependency. Splitting it into files
  gets the lint green. Extracting the hub into its own package (`internal/daemonhub` with a
  `contract.go`, the handler taking it via Deps) is the durable shape, and it is a P4-style
  refactor with real risk. **Recommendation: split by concern in P7b. Extract it as a separate,
  later change** (D3).
- Raising `handler_file_max_loc` is the alternative. It is rejected in D3.

### 2.3 `forgeconv-no-getenv` (4)

**What it requires:** a component package does not read the process environment. Settings come
from `proto/reliant/config/v1` through Deps, so the composition's config reach (and the
workload's env in KCL) can see them. The rule is a warning by default and an error under
`--strict` (`least_knowledge.go:24`).

| Site | What it reads | End state |
|---|---|---|
| `internal/handlers/mcp/mcp.go:618` (`protoConfigToInternal`) | `os.Getenv(key)` for each `${VAR}` in a user's MCP command/args/headers/url | **Remove the server-side expansion.** The API server's environment is not the user's (PLAN §Architecture: the API server never has the user's machine). Store the `${VAR}` text as written. The daemon expands it when it launches the server. A2 first checks whether the daemon's MCP launcher already expands it; if so, this is deletion. Its own PR, ahead of P7b (D2). |
| `internal/handlers/mcp/mcp.go:663` (`scopedConfigToInternal`) | same | same |
| `internal/cliauth/cliauth.go:140` | `FORGE_HOME` | `credentials.DirsFromEnv()` in **forge** (F2). `FORGE_HOME` is forge's variable, `forge login` already resolves it this way, and `cliauth.DepsFromEnv` re-implements it. |
| `internal/cliauth/cliauth.go:141` | `XDG_CONFIG_HOME` | same |

reliant has 33 other `os.Getenv/LookupEnv` sites. The rule doesn't flag them: they are in
`cmd/`, `pkg/config`, or in packages that aren't components.

### 2.4 `forge-config-deps` (11) and `forge-optional-deps-guard` (2)

Both are separate lanes, and both are **advisory by construction**: `gates: false` in
`internal/cli/lint/lint_steps.go:466,491`, and `reportUngated` in `lint_json.go:351-353`. Neither
runs through `lint.rules`. Today a `forge.yaml` entry setting them to `error` is accepted and
silently does nothing: the defect `lint.go:959` fixed for the conventions lane. **F1** routes both
through `EffectiveRules()`, so they can gate like every other forge rule.

`forge-config-deps` flags a scalar Deps field. A scalar is configuration, not a collaborator.

| Deps field | Today it is wired from | End state |
|---|---|---|
| `handlers/chat Deps.TaskQueue` | the constant `v2workflow.SharedTaskQueue` (`compose.go:935`) | **not config at all:** delete the field and read the constant in-package |
| `handlers/attachment Deps.MaxFileSize` | `APISettings().MaxUploadSize` | take the API-server config block (`*configv1.ApiServerConfig`) |
| `handlers/connector Deps.PublicURL`, `handlers/trigger Deps.PublicURL` | `IntegrationSettings().PublicURL` | take `*configv1.IntegrationsConfig` |
| `accesstokenclient Deps.BaseURL`, `controlplane Deps.BaseURL`, `gitcredentialclient Deps.BaseURL`, `tokenauthority Deps.ControlPlaneURL` | `ControlPlaneEndpoint(cfg).URL` | take `*configv1.ControlPlaneConfig` (it already carries `url` + `internal_service_secret`) |
| `tokenauthority Deps.InternalServiceSecret` | same | same block; the field is already a secret in config |
| `dockerengine Deps.SocketPath` | daemon/CLI default | daemon config block (`daemon_config.proto`) |
| `forgecred Deps.CachePath` | CLI-only path | `cli_config.proto` block, or a `credentials.Dirs`-style typed value if F2 lands first |

`forge-optional-deps-guard` reports `internal/handlers/trigger/trigger_alerting.go:38` and `:45`.
`provisionAlerting` and `retireAlerting` dereference the optional `Deps.Alerting`. Every caller
gates on `managed()`, which nil-checks first, so this is safe today but not locally provable.
The fix is an early `if s.deps.Alerting == nil { return nil }` in each, not a
`// forge:optional-checked`: the guard costs one line and survives a new caller.

### 2.5 Tracing: done, nothing to flip

`forge lint --tracing` reports 0. The 4 `forge-tracing-raw-sql-open` disables are reasoned and
test-only. Delete the 13-line history comment in `forge.yaml` under `lint.rules`; it describes
entries that no longer exist. `lint.tracing.log_wrappers` stays.

---

## 3. `internal/grpc/interceptors` and the other `connect.NewError` sites P7a left out

| File | Sites | Shape |
|---|---:|---|
| `interceptors/auth.go` | 5 | 4 × `svcerr.ToConnect(svcerr.Unauthenticated("…"))`; **1 × `connect.NewError(CodeUnavailable, fmt.Errorf("token verification unavailable: %w", err))`** (`:216`) |
| `interceptors/daemon_auth.go` | 7 | 6 × `svcerr.ToConnect(svcerr.Unauthenticated(…))`; **1 × `connect.NewError(CodeUnavailable, fmt.Errorf("daemon credential verification unavailable: %w", err))`** (`:64`) |
| `interceptors/domain_whitelist.go` | 1 | `svcerr.ToConnect(svcerr.PermissionDenied(…))` |
| `grpc/forge_api_disabled.go` | 1 | `svcerr.ToConnect(svcerr.FailedPrecondition(…))` |

**End state: `svcerr.ToConnect(svcerr.<Kind>(…))` everywhere in these files, and no
`connect.NewError` at all.**

- These run in the Auth slot and in `Extras`, both **outside** `svcerr.Interceptor`, which
  `NewChain` installs innermost. A bare svcerr error returned there never passes the
  interceptor that would convert it, so the client reads `unknown`. Converting at the boundary
  is therefore the right contract here. It isn't a leftover.
- The two `%w` sites publish the token store's or JWKS's error text (driver messages, URLs) to
  any caller who presents a token while the store is down. `auth.go:212` logs the cause at ERROR
  first; `daemon_auth.go:64` does not. Both become
  `svcerr.ToConnect(svcerr.WithCause(svcerr.Unavailable("token verification unavailable"), err))`
  (the daemon one `…("daemon credential verification unavailable")…`). The client reads the
  fixed text. A1-ext checks that the "rpc failed" record carries the cause once the error has
  been through `ToConnect`; if it does not, the daemon site logs it the way `auth.go:212` does.
- The code stays `Unavailable`, which the daemon's retry and the web's error mapping key on. Only
  the text after the colon disappears. `rg 'verification unavailable'` finds no client that
  parses it (the HTTP twin in `internal/auth/middleware.go:199` already sends a fixed message).
  A1-ext (§5) carries both.
- **Not** in scope: moving the user/daemon auth onto forge's `authn.NewInterceptor` (`Policy.MapError`,
  `Unauthenticated` allow-list). That is P3c's auth move, which also removes §2.1.3's bridge
  problem at the root. P7b should not pre-empt it.
- The other non-handler sites stay `*connect.Error` by contract (P7a §4): `daemonerr` 4,
  `machinewake` 2, `daemoncmd` 2, `toolexec/daemon_router_nats.go` 1, `daemonoffline` 1. They
  build errors whose code crosses NATS to the daemon and back. F5 adds the lint that keeps the
  error-value shape from coming back anywhere.

---

## 4. Fix in forge first

| # | Defect / gap | Evidence | Blocks |
|---|---|---|---|
| **F1** | `config-deps` and `optional-deps-guard` ignore `lint.rules`: an `error` entry is accepted and inert | `lint_steps.go:466-512` `gates: false`; `lint_json.go:351-353` `reportUngated`; contrast `lint.go:959-967`, where the conventions lane re-grades through `EffectiveRules()` | the flip of those 2 rules (A5's last commit) |
| **F2** | `credentials.Dirs` has no env constructor, so every consumer re-reads `FORGE_HOME`/`XDG_CONFIG_HOME` | `forge project libraries credentials`; `reliant/internal/cliauth/cliauth.go:133-144` | `cliauth`'s 2 getenv sites |
| F3 | no activity codemod (TSV → resolved option blocks + import) | §2.1.4 | nothing (nice to have) |
| F4 | `forge scaffold handler-file` writes a stub only; the lint's hint implies a split tool | §2.2 | nothing (nice to have) |
| F5 | `connect.NewError(code, <error value>)` leaks `Error()` text anywhere, but `forgeconv-handler-connect-error` only looks under `internal/handlers/` | reliant `auth.go:216`, `daemon_auth.go:64`; control-plane 20 non-handler error-value sites (§8) | nothing; prevents regressions |
| F6 | `activity.DefaultActor` silently resolves `system:anonymous` when an app's auth is not on `authn`. Consider a boot-time warning when an authenticated procedure's event resolves an anonymous actor | §2.1.3 | nothing (reliant supplies `ActorFrom`) |
| F7 | `--slow-tests` (107 errors in reliant: worktreereclaim 72, toolexec 28, llm 4, workflow 2, buildmode 1) is opt-in and does not know reliant's own `testing.Short()` conventions | `forge lint --slow-tests --json` | P8 (handoff, not P7b) |

F1 and F2 are small. Land them in one forge PR, then bump reliant's pin. Each needs a test in
forge (`lint.rules` re-grades an advisory-lane finding to error and fails the run;
`DirsFromEnv` honours the precedence).

---

## 5. Lanes

Six lanes. A0 is the forge work. A1 to A5 own **disjoint files** and run in parallel. Z is the
final flip.

| Lane | Owns (files) | Work | Verification | Depends |
|---|---|---|---|---|
| **A0** (forge) | `forge/internal/cli/lint/lint_steps.go`, `lint_json.go`, `forge/pkg/credentials` | F1 + F2 (+ F5 if cheap) | forge `task test:short -- ./internal/cli/lint/... ./pkg/credentials/...`; then reliant pin bump (`make pin-forge`, `forge-generate-check`) | — |
| **A1** activity: agent runtime (71: 45 E / 19 N / 7 R) | `proto/reliant/v1/{chat,run,workflow,scenario,trigger,task,plan,question,approval,tool_call,background,terminal,streaming,package_commands}.proto` + their generated `gen/reliant/v1/*` and `web/src/gen/reliant/v1/*_pb.ts` | declarations per §2.1.2. `BatchApprove/BatchDeny` decide between one event with a `count` detail and handler-written events. **Plus A1-ext:** the two `%w` interceptor sites (§3), `internal/grpc/interceptors/{auth,daemon_auth}.go` | `make forge-generate-check`; `forge lint --conventions --scope proto/reliant/v1/<owned>` shows 0 activity findings for owned files; `go build ./...`; `make test-short PKGS=./internal/grpc/...` | — |
| **A2** activity: workspace (66: 34 E / 28 N / 4 R) | `proto/reliant/v1/{project,worktree,repo,filesystem,mcp,daemon,daemon_registry,tools_daemon,inbox,forge,attachment}.proto` + generated twins | declarations; confirm `DiscoverRepos`/`DiscoverWorktrees` are read-only (else N) | same as A1 | — |
| **A3** activity: identity/settings + wiring (54: 28 E / 21 N / 5 R) | `proto/reliant/v1/{account,settings,connection,connector,token,provider_account,model_endpoint,preset,system}.proto` + generated twins; `internal/grpc/user_chain.go`, `internal/grpc/daemon_listener.go`, new `internal/grpc/activity.go` | declarations; explicit `none` on `CreateSetting`; `ActorFrom`/`ScopeFrom` (§2.1.3); `NoActivity` on the daemon chain | a test in `internal/grpc` that an authenticated `UpdatePreset` writes an event with `actor_kind=user`, `actor_id=<user>`, `scope=<user>`, written first against main to show `anonymous`; `make test-short PKGS=./internal/grpc/...`; plus A1's checks | — |
| **A4** file size (7 files) | the 7 files of §2.2 and the new siblings in `internal/handlers/{chat,project,worktree,workflow,tools_daemon}/` | pure moves per §2.2; **`mcp.go` last, after A5's MCP fix merges** | `go build ./...`; `forge lint --conventions --scope internal/handlers` shows 0 file-size; `make test-short PKGS=./internal/handlers/{chat,project,worktree,workflow,mcp,tools_daemon}/...`; `git diff --stat` shows moves only (no `-`/`+` imbalance beyond imports); last commit sets `forgeconv-handler-file-size: error` | mcp.go after A5's MCP PR |
| **A5** least-knowledge (4 getenv + 11 config-deps + 2 optional) | `internal/handlers/mcp/mcp.go` (the 2 converters only, its own PR first), `internal/cliauth/`, `internal/{accesstokenclient,controlplane,gitcredentialclient,tokenauthority,dockerengine,forgecred}/contract.go`, `internal/handlers/{attachment,chat,connector,trigger}/service.go`, `internal/handlers/trigger/trigger_alerting.go`, `internal/app/{compose,providers_api,providers_worker,server_config}.go` | §2.3, §2.4. The MCP expansion move is PR 1. Then cliauth on F2, config-deps, optional guards | `forge lint --config-deps --optional-deps-guard` 0; `forge lint --conventions` 0 getenv; `go build ./...`; `make test-short PKGS=./internal/app/... ./internal/handlers/{mcp,trigger,attachment,connector,chat}/... ./internal/cliauth/...`; for MCP, a test that `InstallServer` with `${HOME}` in args stores `${HOME}` verbatim; last commit sets `forgeconv-no-getenv: error` and (after A0's pin) `forge-config-deps: error`, `forge-optional-deps-guard: error` | A0 for cliauth and the 2 advisory flips |
| **Z** | `forge.yaml` | delete the `forgeconv-activity-declared: warn` entry and the stale tracing comment block | full `forge lint` (unscoped, pinned, with frontends) ends `ok`; `make test-affected` | A1, A2, A3 merged |

Notes:

- **Shared generated file.** `gen/forge_descriptor.json` is one file that every proto lane
  regenerates. A1, A2 and A3 will conflict there and nowhere else. Resolve by rebasing and
  re-running `make forge-generate-check`, never by hand-merging. Develop the three in parallel and
  merge them one after another.
- **`forge.yaml`:** each lane flips its own rule in its last commit, so its gate protects from the
  moment it merges. The entries are adjacent lines, so expect trivial conflicts.
- **`chat/service.go` (A5) vs `chat/chat_*.go` (A4)**, and **`mcp.go` (A5's converters) vs the
  mcp split (A4)**: different files in the first case; ordered in the second.
- **The PR lint lane already gates them:** `pr-ci.yml:1274` runs
  `forge lint --no-fix --conventions --contract --migration-safety --generated-drift`, so
  activity, file-size and getenv gate pull requests as soon as they are `error`. The two advisory
  lanes gate only in main's full `forge lint` (`pr-ci.yml:1280`) unless P8 adds
  `--config-deps --optional-deps-guard` to the PR line (§6).

Critical path: A0 → pin bump → A5's last commit. A1/A2/A3 → Z runs in parallel with it. The
longest single lane is A1 (71 declarations, chat's 22 the hardest).

---

## 6. Ordering against in-flight P8 and P9

- **P9 (web only).** P7b touches nothing hand-written under `web/`. Two contact points:
  1. The proto lanes regenerate `web/src/gen/reliant/v1/*_pb.ts`, because the embedded file
     descriptor carries the new method options. If P9's D1 (`web/` → `frontends/web`) lands
     mid-P7b, rebase and regenerate. Nothing is hand-merged.
  2. `activity`'s `ViaFrom` reads the `Forge-Via` request header, and reliant's
     `corsAllowedHeaders` (`internal/grpc/server.go:202`) does not list it. If P9 adopts a
     runtime transport that sends `Forge-Via` (control-plane's console does, #1104), every
     browser RPC fails CORS preflight until the header is added. That is the bug class
     `TestCORSAllowsEveryWebClientRequestHeader` exists for. Whoever adds the header to the
     client adds it to the list in the same PR. P7b doesn't need it: the default via is `api`.
- **P8 (tooling).** No file overlap: P7b touches no Makefile, Taskfile or workflow. Three
  coordination points:
  1. After A5 flips the advisory lanes, the PR lint line (`pr-ci.yml:1274`) should add
     `--config-deps --optional-deps-guard`. That is P8's file. P8 adds it, or A5 hands it over.
  2. `buf.yaml breaking: use: [FILE]` includes `RPC_SAME_IDEMPOTENCY_LEVEL`. No workflow runs
     `buf breaking` today. If P8 turns on forge's `proto-breaking.yml` with a base before the
     16 `NO_SIDE_EFFECTS` changes, it flags them. Land A1–A3 first, or have P8 baseline on a main
     that already contains them.
  3. F7 (`--slow-tests`) is P8's to adopt.

---

## 7. Behaviour risk: what operators see change

### 7.1 Activity

- **New log lines.** `activity.event` at INFO, `log_type=activity`, one per successful call of
  the 107 E rpcs (plus the 12 implied CRUD rpcs, which emit today with the wrong actor). These are
  user-initiated, low-frequency actions; the chatty paths (`SendMessage`, filesystem writes,
  settings) are N. They land in `reliant-api-server.log` in dev and in ClickStack in cloud. Each
  carries `actor_id`, `verb`, `target_kind/id/label`, `procedure`, `trace_id`.
- **Failure mode.** The write happens after success and is best-effort. A failed write is logged
  and counted (`forge_activity_write_failures_total`); the rpc still succeeds. With the log
  writer, a write cannot fail in practice.
- **What does not change.** Handler code, response bodies, codes, and Temporal. This `activity`
  is forge's typed activity stream on the RPC chain. It has nothing to do with Temporal
  activities: no Temporal activity wrapper, registration, retry policy or worker changes.
- **Ordering.** The Activity slot runs after `Extras` (the domain whitelist) and next to the
  handler (`chain.go:166-177`). It sees the handler's own result and adds no latency before it.

### 7.2 Tracing

Nothing changes; the rules are already at error.

### 7.3 `NO_SIDE_EFFECTS` on 16 reads

- connect-go serves a `NO_SIDE_EFFECTS` method over **HTTP GET** as well as POST.
- No reliant client opts in (`rg 'useHttpGet|WithHTTPGet'` finds none), so the bytes on the
  wire don't change.
- The server surface grows by GET. The rpcs are bearer-token authenticated, a cross-site GET
  cannot set `Authorization`, and the public ones (`Health/Ready/Info/Version`) are already
  public. This is why §2.1.2 keeps every rpc that calls out (key validation, connection tests,
  forge plans) as N, not R.

### 7.4 File splits

None; same package and same symbols.

### 7.5 MCP `${VAR}`

- Configs written after the fix keep the `${VAR}` text instead of the server's value.
- A config already stored with expanded values keeps them, and it still works.
- A user who relied on server-side expansion had a server variable substituted, never their own,
  so nothing correct is lost.

### 7.6 Interceptor `%w` sites

Same code (`Unavailable`), shorter message.

---

## 8. control-plane (separate section)

Measured at control-plane `origin/main` `97499c94` with its pinned forge `1be0d294d2af`:
`forge lint --json --skip-frontends` reports `errors 0, warnings 277, ok: true`. Its `forge.yaml`
has **no `lint.rules`**.

### 8.1 Activity and tracing: done

- **Activity: 0 findings.** 151 `(forge.v1.method).activity` declarations, 89 of them `none`.
  The interceptor is wired to a store, `activity.Interceptor` with `activitystream.ActorFrom/ScopeFrom`
  (`cmd/control-plane/cmd/serve.go:360`). `ActivityService.ListActivity` reads it. Shipped in #1080.
- **Tracing: 0 findings.** The four rules returned to error in #1081 (log), #1083 (sql), #1089
  (http) and #1094 (contracts), with root spans for background loops (#1092). There are 4 reasoned
  `lint-disable`s (`dbrole.go:100`, `testsrv`).

The "activity ~142 / tracing ~378" in the brief predate those PRs.

### 8.2 Remaining warn-level findings

| Rule | # | Notes |
|---|---:|---|
| `forge-config-deps` | 113 | workers 30, operators 10, then many small packages. Advisory lane (F1 applies) |
| `forgeconv-handler-connect-error` | **72** | default warn; cp never set it to error (reliant did in P7a). 45 fixed-message, 27 error-value |
| `forge-optional-deps-guard` | 58 | handlers/deploy 15, dashboard 7, operators/workspace 6, access_token 5, identity 5, … |
| `forgeconv-deps-surface` | 11 | e.g. `handlers/deploy Deps.Plans` never called; `billing Deps.CouponService` uses 1 of 8; `forge fix narrow-deps` applies |
| `component-drift/*` | 11 | 4 orphan KCL entries for reliant's own workloads (`reliant-api-server`, `reliant-temporal-worker`, `daemon-gateway`, `cloudsql-backup-prune`), which are D4/F6 territory; 7 undeclared (`manageddatabase`, `shared`, `staticsite`, `workload` operators; `gen-llm-gateway`, `pricing-export`, `stripe-catalog` binaries) |
| `scaffold-not-customized` | 8 | live `FORGE_SCAFFOLD` markers in 8 rpc files (issues ×4, observability, deploy, billing, app_database) |
| `forge-time-bucket-session-tz` | 2 | `db/migrations/20261013031500_observability_billing.up.sql:109,139`: two-argument `date_trunc` over TIMESTAMPTZ. A forward migration fixes the view/function; the old migration stays |
| `forgeconv-no-getenv` | 1 | `internal/operators/workspace/operator.go:924` |
| `gen-missing-source` | 1 | `cmd/control-plane/cmd/root_gen.go`, which a regenerate restamps |

### 8.3 The connect-errors worklist

`forge fix connect-errors --dry-run` at cp's pin:

- **135 production rewrites** that are wire-identical. 45 are in `internal/handlers/**` (the 45
  fixed-message findings above). 90 are outside: `svcdaemon` 73, `pkg/middleware` 7,
  `internal/billing` 7, `auth`/`llm`/`ratelimit` 1 each. 101 of the 135 stay
  `svcerr.ToConnect(...)`, because an in-process reader of `*connect.Error` sees them
  (`billing/rpc_redeem_coupon_test.go:343`, `deploy_admin/revoke_telemetry_test.go:51`, and
  svcdaemon's callers). Switching those readers to `svcerr.Code` and re-running drops the wrappers.
- **67 test rewrites**, `connect.CodeOf` → `svcerr.Code`.
- **47 sites left for review** (the semantic worklist):

  | Package | Sites | Shape |
  |---|---:|---|
  | `handlers/billing_admin` | 15 | error value |
  | `internal/auth` | 8 | error value; this is the auth interceptor, the same class as reliant §3 |
  | `handlers/secret_store` | 6 | error value |
  | `internal/convert` | 5 | error value |
  | `svcdaemon` | 4 | 2 computed code (`connecterr.go:59`, `repositories_proxy.go:79`), 2 `%w` |
  | `handlers/proxy_authz` | 2 | `%w` |
  | `handlers/deploy_admin` | 2 | error value |
  | `pkg/middleware/auth.go:298` | 1 | computed code |
  | `handlers/billing`, `handlers/audit_log`, `app/identity_api_wire.go`, `accountgate` | 1 each | error value |

The cp sequence mirrors reliant's P7a, at about a fifth of the size:

1. Run the codemod: one wire-identical PR.
2. Convert the 27 handler error-value sites with `svcerr.WrapOr` (reviewed).
3. Set `forgeconv-handler-connect-error: error` and `forgeconv-no-handler-error-mapping: error`.
4. Treat the 20 non-handler sites as F5's first customers.

Separately, and in parallel: `--fix`-free sweeps of `optional-deps-guard` (58), `narrow-deps`
(11), then `config-deps` (113, the largest and lowest-value; D6).

---

## 9. Owner decisions

| # | Decision | Options | Recommendation |
|---|---|---|---|
| **D1** | Where reliant's activity events go | (a) **log writer only**: `activity.event` lines in ClickStack, no table; (b) `activity_events` table in reliant's DB (`activity.SchemaSQL` migration + `activity.NewStore`), no reader yet; (c) forward to control-plane's stream | **(a) now.** The declarations are the durable investment either way. A table with no reader is data nobody reads, with no retention. Moving to (b) is a one-line slot change plus a migration, the day the product wants a History panel. (c) couples public, self-hostable reliant to the private control plane: rejected. |
| **D2** | MCP `${VAR}` expansion | (a) delete server-side expansion, expand on the daemon at launch; (b) keep it, `forge:lint-disable` with a reason | **(a), as its own PR ahead of P7b.** The API server's environment is not the user's, so (b) suppresses a real defect. |
| **D3** | Oversized handler files | (a) split per §2.2; (b) raise `lint.handler_file_max_loc` to ~2300; (c) split now and extract `tools_daemon`'s hub into `internal/daemonhub` in P7b | **(a)**, with the hub extraction as a separate later change. (b) is a ceiling that only moves up. (c) mixes a risky refactor into a lint-adoption phase. |
| **D4** | The 16 pure reads | (a) `NO_SIDE_EFFECTS`; (b) `activity: { none: "reads only" }` | **(a).** It is the true statement, it removes them from the mutating set for good, and the GET surface is authenticated (§7.3). |
| **D5** | Batch rpcs (`BatchApprove/BatchDeny`, `PollCopilotDeviceAuth`) | one event with a `count` detail, or handler-written per item | **one event + detail** for the batches. For `PollCopilotDeviceAuth`, `activity.Skip(ctx)` on every poll that has not completed, and a `connect provider` event on the one that does. |
| **D6** | control-plane `forge-config-deps` (113) | fix all, then error; or leave the lane advisory there | **Fix all, but last**, after connect-errors and optional-deps. Most are worker/operator intervals and URLs whose typed config block is a mechanical move. Once F1 lands, gate it. |

---

## 10. Recovery (roll forward)

Every lane is reverted by a revert PR. No schema change, and no wire change apart from GET
acceptance on reads.

- If the activity interceptor misbehaves in production (an unexpected log volume), the fix forward
  is `ChainDeps.Activity: serverkit.NoActivity` in one line. The declarations stay; they are inert
  without the slot.
- If an MCP config breaks because a user relied on expansion, the fix forward is daemon-side
  expansion, which is where A5 PR 1 puts it in the first place.

---

## 11. Reproduce

```bash
# pinned forge, from each module (never PATH's)
cd reliant && GOWORK=off go build -o /tmp/p7b/forge-reliant github.com/reliant-labs/forge/cmd/forge
GOBIN=/tmp/p7b/cpbin GOWORK=off go install \
  github.com/reliant-labs/forge/cmd/forge@$(cd control-plane && go list -m -f '{{.Version}}' github.com/reliant-labs/forge)

# reliant: every finding, by rule
cd reliant && GOWORK=off /tmp/p7b/forge-reliant lint --json --skip-frontends > /tmp/p7b/reliant-lint.json
python3 -c "import json,collections; d=json.load(open('/tmp/p7b/reliant-lint.json')); \
  print(collections.Counter((x['severity'],x['rule']) for x in d['findings']))"
GOWORK=off /tmp/p7b/forge-reliant lint --show-suppressed --json     # the lint.rules override
GOWORK=off /tmp/p7b/forge-reliant lint --json --tests --proto-markers --proto-options \
  --time-bucketing --config-reach --slow-tests --go-markers         # opt-in lanes (only slow-tests fires)

# control-plane
cd control-plane && GOWORK=off /tmp/p7b/cpbin/forge lint --json --skip-frontends > /tmp/p7b/cp-lint.json
GOWORK=off /tmp/p7b/cpbin/forge fix connect-errors --dry-run         # 202 rewrites (135 prod + 67 test), 47 left
rg -c 'activity: \{' proto/                                           # 151 declarations

# reliant §3 sites
rg -n 'connect\.NewError|svcerr\.ToConnect' internal/grpc/interceptors internal/grpc/forge_api_disabled.go
```
