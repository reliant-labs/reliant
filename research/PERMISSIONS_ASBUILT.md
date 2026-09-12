# Permission Model — As Built

Research snapshot. All paths relative to `reliant/` (module `github.com/reliant-labs/reliant`), branch `engine`.

---

## Executive summary

The permission ladder is a **tool-handout policy, not an authority boundary**. Because the full shell
family is granted at every level (`permissions.go:47-56`) and the daemon applies no policy to
first-party traffic (`daemonruntime/runtime.go:960-968`), a nominally `readonly` agent has
**unrestricted arbitrary code execution as the daemon user**. The only thing `mutating` adds over
`readonly` is four structured file-editing tools, each of which has a one-line shell equivalent.

The one mechanism designed to gate individual dangerous commands — `RequiresPermission` — is
**dead code in production**. It is implemented by ~35 tools and called by nothing outside tests.

---

## Q2 — What the ladder EXCLUSIVELY gates

Hypothesis confirmed, with one correction.

Enforcement is a single check in `execute_tools.go:310-325`:
`MinimumPermissionForTool(toolName)` vs. the granted level, error string returned to the model on
failure. `MinimumPermissionForTool` (`permissions.go:76-99`) hard-codes `spawn` and `agent` to
orchestrator and otherwise derives from tags.

`mutating` over `readonly` buys exactly:

| Tool | Shell equivalent available at readonly |
|---|---|
| `write` | `cat > f <<'EOF'` |
| `edit` | `sed -i` / `python -c` |
| `find_replace` | `sed -i` |
| `move_code` | `git mv`, `mv` |

`orchestrator` over `mutating` buys exactly `spawn` and `agent`. **Correction to the brief:**
`spawn_status` and `spawn_send` are deliberately *not* orchestrator-gated — see the comment at
`permissions.go:78-82`.

`readonly` *loses* nothing relative to `mutating` on the read side: both get `fetch` and `websearch`
(`permissions.go:58-60`).

So the ladder's true semantic is **"which tools appear in the model's tool array"**. It is an
affordance/nudge mechanism. It is not a capability boundary, and the source says so explicitly at
`permissions.go:100-112`: *"'readonly' no longer means the agent cannot write; it means the agent is
not HANDED write tools... Callers needing a hard read-only boundary must enforce it below the tool
layer (sandbox/filesystem), not via this gate."*

---

## Q3 — The bash escape surface

**There is no command filtering on the executed path.** Trace, end to end:

1. `shellTool.Execute` (`shell.go:329`) — checks `rctx.Daemon != nil`, then applies exactly two
   refusals, both of which are **ergonomic guards, not security**:
   - `unscopedSearchRefusal` (`shell.go:336`) — rejects filesystem-wide scans so they don't burn the
     timeout.
   - `ripgrepReplaceRefusal` (`shell.go:342`) — rejects `rg -r`, which is `--replace` and silently
     corrupts output.
   Neither inspects intent. `rm -rf ~`, `curl … | sh`, `nc -e /bin/sh` all pass.
2. Dispatch as `daemon.RunCommandRequest` (`shell.go:379`).
3. Daemon gate `handleDaemonCommand` (`runtime.go:960-968`) — `daemonpolicy.FromProto(req.GetPolicy())`.
   **First-party agent traffic carries no policy**, and the code comments confirm it is "unaffected."
4. `handleExecRun` → `buildExecCommand` (`cmd_exec.go:42-50`) → `exec.CommandContext(ctx, "bash", "-c", req.Command)`.

`bannedShellCommands` (`shell.go:148-151`) is a 6-entry list — `alias, axel, aria2c, w3m, links, xh`
— and `safeReadOnlyShellCommands` (`shell.go:154-166`) is its counterpart. **Both are referenced
only from `RequiresPermission` (`shell.go:296-327`), which nothing calls.** They are inert.

What a readonly agent can therefore do: write/delete any file the daemon user can, install software,
open network connections, read `~/.ssh` and `~/.aws`, exfiltrate via `curl`, start unbounded
background processes via `run_in_background`, and read the daemon's full environment — because
`ChildEnv` (`daemonpolicy/policy.go:44-49`) returns `os.Environ()` unfiltered when the policy is nil.

---

## Q1 — Enumeration of enforcement points

