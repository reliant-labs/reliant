---
name: skill-creator
description: Create or update SKILL.md-based skills in .reliant.local/skills, .reliant/skills, or ~/.reliant/skills with validation and test guidance.
compatibility: reliant
metadata:
  category: authoring
  owner: reliant
---
# Skill Creator

Use this skill when the user wants to create a new skill or improve an existing one.

## Scope constraints (strict)

Only scaffold into Reliant-managed paths:

- `.reliant.local/skills/<skill-name>/...` (local)
- `.reliant/skills/<skill-name>/...` (project)
- `~/.reliant/skills/<skill-name>/...` (global)

Do **not** scaffold to:

- `.claude/...`
- `.codex/...`
- `.agents/...`

If the user asks for non-Reliant destinations, explain the constraint and propose the equivalent Reliant path.

## Core principles

1. **Keep SKILL.md concise and high-signal**
   - Put essential workflow/instructions in SKILL.md.
   - Move large details into supporting files (`references/`, `scripts/`, `templates/`, `assets/`) when needed.
2. **Write trigger-ready descriptions**
   - Description should clearly state what the skill does and when it should be used.
   - Include likely user intents/phrases so activation is reliable.
3. **Prefer deterministic helpers for repeated work**
   - If the same transformation or command sequence repeats, scaffold a script.
4. **Iterate using concrete tests**
   - Draft realistic prompts, run the skill, inspect outputs, then revise.

## Authoring flow

### 1) Capture intent

Confirm:

- What task/workflow the skill should enable
- Typical user prompts that should trigger the skill
- Expected output format/quality bar
- Required tools, files, APIs, or constraints

### 2) Choose scope and location

Ask whether this should live in:

- local (`.reliant.local/skills/...`)
- project (`.reliant/skills/...`)
- global (`~/.reliant/skills/...`)

### 3) Design structure (progressive disclosure)

Start with:

- `SKILL.md` (required)

Optionally add:

- `references/` for deep docs/policies/schemas
- `scripts/` for deterministic routines
- `templates/` or `assets/` for reusable output artifacts

Keep SKILL.md focused on workflow and decision logic; keep heavy reference material in supporting files.

### 4) Author SKILL.md

Required frontmatter fields:

- `name`
- `description`

Recommended:

- `compatibility: reliant`
- `metadata` fields (owner/category/etc)

Body should include:

- When to use
- Inputs needed
- Step-by-step procedure
- Output expectations
- References to supporting files (with when/why to read)

### 5) Validate and test

Run a short eval loop:

1. Create 2–3 realistic prompts users would actually type.
2. Invoke the skill (`/<skill-name>` or `/skill <skill-name>`).
3. Compare output quality against expectations.
4. Refine description/body/supporting files.
5. Re-run prompts and confirm improvement.

For updates to existing skills, preserve what already works and focus edits on weak or ambiguous sections.

## SKILL.md starter template

```md
---
name: <skill-name>
description: <what it does + when to use it>
compatibility: reliant
metadata:
  owner: <team-or-user>
---
# <Skill Title>

## When to use
- ...

## Inputs
- ...

## Steps
1. ...
2. ...

## Output expectations
- ...

## References
- See `references/...` when ...
```

## Update flow for existing skills

When asked to improve an existing skill:

1. Read current `SKILL.md` and supporting files.
2. Identify likely failure points:
   - weak triggering description
   - ambiguous steps
   - missing edge-case guidance
   - bloated SKILL.md that should be split
3. Propose targeted edits.
4. Re-test using realistic prompts.
5. Summarize what changed and why.

## Quality checks before finishing

- Path is Reliant-managed and correct scope was used.
- Skill name is normalized, stable, and unambiguous.
- Description is specific about both capability and triggering context.
- Instructions are concise, imperative, and actionable.
- Supporting files are referenced only when useful.
- At least one invocation test was suggested (or run, if requested).
