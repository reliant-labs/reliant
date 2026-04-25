---
name: reliant-config
description: Configure Reliant — memory files, skills, MCP servers, presets, and config YAML. Use when adding instructions, creating or updating skills, connecting tools, or customizing agent behavior.
compatibility: reliant
metadata:
  category: configuration
  owner: reliant
---
# Reliant Configuration

Use this skill to configure any aspect of Reliant: memory/instructions, skills, MCP servers, presets, or config YAML.

## Scope model

Reliant uses three configuration scopes, merged with higher priority winning:

| Scope | Location pattern | Git behavior |
|-------|-----------------|--------------|
| **Global (user)** | `~/.reliant/` | N/A (user home) |
| **Project** | `<project>/.reliant/` | Committed to git |
| **Local** | `<project>/.reliant.local/` | Gitignored |

This pattern repeats across all config types. Local always wins over project, which wins over global.

---

## 1. Memory system (reliant.md)

Memory files inject persistent instructions into every LLM call as "User defined rules, memories, and context."

| File | Scope | Git |
|------|-------|-----|
| `~/.reliant/reliant.md` | Global — applies to all projects | N/A |
| `<project>/reliant.md` | Project — committed, shared with team | Committed |
| `<project>/reliant.local.md` | Local — personal, gitignored | Gitignored |

**Precedence:** All three are merged (not overridden). Global loads first, then project, then local. All content is concatenated.

### Add project instructions

```bash
# Create or edit project instructions (shared with team)
cat > reliant.md << 'EOF'
## Project Guidelines
- Use Go 1.22+ features
- All PRs require tests
- Follow existing naming conventions
EOF
```

### Add personal local instructions

```bash
# Create or edit local instructions (gitignored, just for you)
cat > reliant.local.md << 'EOF'
## My Preferences
- I prefer verbose explanations
- Always run tests before committing
EOF
```

### Add global instructions

```bash
# Applies to every project you open
cat > ~/.reliant/reliant.md << 'EOF'
## Global Guidelines
- Always use meaningful variable names
- Write tests for all new features
EOF
```

---

## 2. Skills system

Skills are loadable instruction bundles that agents can discover and use on demand.

### Skill structure

```
<skills-root>/<skill-name>/
├── SKILL.md            # Required — frontmatter + instructions
├── references/         # Optional — deep docs, schemas, policies
├── scripts/            # Optional — deterministic routines
├── templates/          # Optional — reusable output artifacts
└── assets/             # Optional — images, data files
```

**Naming rules:** Skill name must match its parent directory. Lowercase, letters/digits/hyphens only.

### Required SKILL.md frontmatter

```yaml
---
name: my-skill
description: What it does and when to use it
---
```

### Recommended frontmatter

```yaml
---
name: my-skill
description: What it does and when to use it
compatibility: reliant
metadata:
  owner: my-team
  category: tooling
---
```

### Discovery roots (priority order)

Skills are discovered from multiple roots. Higher-priority roots shadow lower ones with the same name.

| Priority | Path | Scope |
|----------|------|-------|
| 1 (highest) | `<project>/.reliant.local/skills/` | Local |
| 2 | `<project>/.reliant/skills/` | Project |
| 3 | `<project>/.claude/skills/` | Claude compat |
| 4 | `<project>/.codex/skills/` | Codex compat |
| 5 | `<project>/.agents/skills/` | Codex agents compat |
| 6 | `~/.reliant/skills/` | Global |
| 7 | `~/.claude/skills/` | Claude global compat |
| 8 | `~/.codex/skills/` | Codex global compat |
| 9 (lowest) | Builtin (embedded) | Builtin |

**Only scaffold to Reliant-managed paths** (`.reliant.local/skills/`, `.reliant/skills/`, `~/.reliant/skills/`). Do not create skills in `.claude/`, `.codex/`, or `.agents/` directories.

### Create a project skill

```bash
mkdir -p .reliant/skills/my-helper
cat > .reliant/skills/my-helper/SKILL.md << 'EOF'
---
name: my-helper
description: Helps with our custom deployment process
compatibility: reliant
metadata:
  owner: platform-team
  category: devops
---
# My Helper

## When to use
- When deploying to staging or production

## Steps
1. Run preflight checks
2. Build the artifact
3. Deploy via our CLI
EOF
```

### Use a skill

```
skill load <name>       # Load a skill by name
skill search <query>    # Find skills by keyword
skill list              # List all available skills
```

