# Product backlog: proposals moved out of the issue tracker

These were open issues on reliant-labs/reliant that recorded a proposal, a
deferred idea or a design to pick up later, not a defect to fix. The owner
had them moved here on 2026-11-04, after launch, and each issue was closed
with a link to this file. Each entry keeps the issue's text as it stood,
under its original number, so cross-references still resolve. Picking one up
starts with a design: none of them is decided beyond what its text says.

| Issue | Title | Labels |
|---|---|---|
| [#1829](#issue-1829) | Workflow source of truth: read automation definitions from git (stages 1–4) | enhancement |
| [#1470](#issue-1470) | Workflow admission: negotiate actual Temporal worker parser capabilities | enhancement |
| [#582](#issue-582) | Workflow builder: edit repo-defined workflows (.reliant/workflows) instead of read-only | enhancement |
| [#526](#issue-526) | Recommended automations: a curated, one-click set of "CTO" workflows (implement issues, triage/merge PRs, CI/deploy failures, periodic reviews, alerts) | enhancement |

<a id="issue-1829"></a>

## #1829: Workflow source of truth: read automation definitions from git (stages 1–4)

Opened 2026-10-29; labels: enhancement; https://github.com/reliant-labs/reliant/issues/1829

Follow-up to Stage 0 (in progress). Owner decision 2026-10-29: Stage 0 only for now; this ticket tracks the rest.

Design: `research/2026-10-29-workflow-source-of-truth-design.md` (project root research dir; copy into dev-docs/ when picked up).

Recommendation: hybrid (B + publish fallback). Each automation names its definition SOURCE separately from the machine it runs on:
- default: the repo's default branch via the GitHub App, at a commit pinned for the whole run; changes go live on merge;
- publish: for non-GitHub projects (`reliant workflow publish` / app), versioned server-side;
- machine: a machine's local files, only as an explicit, labelled choice.

Owner decisions:
- GitHub App `contents:read` (one re-approval per installation), not a personal token.
- Fail CLOSED: running an older version when main's newest definition doesn't parse is a bug. Instead, invest in validation so broken definitions can't reach main (merge-time `reliant workflow validate --base`, CI in every repo with .reliant/workflows, server-side validation on push with an inbox alert and a check run on the commit).

Stages:
1. Server-side catalog read from GitHub on push (webhook) + validation on arrival; daemon snapshot keeps serving interactive runs and local drafts.
2. A `source` column on activations; migrate existing automations dry-run first (switch to git only when main's file byte-matches what runs today; otherwise flag in the inbox). Emergency pin (to a commit or machine) to break self-gating deadlocks, built BEFORE stage 2 ships.
3. Publish for non-GitHub projects; skills/presets resolved from the pinned SHA.
4. Delete the merged cross-machine run source, the root-copy half of control-plane's sync-checkouts, and the duplicate root pin-bot.yaml.

Open: should `machine` sources expire (e.g. 7 days unless renewed) or only nag? Effort: stages 1–2 ~2.5 weeks.

<a id="issue-1470"></a>

## #1470: Workflow admission: negotiate actual Temporal worker parser capabilities

Opened 2026-10-24; labels: enhancement; https://github.com/reliant-labs/reliant/issues/1470

Follow-up to #1463 and task 43632743-3c4e-4ade-90c8-336e314dc07b. The bounded patch shares static validation but intentionally does not solve API/worker parser skew.

Required invariant: newer API/CLI accepting a field (for example hosted_use) must not imply old eligible Temporal workers can execute it. Desired deployment images, READY state and stable BuildID are not capability evidence.

Design before implementation: fresh per-process immutable release/parser contract/capability metadata; account for all workflow and activity pollers including unknown legacy pollers; either conservative complete eligible-pool admission or enforced compatible task routing. Registration without eligibility enforcement is insufficient. Recheck at dispatch and expose actionable incompatible/BROKEN versus transient-unavailable health. No broad immutable revision store needed for the bounded static fix.

Tests: new API + old fake runtime rejects; mixed actual pool remains blocked; crash-loop desired revision contributes nothing; unknown/stale worker evidence fails closed; compatible complete pool admits; pool transition between enable and fire is caught. Never manufacture compatibility from desired image. Architectural review required if new registry/table or Temporal queue/version routing is necessary.

See dev-docs/workflow-admission.md. Independent of runtime f90 recovery; do not block that rollout on this follow-up.

<a id="issue-582"></a>

## #582: Workflow builder: edit repo-defined workflows (.reliant/workflows) instead of read-only

Opened 2026-10-07; labels: enhancement; https://github.com/reliant-labs/reliant/issues/582

Workflows defined in the repo (`.reliant/workflows/*.yaml`) open read-only in the workflow builder ("Edit the YAML file there"), and the builder's agent chat is hidden for them. So the builder can't be used to improve a workflow that lives in git — the workflows most people will actually keep. Today that only happens through file edits in a normal chat.

**Proposal:** let the builder edit repo workflows by writing the YAML file in the chat's worktree and showing the change as a diff, with the builder's agent chat available.

Deferred by the owner (2026-10-06). Found during the v2 screenshot pass (gtm `media/v2/README.md`, R4).

<a id="issue-526"></a>

## #526: Recommended automations: a curated, one-click set of "CTO" workflows (implement issues, triage/merge PRs, CI/deploy failures, periodic reviews, alerts)

Opened 2026-10-06; labels: enhancement; https://github.com/reliant-labs/reliant/issues/526

#### Problem

Reliant already has the primitives to act as an always-on engineering team, but every user has to assemble them by hand: write a workflow, declare a `triggers:` block, pick an integration event, write a CEL filter, choose a daemon, decide on notifications. Most users won't, so on day one Reliant behaves like a chat app instead of what it is meant to be: **your CTO** — combined with forge, and the knowledge and standardization that applies to a given workload — proactively moving the repo forward.

What exists today (checked in the tree):

- **Triggers**: schedule / webhook / integration / workflow_event (`proto/reliant/v1/trigger.proto`). Workflows can declare `triggers:` that a user *activates* (`internal/triggers/declaration.go`, `web/src/components/Automations/ActivateTriggerDialog.tsx`).
- **GitHub events**: `issues.opened|labeled|assigned`, `issue_comment.created`, `pull_request.opened|synchronize|ready_for_review|closed|review_requested`, `pull_request_review.submitted`, `push`, `workflow_run.completed`.
- **GitHub actions**: `issue.create|get|comment|update` (incl. labels), `pr.get`, `pr.list_files`, `pr.review.create`, `workflow.dispatch`.
- **Inbox**: approval, question, waiting-for-machine, automation failing / launch failed, run finished (`proto/reliant/v1/inbox.proto`).
- **Integrations**: github, gmail, slack, twilio, http.

What is missing: no builtin workflow in `internal/workflow/builtin/` declares a `triggers:` block, and the Automations UI has no "recommended" or gallery surface. Every automation starts from a blank page.

#### Proposed solution

Ship a curated set of **recommended automations**: builtin workflows that each declare their own triggers, plus UI to browse and enable them per project in one click.

##### The set

| # | Automation | Fires on | What it does | Output |
|---|---|---|---|---|
| 1 | **Implement issue** | `issues.opened`, or `issues.labeled` with an opt-in label (e.g. `reliant:implement`) | Scopes the issue, implements it in a fresh worktree, opens a PR linked to the issue | PR; inbox question if the spec is ambiguous |
| 2 | **Review & categorize PR** | `pull_request.opened`, `synchronize`, `ready_for_review` | Reviews the PR and labels it by size, risk and area (e.g. `size:small`, `risk:low`, `needs-human`) | Review comments + labels |
| 3 | **Merge small approved PRs** | `pull_request_review.submitted` (approved), given #2's labels and green CI | Merges PRs categorized small/low-risk that are approved and passing | Merge; run-finished notification |
| 4 | **Escalate to inbox** | Same events as #3, when the PR is large or high-risk, or the review left open questions | Summarizes the PR, the decision needed and the open questions | Inbox item for the user |
| 5 | **Periodic reviews** | Schedule (weekly by default, configurable) | Architecture review, security review, proactive code review, feature ideation. Each one is a separate toggle | Issues filed and/or a digest in the inbox |
| 6 | **CI / deploy failure** | `workflow_run.completed` with a failed conclusion; a deploy-failure webhook | Pulls the logs, diagnoses, pushes a fix to the PR branch or opens a fix PR. For deploys, opens an incident issue | Fix PR, comment or inbox item |
| 7 | **Alert debugging & health check** | Webhook from the alert source (Alertmanager, Grafana, PagerDuty, Sentry, …) plus a schedule (daily overview) | Follows a debug methodology: reads logs and metrics, correlates with recent deploys. The scheduled run produces a system health overview | Incident summary / health digest in the inbox |

The **#2 → #3/#4 chain is the core loop**: every PR gets categorized, small safe ones merge on their own, and everything else reaches the inbox with a precise ask. That is the CTO behavior: the human only sees the decisions.

##### UI

- A **Recommended** section in Automations, also surfaced in onboarding and new-project setup. Each automation gets a card showing what it does, what triggers it, what it is allowed to write (comment / label / push / merge) and its expected frequency and cost.
- **One-click Enable** activates the declared trigger through the existing `workflow_trigger` path, with project, connection and daemon prefilled. Advanced options stay available: labels, size thresholds, schedule, filters, `no_machine` where the automation allows it.
- **Guardrails at enable time.** Auto-merge (#3) is off by default and needs explicit opt-in. Size threshold and protected paths (always escalate) are configurable. Branch protection and required checks are always respected.
- Each card shows status from the existing `TriggerHealth` and recent `TriggerEvent`s.
- **Bundle**: "Enable the CTO pack" turns on a sensible default subset (e.g. 1, 2, 4, 5 weekly, 6) and leaves auto-merge off.

##### Forge integration: workload-aware, not generic

In a forge project these workflows should use the project's own forge knowledge and gates instead of generic prompts. For example:

- forge skills `code-review`, `debug`, `ci-watch`, `observability`
- `forge project audit --json` as input to the architecture review
- `forge generate && forge lint && go build ./... && task test` as the definition of done for #1
- `forge env status` for the health check

Repos that don't use forge fall back to generic versions. This is the differentiator: standardized knowledge that applies to the workload, not a generic bot.

##### Gaps this surfaces (likely sub-issues)

- **No merge action**: the GitHub manifest has no `pr.merge`, which #3 needs. Also confirm that labelling a PR works through `issue.update`, or add a PR label action.
- **CI/deploy coverage**: `workflow_run.completed` only covers GitHub Actions. Non-Actions CI and deploys need `check_suite` / `check_run` / `deployment_status` events, or a documented webhook recipe (e.g. from `forge env deploy`).
- **Non-blocking decision items**: the inbox kinds that carry a user decision today (approval, question) block a live run. #4 and the #5 digests need a non-blocking, actionable item (open PR / merge / close) instead of keeping a run alive on `ask_user`.
- **Alert-source presets**: there are no first-class Alertmanager / Grafana / PagerDuty / Sentry integrations. A generic webhook with HMAC works, but per-source presets (payload mapping, filters) would make #7 one click.
- **Self-trigger loops**: an automation's own PRs and comments must not re-fire it, e.g. by filtering on the `sender` attribute against the bot identity. Confirm the existing fork-bomb guard covers integration-triggered chains.
- **Overlap / supersede**: one implement run per issue, and a new `synchronize` should supersede an in-flight review of the old head.

#### Alternatives considered

- **Examples only** (YAML in `examples/workflows/` for users to copy): cheap, but keeps the blank-page problem and does not give users the "enable your CTO" experience.
- **One monolithic scheduled "CTO agent"** that does everything: simpler to ship, but loses per-event latency, per-automation permissions and opt-in, and per-trigger health visibility. Separate declared triggers build on what already exists.
- **Leaning on GitHub Actions** for the event plumbing: duplicates the trigger and inbox system and gives up Reliant's daemon/worktree execution.

#### Acceptance criteria

- Builtin workflows for #1–#7 ship with `triggers:` declarations and scenario tests.
- Automations has a Recommended section; enabling an automation creates an activation with no YAML editing.
- Auto-merge is opt-in and respects branch protection, required checks and the #2 categorization.
- Escalations land in the inbox as non-blocking items with the decision summarized.
- Each automation runs end-to-end on a forge project (e.g. control-plane) using that project's forge skills.

#### Additional context

Positioning: Reliant is meant to be your CTO, combined with forge, and the knowledge and standardization that applies to a given workload. Today it is reactive: you open a chat. This makes it proactive by default.
