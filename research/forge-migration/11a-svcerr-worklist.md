# P7a C0 — svcerr worklist (`forge fix connect-errors` report)

Generated from the report of `forge fix connect-errors` at the forge pin
`v0.1.47-0.20261022063914-6a7c22c22a05` over reliant (plan: `11-svcerr-plan.md`, §4 C0). Line numbers are
those of commit `d0a0162c`, the codemod commit, from the idempotent re-run (0 changes).
This is the lanes' input. Re-run the codemod to refresh it: it is idempotent, and
after a lane converts a package's in-process `*connect.Error` readers to
`svcerr.Code`, a re-run drops that package's `svcerr.ToConnect` wrappers.

## What C0 did

| | Sites |
|---|---:|
| `connect.NewError` rewritten | 905 |
| — to a bare svcerr kind | 316 |
| — kept a `*connect.Error` via `svcerr.ToConnect` | 589 |
| `connect.CodeOf` → `svcerr.Code` (test assertions) | 74 |
| **left as written (this worklist)** | **344** |

Everything below is wire-unchanged by C0. Every rewrite reproduces the old code AND
message exactly, so the D sites (a fixed message that drops `err`, e.g.
`svcerr.Internal("failed to list projects")` after `if err != nil`) were rewritten
and are **not** listed here: find them in your package with
`rg -n -B4 'svcerr\.(Internal|NotFound|Unavailable)\(' internal/handlers/<pkg> --glob '!*_test.go'`
and apply §2.1 (`svcerr.WrapOr`) where `err` is in scope.

## Reasons

- **wraps an error value** — the client reads the error's Error() text, unredacted. svcerr.WrapOr(err, svcerr.<Kind>("<summary>")) redacts it and lets a classified err (a store's NotFound, a deadline) keep its code — which changes what the client reads, so it is a reviewed change. Text the user should read goes in the constructor's argument (svcerr.FailedPrecondition(daemonMsg)); a remote Connect client's error is wrapped with svcerr.WithCause, never WrapOr.
- **nil error** — the client reads an empty message, and every svcerr kind has text: return the sentinel svcerr.Err<Kind> (its message is the category) or a constructor with real text.
- **computed message** — the message is computed and could be empty, and an empty detail makes a constructor return its bare sentinel, whose text is not empty: pass the value to svcerr.<Kind>(…) if it never is.
- **computed code** — the code is not a connect.Code constant: name the kind at the site, or move the mapping to the producer (its sentinels become svcerr kinds) and svcerr.Wrap(err).

## Per lane

| Lane | Left as written | wraps an error value | nil error | computed message | computed code | packages kept on `ToConnect` |
|---|---:|---:|---:|---:|---:|---:|
| L1 chat | 34 | 29 | 0 | 4 | 1 | 1 |
| L2 worktree/project | 26 | 21 | 0 | 5 | 0 | 2 |
| L3 workflow/run | 70 | 68 | 1 | 1 | 0 | 4 |
| L4 triggers/integrations | 95 | 81 | 9 | 5 | 0 | 4 |
| L5 daemon proxies + settings | 109 | 94 | 11 | 3 | 1 | 6 |
| Outside the handler lanes | 10 | 9 | 0 | 0 | 1 | 0 |

## L1 chat

**Packages whose rewrites stay `*connect.Error`** (`svcerr.ToConnect`) because an
in-process reader sees one. Switch the reader to `svcerr.Code` / the `svcerr.Is*`
predicates, then re-run the codemod to drop the wrappers. A reader in another lane's
package is a cross-lane dependency: coordinate with that lane.

- `internal/handlers/chat` — reader `internal/handlers/chat/chat_send.go:1507` (connect.CodeOf)

