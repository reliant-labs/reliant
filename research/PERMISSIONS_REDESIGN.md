# Rethinking the permission model

**Status:** proposal, for discussion. Nothing here is implemented.

Grounded in three research docs in this directory — `PERMISSIONS_ASBUILT.md`,
`PERMISSIONS_INTERACTIVE.md`, `PERMISSIONS_PRIOR_ART.md` — plus direct
verification of the load-bearing claims (noted inline).

---

## The finding that reframes everything

**The interactive approval gate does not run.** `Tool.RequiresPermission` is
implemented by ~35 tools. Its only production caller is the wrapper delegating
to itself (`tool_wrapper.go:290`); `code_context` reports the sole inbound
callers are two tests. I verified this directly:

```
$ rg -n '\.RequiresPermission\(' --glob '*.go' | grep -v '_test.go'
internal/llm/tools/tool_wrapper.go:290:  return t.tool.RequiresPermission(typedParams)
```

`APPROVAL_TYPE_TOOL` exists in `approval.proto:11` and in generated code — and
in no hand-written code anywhere. Nothing ever creates a tool approval.

So the intended design is legible in the source and has never been wired up. The
codebase even says so, at `file_concurrency_test.go:46-50`: "not an
execution-time gate at all… nothing calls that during execution."

This changes the question. It is not "our ladder degraded" — it is **"the
ladder is the only thing running, and it was never meant to carry this alone."**

### What the ladder actually buys

One runtime check, at `execute_tools.go:310-325`: compare the tool's minimum
level against the granted level, return an error string to the model.

- `mutating` over `readonly` grants exactly four tools: `write`, `edit`,
  `find_replace`, `move_code` — each with a one-line shell equivalent
  (`cat > f`, `sed -i`, `mv`).
- `orchestrator` over `mutating` grants exactly `spawn` and `agent`.
- Both tiers get the full shell family, `fetch`, and `websearch`.

So the ladder distinguishes tiers by which *convenience wrappers* an agent is
handed, while every tier holds a general-purpose interpreter. Your read is
right, and the source agrees: `permissions.go:101-112` states that readonly "no
longer means the agent cannot write; it means the agent is not HANDED write
tools, while retaining a shell that can still `>` a file."

### How it got here

Worth stating plainly because it shapes the fix. The comments are a better
record than the log: scoped `grep`/`glob` tools were deleted, which left
readonly agents unable to search at all; the correction dropped the **shell** to
readonly tier rather than restoring scoped search. That was a deliberate trade
of the boundary for capability — the second correction in a sequence, not an
oversight.

### The bash hole, measured

`shellTool.Execute` applies two refusals (`unscopedSearchRefusal`,
`ripgrepReplaceRefusal`) — both ergonomic, neither security. Dispatch reaches
`exec.CommandContext(ctx, "bash", "-c", req.Command)` (`cmd_exec.go:50`). The
daemon's policy gate receives `nil` for first-party traffic and is explicitly
documented as leaving it unaffected (`runtime.go:955-968`), and `ChildEnv`
returns the daemon's full `os.Environ()` when policy is nil.

**A readonly agent has arbitrary code execution as the daemon user, with the
daemon's full environment.**

### The dead gate would not have held either

This matters for the redesign, because "just wire up `RequiresPermission`" is
the tempting one-line fix and it is wrong. Its safe-command check is a **raw
prefix match on the whole command string**, with no segment splitting. I ran its
exact logic against real inputs:

```
ls -la; rm -rf /tmp/x                    -> AUTO-APPROVED
ls -la && rm -rf /tmp/x                  -> AUTO-APPROVED
echo x; rm -rf /tmp/x                    -> AUTO-APPROVED
git status && git push --force origin main -> AUTO-APPROVED
whoami && echo pwned > /etc/cron.d/x     -> AUTO-APPROVED
ls; rm -rf /tmp/x                        -> prompts   (';' is not ' ' or '-')
```

Any safe prefix followed by a space auto-approves everything chained after it.
Reviving this as-is would ship a gate that reads as protection and is not.

---

## What the industry actually does

The one structural lesson, from Codex: **approval policy and sandbox policy are
separate, orthogonal fields** — `AskForApproval` (how much do I interrupt the
human) and `SandboxMode` (what can this process physically do). Collapsing them
into one ladder is precisely the mistake that makes a ladder meaningless. Cursor
independently converged here: its changelog shows rungs deleted as finer
mechanisms subsumed them.

**Nobody gates bash by tool selection.** Three mechanisms, and serious products
use at least two:

1. **Pattern matching** — Claude Code's `Bash(npm run *)` rules, evaluated
   deny → ask → allow, first match wins.
2. **OS sandbox** — Seatbelt on macOS, Landlock/seccomp or bubblewrap on Linux.
   Claude Code, Cursor, and Codex all have one.
3. **Classifier** — Cursor's Auto-review, which Cursor states flatly "is not a
   security boundary."

The most important data point: **Anthropic documents its own pattern matching as
defeatable.** A `Bash(git push *)` deny does not match `git -C . push`, and a
`Bash(command:rm *)` rule is *refused at startup* with a warning because it
"would be bypassable by a compound command." The team with the most developed
matcher in the field treats string matching as a usability layer, not a
boundary.

