---
name: design-patterns
description: Workflow design patterns — do's and don'ts for threading, context, loops, parallelization, inputs, prompts, error handling, and composition. Load when building or reviewing workflows.
---

# Workflow Design Patterns

Do's and don'ts distilled from every builtin workflow.

## Threading and Context

**Do** use `fork` + `memo: false` for disposable work — research, planning, implementation. The fork sees the parent thread but its conversation is discarded. Only the `save_message` summary propagates back.

**Do** use `fork` + `memo: true` only for agents that build on their own prior analysis across iterations — reviewers tracking recurring issues, editors refining across passes.

**Do** use `mode: new` for fully isolated agents — separate worktrees, independent tasks with no shared context.

**Do** use `mode: inherit` sparingly — only for interactive conversations or phases where the user needs the full back-and-forth on the main thread.

**Do** keep the main thread curated: user message, phase summaries, final results. Use `save_message` with `condition` to avoid saving empty or irrelevant messages. Use `display_style: warning` for failures.

**Do** use `inject` for per-iteration context (attempt number, prior feedback, check results). System prompts define roles; inject provides dynamic state.

**Don't** use `memo: true` on implementation agents — accumulated failed approaches degrade quality. Only reviewers benefit from memo.

**Don't** save raw transcripts to the main thread. Save `response_text` summaries from forks.

**Don't** make every agent rediscover the same context. Route durable summaries through save_message so downstream forks start informed.

## Phase Design

**Do** keep phases single-purpose. Each phase has one job: generate, review, transform, or assemble.

**Do** give each phase a crisp success contract: "done when X is true" beats "improve" or "finish."

**Do** front-load quality expectations in generation prompts. Cheaper to produce good output once than to fix it across downstream passes.

**Do** make quality gates enforce their findings. A review that identifies problems but passes anyway is wasted work.

**Don't** build band-aid phases. If you need a cleanup phase to undo systematic problems, fix the upstream prompt instead.

**Don't** add self-review loops inside generation phases when downstream phases already review the same output.

## Loop Design

**Do** always pair `while` with a max iteration bound. Both a semantic exit (`strategy == "pass"`) AND a hard cap (`iter.iteration < max`).

**Do** default loop outputs to "continue" for the first iteration — outputs don't exist yet on the first `while` check.

**Do** use `ask: "inputs.ask"` on loops for user checkpoints between iterations.

**Do** provide an escalation path for stuck states via `ask_question` rather than silently exhausting retries.

**Don't** build item-by-item loops when a single-pass approach works. One agent processing N items is far cheaper than N loop iterations with 3 sub-nodes each.

**Don't** compound autonomy accidentally: retry loop x unbounded agent loop x spawned agents x compaction can turn one bad assumption into hours of churn. Estimate your turn budget before building.

## Parallelization

**Do** use multiple `default` edges from the same node for parallel fan-out. This is the primary parallelization mechanism — each edge target runs concurrently.

**Do** use `type: join` (with `condition: "all"`) to gate on parallel branches completing before the next phase.

**Do** use `parallel: true` on loop nodes with `items` and `key` when running the same logic over multiple items concurrently.

**Do** use `spawn` within agent phases for sub-task parallelization. The agent decides the strategy — spawn is for parallelism within a single phase, parallel edges are for parallelism between phases.

**Do** use `project: path` to redirect sub-workflows to different worktrees when running parallel implementations. Combine with `create_worktree` nodes for full isolation.

**Do** run independent reviews in parallel. If multiple reviewers read the same input and produce separate outputs, fan them out and join.

**Don't** chain reviews sequentially when they don't depend on each other's output. Sequential chains add wall-clock time for no quality benefit.

**Don't** run parallel agents on the same thread. Each parallel branch needs its own thread (`fork` or `new`).

## Run Nodes

**Do** use `type: run` for mechanical checks (lint, test, build) rather than having agents call bash. Run nodes are deterministic, cheaper, and their exit codes are directly usable in edge conditions.

