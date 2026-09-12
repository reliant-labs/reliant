# Tool-permission prior art in coding agents

Research date: 2026. Primary sources: docs.claude.com (`/docs/claude-code/*.md` raw
markdown), cursor.com/docs (`.md` raw), github.com/openai/codex `codex-rs/` source,
goose docs repo, OpenHands README. Where a claim could not be confirmed from a
primary source it is marked **(unconfirmed)**.

Our situation for context: a 3-level static ladder (readonly / mutating /
orchestrator) gating which tools an agent is *handed*. Since the scoped grep/glob
tools were deleted, bash is the only search path and is granted at every level,
including "readonly" — so the ladder no longer means anything.

---

## A. Is a capability ladder the wrong abstraction?

**Yes — but with an important nuance. Nobody has removed the mode enum; they have
demoted it.** Every product studied still ships a small ordered set of modes, and
every one of them treats the mode as a *default disposition* that is then overridden
by finer machinery. No product relies on the mode alone.

The modes, as they actually exist:

| Product | Modes | Source |
|---|---|---|
| Claude Code | `default`(Manual) / `acceptEdits` / `plan` / `auto` / `dontAsk` / `bypassPermissions` | permissions.md |
| Codex CLI | approval policy (`AskForApproval`) × sandbox policy (`SandboxMode`: `read-only` / `workspace-write` / `danger-full-access`) — **two orthogonal axes** | `codex-rs/protocol/src/config_types.rs`, `protocol.rs` |
| Cursor | Auto-review / Allowlist / Run Everything | cursor.com/docs/agent/security/run-modes |
| Goose | session mode (Autonomous / Manual / Smart) + per-tool override | goose tool-permissions.md |

What replaced the ladder as the load-bearing mechanism, in rough order of how much
weight it carries:

1. **An OS sandbox** (Claude Code, Codex, Cursor). This is the biggest shift and the
   one most relevant to us.
2. **Per-tool, per-pattern rules** with explicit precedence (Claude Code
   `deny → ask → allow`; Goose per-tool Always/Ask/Never).
