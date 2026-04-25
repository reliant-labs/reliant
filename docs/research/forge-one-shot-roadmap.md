# `forge-one-shot` Roadmap

This roadmap turns the `forge-one-shot v3` strategy into an implementation sequence across Forge and Reliant.

The goal is not to maximize feature count. The goal is to make one-shot generation reliably produce a **validated, bounded first production slice**.

## Outcome We Want

When this roadmap is complete, a user should be able to describe a realistic app, run `forge-one-shot`, and receive:

- a repo scaffolded for the approved first slice
- contracts and generated code that stay internally consistent
- a green walking skeleton before feature fan-out starts
- bounded repair loops instead of false success
- convergence after parallel work
- a final result that is honest about deferred work and unresolved blockers

## Principles

- optimize for reliability over breadth
- gate aggressively and fail honestly
- keep generated structure and agent-authored implementation clearly separated
- verify the smallest useful surface first, then widen
- preserve SQLite and Postgres parity whenever DB behavior changes
- prefer a validated first slice over pretending to complete the full product

## P0 — Make Core Slice Generation Reliable

P0 is the highest-leverage wave. It focuses on getting the skeleton-first path green and making failure states real.

### Forge deliverables

1. **Generator/runtime consistency fixes**
   - Eliminate the classes of failures surfaced by the `dev/pm-saas` trial.
   - Priorities:
     - handler and generated stub reconciliation
     - route registration consistency
     - authorizer/runtime template consistency
     - webhook/job template correctness
     - frontend/backend contract alignment where generation owns both sides

2. **Dev/runtime verification hooks**
   - Add reliable smoke entrypoints that do not require humans to infer what to run.
   - Examples:
     - backend health/start checks
     - env completeness checks
     - optional local infra readiness checks

### Reliant workflow deliverables

1. **Upgrade skeleton loop to compose Forge primitives in get-it-right loops**
   - The workflow calls `generate`, `lint`, `build`, `test`, and `run` directly.
   - On failure, run one bounded repair step, then re-check.
   - No new Forge commands needed — orchestration lives in the workflow.

2. **Upgrade workflow from hardened v2 to full v3 phase model**
   - Implement:
     - scope first slice
     - scaffold everything
     - skeleton loop
     - parallel feature loops
     - convergence loop
     - reviewer swarm + synthesizer

3. **Make hard gates mandatory**
   - No feature fan-out before the skeleton gate is green.
   - No final success if convergence or review gates fail.
   - Replace soft "best effort" success messaging with explicit blocker exits.

4. **Freeze contracts before parallel fan-out**
   - Freeze:
     - schema ownership
     - auth mode
     - route map
     - env contract
     - integration inventory
   - Parallel feature agents may implement, but not redefine these contracts.

5. **Structured blocker reporting**
   - Every failed gate should emit:
     - failed check
     - evidence summary
     - attempted repairs
     - next best action
   - The workflow should stop pretending partial progress is success.

### Success criteria for P0

- A fresh realistic app can reach a green walking skeleton without manual rescue.
- The workflow exits as failed when the skeleton cannot be repaired.
- Generator/runtime defects no longer masquerade as successful app generation.
- The workflow's final message clearly distinguishes delivered slice vs deferred work.

## P1 — Add Reliable Convergence and Review

Once P0 makes the skeleton path reliable, P1 makes multi-agent parallelism safe.

### Forge deliverables

1. **First-slice smoke harnesses**
   - Make it cheap to verify the golden journey.
   - Prefer deterministic local smokes over hand-wavy "should work" output.

### Reliant workflow deliverables

1. **Parallel feature verticals**
   - Split work into cohesive vertical slices, not layer-by-layer fragmentation.
   - Examples:
     - auth bootstrap
     - core entity create/list path
     - first frontend page
     - required webhook/job path

2. **Convergence loop implementation**
   - Add an explicit reconcile-and-verify phase after feature fan-out.
   - The workflow composes Forge primitives (`generate`, `lint`, `build`, `test`) in a convergence-specific get-it-right loop.
   - It owns:
     - schema consistency
     - auth consistency
     - route and service wiring
     - frontend/backend consistency
     - env/config reconciliation

3. **Reviewer swarm**
   - Run specialized reviewers in parallel:
     - security
     - performance
     - UX
     - ops/deploy
   - Then synthesize findings into one bounded repair queue.

4. **Post-review verify pass**
   - Any review-driven fix must pass a final integrated verify step (re-run Forge primitives).

### Success criteria for P1

- Parallel feature work converges into one coherent first slice.
- Reviewers find meaningful issues without overwhelming the workflow with polish noise.
- The workflow only reports success when no critical first-slice issues remain.

## P2 — Benchmark and Harden Toward "Better Than Loveable/Base44"

P2 turns the system from promising into provably strong.

### Forge deliverables

1. **Golden SaaS benchmark app**
   - Maintain one realistic reference app with:
     - auth
     - CRUD or workflow core
     - frontend
     - at least one integration or webhook/job
     - env/config requirements
   - Use it as the regression target for one-shot generation quality.

2. **Regression suites for generated-app quality**
   - Measure:
     - scaffold correctness
     - verification pass rates
     - convergence failures
     - reviewer findings
     - time to green skeleton

### Reliant workflow deliverables

1. **Productionized one-shot operating mode**
   - Default to bounded first-slice behavior.
   - Keep success criteria strict and honest.

2. **Prompt/skill refinement from benchmark evidence**
   - Improve planner, architect, integrator, and reviewer instructions using benchmark failures.
   - Optimize prompts for reliable structure, not aspirational breadth.

3. **Operational reporting**
   - Make workflow output easy to inspect and debug:
     - gate summaries
     - repair history
     - deferred work summary
     - critical blockers summary

### Success criteria for P2

- The golden benchmark app generates successfully at a high rate.
- Failure modes are narrow, repeatable, and actionable.
- The workflow is measurably more reliable than breadth-first "build everything" competitors.

## Recommended Build Order

1. fix remaining generator/runtime inconsistencies exposed by realistic smoke apps
2. upgrade the workflow's skeleton loop to compose Forge primitives (`generate`, `lint`, `build`, `test`, `run`) in get-it-right loops
3. upgrade `forge-one-shot` YAML to the full v3 six-phase model
4. add convergence loop composing the same Forge primitives
5. add reviewer swarm and post-review verify
6. stand up the golden SaaS benchmark and regressions

## What We Should Not Do Yet

To preserve focus, do not expand into these until the roadmap above is working:

- scorecards
- domain packs
- broad benchmark matrices
- unbounded full-product generation
- "success" definitions based on repo creation plus pretty screenshots

## Immediate Next Move

The best next implementation wave is:

- upgrade the workflow's skeleton loop to compose Forge primitives (`generate`, `lint`, `build`, `test`, `run`) in a get-it-right loop with bounded repair
- fix generator/runtime inconsistencies that the loop will surface

Forge already has the primitives. The gap is the workflow's skeleton loop — it needs to call them systematically, check results, and retry on failure. That is the shortest path to making one-shot generation actually dependable instead of merely impressive.
