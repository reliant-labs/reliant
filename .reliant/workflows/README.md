# Workflow Collection

11 workflows implementing viral AI coding methodologies and original patterns.

## Quick Reference

| Workflow | Inspiration | When To Use | Complexity |
|----------|-------------|-------------|------------|
| **simplify-first** | Original | Brownfield code that needs cleanup before a feature | Moderate |
| **get-it-right** | Original | Complex brownfield where you need to fail to understand | High |
| **one-workflow** | Original | Want a buffet of toggleable steps | Moderate |
| **spec-driven** | GitHub Spec Kit / Kiro | Requirements-first, spec-driven development | Simple |
| **ralph-wiggum** | Ralph Wiggum method | Autonomous loop through a task list / spec | High |
| **deep-research** | Original | Need thorough parallel investigation before acting | Moderate |
| **gsd** | Get Shit Done | Rapid pragmatic execution, minimal ceremony | Simple |
| **superpowers** | Obra / Superpowers | Strict TDD gates with mandatory phases | High |
| **bmad-lite** | BMAD | Persona-driven planning (PM → Architect → Dev) | Moderate |
| **devils-advocate** | Original | Want adversarial review of plans before implementing | Moderate |
| **rubber-duck** | Rubber Duck Debugging | Debugging via Socratic questioning | Simple |

## Workflows from Viral Methodologies

### spec-driven (GitHub Spec Kit / Kiro)

**Source:** GitHub's Spec Kit and Kiro's spec-first approach.

**Philosophy:** Define *what* you want before *how* to build it. Requirements written without tech decisions, then layered with technical planning.

**Flow:** `specify` → `plan` → `implement` → `complete`

**Best for:** Greenfield features where requirements clarity is the bottleneck.

---

### ralph-wiggum (Ralph Wiggum Method)

**Source:** The viral Ralph Wiggum autonomous loop pattern.

**Philosophy:** Loop-based autonomous agent that reads specs, picks the highest priority task, implements, verifies, and repeats. Fresh context each iteration prevents context rot.

**Flow:** `task_loop` (orient → implement → verify) × N iterations → `complete`

**Key features:**
- `max_iterations` input prevents runaway loops
- Structured verification with pass/fail after each task
- Loop continues while tasks remain and iteration limit not reached

**Best for:** Implementing a spec or task list autonomously.

---

### gsd (Get Shit Done)

**Source:** The GSD methodology.

**Philosophy:** Minimal ceremony, maximum velocity. Brief discussion to capture preferences, then rapid parallel execution.

**Flow:** `discuss` (yield) → `research` → `plan` → `implement` → `verify` (lint/test/build parallel) → `complete`

**Key features:**
- Discussion phase always yields for user input (the one ceremony moment)
- Every agent forks for fresh context
- Verification runs lint/test/build in parallel

**Best for:** When you know roughly what you want and just need it done.

---

### superpowers (Obra)

**Source:** The Superpowers / obra methodology.

**Philosophy:** Mandatory skill gates prevent skipping phases. Strict TDD enforcement. Two-stage review ensures both spec compliance and code quality.

**Flow:** `brainstorm` (yield) → `plan` → `tdd` → `implement` → `review` (structured pass/fail) → `complete`

**Key features:**
- Socratic brainstorming before any code
- Mandatory TDD phase (write failing tests first)
- Structured review with `builtin://structured-agent` and pass/fail schema

**Best for:** When you want rigid discipline and TDD enforcement.

---

### bmad-lite (BMAD)

**Source:** BMAD (Business/Marketing/Architecture/Development) methodology, simplified.

**Philosophy:** Different personas for different phases. A PM brainstorms and writes requirements, an Architect designs the system, a Developer implements.

**Flow:** `ideate` (PM) → `requirements` (PM) → `architecture` (Architect) → `implement` (Dev) → `complete`

**Note:** Full BMAD has 34+ workflows and is enterprise-scale. This captures the core persona-switching insight in a single workflow.

**Best for:** Larger features that benefit from distinct planning perspectives.

---

## Original Workflows

### simplify-first

**Philosophy:** Refactor before implementing. By simplifying the codebase first, the plan itself becomes simpler and the implementation is cleaner.

**Flow:** `research` → `refactor` (fork) → `verify` (lint/test/build parallel) → `plan` → `implement` → `complete`

**Key features:**
- Refactor runs in a forked thread (isolated from main conversation)
- Verification after refactor ensures nothing broke
- Planning happens on the *simplified* codebase

**Best for:** Features in messy codebases where the existing code obscures the right approach.

---

### get-it-right