3. **An LLM classifier** reviewing each call (Cursor Auto-review; Claude Code `auto`
   mode; Codex `approvals_reviewer = auto_review`, a "carefully prompted subagent
   [that applies] a risk-based decision framework"). All three ship this recently.
4. **Scoped, persisted approvals** (below, D).

The sharpest structural lesson is Codex's: **approval policy and sandbox policy are
separate fields**, not one ladder. "How much do I interrupt the human" and "what can
the process physically do" are different questions, and collapsing them is exactly
the mistake that makes a ladder degrade into meaninglessness.

The second sharpest is Cursor's changelog, which records a ladder rung being *deleted*
because a mechanism subsumed it: in Cursor 3.5, "**Ask Every Time** was deprecated…
Use **Allowlist** with an empty allowlist for the same behavior. **Run in Sandbox**
was folded into **Allowlist** with sandboxing enabled." Modes became derived states
of the rule system rather than a separate axis.

---

## B. How is BASH specifically gated?

This is the question we care about, and there are exactly three answers in use. Every
serious product uses at least two of them together.

### B1. Command-string pattern matching (Claude Code, most precise syntax)

Rules are `Tool` or `Tool(specifier)`, in `settings.json`:

```json
{
  "permissions": {
    "allow": ["Bash(npm run *)", "Bash(git commit *)"],
    "deny":  ["Bash(git push *)"],
    "ask":   ["Bash(dangerouslyDisableSandbox:true)"]
  }
}
```

Evaluation is **deny, then ask, then allow; first match wins; specificity does not
break the order.** A broad `Bash(aws *)` deny beats a narrow `Bash(aws s3 ls)` allow —
so "deny rules cannot carry allowlist exceptions." Bare `Bash` as a deny rule *removes
the tool from the model's context entirely*; a scoped `Bash(rm *)` leaves the tool
present and blocks matching calls.

Matching semantics, which are unusually well specified and worth copying:

- `Bash(npm run *)` matches `npm run build`, `npm run test --watch`, and bare `npm run`.
- `Bash(ls *)` matches `ls -la` and `ls`, but **not** `lsof` — the space before the
  trailing `*` is part of the rule. `Bash(ls*)` does match `lsof`.
- `Bash(ls:*)` is an equivalent spelling of `Bash(ls *)`; the `:*` form is recognized
  only at the end.
- Put the `*` **after the subcommand**. `Bash(git * main)` matches `git push origin
  main` *and* `git -c core.fsmonitor=<script> diff main` — i.e. a wildcard in the
  subcommand position hands over `-c`, which runs arbitrary programs. Claude Code emits
  a startup warning for an allow rule with a wildcard before the subcommand.

**Claude Code documents the limits of this approach rather than hiding them**, and this
is the single most useful finding for us:

- A `Bash(git push *)` deny does **not** match `git -C . push`. The docs say so
  explicitly, under a heading about what a Bash rule doesn't match.
- Matching a tool's *primary content field* by parameter is refused outright:
  "A rule like `Bash(command:rm *)` would be bypassable by a compound command, so
  Claude Code ignores it and emits a startup warning."
- And the governing note: "Permission rules are enforced by Claude Code, not by the
  model."

So the authors of the most developed pattern-matching system in the field state that
command-string matching is defeatable by shell composition. It is a usability layer
(fewer prompts for known-good commands), not a boundary.

### B2. An OS sandbox that makes the string irrelevant

Claude Code's sandboxed Bash tool: macOS Seatbelt (built in, no install); Linux/WSL2
`bubblewrap` for filesystem + `socat` for the network proxy, plus an optional seccomp
filter (`npm install -g @anthropic-ai/sandbox-runtime`) that blocks Unix domain
sockets. Native Windows unsupported. Config:

```json
{
  "sandbox": {
    "enabled": true,
    "filesystem": { "denyRead": ["~/"], "allowRead": ["."],
                    "allowWrite": ["~/.kube", "/tmp/build"] },
    "network": { "allowedDomains": ["github.com", "*.npmjs.org"] },
    "allowUnsandboxedCommands": false
  }
}
```

Defaults: writable = cwd + session tempdir + `--add-dir` paths. Overlap rules are
specified: a narrower `allowRead` re-opens a denied region, but a `denyRead` holds
*inside* a wider allow, "so a broad allow can't silently re-expose a secret."
`sandbox.failIfUnavailable: true` turns a missing sandbox into a hard failure instead
of a silent unsandboxed fallback.

Codex expresses the same idea as typed policy in `codex-rs/protocol/src/permissions.rs`:
`FileSystemSandboxKind::{Restricted, Unrestricted, ExternalSandbox}`,
`FileSystemAccessMode::{Read, Write, Deny}` with documented conflict precedence
(*"deny beats write, and write beats read"*), and `NetworkSandboxPolicy::{Restricted,
Enabled}` defaulting to Restricted. `FileSystemSandboxPolicy::default()` is
`read_only()`. Codex also hard-protects workspace metadata even inside writable roots:
`PROTECTED_METADATA_PATH_NAMES = [".git", ".agents", ".codex"]`.

Cursor: macOS Seatbelt via `sandbox-exec`; Linux Landlock (kernel ≥6.2,
`CONFIG_SECURITY_LANDLOCK=y`) + seccomp, with a bubblewrap fallback reported via
`CURSOR_SANDBOX_LANDLOCK_STATUS`. Network blocked by default, opened by a
`sandbox.json` domain allowlist plus a ~100-entry built-in package-manager default
list. **"If your kernel does not meet these requirements, Cursor falls back to asking
for approval before running commands"** — degrade to prompting, never to silence.

### B3. A classifier reading the command

Cursor Auto-review checks each shell/MCP/Fetch call in order: allowlist → sandbox if
the command fits the sandbox's limits → otherwise a classifier (a Cursor-managed small
model, currently Claude 4.5 Haiku or GPT-5.4 Mini). Configured in
`~/.cursor/permissions.json` or `<project>/.cursor/permissions.json` **in plain
English**:

```json
{ "autoRun": {
    "allow_instructions": [],
    "block_instructions": [
      "Every AWS CLI command should go through approval first.",
      "Every command that modifies Kubernetes resources should go through approval first."
] } }
```

Cursor states plainly: **"Auto-review is not a security boundary. The classifier can
make mistakes."**

### B4. The escape hatch, which every sandboxed product needs

Claude Code: a blocked command returns the sandbox violation (naming the denied path
or host) *to the model*, which may retry with `dangerouslyDisableSandbox: true`; the
retry then goes through the normal permission flow. Disable with
`allowUnsandboxedCommands: false` ("Strict sandbox mode"), or force a prompt with an
ask rule on `Bash(dangerouslyDisableSandbox:true)`. Cursor: "Some commands need full
system access and bypass the sandbox. Cursor will indicate when a command runs outside
the sandbox and ask for your approval." Claude Code titles those prompts "Bash command
(unsandboxed)" rather than "Bash command".