### Authoring a new skill

When the user wants to create a new skill or improve an existing one, follow this flow.

#### Core principles

1. **Keep SKILL.md concise and high-signal** — put essential workflow/instructions in SKILL.md. Move large details into supporting files (`references/`, `scripts/`, `templates/`, `assets/`).
2. **Write trigger-ready descriptions** — description should clearly state what the skill does and when to use it. Include likely user intents/phrases so activation is reliable.
3. **Prefer deterministic helpers for repeated work** — if the same transformation or command sequence repeats, scaffold a script.
4. **Iterate using concrete tests** — draft realistic prompts, run the skill, inspect outputs, then revise.

#### Step 1: Capture intent

Confirm:
- What task/workflow the skill should enable
- Typical user prompts that should trigger the skill
- Expected output format/quality bar
- Required tools, files, APIs, or constraints

#### Step 2: Choose scope and location

Ask whether this should live in:
- local (`.reliant.local/skills/...`)
- project (`.reliant/skills/...`)
- global (`~/.reliant/skills/...`)

#### Step 3: Design structure (progressive disclosure)

Start with `SKILL.md` (required). Optionally add:
- `references/` for deep docs/policies/schemas
- `scripts/` for deterministic routines
- `templates/` or `assets/` for reusable output artifacts

#### Step 4: Author SKILL.md

Required frontmatter: `name`, `description`. Recommended: `compatibility: reliant`, `metadata` (owner/category).

Body should include:
- When to use
- Inputs needed
- Step-by-step procedure
- Output expectations
- References to supporting files (with when/why to read)

**Starter template:**

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

#### Step 5: Validate and test

1. Create 2–3 realistic prompts users would actually type.
2. Invoke the skill (`skill load <skill-name>`).
3. Compare output quality against expectations.
4. Refine description/body/supporting files.
5. Re-run prompts and confirm improvement.

### Updating an existing skill

When asked to improve an existing skill:

1. Read current `SKILL.md` and supporting files.
2. Identify likely failure points: weak triggering description, ambiguous steps, missing edge-case guidance, bloated SKILL.md that should be split.
3. Propose targeted edits.
4. Re-test using realistic prompts.
5. Summarize what changed and why.

### Quality checks before finishing

- Path is Reliant-managed and correct scope was used.
- Skill name is normalized, stable, and unambiguous.
- Description is specific about both capability and triggering context.
- Instructions are concise, imperative, and actionable.
- Supporting files are referenced only when useful.
- At least one invocation test was suggested (or run, if requested).

---

## 3. MCP server configuration

MCP (Model Context Protocol) servers extend agent capabilities with external tools.

### Config file locations

| Priority | Path |
|----------|------|
| 1 (lowest) | `~/.reliant/mcp.json` |
| 2 | `<project>/.reliant/mcp.json` |
| 3 (highest) | `<project>/.reliant.local/mcp.json` |

Servers are merged across scopes. Higher-priority scopes override servers with the same name.

### JSON format

```json
{
  "mcpServers": {
    "server-name": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server"],
      "env": { "API_KEY": "..." },
      "type": "stdio",
      "enabled": true
    }
  }
}
```

### Server types

| Type | Transport | Config fields |
|------|-----------|---------------|
| `stdio` (default) | stdin/stdout | `command`, `args`, `env` |
| `sse` | Server-Sent Events | `url`, `env` |
| `http` | HTTP | `url`, `env` |

### Add an MCP server (stdio)

```bash
mkdir -p .reliant
cat > .reliant/mcp.json << 'EOF'
{
  "mcpServers": {
    "my-tool": {
      "command": "npx",
      "args": ["-y", "@company/mcp-tool"],
      "env": {
        "API_KEY": "sk-..."
      }
    }
  }
}
EOF
```

### Add an MCP server (SSE/HTTP)

```json
{
  "mcpServers": {
    "remote-tool": {
      "type": "sse",
      "url": "https://mcp.example.com/sse",
      "env": { "TOKEN": "..." }
    }
  }
}
```

### Disable a server without removing it

```json
{
  "mcpServers": {
    "noisy-server": {
      "enabled": false
    }
  }
}
```

### Keep secrets local

Put API keys in `.reliant.local/mcp.json` (gitignored) rather than `.reliant/mcp.json`:

