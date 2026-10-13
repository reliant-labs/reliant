# P3a: handler map of `internal/grpc/services`

Input for P3a (extract shared helpers) and P3b (one `internal/handlers/<svc>/`
per Connect service) in [PLAN.md](PLAN.md) §4. Measured on origin/main
`cec97c25` (2026-10-13).

## Summary

**Status (2026-10-13):** wave 1 (G1, G2, G4, G5, G12) is merged (#772–#777).
Wave 2 (G3, G6–G11, G13, the shared test fixtures and the dead code) is the
branch series under "Wave plan" below. With both waves in, every service is
extraction-ready for P3b on the production side. The tables below describe
the package as measured before either wave.

- **80 non-test files, 42,290 LOC; 192 test files.** 35 Connect services are
  implemented here (39 handler types, because FileSystem, Background, Terminal
  and PackageCommands each have a local and a `*Proxy` implementation). Four
  more entry points are reached from outside the package:
  `TerminalWSHandler`, `AttachmentContentHandler`,
  `NewMachineWakingInterceptor` and `NewDBBackgroundProcessProvider`.
- **The cross-service surface is small.** The package has 3,216 top-level
  declarations including methods, 1,280 of them non-test. 63 non-test
  declarations are reached by two or more services. They fall into **13
  helper groups** (G1–G13 below), about 820 lines in total. Everything else
  is private to one service and moves with it in P3b.
- **`chat_helpers.go` is not shared.** All 12 of its declarations are
  reached only by ChatService, so it moves with Chat in P3b and needs no P3a
  work. The same holds for `mcp_status_cache.go` and `mcp_daemon_proxy.go`
  (MCP), `trigger_automation.go` (Trigger), and `workflow_convert.go` and
  `scenario_workflow_loader.go` (Workflow and Scenario).
- **One service-to-service edge:** RunService holds a `*ChatService` and calls
  `StartChat`, `SendMessage`, `PauseChat`, `ResumeChat`, `TerminateChat` and
  `InterruptThread`. These are exported methods, so the edge creates no cycle.
  In P3b, Run should declare its own 6-method consumer interface rather than
  import the chat handler package.
- **14 services already need no shared helper.** Approval, Attachment,
  Connector, DaemonRegistry, Inbox, MCP, ModelEndpoint, Question, Repo,
  Scenario, Settings, System, Token and ToolCall reach only their own
  declarations. Their production code can move today. Their tests still share
  fixtures; see "Test-side coupling".
- **Dead code found:** `chatUpdateTypeFromString` (only its own test calls it)
  and the `LOG_PREFIX_STREAM` constant (no caller).

## How this was measured

A throwaway `go/packages` + `go/types` analyzer (not committed) loaded the
package's test variant, giving one node per top-level declaration (func,
method, type, var, const) and one edge per resolved identifier use (fields
resolve to their struct type, methods to their receiver).

- **Who owns a declaration.** A type owns its methods. Each service owns its
  handler types, their methods and the `New*` constructors that return them.
- **Who uses a declaration.** For each service, the closure was computed from
  the service's owned declarations. The walk stops at declarations owned by
  another service, and records every edge it stops at as a cross-service edge.
- **Shared vs. private.** An unowned declaration in two or more closures is
  shared. One in exactly one closure is private to that service.
- **Spot checks.** gopls call hierarchy (`code_context`) confirmed the
  analyzer for `machineStateConnectError`, `machineUnreachable` and
  `remoteURLForLog`.
- **Limit.** Calls through an interface do not create edges. That is why a
  type's methods count as reached whenever the type is.

## Service → shared helpers it needs

"Decl lines" counts the lines of the declarations the service owns, plus those
private to it. Files are listed with the largest first. A file appearing under
two services, e.g. `proto_enum_mappers.go`, is split along service lines in
P3b.

