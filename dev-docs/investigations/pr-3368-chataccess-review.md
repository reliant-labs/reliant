# PR #3368: remaining review coverage

Reviewed against head `65d1c320941155e1d8b951a5e43a11dea22ab585` and its merge base with `origin/main`.

## [B1] Activity test fixtures

The remaining activity test deltas explicitly supply the dependencies formerly
constructed inside the activities. In particular:

- `call_llm_delta_identity_test.go`: all five constructors receive Threads backed
  by the fixture's repository, chataccess, message store and sender store. Delta
  IDs, cancelled partial turns and completed/interrupted assertions are unchanged.
- `call_llm_tools_runtime_test.go`, `agent_node_toolset_test.go`,
  `capabilities_flow_test.go`, `unattended_capabilities_test.go` and
  `router_call_llm_test.go`: the same Threads injection preserves the existing
  tools factory, driver resolver, configuration and MCP binder.
- `drain_agent_messages_test.go`: drain and message-save activities share the
  fixture repository through their explicit thread service. Ordering assertions
  remain intact. `drain_concurrency_test.go` still passes its coordinated store
  wrapper to the drain activity, not the underlying repository, so its concurrency
  interlock is not bypassed.
- `call_llm_usage_ledger_test.go` and `llm_usage_paths_test.go`: usage recorders,
  retry drivers and ledger assertions are retained; only constructor dependencies
  change.
- `signed_thinking_turn_e2e_test.go`: both signed and redacted drivers and their
  content-preservation assertions remain unchanged.
- Remaining call-LLM, save-message, compaction, cleanup, workflow/thread creation,
  result-fetch and roundtrip fixtures follow the same constructor migration.
  Cleanup tool-result reads now call `store.FindToolBlock` with the explicit
  TOOL_RESULT discriminator; preemption reads use `store.GetAgentMessage`.
  These still inspect the real fixture database, not fabricated results.

## [B2] Chat handler fixtures

Reviewed the handler test deltas including the worktree-invariant tail through
line 272. They supply chataccess, mailbox and the extracted store dependencies
against the same fixture ORM. Failure-injecting repository wrappers use
`chataccesstest.For`, and ownership error fixtures use `Failing`/`Func`/`Bound`,
rather than silently replacing the intended errors with successful real reads.
Temporal signalers and tool cancelers are retained in thread-service Deps.

The worktree-invariant tail still checks foreign-project rejection,
FailedPrecondition for a missing main worktree, and successful omitted-worktree
creation with both response and persisted non-null worktree ID equal to the
project's main worktree. No assertion is removed or weakened.

As an additional check, compared assertion lines across all 158 changed test
files in these two directories (5,045 current assertion-start lines). The only
text differences are constructors embedded in assertions in
`chat_person_turn_test.go`, and shifted multiline assertion contexts in
`capabilities_flow_test.go` and `execute_tools_media_test.go`; their predicates
and expected values are unchanged. Generated test helpers were considered as
generated output, not edited.

## [B3] Production thread-service construction

The only production calls to the thread service's `InterruptThread` and
`CancelChatToolCalls` are `rpc_interrupt_thread.go:29` and
`rpc_pause_chat.go:61`. Both use the chat handler's service constructed at
`service.go:187`, or a local fallback; all three constructors supply
`Chats: deps.Chats`. The API graph supplies real chataccess at
`internal/app/compose.go:1097`, as does the worker chat handler in
`internal/app/providers_worker.go` (`NewWorkerChat`). Run interruption delegates
to this chat RPC, not to a separately constructed thread service.

`NewRunLauncher` at `providers_worker.go:338` deliberately constructs a
Repo-only thread service: launch's `ThreadCreator` interface
(`internal/launch/deps.go:163`) exposes only `CreateWorkflowWithThread`, called
at `launcher.go:583`. It cannot interrupt or cancel tool calls.

The worker activity service at `internal/workersetup/setup.go:262` supplies
Messages and Senders and is passed to activity registration. Those consumers
save/read messages and create/fetch threads; none calls either cancellation
method. Other Repo-only constructors serve list-messages, streaming, get-run,
spawn-status, workflow status summaries and DB helpers, not cancellation.
Missing Chats still fails closed in `internal/threads/interrupt.go:94` and
`pause_tool_cancel.go:50`; no compatibility fallback bypasses authorization.

## Validation

Passed in the PR's private checkout:

- `go build ./...`
- `task test:short -- ./internal/chataccess/... ./internal/threads/... ./internal/handlers/chat/... ./internal/workflow/runtime/activities/handlers/...`

No production or test code change was needed for these coverage blockers.
No Go files were changed in this follow-up, so golangci-lint was not run.
The wall-clock risk noted in the review was already refuted by both passes:
fixture timestamps do not determine a boundary-sensitive assertion.