```bash
# .reliant/mcp.json — committed, no secrets
{
  "mcpServers": {
    "my-tool": {
      "command": "npx",
      "args": ["-y", "@company/mcp-tool"]
    }
  }
}

# .reliant.local/mcp.json — gitignored, has secrets
{
  "mcpServers": {
    "my-tool": {
      "env": { "API_KEY": "sk-secret-key" }
    }
  }
}
```

---

## 4. Presets

Presets are YAML files that bundle spawn parameters for reusable agent configurations.

### Preset locations

| Priority | Location | Source |
|----------|----------|--------|
| 1 (lowest) | Builtin (embedded) | `builtin` |
| 2 | `<project>/.reliant/presets/*.yaml` | `project` |
| 3 (highest) | User presets (database) | `user` |

Project presets with the same name as a builtin will override the builtin.

### Preset format

```yaml
name: careful-reviewer
description: Slow, methodical code review with high scrutiny
tag: agent
params:
  permission: readonly
  model:
    tags: [flagship]
  system_prompt: |
    You are a meticulous code reviewer. Check for:
    - Security vulnerabilities
    - Performance issues
    - Code style consistency
  tools:
  - tag:default
  spawn_presets:
  - researcher
  temperature: 0.2
  thinking_level: high
```

### Key preset fields

| Field | Description |
|-------|-------------|
| `name` | Preset identifier (matches filename without `.yaml`) |
| `description` | Shown in the preset picker UI |
| `tag` | Must match the workflow/group tag (usually `agent`) |
| `params.permission` | `readonly`, `mutating`, or `orchestrator` |
| `params.model` | Use `tags: [flagship]`, `tags: [moderate]`, etc. Never hardcode model names. |
| `params.system_prompt` | System instructions for the spawned agent |
| `params.tools` | Tool access: `tag:default`, `tag:mcp`, etc. |
| `params.spawn_presets` | Which presets this agent can use to spawn sub-agents |
| `params.temperature` | Sampling temperature (0.0–1.0) |
| `params.thinking_level` | `high`, `medium`, `low` |

### Create a project preset

```bash
mkdir -p .reliant/presets
cat > .reliant/presets/my-preset.yaml << 'EOF'
name: my-preset
description: Custom agent for our specific workflow
tag: agent
params:
  permission: mutating
  model:
    tags: [flagship]
  system_prompt: |
    You specialize in our internal API patterns.
  tools:
  - tag:default
  spawn_presets:
  - researcher
  - general
EOF
```

---

## 5. Config YAML

General Reliant settings via YAML config files.

### Config file locations

| Priority | Path |
|----------|------|
| 1 (lowest) | `~/.reliant/config.yaml` |
| 2 | `<project>/.reliant/config.yaml` |
| 3 (highest) | `<project>/.reliant.local/config.yaml` |

Settings are merged across scopes with higher priority winning.

---

## 6. Project structure summary

```
<project>/
├── reliant.md                          # Project memory (committed)
├── reliant.local.md                    # Local memory (gitignored)
├── .reliant/
│   ├── config.yaml                     # Project config
│   ├── mcp.json                        # Project MCP servers
│   ├── skills/
│   │   └── <skill-name>/
│   │       └── SKILL.md                # Project skill
│   └── presets/
│       └── <preset-name>.yaml          # Project preset
├── .reliant.local/
│   ├── config.yaml                     # Local config (gitignored)
│   ├── mcp.json                        # Local MCP servers (gitignored)
│   └── skills/
│       └── <skill-name>/
│           └── SKILL.md                # Local skill (gitignored)
~/.reliant/
├── reliant.md                          # Global memory
├── config.yaml                         # Global config
├── mcp.json                            # Global MCP servers
└── skills/
    └── <skill-name>/
        └── SKILL.md                    # Global skill
```

## Quick reference: "Where do I put...?"

| I want to... | File |
|---------------|------|
| Add team-wide coding rules | `reliant.md` |
| Add personal preferences | `reliant.local.md` |
| Add rules for all projects | `~/.reliant/reliant.md` |
| Share an MCP tool with the team | `.reliant/mcp.json` |
| Add an MCP tool with secret keys | `.reliant.local/mcp.json` |
| Create a reusable agent preset | `.reliant/presets/<name>.yaml` |
| Add a project skill | `.reliant/skills/<name>/SKILL.md` |
| Add a personal skill | `.reliant.local/skills/<name>/SKILL.md` |
| Add a skill for all projects | `~/.reliant/skills/<name>/SKILL.md` |