**Do** use `condition` on run nodes to skip unconfigured checks: `condition: "inputs.lint_command != ''"`.

**Do** use `log_file` to capture run output for debugging. Instruct downstream agents to read the log file for error details.

**Do** fan out multiple run nodes in parallel for independent checks (lint + test + build), then join before evaluating results.

**Do** use `save_message` with `condition: "output.exit_code != 0"` on run nodes to only surface failures.

**Don't** send mechanical check failures to a reviewer. If lint/test/build fail, short-circuit past review and loop back to implement — reviewing broken builds wastes context.

## Input Design

**Do** use `ui: toolbar` for frequently adjusted inputs (mode, ask, model). Use `ui: hidden` for plumbing (system_prompt, passthrough inputs).

**Do** use `type: group` for related settings, `type: model` with `tags` for model selection, `type: enum` with `multi: true` for feature toggles (instead of separate booleans).

**Do** use hidden string inputs with defaults for large shared context (style guides, schemas). Avoids file-read failures and keeps content in one place.

**Do** provide sensible defaults for everything. Users should run workflows without configuring anything.

**Don't** hardcode model names — use tags. Don't use `type: string` where typed inputs exist (model, tools, preset).

## System Prompts

**Do** make prompts role-specific and actionable: what the agent IS, what it should DO, what it should NOT do. Include concrete anti-pattern examples.

**Do** scope tools to the phase contract. Tool availability is policy; prose guidance is advisory.

**Do** keep prompts focused on what to produce. Agents don't need to know they're "Phase 3" or understand the full pipeline.

**Don't** make prompts encyclopedic when the phase is narrow — long prompts invite exploration instead of completion.

**Don't** duplicate instructions that belong in a preset. Shared boilerplate goes in one place.

**Don't** rely on "be careful" prompts to constrain behavior. Enforce boundaries with tool filters and routing.

## Structured Output

**Do** use `builtin://structured-agent` when you need typed output for downstream routing. Design response schemas with clear enum values and descriptive `description` fields.

**Do** guard all response access: `has(nodes.X) && has(nodes.X.response) && nodes.X.response != null`. Use `expected_response_tools` to pre-create keys for safe CEL access.

**Don't** use unstructured text for decisions that control routing. If an edge depends on the output, it must be structured.

## Error Handling

**Do** separate mechanical gates from qualitative review. Build failures loop back to implement; reviewers judge design and completeness.

**Do** distinguish strategies: `continue` (incomplete but on track), `refactor` (fundamentally wrong, undo first), `stuck` (external blocker, escalate to user).

**Do** provide log file paths in failure messages. Tell the agent the log format and what to look for.

**Don't** retry blindly without new information. Each retry needs review feedback or error output — unless you're using brute-force with sampling randomness.

**Don't** let implementation agents debug the platform unless that's the phase's job. External failures should escalate.

## Composition

**Do** delegate to `ref: builtin://agent`, `builtin://structured-agent`, `builtin://get-it-right`. Don't reimplement their loops.

**Do** use `inline` for single-use logic, separate files for reusable components.

**Do** use `presets` on sub-workflow nodes for role-specific configuration. Pass through `model`, `mode`, `temperature` so top-level settings are respected everywhere.

**Do** persist critical state via `save_message` so compaction doesn't erase the workflow's memory.

**Don't** rely on external file reads for critical shared context. Files may not exist across machines. Inline via inputs and inject.

## Edge and Routing

**Do** use `type: router` for LLM-based classification at pipeline entry points. Use fast models with clear workflow descriptions.

**Do** use `condition` on nodes for skippable phases — cleaner than complex edge conditions.

**Do** make checkpoints workflow-driven. Route deterministically to `ask_question` at natural boundaries instead of relying on the LLM to pause.

## Testing

**Do** run end-to-end at least once before shipping. Write 3+ scenarios covering happy path, errors, and edge cases.

**Do** check tool error rates after the first run. Systematic errors point to workflow design problems.

**Do** check where turns are going. If one phase consumes 30%+ of total turns, optimize there first.