---

## C. Where does enforcement live?

| Product | Real (OS/container) boundary | Advisory (harness) layer |
|---|---|---|
| Claude Code | Yes — Seatbelt / bubblewrap+seccomp, "enforced at the OS level, so all commands running inside the sandbox, including their child processes, respect them" | allow/ask/deny rules, PreToolUse hooks, `auto`-mode classifier |
| Codex CLI | Yes — sandbox policy applied per exec; typed FS/network policy | `AskForApproval` policy, `auto_review` subagent |
| Cursor | Yes — Seatbelt / Landlock+seccomp, with documented fallback to prompting | allowlist, Auto-review classifier (explicitly not a boundary) |
| OpenHands | Yes, **only** in the Docker-sandbox deployment | — |
| Goose | **No sandbox found.** Per-tool Always/Ask/Never only — harness-level | all of it |
| Devin | Cloud VM per session; mechanism not documented publicly **(unconfirmed)** | — |

Nothing anywhere is prompt-level. Claude Code's note — *"Instructions in your prompt or
`CLAUDE.md` shape what Claude tries to do, but they don't change what Claude Code
allows"* — is the universal position: the harness decides, never the system prompt.

OpenHands is the clearest statement of the tradeoff because it ships both and labels
them. Running the agent-server directly on the host carries, in the README:
"⚠️ **WARNING** This runs the agent-server directly on the machine you're installing on
— the agent will have full access to your filesystem!" The Docker option mounts only
`PROJECTS_PATH`: `-v "${PROJECTS_PATH}:/projects"`, and "the agent will be able to
access any project under `PROJECTS_PATH`." Container isolation replaces per-command
reasoning entirely. Cursor's Cloud Agents take the same line: "Run Modes apply to local
agents. Cloud Agents run inside their own dedicated machine, so the agent never asks
you to approve an action."

**Takeaway for us: with no sandbox, we are in Goose's position, and Goose does not
claim a read-only guarantee.** Any enforcement we build in the harness is advisory
against a shell, and we should either say so or build a boundary.

---

## D. Approval with memory — scoping so "always allow" ≠ "allow everything"

Claude Code is the most concrete. Its scoping table:

| Tool type | "Yes, and don't ask again" persists as |
|---|---|
| Bash | **Permanently, per repository and per command pattern** |
| File modification | **Until session end only** — never written to a file |
| WebFetch | Permanently, per repository and per domain |
| Read-only (reads, Grep) | N/A — no approval inside working directories |

Persisted rules are written to `.claude/settings.local.json` **at the git repository
root, resolved through worktrees to the main checkout**, so an approval in a worktree
applies repo-wide (changed in v2.1.211; before that it saved in the starting
directory). Settings precedence, highest first: managed → `--settings` CLI →
`.claude/settings.local.json` → `.claude/settings.json` → `~/.claude/settings.json`.

Two design details worth stealing:

1. **The prompt refuses to offer "don't ask again" when it cannot show you everything
   the rule would cover.** Named cases: the command or edit is too large to display in
   full; the label can't fit all the commands or paths the rule would cover; the
   starting directory can't be displayed safely. You then get one-time approval only.
   This prevents a broad rule being granted from a truncated prompt.
2. **The saved rule covers only what the option's label named** — approval is scoped to
   the displayed command prefix, not to the tool.

Codex persists amendments as typed objects (`ExecPolicyAmendment`,
`NetworkPolicyAmendment`, `NetworkPolicyRuleAction` in `protocol.rs`) rather than free
text, and supports named permission profiles (`PermissionProfile`,
`ActivePermissionProfile`). Cursor scopes by file location: `~/.cursor/` (all
projects) merged with `<project>/.cursor/` (one project), with team dashboard config
overriding both — for `permissions.json` a team config makes Cursor **ignore** the user
and project files entirely, while `sandbox.json` merges with project taking priority
and "local files cannot weaken" admin policy or Cursor's hardcoded rules.

---

## E. Read-only enforcement, given a shell

**Direct answer: nobody achieves read-only through tool selection. It is achieved by a
read-only filesystem policy in a sandbox, or it is not achieved.**

- **Codex** is the cleanest: `SandboxMode::ReadOnly` is the `#[default]`, and
  `FileSystemSandboxPolicy::read_only()` is a single entry granting
  `FileSystemSpecialPath::Root` with `FileSystemAccessMode::Read`. The shell is still
  present and fully usable; writes fail at the syscall. This is the design our
  "readonly" level was reaching for and cannot reach without a sandbox.
