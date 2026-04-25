# Forge One-Shot v3

`forge-one-shot v3` is the next iteration of our one-shot app-generation workflow. The goal is not to produce flashy demos. The goal is to produce a **validated first production slice** through a workflow that behaves more like a compiler plus CI pipeline than a prompt runner.

## Core Thesis

We do not beat `loveable` or `base44` by making the model “sound smarter.” We win by making the system more reliable:

- tighter scope
- stronger scaffolding
- harder gates
- richer repair loops
- clearer separation between generated structure and agent-written code

The workflow should only claim success when the app is generated, builds, passes the required checks, boots enough for smoke validation, and survives specialist review.

## Non-Goals

These are intentionally out of scope for v3:

- scorecards
- domain packs
- broad multi-profile benchmark suites
- breadth-first “build the whole company app in one pass” behavior

v3 optimizes for a **bounded, production-worthy first slice**.

## Success Definition

A successful `forge-one-shot v3` run means:

- the project exists at the requested path
- the approved first slice is explicit and bounded
- services, packages, frontend, migrations, env contract, and optional webhook/job scaffolds exist where needed
- `forge generate` succeeds
- contract lint succeeds
- build succeeds
- smoke checks succeed for the first slice
- convergence succeeds after parallel work
- reviewer swarm leaves no unresolved critical issues for the first slice
- the final output is honest about deferred work and stubbed integrations

## Workflow Shape

The workflow has six phases.

### Phase 1 — Scope First Slice

The planner must produce a bounded first slice before any scaffolding happens.

Required outputs:

- `project_name`
- `go_module`
- `app_summary`
- `first_slice` with one explicit golden journey
- `included_components`
  - services
  - packages
  - frontends
  - db/migrations needed now
  - jobs/webhooks needed now
  - infra/deploy pieces needed now
- `external_integrations_now`
- `external_integrations_stubbed`
- `env_contract`
- `routes_pages_now`
- `schema_now`
- `auth_mode_now`
- `defer_list`
- `acceptance_checks`

Rules:

- prefer `<= 2` services
- prefer `<= 1` frontend
- only include jobs/webhooks required for the first slice
- force explicit deferred work
- force explicit env/integration inventory

Escape hatches:

- `needs_user_scope_decision`
- `rescope_required`
- `stubbed_integration_mode`

### Phase 2 — Scaffold Everything

The workflow should scaffold the full repo shape required for the first slice, not just the obvious app files.

Scaffold branches:

- project
- services
- packages
- frontend
- infra/dev runtime
- db/migrations
- auth scaffolding
- webhook scaffolding
- job scaffolding
- env docs / `.env.example`

Then:

- define contracts for the first slice
- run `forge generate`

The architect phase should only define contracts needed for the first slice and minimal support paths. It should not widen into full CRUD unless the slice actually needs it.

### Phase 3 — Skeleton Loop

This is the core reliability gate. No feature fan-out happens until the skeleton is green.

The skeleton loop should repeat until green or until repair attempts are exhausted.

Checks in the loop:

- `forge generate`
- build
- contract lint
- backend smoke
- dev infra / env smoke
- optional frontend smoke

Repair behavior:

- run checks first
- if any check fails, run one bounded repair step
- rerun focused checks, then the full skeleton gate

The workflow composes Forge primitives (`generate`, `lint`, `build`, `test`, `run`) in a get-it-right loop — no dedicated Forge verify/repair commands needed.

If the skeleton never goes green, the workflow exits with a structured blocker report rather than pretending partial progress is success.

### Phase 4 — Parallel Feature Loops

Feature fan-out only begins after the skeleton is green.

Feature units should be cohesive verticals, not arbitrary layer splits. Examples:

- auth/session bootstrap
- primary entity create/list flow
- frontend first-slice page
- required job/webhook path

Per-feature loop requirements:

- implement only the approved slice unit
- add focused tests
- run the smallest relevant checks first
- report blocked dependencies honestly
- never silently mutate frozen contracts

Feature branches may run in parallel, but they should not be allowed to redefine core contracts independently.

### Phase 5 — Convergence Loop

The convergence loop reconciles cross-feature breakage after parallel work.

It is responsible for:

- schema consistency
- auth consistency
- frontend/backend consistency
- env/config consistency
- route registration
- service/package wiring
- job/webhook integration

Checks in convergence:

- regenerate if needed
- build
- contract lint
- tests
- smoke checks

The workflow composes the same Forge primitives (`generate`, `lint`, `build`, `test`) in a convergence-specific get-it-right loop.

The convergence loop is what turns parallel feature work into one coherent app.

### Phase 6 — Reviewer Swarm

Once convergence is green, run a reviewer swarm in parallel:

- security
- performance
- UX
- ops/deploy

Then run a synthesizer that:

- deduplicates findings
- filters out non-blocking polish
- routes critical first-slice issues into one bounded repair path
- requires a final verify pass after review-driven changes

The workflow should only report success when there are no unresolved critical findings for the first slice.

## Hard Gates

The workflow should feel like a pipeline with mandatory gates.

### Gate 1 — Scaffold / Contract Gate

Must pass before skeleton work proceeds.

- scaffold branches complete
- contracts defined for the first slice
- `forge generate` succeeds
- contract lint succeeds

### Gate 2 — Walking Skeleton Gate

Must pass before feature fan-out begins.

- build succeeds
- generated/runtime wiring is coherent
- app boots enough for smoke checks
- env and local infra assumptions are valid
- frontend smoke passes when applicable

### Gate 3 — Feature Verification Gate

Must pass per feature branch.

- feature code implemented
- focused tests added
- targeted checks pass
- blocked dependencies are reported explicitly

### Gate 4 — Convergence Gate

Must pass before review.

- schema/auth/frontend/env/routes/wiring are globally consistent
- integrated verification passes

### Gate 5 — Review Gate

Must pass before final success.

- no unresolved critical security/performance/UX/ops findings for the first slice
- final verify passes after review-driven fixes

## Threading and Parallelism Guidance

- Use `inherit` only for user-facing scoping conversation.
- Use `fork` for serial phases that need prior context without polluting the main thread.
- Use `new` or isolated worktrees for parallel feature branches and reviewers.
- Freeze contracts, auth mode, schema ownership, env contract, and route map before fan-out.
- Reopen those concerns only inside the convergence loop.

## Forge’s Role: Primitives, Not Orchestration

Forge provides the building-block commands: `generate`, `lint`, `build`, `test`, `run` (migrations, dev infra, etc.). The Reliant workflow owns all orchestration — skeleton loops, convergence gates, review passes — by composing those primitives in get-it-right loops.

There are no `forge verify` or `forge repair` commands. Validation and repair are workflow concerns, not Forge concerns.

The only potential future Forge top-level addition is `forge doctor` — a convenience command for diagnosing environment/tooling issues (missing tools, broken Docker, bad buf, etc.), not app correctness.

## Main Failure Modes and Escape Hatches

The workflow should fail honestly using structured states, not vague apologies.

Primary states:

- `needs_user_scope_decision`
- `rescope_required`
- `stubbed_integration_mode`
- `manual_env_intervention_required`
- `not_production_ready_first_slice`
- `partial_success`

## Why This Beats Demo-First Generators

The competitive thesis is reliability.

A better one-shot system should produce apps that:

- come with contracts
- generate predictably
- build cleanly
- include tests and smoke checks
- encode env/runtime assumptions
- support deploy validation
- recover through known repair loops

That is the path to “one-shot production-ready” being credible rather than theatrical.