| # | Mechanism | Where enforced | What it gates | Who sets it | Real? |
|---|---|---|---|---|---|
| 1 | **Permission ladder** | `execute_tools.go:310-325` | Rejects a tool call whose `MinimumPermissionForTool` exceeds the granted level | workflow `tools_config.permission`; default `mutating` (`call_llm.go:802-806`) | **Yes** — the only live runtime gate |
| 2 | **Parent-permission cap** | `call_llm.go:807-815`; derived in `workflow.go:~2600-2620` (`resolveParentPermission`) | A child workflow's permission is clamped to ≤ parent's. `plan`→readonly, `manual`/`auto`→mutating | propagated via `buildSpawnChildInputs` | **Yes** |
| 3 | **`tools:` filter** | `ExpandToolFilter` / `ExpandToolFilterWithSpawn` (`registry.go`) at request-build time in `call_llm` | Which tools are in the LLM's tool array | workflow YAML `filter:` | **Partial** — shapes the array only; *not* re-checked at execute time and not enforced against `load_tool` (known issue) |
| 4 | **`mode:` (plan/auto/manual)** | Pure YAML/CEL in `agent.yaml:201,212,214` | Selects prompt, filter (`['tag:plan','tag:shell']`), and permission (`readonly`) | user/caller input | **Derived only** — see Q4 |
| 5 | **Tool tags** | `minimumPermissionFromTags` (`permissions.go:113-128`); `TagPlan` membership (`registry.go:426-525`) | Feeds #1 and #3 | tag literals in the registry table | **Yes**, as input to #1/#3 |
| 6 | **`RequiresPermission`** | Declared `tools.go:76`, `tool_wrapper.go:229,282`; shell impl `shell.go:296` | *Intended*: per-call interactive approval + banned-command block | — | **NO — dead.** Only non-test caller is the wrapper delegating to itself (`tool_wrapper.go:290`) |
| 7 | **Approval nodes** | `runtime/approval_flow.go`, `step_executor.go` | A workflow-graph node that pauses for human input | workflow author | **Yes**, but author-placed and orthogonal to tools |
| 8 | **`daemonpolicy` confinement** | `runtime.go:963-968`; `policy.go:170-195`; `ChildEnv`, `ResolveDir` | Exec mode (denied/allowlist/unrestricted), path root, env scrubbing | `connectorgrant.Grant.ToPolicy` (`connectorgrant/contract.go:178`), delivered via `mcpserver/server.go:329` | **Yes — but ONLY for third-party MCP connectors.** Nil for agent shell |
| 9 | **Spawn preset allowlist** | `execute_tools.go:~327` | `spawn` preset must be in `toolCall.AvailablePresets` | workflow | Yes, narrow |
| 10 | **Spawn depth cap** | `call_llm.go:~795` (`maxSpawnDepth = 1`) | Recursion depth | const | Yes |

---

## Q4 — `mode: plan`

Plan mode is **not a permission primitive**. It is a YAML/CEL convention inside `agent.yaml` that
fans out into three *existing* mechanisms:

- `agent.yaml:201` — appends `planning_prompt` to the system prompt (instruction).
- `agent.yaml:212` — sets `filter: ['tag:plan', 'tag:shell']` (tool array shaping).
- `agent.yaml:214` — sets `permission: 'readonly'` (the ladder).

`TagPlan` is the filter's mechanism, not plan mode's enforcement. Note `agent.yaml:212` explicitly
adds `tag:shell` alongside `tag:plan`, and `registry.go:461-463` tags `shell_list`/`shell_output`/
`shell_wait` with `TagPlan` directly.

**It does not prevent writes.** Nothing stops a plan-mode agent from `echo x > file.go` — it holds
the shell by design, and the shell is unfiltered (Q3). Plan mode is an instruction plus a reduced
tool menu.

The one place it has teeth is #2: a plan-mode parent caps its spawned children at `readonly`
(`workflow.go` `resolveParentPermission`) — but that inherits the same weakness.

---

## Q5 — `RequiresPermission`

**Implemented by ~35 tools. Called by zero production code paths.**