And on read-only specifically: **nobody achieves it through tool selection.**
Codex's `ReadOnly` sandbox still hands over a full shell — writes fail at the
syscall. Goose, which has no sandbox, correspondingly does not claim a read-only
guarantee. That is our current position, minus the honesty.

---

## Proposal: two orthogonal axes, and stop pretending

Replace the single ladder with two independent fields, because they answer
different questions and one of them we cannot currently answer at all.

### Axis 1 — Capability (what the agent is offered)

What the ladder does today, kept and made honest. It decides which tools go in
the LLM's tool array, and it is genuinely useful for **steering** — a planning
agent shouldn't be handed `write`, because handing it one invites use. What it
is not is a security boundary, and the docs should say so in those words.

This is where the `tools:` filter belongs too (PR 4 in the engine plan): one
declared allow-set, enforced at both request-build and execute, covering
`load_tool` and MCP.

### Axis 2 — Containment (what the process can physically do)

New, and the only thing here that is actually enforceable. Three values:

- **`none`** — today's behavior. Full daemon privileges. Honest default for
  local dev on your own machine.
- **`workspace`** — filesystem writes confined to the worktree; reads broader;
  network allowed. The common case.
- **`readonly`** — no filesystem writes at all, enforced below the tool layer.

The critical property: **`containment: readonly` must be enforced by the OS, not
by tool selection.** If we cannot enforce it, we must not offer it — Goose's
honesty is the right model. Offering a `readonly` that a shell walks through is
worse than not offering one.

**We already have most of the machinery.** `internal/daemonpolicy` is a
fail-closed, path-confined, env-scrubbing argv allowlist with a genuinely good
design — and it is wired only for third-party connector grants. Its central
insight is exactly the one the shell's dead gate got wrong
(`policy.go:185-195`): commands must arrive as **argv, never a shell string**,
because a denylist loses to the interpreter (`PATH=/planted git status` passed
every check). `buildExecCommand` already treats the argv/shell split as "a
security boundary rather than a convenience."

So the work is largely *connecting an existing primitive to first-party traffic*
rather than building one. What is genuinely missing is OS-level confinement
(Seatbelt/Landlock) for the case where the agent legitimately needs a real shell.

### Axis 3 — Interaction (when to ask a human)

Currently absent for tools; it must be built, not repaired. Reusable pieces
exist and are good: the approval-row + Temporal-signal + timeout flow
(`approval_flow.go:150-196`, replay-safe and loop-addressable) and the daemon
cancel path (`interrupt.go:134-140`, which genuinely kills process groups).

What has to be new:

- A call site in `execute_tools.go` that inspects tool **arguments**, not just
  the tool name.
- A decision store. **There is none today** — `auto_approve` was a real chat
  field that was deliberately removed (`chat.proto:258`,
  `reserved 11; // was: auto_approve`).
- A per-approval UI. Today it is two buttons, "Approve All" / "Deny All", over
  every pending approval, showing no command string, no diff, and no tool name
  (`ApprovalActions.tsx:32,50`) — the proto's `tool_name` / `tool_call_id`
  fields are never populated.

Two rules worth adopting wholesale from Claude Code:

- **Scope remembered approvals by kind.** Bash approvals persist per repo and
  per command pattern; file-modification approvals last only the session and are
  never written to disk.
- **Withhold "don't ask again" whenever the prompt cannot display everything the
  rule would cover.** A broad rule must never be grantable from a truncated
  prompt.

### One more, regardless of design

A write-capable agent can rewrite its own permission config to widen its access
on the next run. Claude Code refuses `sandbox.filesystem.disabled` from
project-level settings so a checked-out repo cannot disable its own isolation.
Whatever we build needs the equivalent: **agent-writable files must not be able
to grant the agent more.**

---

## Sequencing

1. **Tell the truth first.** Rename or document the ladder as *capability
   selection*, not a security boundary, and stop `plan` mode implying
   containment it does not provide. Cheap, immediate, and it stops anyone
   building on a guarantee that does not exist.
2. **Finish the `tools:` filter** (PR 4). Makes axis 1 coherent on its own terms.
3. **Wire `daemonpolicy` to first-party exec**, starting with `containment:
   workspace`. The primitive exists; this is connection work.
4. **Build the interaction axis**: argument-aware call site, decision store,
   real per-approval UI. Ship *without* remembered approvals first, then add
   memory with the two scoping rules above.
5. **OS sandbox** (Seatbelt/Landlock) for `containment: readonly`. Largest
   piece; only after the above, and it is what makes `readonly` truthful.

Not proposed: reviving `RequiresPermission` as-is, or any design where a string
matcher is the boundary.

---

## Open questions for the team

1. **Is `readonly` a product requirement?** If yes, it implies an OS sandbox and
   that is a real project. If no, we should delete the tier rather than ship a
   name that overpromises.
2. **Do we restore scoped search tools?** Cheaper containment for the common
   case: an agent that can search without an interpreter needs far less
   confinement. The original deletion is what forced the shell down to readonly.
3. **Where does containment default live** — workflow YAML, preset, project
   config, or a user-level setting the agent cannot write?
4. **Local vs. cloud daemon.** `containment: none` may be right on your own
   machine and clearly wrong for a hosted fleet running untrusted workflows. The
   split is likely per-runtime, not per-workflow.
5. **What breaks?** Every builtin workflow assumes an unrestricted shell. A
   containment default other than `none` is a breaking change for them.
