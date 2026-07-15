# A team of agents, not one chatbot in a loop

**14 specialized agents**
Planner, researcher, implementer, reviewer, tester, debug, git, and 7 more.

**CEL decides what runs next**
Node conditions are code (`exit_code == 0`), not the model's guess.

**Git worktrees run in parallel**
Separate branches, separate agents, at once. This repo ships that way.

**Runs headless and remote**
The daemon does the file work; it need not sit on your laptop.

---
**Speaker Notes:**
So this is the part that's hard to copy. Most tools give you one model chomping through a loop. We ship 14 distinct agent presets — a planner that only plans, a reviewer that only reviews, a git specialist for merges and rebases. They spawn each other. The second thing, and this is the one engineers care about: control flow is deterministic. When a workflow says "run tests, then deploy if they pass," that "if they pass" is a CEL expression evaluating an actual exit code — not the model deciding whether it feels like tests passed. And the parallelism is real, not simulated. We use git worktrees so three agents work three branches at the same time without stepping on each other. We know it works because we build Reliant with Reliant this way, right now.

---
**Layout Hint:**
card-grid

---
**Sources:**
- 14 agent presets: this repo, `internal/workflow/builtin/presets/*.yaml` (documentation, implementer, researcher, git, code_reviewer, forge, planner, workflow_builder, refactor, tester, debug, migrate, general, ux). NOTE: brief/website say "20+ agents" — repo shows 14 built-in presets. Used the verifiable count. Gap to 20+ [needs verification] — may include sub-agent variants or unshipped presets.
- CEL control flow: this repo, `internal/cel/engine.go`, `internal/workflow/cel/evaluate.go`; example `condition: nodes.run_tests.exit_code == 0` from `docs/reference/cel-expressions.mdx:145`
- Git worktree parallelism: this repo, `worktree` Connect RPC service (`gen/reliant/v1/reliantv1connect/worktree.connect.go`); verified in-use — this working tree is a git worktree (system context)
- Remote/headless daemon execution: project architecture context — daemon holds filesystem access, "may not run on the same device as the user" (in-conversation project brief)