**Philosophy:** Sometimes you need to try and fail to truly understand. LLMs often paper-mache code on top of existing messes. This workflow intentionally lets the first attempt fail, captures what was learned, then implements correctly.

**Flow:** `attempt` (loop: try → diagnose × N) → `diagnose` (final synthesis) → `implement` → `complete`

**Key features:**
- `attempt` is a loop that tries implementing, then diagnoses what went wrong
- Structured diagnosis captures: what was hard, what abstractions are wrong, what the right approach is
- Final `implement` is armed with real understanding from failed attempts
- `max_retries` controls the attempt loop

**Best for:** Complex brownfield work where the LLM consistently gets the wrong abstraction on the first try.

---

### one-workflow (One Ring)

**Philosophy:** One workflow to rule them all. A single configurable workflow where you toggle steps on/off like a buffet.

**Steps available:** `research`, `plan`, `simplify`, `tdd`, `implement`, `lint`, `test`, `build`, `code_review`, `security_audit`

**Key features:**
- Multi-select `steps` enum — pick any combination
- Each step has `condition: "'step' in inputs.steps"`
- Linear chain; skipped steps complete instantly
- Defaults to `plan`, `implement`, `code_review`

**Best for:** When you want to compose your own workflow on the fly without creating a new YAML file.

---

### deep-research

**Philosophy:** Multiple research agents investigate different aspects in parallel, then a synthesis agent produces a unified report.

**Flow:** `decompose` → `researcher_1` + `researcher_2` + `researcher_3` (parallel) → `join` → `synthesize` → `complete`

**Key features:**
- Fan-out: 3 parallel research agents from a single decompose step
- Join node waits for all researchers to complete
- Synthesis agent merges findings into a coherent report

**Best for:** Complex research questions that benefit from multiple investigation angles.

---

### devils-advocate

**Philosophy:** Plans are better when stress-tested. One agent creates a plan, another pokes holes and proposes alternatives, then a synthesis step merges the best ideas.

**Flow:** `plan` → `counter_plan` (skeptic) → `synthesize` → `implement` → `complete`

**Best for:** High-stakes changes where you want to consider alternatives before committing.

---

### rubber-duck

**Philosophy:** Forcing articulation of a problem often reveals the solution. The agent asks probing questions rather than jumping to a fix.

**Flow:** `explain` (Socratic, yield) → `diagnose` → `plan` → `implement` → `complete`

**Key features:**
- Explain phase yields for user interaction (multiple back-and-forth rounds)
- Agent is instructed to ask questions, not provide solutions
- Only after thorough understanding does it move to diagnosis and implementation

**Best for:** Debugging when you're stuck and need to think through the problem.

---

## Choosing a Workflow

**Quick fixes / small tasks:**
- `gsd` — minimal ceremony, get it done
- `rubber-duck` — if you're stuck and need to think it through

**Feature development:**
- `spec-driven` — requirements-first, good for greenfield
- `one-workflow` — customize the steps you want
- `bmad-lite` — persona-driven planning for larger features
- `superpowers` — strict TDD + mandatory gates

**Brownfield / legacy code:**
- `simplify-first` — when the code needs cleanup before the feature
- `get-it-right` — when you need to fail to understand the right approach

**Research / investigation:**
- `deep-research` — parallel multi-agent investigation

**Quality assurance:**
- `devils-advocate` — stress-test plans before implementing
- `superpowers` — mandatory review gates

**Autonomous execution:**
- `ralph-wiggum` — loop through a task list autonomously

## Methodology Feasibility Notes

| Methodology | Feasibility | Notes |
|-------------|-------------|-------|
| GitHub Spec Kit / Kiro | Implemented as `spec-driven` | Linear spec-first pipeline maps cleanly |
| Ralph Wiggum | Implemented as `ralph-wiggum` | Loop pattern is a natural fit |
| Get Shit Done | Implemented as `gsd` | Discussion + parallel execution works well |
| Superpowers / obra | Implemented as `superpowers` | Mandatory gates via conditions + structured review |
| BMAD | Partially as `bmad-lite` | Full BMAD (34 workflows) is too complex for a single file |
| Simplify First | Implemented | Original concept, clean fit |
| Get It Right | Implemented | Loop + structured diagnosis captures the fail-forward pattern |
| One Ring | Implemented as `one-workflow` | Condition-based toggling works via multi-select enum |
| Deep Research | Implemented | Fan-out + join is a native pattern |
| Devil's Advocate | Implemented | Adversarial planning with sequential steps |
| Rubber Duck | Implemented | Socratic questioning via yield + inject |
