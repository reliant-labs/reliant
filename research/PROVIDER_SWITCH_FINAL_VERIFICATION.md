# Provider switch: final review resolutions

## Policies

- Only an explicit root picker choice naming a different provider binds the chat. Ordinary stored picks, sends and tag-only picks do not bind it. The binding is server-owned `Selection.ChatProvider`, persisted in the existing provider-policy JSON and injected into fresh runs.
- Local root choices resolve against the same daemon/endpoint inventory resolver used by model calls and clear the binding. Unavailable explicit picks are refused before any write or signal. Thread picks on a different provider (including local) are refused on bound chats. Tagged selectors naming providers resolve strictly within that set.
- Every user message and explicit Resume retries unfinished threads, including durable stopped siblings. Ordinary messages preserve unbound opt-in fallback leases. Ordinary recovery cannot move a bound chat off its provider, including by clearing its binding for a local selector.

## Original independent review

| Item | Resolution |
|---|---|
| B1, major 8 | Removed stored-model and tag-only auto-binding. Fresh stored-model regression covers exact, tags and local selectors; existing unbound resolver/local/tag-preference tests remain active. |
| B2 | Local picker uses `local.ListModels`/`local.Resolve`; successful local root pick unbinds, with live/reset regression. |
| Major 1 | Superseded by second-review explicit-binding policy: ordinary recovery must preserve binding, not rebind. Bound sends do not invoke substitution; runtime visibly holds unsupported calls. |
| Major 2 | Global updates skip only thread-owned keys; passthrough fork regression verifies root tracking. |
| Major 3 | Compaction carries descendant/provenance context, distinguishes root, restricts fallback to bound provider. Independent compaction tests included. |
| Major 4 | Approved any-message whole-chat resume; unbound leases preserved. Set-based stopped lookup, async signal dispatch, live/participation dedupe and stable cards/repeated failure reports avoid duplicate retries/notices. |
| Major 5 | Rebuild candidates after awaited lookups; independent ending-run tests included. |
| Major 6 | Cache first non-default version answer per category; bounded-marker regression uses production Temporal harness. |
| Major 7 | Bound cross-provider thread edits are refused. |
| Minor: account pins | Cleanup scoped by thread and dispatch routing epoch, acknowledged on successful model activity. A failed model activity may repeat the idempotent DELETE; no durable epoch subsystem was added. |
| Minor: hold cards | Stable per-chat/thread ErrorID in workflow and real wrapper. |
| Minor: strict tagged picks | Explicit provider set restricts eligible drivers, not merely preference order. |
| Minor: grouped model recovery | Explicit root choice bypasses substitution without disabling recovery of unrelated grouped model inputs on unbound chats. |
| Minor: blocking lookup | Off signal receiver, coalesced; failures logged and next recovery retries. |
| ResumeAllStopped dedupe | Requested agent included in lookup results; one turn per requested/sibling. |
| Wrapper coverage | Independent real ActivityWrapper/flattenForTemporal tests included. |

## Second review

1. Ordinary recovery no longer changes or clears explicit binding; bound sends cannot use off-provider substitution.
2. Root sends, late-message fresh runs and queued continuation runs carry ResumeAllStopped, not only agent-specific launches.
3. Reset/resume always reconciles persisted binding, including explicit empty unbind; binding-only updates are applied by runtime.
4. Recovery snapshots participating live agents before yielding to durable lookup. A fast refailure is not retried again by the same lookup; delayed-lookup regression included.
5. Model patch and binding are persisted by one MutateChatSelection under the same expected-version check; conflict regression verifies neither partially changes.
6. Fixture generator sends the server binding key. Previous histories frozen under 2026-10-24-explicit-provider-binding, current fixture regenerated. Explicit-binding and async-lookup version gates retain old recorded commands.

## Final high blocker: recovery-only startup (2026-10-24)

`resumeWholeChatInput(nil)` now creates `ResumeInput{RecoveryOnly:true}` rather
than an empty positional resume. `resolveResumeTarget` returns graph entry only
for that explicitly new flag. Existing recorded empty ResumeInput values still
use resume_node/checkpoint/single-loop fallback, unchanged: the new boolean is
absent/false in every existing history, so no recorded command changes.

Regressions cover: a real DynamicWorkflow setup call before its sole root loop
while a durable sibling recovers once; the actual embedded parallel-compete
workflow retaining improve_prompt entry for recovery while empty positional
resume still selects implementations; and both root-send/agent-message fresh
launch payloads explicitly carrying RecoveryOnly. Scoped regressions passed
three times. `make test-affected` passed; full runtime/chat/replay packages passed
`-race -count=1`; all current and frozen replay fixtures passed; all 26 lint
gates passed. Logs: `recovery-affected.log`, `recovery-repeat.log`,
`recovery-race-final.log`, `recovery-chat-race-retry.log`, and
`recovery-lint-retry.log` in the external verification directory. An unrelated
chat greenfield probe deadline under load and a shared-cache eviction interrupted
initial attempts; neither assertion nor production timeout was changed.

## Verification

- `make test-affected`: PASS after fixing star-select and Temporal harness guards. Includes generated/docs no-op checks and affected Go lane.
- Full `-race -count=1`: runtime, replaytest, activity handlers, chat, launch, chatselection, db, store, workersetup passed. Runtime/chat/db/replay rerun after rebase; db/store rerun after explicit-column query change.
- New/changed regression selection `-count=3`: chat, runtime, activity handlers, db and workersetup PASS. Independent tester also ran its compaction/wrapper/ending/passthrough tests three times.
- `make lint`: all 26 gating linters PASS, including frontend lint/typecheck.
- Private DBs: repo's forge pgtest harness creates isolated test databases; no access to real-data port 5434.
- Shared cache eviction and machine reconnect interrupted earlier attempts; only successful runs count above. Forge generation temporarily dropped unrelated stubs during cache/package-load failures; rerun restored converged generated outputs. Product bug reports filed for both issues.

Logs outside repository: `/home/workspace/provider-switch-verification/`. Branch rebased onto main `0ee6bc85`. No merge or release performed; PR remains for independent review.