- Interface: `tools.go:76` and `tool_wrapper.go:229`.
- Generic bridge: `tool_wrapper.go:282-291` — unmarshals params and delegates to the inner tool.
- `rg '\.RequiresPermission\(' --glob '!*_test.go'` returns exactly **one** hit: `tool_wrapper.go:290`,
  the wrapper calling its own inner tool. `code_context` on the wrapper method confirms the only
  inbound edges are `file_concurrency_test.go:77` and `response_tool_test.go:285`.

The shell **does** implement it (`shell.go:296-327`), and it is the most sophisticated implementation
in the tree — banned-command rejection, plus a prefix-matched safe-read-only allowlist that returns
`false` (no approval) for `ls`, `git log`, `go test`, etc., and `true` (approval required) for
everything else.

**This is the single most consequential finding for a redesign.** The intended model is legible in
this function: an interactive approval gate where risky shell commands prompt a human. Nothing wires
it to the executor. Whether it was never connected or was disconnected is not recoverable from the
history I read, but the result is that the shell's own designed safety check never runs.

---

## Q6 — Sandboxing

**For agent traffic: none.** `cmd_exec.go:50` is a bare `exec.CommandContext(ctx, "bash", "-c", cmd)`
with the daemon's own uid, network, and filesystem. Process-group isolation (`setExecProcessGroup`)
and a timeout exist, but those bound *duration*, not *authority*.

A real confinement layer exists and is well designed — `internal/daemonpolicy` — but it is reachable
only through `internal/mcpserver` for third-party connector grants. It offers:

- `ExecMode`: `ExecDenied` (zero value — fails closed), `ExecAllowlist`, `ExecUnrestricted`
  (`policy.go:37-57`).
- Path confinement to a `PathRoot`, applied to **every** command, not just `fs.*` (`policy.go:150-153`),
  walking a known set of path/pattern fields (`paths.go`).
- Env scrubbing to an inheritance allowlist (`policy.go:44-76`).

The allowlist design contains the key insight a redesign should reuse verbatim
(`policy.go:185-195`): **under an allowlist the command MUST arrive as `argv`, never as a shell
string.** The comment records that the previous denylist approach lost to the interpreter —
`PATH=/planted git status` ran a planted binary while passing every check. `buildExecCommand`
(`cmd_exec.go:35-50`) already honors this: `Argv` execs directly with no interpreter; `Command` goes
through `bash -c`. The two-shape split is described in-source as "a security boundary rather than a
convenience."

So the primitive for a real sandbox already exists. The agent's shell simply does not use it.

---

## Q7 — History

Evidence is thinner than the code comments, which are the better record.

- `git log --diff-filter=D` on `internal/llm/tools/{grep,glob}.go` surfaces `398b30f6` /
  `0359a80a` ("workflow: record the run's verdict…", PR #135) and `2c6649c5`
  ("reliant: workflow verdicts, daemon wait UX, worktree async, tool safety").
- The authoritative rationale is the comment at `permissions.go:100-112`, which states the shell was
  moved to readonly tier **deliberately**, because gating it at `mutating` left readonly and
  plan-mode agents unable to search at all — described as "the exact regression the earlier removal
  produced." So this was a *second* correction: grep/glob were removed, readonly agents went blind,
  and the fix was to drop the shell to readonly rather than restore scoped search.
- `permissions.go:41-46` reinforces it: `tag:shell` is kept whole because the shell's own description
  references `shell_output`/`shell_kill`/`shell_list`.

**The prior model, reconstructed:** scoped `grep`/`glob` tools were readonly-tier and satisfied the
search need, while `shell` sat at `mutating`. That made `readonly` a genuine (if soft) boundary.
Deleting the scoped tools forced the choice between a blind readonly tier and a readonly tier with a
shell; the shell won, and the boundary was the cost.

---

## Redesign notes

1. The load-bearing question is not the ladder's shape but **whether shell execution is confined**.
   Everything else is downstream.
2. `daemonpolicy` is the right primitive and already exists, wire-serializable, fail-closed, and
   argv-based. Extending it from connector grants to first-party agent traffic is the highest-leverage
   change available.
3. `RequiresPermission` should be either wired to the executor or deleted. ~35 implementations of a
   never-called interface is a standing invitation to believe a gate exists that does not.
4. The `tools:` filter and the ladder overlap confusingly: the filter shapes the array, the ladder
   checks at execute time, and they disagree (`load_tool` bypasses the filter). One of them should own
   the question.