### `internal/handlers/chat` (34)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/chat/chat_crud.go:226` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up workflow draft: %w", err))` |
| `internal/handlers/chat/chat_crud.go:392` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get root workflow: %w", err))` |
| `internal/handlers/chat/chat_crud.go:910` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, err)` |
| `internal/handlers/chat/chat_crud.go:1198` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", err))` |
| `internal/handlers/chat/chat_helpers.go:229` | computed message | `connect.NewError(connect.CodeFailedPrecondition, errors.New(validationErr.Reason))` |
| `internal/handlers/chat/chat_helpers.go:231` | computed message | `connect.NewError(connect.CodeInvalidArgument, errors.New(validationErr.Reason))` |
| `internal/handlers/chat/chat_helpers.go:236` | computed message | `connect.NewError(connect.CodeNotFound, errors.New(notFoundErr.Reason))` |
| `internal/handlers/chat/chat_helpers.go:241` | wraps an error value | `connect.NewError(connect.CodeAlreadyExists, errors.New(alreadyErr.Error()))` |
| `internal/handlers/chat/chat_helpers.go:245` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, err)` |
| `internal/handlers/chat/chat_helpers.go:252` | computed message | `connect.NewError(connect.CodeInternal, errors.New(internalErr.Reason))` |
| `internal/handlers/chat/chat_helpers.go:255` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/chat/chat_interrupt.go:51` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/chat/chat_interrupt.go:53` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/chat/chat_interrupt.go:55` | wraps an error value | `connect.NewError(connect.CodeUnavailable, err)` |
| `internal/handlers/chat/chat_resume_paused.go:134` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume paused chat: %w", err))` |
| `internal/handlers/chat/chat_run_now.go:59` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/chat/chat_run_now.go:64` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", msg.GetWorkflow(), err))` |
| `internal/handlers/chat/chat_run_now.go:68` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/chat/chat_selection.go:260` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/chat/chat_send.go:430` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/chat/chat_send.go:572` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/chat/chat_send.go:649` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", temporalErr))` |
| `internal/handlers/chat/chat_send.go:744` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/chat/chat_send.go:764` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume paused workflow: %w", resumeErr))` |
| `internal/handlers/chat/chat_send.go:837` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message: %w", err))` |
| `internal/handlers/chat/chat_send.go:975` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", inspectErr))` |
| `internal/handlers/chat/chat_send.go:1068` | wraps an error value | `connect.NewError(connect.CodeInternal, saveErr)` |
| `internal/handlers/chat/chat_send.go:1075` | wraps an error value | `connect.NewError(connect.CodeInternal, resumeErr)` |
| `internal/handlers/chat/chat_send.go:1236` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/chat/chat_send.go:1363` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check for an earlier send: %w", err))` |
| `internal/handlers/chat/chat_send_idempotency.go:73` | computed code | `connect.NewError(code, err)` |
| `internal/handlers/chat/chat_send_idempotency.go:233` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retry an undelivered message: %w", err))` |
| `internal/handlers/chat/chat_usage_limit.go:79` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/chat/chat_workflow.go:60` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |

## L2 worktree/project

**Packages whose rewrites stay `*connect.Error`** (`svcerr.ToConnect`) because an
in-process reader sees one. Switch the reader to `svcerr.Code` / the `svcerr.Is*`
predicates, then re-run the codemod to drop the wrappers. A reader in another lane's
package is a cross-lane dependency: coordinate with that lane.

- `internal/handlers/project` — reader `internal/handlers/project/project.go:1775` (errors.As into a *connect.Error)
- `internal/handlers/worktree` — reader `internal/handlers/worktree/machine_wake_test.go:220` (errors.As into a *connect.Error)

### `internal/handlers/project` (7)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/project/project.go:467` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/project/project.go:1396` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/project/project.go:1418` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to initialize git repository: %v", err))` |
| `internal/handlers/project/project.go:1422` | computed message | `connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", initResp.Error))` |
| `internal/handlers/project/project.go:1748` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("git pull failed: %w", err))` |
| `internal/handlers/project/project.go:1792` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete project_daemons row: %w", err))` |
| `internal/handlers/project/project.go:1829` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("git reclone failed: %w", err))` |

### `internal/handlers/worktree` (19)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/worktree/worktree.go:353` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("cannot access worktree directory: %w", err))` |
| `internal/handlers/worktree/worktree.go:418` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/worktree/worktree.go:559` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, branchHeld)` |
| `internal/handlers/worktree/worktree.go:1056` | wraps an error value | `connect.NewError(connect.CodeUnavailable, err)` |
| `internal/handlers/worktree/worktree.go:1781` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("could not reach the machine that holds this workspace: %w", err))` |
| `internal/handlers/worktree/worktree.go:1854` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get worktree changes: %w", err))` |
| `internal/handlers/worktree/worktree.go:1857` | computed message | `connect.NewError(connect.CodeInternal, fmt.Errorf("%s", changesResp.Error))` |
| `internal/handlers/worktree/worktree.go:1939` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get git status: %w", err))` |
| `internal/handlers/worktree/worktree.go:2106` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stage files: %w", err))` |
| `internal/handlers/worktree/worktree.go:2161` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to unstage files: %w", err))` |
| `internal/handlers/worktree/worktree.go:2215` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to commit: %w", err))` |
| `internal/handlers/worktree/worktree.go:2260` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to push: %w", err))` |
| `internal/handlers/worktree/worktree.go:2303` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to pull: %w", err))` |
| `internal/handlers/worktree/worktree.go:2443` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create PR: %w", err))` |
| `internal/handlers/worktree/worktree.go:2449` | computed message | `connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", errMsg))` |
| `internal/handlers/worktree/worktree.go:2451` | computed message | `connect.NewError(connect.CodeInternal, fmt.Errorf("%s", errMsg))` |
| `internal/handlers/worktree/worktree.go:2522` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to revert files: %w", err))` |
| `internal/handlers/worktree/worktree.go:2526` | computed message | `connect.NewError(connect.CodeInternal, fmt.Errorf("%s", revertResp.Error))` |
| `internal/handlers/worktree/worktree_placement.go:60` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon available to %s worktree: %w", verb, err))` |