- **Claude Code plan mode** is *not* an enforcement boundary in the OS sense. The docs
  describe it behaviorally: "Claude reads files and runs read-only shell commands to
  explore but doesn't edit your source files; with auto mode available,
  classifier-approved commands also run." There is a built-in set of read-only Bash
  commands that skip approval; everything else prompts. Plan mode also *tightens*
  sandbox behavior: a bare `Bash` ask rule, normally skipped for sandboxed commands, is
  **not** skipped in plan mode — it prompts even for read-only sandboxed commands
  (v2.1.212+). I could not find a primary source enumerating the read-only command set
  or claiming plan mode is unbypassable. **Treat plan mode as a strong default plus a
  classifier, not a guarantee (partially unconfirmed).**
- Claude Code's nearest thing to a real read-only posture is sandbox config, not a
  mode: `"denyRead": ["~/"], "allowRead": ["."]`, or the
  `permissions.blockReadsOutsideWorkingDirectories` setting. Note this restricts
  *reads*; writes are constrained by the writable-roots default (cwd + tempdir).
- **Cursor** offers no read-only mode. It offers sandboxing plus File-Deletion
  Protection and External-File Protection, which "prevent the agent from automatically"
  deleting files or touching files outside the workspace — i.e. force approval, not
  denial.
- **Goose** offers "Never Allow" per tool, which is real removal but tool-granular: set
  the Developer extension's shell tool to Never Allow and you have no shell at all.
  That is the honest version of our ladder — you don't get "readonly with a shell," you
  get "no shell."

A corollary Claude Code documents that bears directly on us: with filesystem isolation
off and commands auto-allowed, "a sandboxed command can write files that later commands
run or read, such as shell startup files, executables on `$PATH`, or
`~/.claude/settings.json`, and use them to widen its own access on the next run." **A
write-capable agent can rewrite its own permission config.** Hence Claude Code refuses
`sandbox.filesystem.disabled` from project-level settings — only user, managed, or
`--settings` may set it, "so a checked-out project can't switch filesystem isolation
off." Our permission config needs the same protection regardless of which model we pick.

---

## Mechanisms worth lifting, ranked

1. **Split the single ladder into two orthogonal axes** (Codex): approval policy ×
   execution policy. This alone fixes the "readonly agent holding a shell" incoherence —
   readonly becomes a filesystem policy, not a tool list.
2. **Deny > ask > allow, first match wins, specificity irrelevant** (Claude Code). Easy
   to implement, easy to reason about, and it makes deny rules trustworthy.
3. **Deny-by-bare-name removes the tool from context; deny-by-pattern leaves it and
   blocks calls.** Two useful behaviors from one syntax.
4. **Refuse rules you cannot enforce, loudly.** Claude Code ignores
   `Bash(command:rm *)` and warns at startup, rather than shipping a rule that a
   compound command defeats. We should apply this to any bash rule we cannot actually
   enforce.
5. **Scope persisted approvals to (repo, command-prefix)** and suppress the "don't ask
   again" option whenever the prompt cannot display everything the rule would cover.
6. **Degrade to prompting, never to silence** when the enforcement layer is
   unavailable (Cursor's kernel fallback; Claude Code's `failIfUnavailable`).
7. **Protect the permission config and VCS metadata from the agent** even inside
   writable roots (Codex's `.git` / `.agents` / `.codex`).

## Gaps / not confirmed

- **Devin**: no primary technical documentation on its isolation mechanism was
  reachable. Only that it runs in a cloud VM/workspace. Not confirmed.
- **Aider**: not investigated in this pass; no approval/sandbox source reviewed.
- **Zed agent, Cline**: not reached before the search budget ran out.
- **Claude Code's built-in read-only Bash command set**: referenced by
  `permissions.md` as "a built-in set of read-only commands" but the list itself was
  not located in the docs.
- **Codex `AskForApproval` variant names**: the type is referenced throughout
  `protocol.rs` (`approval_policy: Option<AskForApproval>`) but its enum definition is
  in a file not fetched, so the exact variant spellings (commonly cited as
  untrusted / on-failure / on-request / never) are **unconfirmed here**.
- Both `docs.claude.com` and `cursor.com/docs` render as SPAs; append `.md` to the
  path to get raw markdown. `raw.githubusercontent.com/openai/codex/main/docs/*.md`
  are stubs that redirect to developers.openai.com — read `codex-rs/` source instead.
