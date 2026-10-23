# Skills

Skills are focused, self-contained playbooks that teach an AI agent (or a human) how to accomplish a specific task in a Forge project.

Each skill has a path-based name derived from its directory (`db`, `frontend/state`, `debug/investigate`).

## Where the bytes live, and which copy is authoritative

Two places. Forge never writes skills into a project; a leftover `.claude/skills/` render from an older forge is removed on the next `forge generate`:

| | Path | Who writes it |
|---|---|---|
| **Source** | `internal/templates/project/skills/{forge,general}/**/SKILL.md` (this tree) | you, in the forge repo — `//go:embed`ed into the binary |
| **Project / user skills** | `<project>/.forge/skills/**`, `~/.forge/skills/**` | you. Precedence on a path collision: forge-shipped < project < user-global |

`forge skill load <name>` prints from the binary — that is the authoritative copy, and it carries no banner or preamble. Non-reliant harnesses: add your own skill telling the agent to run `forge skill search <query>` / `forge skill load <name>`.

## Structure

Skills are organized by **action** — what you're trying to do — not by CLI command. A skill with sub-topics is a directory of directories: `testing/SKILL.md` beside `testing/unit/SKILL.md`, `testing/integration/SKILL.md`, `testing/e2e/SKILL.md`. Run `forge skill list` for the live catalog — do not maintain a copy of it here.

## How skills are used

Run `forge skill list` to see all available skills with descriptions. Run `forge skill load <name>` to print a skill's content to stdout. When an agent hits a task that matches a skill's description, it should load the skill and follow it.

Skills are **opinionated**. They encode project conventions and the non-obvious gotchas. Don't treat them as optional — the shortcuts around them cause pain.

## Dual-audience skills: `emit:` and `@forge-only` blocks

A skill can be authored once and emitted to two audiences: **general** (any project — methodology that doesn't depend on forge) and **forge** (forge projects, which see the full thing including framework-specific tooling). The compiler picks what to include based on per-skill frontmatter and inline block markers.

**Directory layout** decides the default audience:

```
skills/
├── forge/      # default emit: forge — framework skills (db, proto, api, ...)
└── general/    # default emit: general — methodology (code-review, refactor, ...)
```

A skill placed under `skills/forge/<name>/SKILL.md` defaults to `emit: forge`; under `skills/general/<name>/SKILL.md` defaults to `emit: general`. The frontmatter `emit:` field overrides this default — `debug` lives under `skills/forge/` but declares `emit: both` because its methodology applies anywhere while the forge-CLI tooling guidance is forge-only.

**Frontmatter `emit:` field** declares which audiences see the skill at all:

```yaml
emit: forge      # forge projects only (default for framework skills like proto, db, api)
emit: general    # any project (methodology that has no forge content)
emit: both       # both audiences (compiler strips @forge-only blocks for general emit)
```

**`@forge-only` block markers** mark content that only appears in the forge-audience emit. Use HTML comment markers so the raw source still renders cleanly in any markdown viewer:

```markdown
<!-- @forge-only:start -->
## Forge-Specific Debug Tools

forge debug start              # attach Delve debugger
task test              # run tests with race detector
<!-- @forge-only:end -->
```

**Markers must sit on their own line** (whitespace around the line and inside the comment is fine). The renderer is line-based — inline markers in the middle of a sentence will not be stripped. If you need to gate a single sentence, lift it into its own paragraph between the markers.

Content outside `@forge-only` blocks is included in both emits. **The general prose has to be more than just CLI-free — it has to be architecture-free.** Anything that assumes a forge-shaped project belongs in `@forge-only`, including:

- Specific generated files / paths (`wire_gen.go`, `internal/handlers/<svc>/`, `pkg/tdd`).
- Forge architectural concepts (proto-as-canonical-input, generated mocks, Tier-1 vs Tier-2 ownership, DI wiring, `forge generate` pipelines, `forge project audit`).
- Cross-references to forge sibling skills (`see the X sub-skill`) — those links are dead in a non-forge project. Fold the key idea inline in the general prose, then name the sub-skill inside the `@forge-only` block.
- Stack-specific tooling that only makes sense in a forge project (Connect RPC handler patterns, KCL deploy specifics, `_repo_ext.go` query seams).

Generic principles (mock-vs-real, test pyramid, race detection, the verify-visually loop) stay in the general prose — they apply anywhere. The test: a reader on a Python or Rust project should still get value from the general emit; if they hit "see the wire_gen.go" or "swap the generated mock," you've leaked.

See `forge/debug/SKILL.md` and `forge/diagrams/SKILL.md` for worked examples.

## Adding your own skills

Project-specific skills go in `<project>/.forge/skills/<name>/SKILL.md` (or `~/.forge/skills/` to carry one across projects) — never in this tree, which forge owns. Use the existing forge skills as a template:

1. **YAML frontmatter** with `name` (must match directory name) and `description` — the file must OPEN with it, at byte 0
2. **Action-oriented structure** — organize by what the developer wants to do, not by CLI subcommand
3. **Rules** — invariants and things you must not do
4. **When this skill is not enough** — pointers to other skills or approaches

Keep skills short and actionable. `TestShippedSkillsFitDeliveryBudget` enforces a hard delivered-size cap (the template plus the banner and preamble forge prepends); past it the tail is truncated away, taking the sub-skill pointers with it. Split a long skill into a sibling `<skill>/<subtopic>/SKILL.md` and leave a one-line pointer — never trade the cap for a longer file.

## Every claim in a skill is checked

`internal/templates/skills_validation_test.go` fact-checks every shipped SKILL.md against the binary it ships inside: `forge <chain>` references against the real cobra tree, the `--flags` after them against what that command accepts, path references against a real scaffold, `(forge.v1.*)` annotation fields against the compiled descriptors, `forge skill load <path>` targets, `forge/pkg/*` subpackages, audit categories, feature flags, proto packages, and forge.yaml keys. Legitimate exceptions go in `internal/templates/testdata/skills_validation_allowlist.txt` **with a justification** — an unexplained entry fails the test, and so does one whose claim no longer appears in the skill.

What the validators cannot see, you have to check yourself: **run the command and read the emitted file.** A claim about what generated code looks like is only as good as the last time someone scaffolded a project and looked.