## L3 workflow/run

**Packages whose rewrites stay `*connect.Error`** (`svcerr.ToConnect`) because an
in-process reader sees one. Switch the reader to `svcerr.Code` / the `svcerr.Is*`
predicates, then re-run the codemod to drop the wrappers. A reader in another lane's
package is a cross-lane dependency: coordinate with that lane.

- `internal/handlers/approval` — reader `internal/handlers/approval/approval_test.go:225` (require.ErrorAs into a *connect.Error)
- `internal/handlers/message` — reader `internal/handlers/chat/chat_send.go:1507` (connect.CodeOf)
- `internal/handlers/run` — reader `internal/handlers/chat/chat_send.go:1507` (connect.CodeOf)
- `internal/handlers/streaming` — reader `internal/handlers/chat/chat_send.go:1507` (connect.CodeOf)

### `internal/handlers/plan` (1)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/plan/plan.go:177` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get plan: %w", err))` |

### `internal/handlers/question` (4)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/question/async_answer.go:165` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("check the asking thread's questions: %w", err))` |
| `internal/handlers/question/async_answer.go:190` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("queue the answer: %w", err))` |
| `internal/handlers/question/question.go:148` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("list pending questions: %w", err))` |
| `internal/handlers/question/rpc_list_async_questions.go:42` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("list async questions: %w", err))` |

### `internal/handlers/scenario` (21)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/scenario/scenario.go:121` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list scenarios: %w", err))` |
| `internal/handlers/scenario/scenario.go:127` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert scenario: %w", err))` |
| `internal/handlers/scenario/scenario.go:139` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to discover project scenarios: %w", err))` |
| `internal/handlers/scenario/scenario.go:195` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to parse workflow: %w", err))` |
| `internal/handlers/scenario/scenario.go:212` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal scenario: %w", err))` |
| `internal/handlers/scenario/scenario.go:232` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal result: %w", err))` |
| `internal/handlers/scenario/scenario.go:238` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save scenario: %w", err))` |
| `internal/handlers/scenario/scenario.go:243` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert scenario: %w", err))` |
| `internal/handlers/scenario/scenario.go:311` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/scenario/scenario.go:328` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scenario %s: %w", found.Name, err))` |
| `internal/handlers/scenario/scenario.go:369` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/scenario/scenario.go:387` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to parse workflow: %w", err))` |
| `internal/handlers/scenario/scenario.go:397` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal result: %w", err))` |
| `internal/handlers/scenario/scenario.go:400` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update scenario result: %w", err))` |
| `internal/handlers/scenario/scenario.go:429` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete scenario: %w", err))` |
| `internal/handlers/scenario/scenario.go:480` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid scenario YAML: %w", err))` |
| `internal/handlers/scenario/scenario.go:503` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write scenario file via daemon: %w", err))` |
| `internal/handlers/scenario/scenario.go:512` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert scenario: %w", err))` |
| `internal/handlers/scenario/scenario.go:567` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/scenario/scenario.go:602` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/scenario/scenario.go:608` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize scenario: %w", err))` |

### `internal/handlers/streaming` (1)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/streaming/streaming.go:80` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |

### `internal/handlers/tool_call` (2)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/tool_call/tool_call.go:89` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("could not cancel this spawn: %w", err))` |
| `internal/handlers/tool_call/tool_call.go:300` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("could not reach the machine running this command: %w", err))` |

### `internal/handlers/workflow` (41)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/workflow/workflow.go:118` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read builtin workflows: %w", err))` |
| `internal/handlers/workflow/workflow.go:450` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get workflow draft by ID: %w", err))` |
| `internal/handlers/workflow/workflow.go:461` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check slug conflict: %w", err))` |
| `internal/handlers/workflow/workflow.go:474` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:499` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load project workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:509` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow name: %w", err))` |
| `internal/handlers/workflow/workflow.go:519` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:554` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get project: %w", err))` |
| `internal/handlers/workflow/workflow.go:563` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid source path: %w", err))` |
| `internal/handlers/workflow/workflow.go:567` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve workflows directory: %w", err))` |
| `internal/handlers/workflow/workflow.go:585` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write workflow to file via daemon: %w", err))` |
| `internal/handlers/workflow/workflow.go:661` | wraps an error value | `connect.NewError(connect.CodeAlreadyExists, err)` |
| `internal/handlers/workflow/workflow.go:663` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:668` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get updated workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:674` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:728` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:737` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow visibility: %w", err))` |
| `internal/handlers/workflow/workflow.go:742` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse stored workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:860` | computed message | `connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", completeRejectedMessage(check)))` |
| `internal/handlers/workflow/workflow.go:877` | wraps an error value | `connect.NewError(connect.CodeAlreadyExists, err)` |
| `internal/handlers/workflow/workflow.go:948` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:955` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow name: %w", err))` |
| `internal/handlers/workflow/workflow.go:962` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load project workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:984` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse template %q: %w", template, err))` |
| `internal/handlers/workflow/workflow.go:993` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal new workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1053` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow status: %w", err))` |
| `internal/handlers/workflow/workflow.go:1085` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load draft: %w", err))` |
| `internal/handlers/workflow/workflow.go:1096` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1125` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse builtin workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1163` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1168` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve workflow: %w", resolveErr))` |
| `internal/handlers/workflow/workflow.go:1222` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1245` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1318` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid YAML: %w", err))` |
| `internal/handlers/workflow/workflow.go:1336` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1396` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1450` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse builtin workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1464` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1474` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse stored workflow: %w", err))` |
| `internal/handlers/workflow/workflow.go:1480` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal workflow to YAML: %w", err))` |
| `internal/handlers/workflow/workflow.go:1519` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal workflow to YAML: %w", err))` |

## L4 triggers/integrations

**Packages whose rewrites stay `*connect.Error`** (`svcerr.ToConnect`) because an
in-process reader sees one. Switch the reader to `svcerr.Code` / the `svcerr.Is*`
predicates, then re-run the codemod to drop the wrappers. A reader in another lane's
package is a cross-lane dependency: coordinate with that lane.

- `internal/handlers/catalog` — reader `internal/handlers/catalog/catalog_input_options_test.go:40` (errors.As into a *connect.Error)
- `internal/handlers/connection` — reader `internal/handlers/connection/connection.go:26` (errors.As into a *connect.Error)
- `internal/handlers/inbox` — reader `internal/handlers/trigger/trigger_workflow.go:575` (errors.As into a *connect.Error)
- `internal/handlers/trigger` — reader `internal/handlers/trigger/trigger_workflow.go:575` (errors.As into a *connect.Error)

### `internal/handlers/catalog` (11)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/catalog/catalog.go:416` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/catalog/catalog_search.go:156` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/catalog/catalog_search.go:158` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/catalog/catalog_search.go:160` | wraps an error value | `connect.NewError(connect.CodeCanceled, err)` |
| `internal/handlers/catalog/catalog_search.go:162` | wraps an error value | `connect.NewError(connect.CodeDeadlineExceeded, err)` |
| `internal/handlers/catalog/rpc_list_input_options.go:119` | computed message | `connect.NewError(connect.CodeNotFound, errors.New(inputoptions.Message(err)))` |
| `internal/handlers/catalog/rpc_list_input_options.go:122` | computed message | `connect.NewError(connect.CodeInvalidArgument, errors.New(inputoptions.Message(err)))` |
| `internal/handlers/catalog/rpc_list_input_options.go:125` | computed message | `connect.NewError(connect.CodeUnavailable, errors.New(upstream.Message))` |
| `internal/handlers/catalog/rpc_list_input_options.go:127` | computed message | `connect.NewError(connect.CodeFailedPrecondition, errors.New(upstream.Message))` |
| `internal/handlers/catalog/rpc_list_input_options.go:129` | wraps an error value | `connect.NewError(connect.CodeCanceled, err)` |
| `internal/handlers/catalog/rpc_list_input_options.go:131` | wraps an error value | `connect.NewError(connect.CodeDeadlineExceeded, err)` |