| Service (Connect) | Impl types | Files (by decl lines) | Decl lines | Shared groups |
|---|---|---|---|---|
| AccountService | AccountService | `account.go` | 184 | G11 |
| ApprovalService | ApprovalService | `approval.go` | 684 | — |
| AttachmentService (+ `AttachmentContentHandler`) | AttachmentService | `attachment.go`, `attachment_content.go` | 331 | — |
| BackgroundService (+ `DBBackgroundProcessProvider`) | BackgroundService, BackgroundProxyService | `background.go`, `background_proxy.go`, `background_provider_db.go`, `background_provider.go`, `proto_enum_mappers.go` | 679 | G1, G8 |
| CatalogService | CatalogService | `catalog.go`, `catalog_search.go` | 992 | G10 |
| ChatService | ChatService | `chat_send.go`, `chat_crud.go`, `chat_branch.go`, `chat_workflow.go`, `chat_continue.go`, `chat_helpers.go`, `chat_send_idempotency.go`, `chat_model_fallback.go`, `chat_step_executions.go`, `chat.go`, `chat_interrupt.go`, `chat_wake.go` | 5,560 | G4, G5, G8, G9, G12 |
| ConnectionService | ConnectionService | `connection.go` | 243 | G10 |
| ConnectorService | ConnectorService | `connector.go`, `connector_consent.go` | 511 | — |
| DaemonRegistryService | DaemonRegistryService | `daemon_registry.go` | 436 | — |
| DaemonService | DaemonProxyService | `daemon_proxy.go` | 115 | G1 |
| FileSystemService | FileSystemService, FileSystemProxyService | `filesystem.go`, `fs_proxy.go`, `fs_proxy_checkout.go`, `proto_enum_mappers.go` | 2,101 | G1, G2, G5, G13 |
| ForgeService | ForgeService | `forge.go` | 1,031 | G1, G3 |
| InboxService | InboxService | `inbox.go` | 509 | — |
| MCPService | MCPService | `mcp.go`, `mcp_status_cache.go`, `mcp_daemon_proxy.go` | 2,011 | — |
| MessageService | MessageService | `message.go` | 106 | G4 |
| ModelEndpointService | ModelEndpointService | `model_endpoint.go` | 121 | — |
| PackageCommandsService | PackageCommandsService, PackageCommandsProxyService | `package_commands.go`, `package_commands_proxy.go`, `proto_enum_mappers.go` | 470 | G1, G3, G8 |
| PlanService | PlanService | `plan.go`, `proto_enum_mappers.go` | 323 | G8 |
| PresetService | PresetService | `preset.go` | 976 | G6 |
| ProjectService | ProjectService | `project.go`, `project_from_repo.go`, `proto_enum_mappers.go` | 1,889 | G7, G11 |
| QuestionService | QuestionService | `question.go` | 240 | — |
| RepoService | RepoService | `repo.go` | 276 | — |
| RunService | RunService | `run.go` | 539 | G9, **→ ChatService** |
| ScenarioService | ScenarioService | `scenario.go`, `scenario_workflow_loader.go` | 794 | — |
| SettingsService | SettingsService | `settings.go`, `proto_enum_mappers.go` | 1,684 | — |
| StreamingService | StreamingService | `streaming.go` | 1,103 | G4 |
| SystemService | SystemService | `system.go` | 145 | — |
| TaskService | TaskService | `task.go` | 313 | G8 |
| TerminalService (+ `TerminalWSHandler`) | TerminalService, TerminalProxyService | `terminal_proxy.go`, `terminal_ws.go`, `terminal.go` | 730 | G1, G13 |
| TokenService | TokenService | `token.go`, `token_exchange.go`, `token_git.go` | 538 | — |
| ToolCallService | ToolCallService | `tool_call.go` | 335 | — |
| ToolsDaemonService | ToolsDaemonService | `tools_daemon.go`, `tools_daemon_reconcile.go`, `tools_daemon_localmodel.go` | 2,948 | G7 |
| TriggerService | TriggerService | `trigger.go`, `trigger_inbound.go`, `trigger_automation.go`, `trigger_senders.go` | 1,145 | G12 |
| WorkflowService | WorkflowService | `workflow.go`, `workflow_validation.go`, `workflow_catalog.go`, `workflow_convert.go` | 1,606 | G6 |
| WorktreeService | WorktreeService | `worktree.go`, `worktree_git_ops.go`, `worktree_placement.go`, `machine_wake.go`, `proto_enum_mappers.go` | 2,318 | G1, G2, G5 |

