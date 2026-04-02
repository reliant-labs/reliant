# Workflow Collection

4 workflow YAML files currently exist.

## Available Workflows

| Workflow | File | Description |
|----------|------|-------------|
| **router** | `router.yaml` | Routes tasks to appropriate workflows |
| **simplify-first** | `simplify-first.yaml` | Refactor before implementing — simplify the codebase first so the plan and implementation are cleaner |
| **spec-driven** | `spec-driven.yaml` | Requirements-first, spec-driven development (inspired by GitHub Spec Kit / Kiro) |
| **superpowers** | `superpowers.yaml` | Strict TDD gates with mandatory phases (inspired by Obra / Superpowers methodology) |

## Workflow Details

### router

Routes incoming tasks to the most appropriate workflow based on the task characteristics.

---

### simplify-first

**Philosophy:** Refactor before implementing. By simplifying the codebase first, the plan itself becomes simpler and the implementation is cleaner.

**Flow:** `research` → `refactor` (fork) → `verify` (lint/test/build parallel) → `plan` → `implement` → `complete`

**Key features:**
- Refactor runs in a forked thread (isolated from main conversation)
- Verification after refactor ensures nothing broke
- Planning happens on the *simplified* codebase

**Best for:** Features in messy codebases where the existing code obscures the right approach.

---

### spec-driven (GitHub Spec Kit / Kiro)

**Philosophy:** Define *what* you want before *how* to build it. Requirements written without tech decisions, then layered with technical planning.

**Flow:** `specify` → `plan` → `implement` → `complete`

**Best for:** Greenfield features where requirements clarity is the bottleneck.

---

### superpowers (Obra)

**Philosophy:** Mandatory skill gates prevent skipping phases. Strict TDD enforcement. Two-stage review ensures both spec compliance and code quality.

**Flow:** `brainstorm` (yield) → `plan` → `tdd` → `implement` → `review` (structured pass/fail) → `complete`

**Key features:**
- Socratic brainstorming before any code
- Mandatory TDD phase (write failing tests first)
- Structured review with `builtin://structured-agent` and pass/fail schema

**Best for:** When you want rigid discipline and TDD enforcement.

---

## Choosing a Workflow

**Quick fixes / small tasks:**
- `simplify-first` — when the code needs cleanup before the feature

**Feature development:**
- `spec-driven` — requirements-first, good for greenfield
- `superpowers` — strict TDD + mandatory gates

**Quality assurance:**
- `superpowers` — mandatory review gates