### `internal/handlers/connection` (7)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/connection/connection.go:32` | wraps an error value | `connect.NewError(connect.CodeCanceled, err)` |
| `internal/handlers/connection/connection.go:35` | wraps an error value | `connect.NewError(connect.CodeDeadlineExceeded, err)` |
| `internal/handlers/connection/connection.go:42` | wraps an error value | `connect.NewError(connect.CodeNotFound, msg)` |
| `internal/handlers/connection/connection.go:44` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, msg)` |
| `internal/handlers/connection/connection.go:46` | wraps an error value | `connect.NewError(connect.CodeAlreadyExists, msg)` |
| `internal/handlers/connection/connection.go:48` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, msg)` |
| `internal/handlers/connection/connection.go:50` | wraps an error value | `connect.NewError(connect.CodeUnavailable, msg)` |

### `internal/handlers/connector` (19)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/connector/connector.go:62` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector.go:68` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/connector/connector.go:73` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/connector/connector.go:256` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector.go:261` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("list connectors: %w", err))` |
| `internal/handlers/connector/connector.go:278` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector.go:290` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("revoke connector: %w", err))` |
| `internal/handlers/connector/connector.go:296` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("revoke connector credential: %w", err))` |
| `internal/handlers/connector/connector.go:309` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector.go:324` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("list connector activity: %w", err))` |
| `internal/handlers/connector/connector.go:353` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector_consent.go:33` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector_consent.go:54` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("record consent: %w", err))` |
| `internal/handlers/connector/connector_consent.go:97` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/connector/connector_consent.go:101` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/connector/connector_consent.go:118` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector_consent.go:123` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("list authorized clients: %w", err))` |
| `internal/handlers/connector/connector_consent.go:159` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/connector/connector_consent.go:169` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("revoke authorization: %w", err))` |

### `internal/handlers/mcp` (26)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/mcp/mcp.go:727` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:794` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:845` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:870` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list tools: %w", err))` |
| `internal/handlers/mcp/mcp.go:926` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:967` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:977` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/mcp/mcp.go:1297` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1301` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/mcp/mcp.go:1346` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1431` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load recommended MCPs: %w", err))` |
| `internal/handlers/mcp/mcp.go:1552` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1562` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1580` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1586` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/mcp/mcp.go:1598` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("configuration saved to %s scope but failed to restart server: %w", scoped.Scope.String(), err))` |
| `internal/handlers/mcp/mcp.go:1635` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1645` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1656` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1662` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/mcp/mcp.go:1750` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1770` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read source MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1775` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read target MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1785` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write target MCP configuration: %w", err))` |
| `internal/handlers/mcp/mcp.go:1793` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to finalize move from %s to %s: %w", scoped.Scope.String(), targetScope.String(), err))` |
| `internal/handlers/mcp/mcp.go:1800` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |

### `internal/handlers/model_endpoint` (7)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/model_endpoint/model_endpoint.go:58` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/model_endpoint/model_endpoint.go:60` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, err)` |
| `internal/handlers/model_endpoint/model_endpoint.go:62` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/model_endpoint/model_endpoint.go:64` | wraps an error value | `connect.NewError(connect.CodeAlreadyExists, err)` |
| `internal/handlers/model_endpoint/model_endpoint.go:66` | wraps an error value | `connect.NewError(connect.CodeUnavailable, err)` |
| `internal/handlers/model_endpoint/model_endpoint.go:68` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/model_endpoint/model_endpoint.go:74` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |

### `internal/handlers/trigger` (25)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/trigger/trigger.go:411` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("start fire: %w", err))` |
| `internal/handlers/trigger/trigger.go:502` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger.go:654` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, cfgErr)` |
| `internal/handlers/trigger/trigger.go:656` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger.go:754` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", def.Workflow, err))` |
| `internal/handlers/trigger/trigger.go:764` | computed message | `connect.NewError(connect.CodeNotFound, errors.New(msg))` |
| `internal/handlers/trigger/trigger.go:777` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, declErr)` |
| `internal/handlers/trigger/trigger.go:779` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger.go:977` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, cfgErr)` |
| `internal/handlers/trigger/trigger.go:979` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("schedule backend: %w", err))` |
| `internal/handlers/trigger/trigger_inbound.go:78` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fe)` |
| `internal/handlers/trigger/trigger_inbound.go:80` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger_inbound.go:193` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger_inbound.go:201` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s triggers need access to your %s repositories: %w", cfg.Integration, cfg.Integration, err))` |
| `internal/handlers/trigger/trigger_inbound.go:229` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger_inbound.go:249` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("seal webhook secret: %w", err))` |
| `internal/handlers/trigger/trigger_inbound.go:261` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/trigger/trigger_inbound.go:326` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("record fire: %w", err))` |
| `internal/handlers/trigger/trigger_replay.go:69` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("render event %s: %w", firing.Event.ID, err))` |
| `internal/handlers/trigger/trigger_replay.go:105` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/trigger/trigger_replay.go:114` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", msg.GetWorkflow(), err))` |
| `internal/handlers/trigger/trigger_replay.go:120` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/trigger/trigger_replay.go:126` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("input %q is not representable: %w", name, err))` |
| `internal/handlers/trigger/trigger_senders.go:60` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/trigger/trigger_workflow.go:68` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", msg.Workflow, err))` |

## L5 daemon proxies + settings

**Packages whose rewrites stay `*connect.Error`** (`svcerr.ToConnect`) because an
in-process reader sees one. Switch the reader to `svcerr.Code` / the `svcerr.Is*`
predicates, then re-run the codemod to drop the wrappers. A reader in another lane's
package is a cross-lane dependency: coordinate with that lane.

- `internal/handlers/account` — reader `internal/handlers/account/account.go:147` (connect.CodeOf)
- `internal/handlers/daemon_registry` — reader `internal/handlers/daemon_registry/daemon_registry.go:234` (errors.As into a *connect.Error)
- `internal/handlers/file_system` — reader `internal/handlers/file_system/fs_proxy_checkout_test.go:116` (errors.As into a *connect.Error)
- `internal/handlers/forge` — reader `internal/handlers/forge/forge.go:423` (connect.CodeOf)
- `internal/handlers/token` — reader `internal/handlers/token/token_git_test.go:71` (errors.As into a *connect.Error)
- `internal/handlers/tools_daemon` — reader `internal/handlers/tools_daemon/daemon_registry_localmodel_test.go:366` (errors.As into a *connect.Error)

### `internal/handlers/account` (1)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/account/account.go:158` | computed message | `connect.NewError(connect.CodeFailedPrecondition, errors.New(result.Blockers[0].Detail))` |

### `internal/handlers/background` (8)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/background/background_proxy.go:44` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))` |
| `internal/handlers/background/background_proxy.go:56` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/background/background_proxy.go:60` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |
| `internal/handlers/background/background_proxy.go:72` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/background/background_proxy.go:120` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/background/background_proxy.go:153` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/background/background_proxy.go:183` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/background/background_proxy.go:192` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("subscribe process output: %w", err))` |

### `internal/handlers/daemon` (6)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/daemon/daemon_proxy.go:41` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))` |
| `internal/handlers/daemon/daemon_proxy.go:61` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |
| `internal/handlers/daemon/daemon_proxy.go:92` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", doing, err))` |
| `internal/handlers/daemon/daemon_proxy.go:115` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))` |
| `internal/handlers/daemon/daemon_proxy.go:132` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |
| `internal/handlers/daemon/daemon_proxy.go:164` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |

### `internal/handlers/daemon_registry` (15)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/daemon_registry/daemon_registry.go:42` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/daemon_registry/daemon_registry.go:53` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list daemons: %w", err))` |
| `internal/handlers/daemon_registry/daemon_registry.go:88` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("reading daemon liveness: %w", err))` |
| `internal/handlers/daemon_registry/daemon_registry.go:103` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/daemon_registry/daemon_registry.go:111` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found: %w", err))` |
| `internal/handlers/daemon_registry/daemon_registry.go:157` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/daemon_registry/daemon_registry.go:165` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("decode %s response: %w", diskwatch.ClearCachesCommand, err))` |
| `internal/handlers/daemon_registry/daemon_registry.go:206` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/daemon_registry/daemon_registry.go:231` | wraps an error value | `connect.NewError(connect.CodeUnavailable, err)` |
| `internal/handlers/daemon_registry/daemon_registry.go:237` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/daemon_registry/daemon_registry.go:270` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/daemon_registry/daemon_registry.go:288` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/daemon_registry/daemon_registry.go:293` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("listing daemons: %w", err))` |
| `internal/handlers/daemon_registry/daemon_registry.go:334` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/daemon_registry/daemon_registry.go:344` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found: %w", err))` |

