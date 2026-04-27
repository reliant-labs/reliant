---
name: migration-guidance
description: Migration methodology — discovering, evaluating, and converting configuration from Claude Code, Cursor, Codex, and Windsurf into Reliant
---

# Migration Guidance

## Sources to Inspect
Check both project-local and user-home locations when relevant.

### Claude Code
- `~/.claude/`
- `~/.claude/CLAUDE.md`
- `~/.claude.json`
- `CLAUDE.md`
- `.claude/CLAUDE.md`
- `.claude/commands/`
- `.mcp.json`

### Cursor
- `.cursor/rules/`
- `.cursorrules`
- `.cursor/mcp.json`
- `AGENTS.md`

### Codex
- `~/.codex/config.toml`
- `.codex/config.toml`
- `.codex/skills/`
- `AGENTS.md`
- `AGENTS.override.md`

### Windsurf
- `.windsurf/rules/`
- `.windsurfrules`
- `.windsurf/mcp.json`

## Reliant Targets
- Memory/instructions → `reliant.md`
- MCP config → `.reliant/mcp.json`
- Skills → `.reliant/skills/<name>/SKILL.md`
- Custom workflows → `.reliant/workflows/<name>.yaml` or workflow drafts when using workflow builder

## Working Style
- Start with discovery. Do not write files until the workflow has explicitly reached a write phase.
- Prefer preserving user intent over preserving source-tool structure.
- Merge and simplify where possible. Remove obsolete paths instead of keeping compatibility shims.
- Use default tools effectively: `glob`, `grep`, `view`, `bash`, `write`, `edit`, and `spawn`.
- Use `bash` only when it materially helps, especially for checking home-directory locations or listing command folders.
- Use `spawn` aggressively for independent migration workstreams so MCP, memory, skills, and workflow analysis can happen in parallel.
- Be explicit about assumptions and uncertainty.
- If a config source is ambiguous or low-value, say so and avoid over-migrating junk.

## Conversion Guidance
- Merge instruction files into a coherent Reliant voice instead of concatenating blindly.
- Deduplicate repetitive rules and prefer the clearest surviving version.
- Convert context-dependent rules into skills when that maps better than global memory.
- Only generate workflows for commands or automations that are genuinely reusable in Reliant.
- Keep generated workflow names descriptive and stable.

## Workflow Builder Usage
When another phase determines a command should become a Reliant workflow:
1. Preserve the original intent, arguments, and success conditions.
2. Create a workflow draft first.
3. Hand the candidate to the workflow builder with the draft id and precise instructions.
4. Do not create throwaway workflows for one-off prompts.

## Output Expectations
During conversational phases:
- Summarize findings clearly.
- Call out what you recommend migrating vs skipping.
- Mention exact files you changed when you write anything.

During execution phases:
- Make the requested changes directly.
- Keep progress updates brief and practical.
- End with what changed, what was skipped, and any follow-up suggestions.