Two pieces travel with a service even though they are shared inside it:

- `closeDaemonTerminalSession` / `terminalCloseTimeout` are shared by
  `TerminalProxyService` and `TerminalWSHandler`. Both are Terminal, so they
  are not cross-service.
- The `BackgroundProcessInfo` family of types in `background_provider.go` is
  shared by the Background handlers and `DBBackgroundProcessProvider`. Both
  are Background.

## Shared helper groups → services, and where each goes

All identifiers are unexported unless capitalised.

| Group | Lives in today | Identifiers | Used by | Destination |
|---|---|---|---|---|
| **G1 daemon-command errors** | `daemon_proxy_base.go`, `machine_wake.go` | `machineUnreachable`, `machineStateConnectError`, `IsDaemonOutdatedError`, `DaemonOutdatedConnectError`, `mapDaemonDispatchError`, `daemonErrOutdatedMarker`, `daemonErrDirNotExistMarker` | Background, Daemon, FileSystem, Forge, PackageCommands, Terminal (+WS), Worktree | `internal/daemonerr`, pure (wave 1) |
| **G2 machine wake** | `machine_wake.go` | `machineWake` (+`wake`, `ownedBy`, `afterFailure`), `wakeTarget`, `machineWakingError`, `noWakeKey`, `wakeSuppressed`, `withoutWake`, `isMachineWaking`, `requestWakeTimeout`, `machineResumer`, `machineOwners`, `NewMachineWakingInterceptor` | FileSystem, Worktree, server wiring (interceptor) | `internal/machinewake`, canonical contract (wave 1; needs G1) |
| **G3 daemon proxy base** | `daemon_proxy_base.go` | `daemonProxyBase` (+`userID`, `dispatch`) | Forge, PackageCommands | `internal/daemoncmd`, canonical contract (wave 2): `Service.Dispatch` over a 1-method `Sender`; `daemoncmd.UserID` is a function. The two services hold it as a named field, so their method sets do not grow |
| **G4 message → proto** | `proto_converters.go` | `MessageToProtoOptions`, `messageToProto`, `contentBlockToProto`, `toolCallStatusToProto`, `inheritedInFlightCall`, `streamingStateFromComputedString`, `assembleMessagesForDisplay` | Chat, Message, Streaming | `internal/messageproto`, pure (wave 1) |
| **G5 chat/worktree placement** | `worktree_placement.go`, `worktree.go` | `chatDaemonID`, `chatWorktreeReader`, `worktreeOwner` | Chat, FileSystem, Worktree | `internal/daemonplacement` (wave 1) |
| **G6 workflow catalog** | `workflow_catalog.go` | `workflowCatalog` (+4 methods), `workflowCatalogReads`, `loadWorkflowCatalog`, `withBuiltinCache`, `cachedBuiltinParse`, `builtinParses`, `memoizeRefLoader` | Preset, Workflow | `internal/workflow/workflowcatalog` (wave 2), which owns the `builtinParses` cache. WorkflowService reads the drafts through `Catalog.Drafts()` |
| **G7 project git metadata** | `forge_name_learn.go`, `project.go`, `log_redact.go` | `learnForgeProjectName`, `forgeNameReader`/`Writer`, `forgeNameReadTimeoutMs`, `reconcileProjectGitRepo`, `remoteURLForLog` | Project, ToolsDaemon | wave 2, two homes: `internal/projectfacts` (`LearnForgeName`, `ReconcileGitRepo`: the project row's cached copies of facts only a daemon can see) and `internal/gitremote` (`Redacted`) |
| **G8 shared enum mappers** | `proto_enum_mappers.go` | `backgroundProcessStatusFromString`, `outputStreamTypeFromString` (Background, PackageCommands); `planStatusFromInt32` (Chat, Plan); `taskStatusFromInt32` (Plan, Task) | Background, Chat, PackageCommands, Plan, Task | wave 2, two homes: `internal/processproto` and `internal/planproto`, each with its whole enum family. The file's remaining single-service mappers move with their service in P3b |
| **G9 run-state → proto** | `run.go` | `workflowStateToProto`, `workflowStopReasonToProto` | Chat, Run | `internal/runstateproto` (wave 2) |
| **G10 connection → proto** | `connection.go` | `connectionCaller`, `connectionAuthKindToProto` | Catalog, Connection | `internal/connectionrpc` (wave 2) |
| **G11 bearer token** | `account.go` | `bearerToken` | Account, Project | `auth.BearerToken` (wave 2; existing package) |
| **G12 daemon choice** | `daemon_choice.go` | `validateOwnedProjectDaemon` | Chat, Trigger | `internal/daemonchoice` (wave 1). It took the 442-method `db.Repository`; the new package declares a 2-method `Reader` |
| **G13 checkout state** | `fs_proxy_checkout.go` | `checkoutStateOn` | FileSystem, Terminal (WS) | `internal/checkoutstate` (wave 2) |

Order of dependencies: G2 → G1 (`afterFailure` calls `machineUnreachable`),
G3 → G1 (`dispatch` maps through `mapDaemonDispatchError`), and G4's
`assembleMessagesForDisplay` → `messageToProto` (within the group). No other
group references another, so every group except G2 and G3 can branch off
main.

## Every non-test file

"Used by" counts the decl lines each service owns or alone reaches.
"Cross-service identifiers" lists the shared declarations (G1–G13) that the
file defines.

| File | LOC | Used by (decl lines) | Cross-service identifiers |
|---|---|---|---|
| `account.go` | 259 | Account (184) | `bearerToken` → Account, Project |
| `approval.go` | 747 | Approval (684) | |
| `attachment.go` | 333 | Attachment (286) | |
| `attachment_content.go` | 68 | Attachment HTTP handler (45) | |
| `background.go` | 344 | Background (294) | |
| `background_provider.go` | 86 | Background (33) | `BackgroundProcessInfo`, `BackgroundProcessOutput`, `OutputSubscriptionInfo`, `PortInfo` → Background handlers + DB provider (same service) |
| `background_provider_db.go` | 184 | Background DB provider (143) | |
| `background_proxy.go` | 246 | Background (202) | |
| `catalog.go` | 880 | Catalog (771) | |
| `catalog_search.go` | 284 | Catalog (221 + `IntegrationMethodsFrom`, called by `server.go`) | |
| `chat.go` | 78 | Chat (47) | |
| `chat_branch.go` | 773 | Chat (709) | |
| `chat_continue.go` | 395 | Chat (292) | |
| `chat_crud.go` | 1766 | Chat (1583) | |
| `chat_helpers.go` | 344 | Chat (251) | |
| `chat_interrupt.go` | 60 | Chat (41) | |
| `chat_model_fallback.go` | 135 | Chat (73) | |
| `chat_send.go` | 1850 | Chat (1630) | |
| `chat_send_idempotency.go` | 333 | Chat (232) | |
| `chat_step_executions.go` | 98 | Chat (72) | |
| `chat_wake.go` | 63 | Chat (34) | |
| `chat_workflow.go` | 673 | Chat (596) | |
| `connection.go` | 311 | Connection (243) | `connectionAuthKindToProto`, `connectionCaller` → Catalog, Connection |
| `connector.go` | 447 | Connector (378) | |
| `connector_consent.go` | 176 | Connector (133) | |
| `daemon_choice.go` | 43 | — | `validateOwnedProjectDaemon` → Chat, Trigger |
| `daemon_proxy.go` | 162 | Daemon (115) | |
| `daemon_proxy_base.go` | 187 | — | G1 (`machineStateConnectError` → Background, Daemon, FileSystem, Forge, PackageCommands, Terminal; outdated → Daemon, Forge, PackageCommands; `mapDaemonDispatchError` → Forge, PackageCommands); G3 `daemonProxyBase` → Forge, PackageCommands |
| `daemon_registry.go` | 552 | DaemonRegistry (436) | |
| `filesystem.go` | 1251 | FileSystem (1125) | |
| `forge.go` | 1649 | Forge (1031 + `ForgeRPCDeadlines`, called by `server.go`) | |
| `forge_name_learn.go` | 122 | — | `learnForgeProjectName`, `forgeNameReader`, `forgeNameWriter`, `forgeNameReadTimeoutMs` → Project, ToolsDaemon |
| `fs_proxy.go` | 1097 | FileSystem (899) | |
| `fs_proxy_checkout.go` | 134 | FileSystem (67) | `checkoutStateOn` → FileSystem, Terminal WS |
| `inbox.go` | 622 | Inbox (509) | |
| `log_redact.go` | 15 | — | `remoteURLForLog` → Project, ToolsDaemon |
| `machine_wake.go` | 234 | Worktree (7: `withoutWake`, `isMachineWaking`), interceptor (12) | G1 `machineUnreachable` → 8 services; G2 rest → FileSystem, Worktree |
| `mcp.go` | 1876 | MCP (1743) | |
| `mcp_daemon_proxy.go` | 167 | MCP (123) | |
| `mcp_status_cache.go` | 215 | MCP (145) | |
| `message.go` | 127 | Message (106) | |
| `model_endpoint.go` | 160 | ModelEndpoint (121) | |
| `package_commands.go` | 406 | PackageCommands (356) | |
| `package_commands_proxy.go` | 139 | PackageCommands (90) | |
| `plan.go` | 349 | Plan (313) | |
| `preset.go` | 1089 | Preset (976) | |
| `project.go` | 1919 | Project (1688) | `reconcileProjectGitRepo` → Project, ToolsDaemon |
| `project_from_repo.go` | 257 | Project (187) | |
| `proto_converters.go` | 339 | — | all of G4 → Chat, Message, Streaming |
| `proto_enum_mappers.go` | 221 | PackageCommands (24), Project (14), Settings (10), FileSystem (10), Plan (10), Background (7), Worktree (3); dead (43) | G8 |
| `question.go` | 299 | Question (240) | |
| `repo.go` | 344 | Repo (276) | |
| `run.go` | 687 | Run (539) | `workflowStateToProto`, `workflowStopReasonToProto` → Chat, Run |
| `scenario.go` | 873 | Scenario (791) | |
| `scenario_workflow_loader.go` | 17 | Scenario (3) | |
| `settings.go` | 1881 | Settings (1674) | |
| `streaming.go` | 1312 | Streaming (1103) | |
| `system.go` | 190 | System (145) | |
| `task.go` | 347 | Task (313) | |
| `terminal.go` | 95 | Terminal (70) | |
| `terminal_proxy.go` | 410 | Terminal (325) | `closeDaemonTerminalSession`, `terminalCloseTimeout` → Terminal + its WS handler (same service) |
| `terminal_ws.go` | 445 | Terminal WS handler (335) | |
| `token.go` | 325 | Token (248) | |
| `token_exchange.go` | 310 | Token (202) | |
| `token_git.go` | 129 | Token (88) | |
| `tool_call.go` | 427 | ToolCall (335) | |
| `tools_daemon.go` | 3345 | ToolsDaemon (2638) | |
| `tools_daemon_localmodel.go` | 201 | ToolsDaemon (153) | |
| `tools_daemon_reconcile.go` | 210 | ToolsDaemon (157) | |
| `trigger.go` | 945 | Trigger (775) | |
| `trigger_automation.go` | 85 | Trigger (59) | |
| `trigger_inbound.go` | 336 | Trigger (255) | |
| `trigger_senders.go` | 84 | Trigger (56) | |
| `workflow.go` | 1473 | Workflow (1357) | |
| `workflow_catalog.go` | 257 | Workflow (52) | G6 → Preset, Workflow |
| `workflow_convert.go` | 60 | Workflow (44) | |
| `workflow_validation.go` | 229 | Workflow (153) | |
| `worktree.go` | 2545 | Worktree (2226) | `worktreeOwner` → Chat, FileSystem, Worktree |
| `worktree_git_ops.go` | 70 | Worktree (49) | |
| `worktree_placement.go` | 94 | Worktree (33: `placeNewWorktree`) | `chatDaemonID`, `chatWorktreeReader` → Chat, FileSystem, Worktree |

## Exported API other packages use

These names have to be re-exported from the handler package, or move with the
service, when P3b moves it.

| Name | Defined in | Imported by |
|---|---|---|
| `New*Service` (35 constructors), `NewTriggerServiceFor`, `With*` options | per service | `internal/grpc/server.go`, `serverapi`, `serverworker`, `servergateway`, tests |
| `HeldDaemonStream` | `tools_daemon.go` | `servergateway/flowhealth.go` (22 uses) |
| `SenderDirectory`, `ResolvedSender` | `trigger_senders.go` | `serverapi/github_access.go`, `grpc/server.go` |
| `InboundOptions`, `IntegrationAccess` | `trigger_inbound.go` | `grpc/server.go`, `serverapi`, `serverworker`, `triggertools` tests |
| `TokenControlPlane` | `token_exchange.go` | `grpc/server.go`, e2e tests, `cmd/reliant/commands` test |
| `BackgroundProcessProvider`, `NewDBBackgroundProcessProvider` | `background_provider*.go` | `grpc/server.go`, `serverapi` |
| `TerminalWSHandler`, `WithTerminalCheckouts` | `terminal_ws.go` | `grpc/server.go` |
| `AttachmentContentHandler`, `AttachmentContentPathPrefix` | `attachment_content.go` | `grpc/server.go` |
| `NewMachineWakingInterceptor` | `machine_wake.go` | `grpc/server.go` |
| `ForgeRPCDeadlines` | `forge.go` | `grpc/server.go` |
| `IntegrationMethodsFrom` | `catalog_search.go` | `grpc/server.go` |
| `services.ChatService` (type) | `chat.go` | `serverworker` (`agentruns`), `replaytest` |

## Test-side coupling (P3a wave 2 / P3b)

Test fixtures are shared across services more than production code is. Each
row below is a fixture defined in one service's `_test.go` and used by the
test files of other services. A P3b lane cannot move those tests until the
fixture has a home.

Wave 2 moved the fixtures that need nothing from a service's internals into
non-test packages that only `_test.go` files import:

| Fixture | Defined in | Test files of | Using files | Now |
|---|---|---|---|---|
| `worktreeTestDaemonRouter` | `worktree_test.go` | Background, Daemon, FileSystem, Forge, PackageCommands, Project, Worktree | 19 | `toolexectest.Stub` |
| `fakeDaemonRouter` | `tool_call_test.go` | Chat, Terminal (+WS), ToolCall, ToolsDaemon | 11 | `toolexectest.Recorder` |
| `setupToolCallDurableFixture` / `toolCallDurableFixture` | `tool_call_durable_status_test.go` | Approval, Message, Streaming, ToolCall | 4 | `dbtest.SeedToolCall` / `dbtest.ToolCall` |
| `recordedSignal`, `spawnCancelTemporalClient` | `tool_call_cancel_spawn_test.go` | Chat, ToolCall | 5 | `signaltest.Signal` / `signaltest.Recorder` |
| `newWorkflowListFixture`, `workflowListRepo` | `workflow_list_reads_test.go` | Preset, Workflow | 1 | `workflowlisttest.NewFixture` / `.Repo` |

The rest stay where they are, for a stated reason:

| Fixture | Defined in | Test files of | P3b handling |
|---|---|---|---|
| `strPtr`, `ptrStr` | `chat_send_resume_name_test.go`, `tools_daemon_test.go` | Chat, Inbox, PackageCommands, Run, ToolsDaemon, Trigger | one line each: use `ptr.Of` |
| `authedCtx`, `userCtx` | `package_commands_proxy_test.go`, `daemon_registry_localmodel_test.go` | Background, Daemon, DaemonRegistry, Forge, PackageCommands, ToolsDaemon | one line each: inline `context.WithValue(ctx, auth.UserIDContextKey, id)`, as 192 tests in the package already do |
| `placementFixture`, `newPlacementFixture`, `placementRouter`, `asleepFixture`, `awaitWorktreeStatus` | `worktree_placement_test.go`, `machine_wake_test.go`, `worktree_test.go` | FileSystem, Worktree | they build a real `WorktreeService` (exported constructor). The second of the two lanes to move takes `machine_wake_test.go` and `fs_proxy_routing_test.go`, and its tests import the first lane's handler package. That is allowed because neither production package imports the other |
| `runListFixture`, `newRunListFixture` | `run_list_test.go` | Inbox, Run | builds a real `RunService` (exported). The Inbox lane goes after Run, and its tests import the run handler package |
| `newParkedStream`, `newTestConn`, `registerTestConn`, `localModelRouter`, `recordingUserHub` | `tools_daemon_*_test.go`, `daemon_registry_localmodel_test.go` | DaemonRegistry, ToolsDaemon | they reach ToolsDaemon's unexported connection types (`daemonConnection`, `daemonStream`). `daemon_registry_disk_test.go` and `daemon_registry_localmodel_test.go` exercise both services together, so they move with ToolsDaemon |
| `fakeControlPlaneClient`, `mintCall` | `settings_reliant_test.go` | Settings, Trigger | moving it out of `_test.go` gives `controlplane.Client` a second implementation in the module, and forge then refuses `controlplane`'s `//forge:exclude-contract` (`forge-exclude-contract-multi-impl`). The rule is right: a shared fake is the sign that `Client` is a seam. So the fake's home is the `MockClient` forge generates once P6 moves `Client` into a `contract.go`. Until then, the second of the two lanes copies it |
| `setupTestFileSystemService`, `recordingFSDaemonRouter`, `createProjectDaemonRouter` | `filesystem_test.go`, `fs_proxy_test.go`, `project_create_gitinit_test.go` | FileSystem, Project | used only by `windows_path_test.go`, which mixes three services. Split it by service in P3b |

Mixed-service test files, which are split or assigned in P3b:

- `config_row_tail_test.go` (ToolsDaemon, Scenario)
- `daemon_proxy_machine_state_test.go` (Daemon, Terminal, Background)
- `machine_wake_test.go` (FileSystem, Worktree)
- `user_error_class_test.go` (FileSystem, ToolsDaemon, Chat)
- `windows_path_test.go` (Project, FileSystem, ToolsDaemon)
- `workflow_draft_gate_test.go` (Chat, Preset, Scenario)
- `workflow_list_chat_contract_test.go` (Chat, Workflow)
- `tool_call_status_read_test.go` (Message, Chat)
- `model_endpoint_test.go` (ModelEndpoint, Catalog)

## Wave plan and P3b readiness

**Wave 1 (merged: #772–#777).** One branch and one commit per group:

| Branch | Group | Base |
|---|---|---|
| `refactor/extract-daemonerr` | G1 | origin/main |
| `refactor/extract-machinewake` | G2 | stacked on `refactor/extract-daemonerr` (needs `daemonerr.Unreachable`) |
| `refactor/extract-daemonplacement` | G5 | stacked on `refactor/extract-machinewake`: no code dependency, but both rewrite the same lines of `fs_proxy.go`, `worktree.go` and `worktree_placement.go` |
| `refactor/extract-messageproto` | G4 | origin/main (touches no file the chain does) |
| `refactor/extract-daemonchoice` | G12 | origin/main |

All five branches merge into one tree without conflicts, in any order the
stack allows. Each adds its package to `serverSidePackages` at a different
spot in the list, so the edits do not collide.

Every new package goes into `internal/architecture`'s `serverSidePackages`.
Code moved out of `internal/grpc/services` keeps the same ban on direct
filesystem access.

**Services extraction-ready for P3b, production code only:**

| When | Services |
|---|---|
| Today (no shared group) | Approval, Attachment, Connector, DaemonRegistry, Inbox, MCP, ModelEndpoint, Question, Repo, Scenario, Settings, System, Token, ToolCall |
| After wave 1 | adds Daemon (G1), Message (G4), Streaming (G4), Trigger (G12), Worktree (G1, G2, G5) |
| After wave 2 | adds the remaining 16: Account (G11), Background (G8), Catalog (G10), Chat (G8, G9), Connection (G10), FileSystem (G13), Forge (G3), PackageCommands (G3, G8), Plan (G8), Preset (G6), Project (G7, G11), Run (G9; declares its Chat interface in P3b), Task (G8), Terminal (G13), ToolsDaemon (G7), Workflow (G6) |

**Wave 2.** One branch and one commit per group, all on origin/main except
where noted:

| Branch | Group |
|---|---|
| `refactor/delete-dead-chat-update-mapper` | dead code: `chatUpdateTypeFromString` (+ its tests), `LOG_PREFIX_STREAM` |
| `refactor/extract-daemoncmd` | G3 → `internal/daemoncmd` |
| `refactor/extract-workflowcatalog` | G6 → `internal/workflow/workflowcatalog` |
| `refactor/extract-projectfacts` | G7 → `internal/projectfacts` |
| `refactor/extract-gitremote` | G7 → `internal/gitremote` |
| `refactor/extract-processproto` | G8 → `internal/processproto` |
| `refactor/extract-planproto` | G8 → `internal/planproto` |
| `refactor/extract-runstateproto` | G9 → `internal/runstateproto` |
| `refactor/extract-connectionrpc` | G10 → `internal/connectionrpc` |
| `refactor/extract-bearer-token` | G11 → `auth.BearerToken` |
| `refactor/extract-checkoutstate` | G13 → `internal/checkoutstate` |
| `refactor/extract-toolexectest` | fixtures → `internal/toolexec/toolexectest` |
| `refactor/extract-dbtest-toolcall` | fixtures → `internal/db/dbtest`; stacked on toolexectest, because both edit the import block of `tool_call_durable_status_test.go` |
| `refactor/extract-temporal-signal-recorder` | fixtures → `internal/temporal/signaltest`; stacked on toolexectest, because both rewrite the same `NewToolCallService(...)` lines |
| `refactor/extract-workflowlisttest` | fixtures → `internal/workflow/workflowlisttest` |

**After wave 2, every service is extraction-ready for P3b on the production
side.** No declaration in `internal/grpc/services` is reached by two
services any more. The one service-to-service edge remains RunService →
`*ChatService` (6 exported methods). It creates no cycle; in P3b, Run
declares a consumer interface for those methods.

On the test side, the remaining couplings are the ones in the second table
above. Each has a P3b recipe and none needs another P3a extraction.