### `internal/handlers/file_system` (21)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/file_system/fs_proxy.go:71` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))` |
| `internal/handlers/file_system/fs_proxy.go:213` | wraps an error value | `connect.NewError(connect.CodePermissionDenied, err)` |
| `internal/handlers/file_system/fs_proxy.go:217` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/file_system/fs_proxy.go:248` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))` |
| `internal/handlers/file_system/fs_proxy.go:266` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |
| `internal/handlers/file_system/fs_proxy.go:298` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/file_system/fs_proxy.go:300` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/file_system/fs_proxy.go:310` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:421` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:459` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:497` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:552` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:643` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:687` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("decode preview bytes: %w", err))` |
| `internal/handlers/file_system/fs_proxy.go:711` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:765` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:798` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:843` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:939` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:1022` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/file_system/fs_proxy.go:1076` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |

### `internal/handlers/forge` (2)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/forge/forge.go:439` | computed message | `connect.NewError(connect.CodeDeadlineExceeded, errors.New(message))` |
| `internal/handlers/forge/forge.go:441` | computed message | `connect.NewError(connect.CodeFailedPrecondition, errors.New(message))` |

### `internal/handlers/package_commands` (16)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/package_commands/package_commands.go:96` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found: %w", err))` |
| `internal/handlers/package_commands/package_commands.go:128` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to run command: %w", err))` |
| `internal/handlers/package_commands/package_commands.go:171` | nil error | `connect.NewError(connect.CodeInvalidArgument, nil)` |
| `internal/handlers/package_commands/package_commands.go:176` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/package_commands/package_commands.go:191` | nil error | `connect.NewError(connect.CodeInvalidArgument, nil)` |
| `internal/handlers/package_commands/package_commands.go:197` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/package_commands/package_commands.go:203` | wraps an error value | `connect.NewError(connect.CodeNotFound, err)` |
| `internal/handlers/package_commands/package_commands.go:229` | nil error | `connect.NewError(connect.CodeInvalidArgument, nil)` |
| `internal/handlers/package_commands/package_commands.go:240` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to kill process: %w", err))` |
| `internal/handlers/package_commands/package_commands.go:256` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/package_commands/package_commands.go:267` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found: %w", err))` |
| `internal/handlers/package_commands/package_commands.go:274` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list command favorites: %w", err))` |
| `internal/handlers/package_commands/package_commands.go:290` | nil error | `connect.NewError(connect.CodeUnauthenticated, nil)` |
| `internal/handlers/package_commands/package_commands.go:306` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found: %w", err))` |
| `internal/handlers/package_commands/package_commands.go:318` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update command favorite: %w", err))` |
| `internal/handlers/package_commands/package_commands_proxy.go:85` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found: %w", err))` |

### `internal/handlers/preset` (8)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/preset/preset.go:168` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("preset not found: %w", err))` |
| `internal/handlers/preset/preset.go:173` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert preset: %w", err))` |
| `internal/handlers/preset/preset.go:244` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %w", wfErr))` |
| `internal/handlers/preset/preset.go:554` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create preset: %w", err))` |
| `internal/handlers/preset/preset.go:703` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preset: %w", err))` |
| `internal/handlers/preset/preset.go:774` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete preset: %w", err))` |
| `internal/handlers/preset/preset.go:870` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize defaults: %w", err))` |
| `internal/handlers/preset/preset.go:875` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to set default preset: %w", err))` |

### `internal/handlers/settings` (6)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/settings/settings.go:443` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get hidden workflows: %w", err))` |
| `internal/handlers/settings/settings.go:447` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get hidden presets: %w", err))` |
| `internal/handlers/settings/settings.go:948` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Codex OAuth: %w", err))` |
| `internal/handlers/settings/settings.go:982` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Antigravity OAuth: %w", err))` |
| `internal/handlers/settings/settings.go:1107` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Claude OAuth: %w", err))` |
| `internal/handlers/settings/settings.go:1147` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to mint Reliant LLM key: %w", err))` |

### `internal/handlers/system` (1)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/system/system.go:131` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to complete %s OAuth sign-in: %w", provider, err))` |

### `internal/handlers/terminal` (15)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/terminal/terminal_proxy.go:98` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/terminal/terminal_proxy.go:103` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:111` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/terminal/terminal_proxy.go:124` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:155` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/terminal/terminal_proxy.go:167` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:186` | wraps an error value | `connect.NewError(connect.CodeInternal, err)` |
| `internal/handlers/terminal/terminal_proxy.go:193` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:215` | wraps an error value | `connect.NewError(connect.CodeUnauthenticated, err)` |
| `internal/handlers/terminal/terminal_proxy.go:221` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to receive initial message: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:235` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal create request: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:245` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("create terminal session: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:250` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("create terminal session: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:261` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal create response: %w", err))` |
| `internal/handlers/terminal/terminal_proxy.go:291` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("subscribe terminal output: %w", err))` |

### `internal/handlers/token` (4)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/token/token.go:132` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/token/token.go:291` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/token/token_exchange.go:129` | wraps an error value | `connect.NewError(connect.CodeInvalidArgument, err)` |
| `internal/handlers/token/token_git.go:83` | computed code | `connect.NewError(code, errors.New(msg))` |

### `internal/handlers/tools_daemon` (6)

| Site | Reason | Expression |
|---|---|---|
| `internal/handlers/tools_daemon/tools_daemon.go:259` | wraps an error value | `connect.NewError(connect.CodeUnavailable, svcerr.Draining( "gateway draining: this daemon-gateway replica is shutting down; reconnect to reach a live replica"))` |
| `internal/handlers/tools_daemon/tools_daemon.go:977` | nil error | `connect.NewError(connect.CodeInvalidArgument, nil)` |
| `internal/handlers/tools_daemon/tools_daemon.go:1005` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list project paths: %w", err))` |
| `internal/handlers/tools_daemon/tools_daemon.go:1011` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to encode capabilities: %w", err))` |
| `internal/handlers/tools_daemon/tools_daemon.go:1015` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to encode project paths: %w", err))` |
| `internal/handlers/tools_daemon/tools_daemon.go:1031` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist daemon registration: %w", err))` |

## Outside the handler lanes

Not in a handler lane. `internal/daemonerr`, `internal/daemoncmd`, `internal/machinewake`
and `internal/toolexec` are L0's producer packages; `internal/grpc/interceptors` sits
outside `svcerr.Interceptor` and is out of P7a (plan §5, "Interceptors").

### `internal/daemoncmd` (2)

| Site | Reason | Expression |
|---|---|---|
| `internal/daemoncmd/daemoncmd.go:44` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("marshal %s request: %w", commandType, err))` |
| `internal/daemoncmd/daemoncmd.go:53` | wraps an error value | `connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal %s response: %w", commandType, err))` |

### `internal/daemonerr` (4)

| Site | Reason | Expression |
|---|---|---|
| `internal/daemonerr/daemonerr.go:233` | computed code | `connect.NewError(code, err)` |
| `internal/daemonerr/daemonerr.go:240` | wraps an error value | `connect.NewError(connect.CodeFailedPrecondition, err)` |
| `internal/daemonerr/daemonerr.go:254` | wraps an error value | `connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon command %s: %w", commandType, err))` |
| `internal/daemonerr/daemonerr.go:257` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon command %s failed: %w", commandType, err))` |

### `internal/grpc/interceptors` (2)

| Site | Reason | Expression |
|---|---|---|
| `internal/grpc/interceptors/auth.go:216` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("token verification unavailable: %w", err))` |
| `internal/grpc/interceptors/daemon_auth.go:64` | wraps an error value | `connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon credential verification unavailable: %w", err))` |

### `internal/machinewake` (1)

| Site | Reason | Expression |
|---|---|---|
| `internal/machinewake/machinewake.go:160` | wraps an error value | `connect.NewError(connect.CodeUnavailable, svcerr.WithClass(errors.New(e.Error()), svcerr.ClassUser))` |

### `internal/toolexec` (1)

| Site | Reason | Expression |
|---|---|---|
| `internal/toolexec/daemon_router_nats.go:911` | wraps an error value | `connect.NewError(connect.CodeUnavailable, errDaemonNotConnected)` |
