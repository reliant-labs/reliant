# Reliant Configuration Reference

## Overview

Reliant separates user-editable configuration from internal application data. Everything you configure lives in `~/.reliant/` on all platforms, while databases, analytics, and other internal data use platform-appropriate locations.

## Configuration Structure

### User Configuration (`~/.reliant/`)

User-editable files that you can version control, share, and customize:

```
~/.reliant/
├── config.yaml      # Global settings (models, shell preferences)
├── mcp.json         # Global MCP server definitions
├── reliant.md       # Global context and instructions
└── worktrees/       # Git worktree storage
```

This directory uses a simple, consistent location across all platforms. No environment variables required—Reliant always looks in `~/.reliant/`.

### Internal Application Data

Platform-specific locations for databases, analytics, and other managed data:

| Platform | Location |
|----------|----------|
| **macOS** | `~/Library/Application Support/reliant/` |
| **Linux** | `~/.local/share/reliant/` |
| **Windows** | `%APPDATA%\reliant\` |

Contains:
```
data/            # Databases (reliant.db, temporal.db)
analytics/       # Usage tracking data
auth/            # Authentication tokens
cache/           # Temporary caches
```

### Log Files

Platform-specific log locations:

| Platform | Location |
|----------|----------|
| **macOS** | `~/Library/Logs/Reliant/` |
| **Linux** | `~/.local/state/reliant/logs/` or `~/.reliant/logs/` |
| **Windows** | `%APPDATA%\reliant\logs\` |

---

## Global Config (`~/.reliant/config.yaml`)

User-level settings that apply across all projects.

```yaml
# Models configuration (global only - Reliant runs as one server with many project windows)
models:
  providers:
    local:
      base_url: http://localhost:11434/v1  # Ollama, LM Studio, llama.cpp, vLLM
  
  custom:
    - id: my-local-model
      name: "My Local Model"
      tags: [local, fast]
      capabilities:
        supports_tools: true
        max_context_window: 200000
        max_output_tokens: 32768
      providers:
        - driver: local
          api_model: qwen3:latest
  
  tag_preferences:
    fast: [gpt-4o-mini, claude-3-haiku]
    flagship: [gpt-5.3-codex, claude-4.6-opus]

# Shell configuration
shell:
  path: /bin/bash
  args: ["-l"]

# Worktree storage location
worktree_dir: ~/.reliant/worktrees

# Other settings
auto_compact: true
debug: false
```

### Why Global Config?

Reliant runs as a single server that can have multiple project windows open simultaneously. Model configuration, shell preferences, and worktree locations are machine-specific, not project-specific, so they live in global config.

---

## Project Config (`.reliant/config.yaml`)

Project-specific settings. Committed to git and shared with your team.

```yaml
# MCP servers for this project
mcp_servers:
  my-server:
    command: npx
    args: ["-y", "@my/mcp-server"]
    env: ["API_KEY=${MY_API_KEY}"]

# Project-specific overrides
debug: true  # Enable debug logging for this project
```

---

## Project-Local Config (`.reliant.local/config.yaml`)

Machine-specific overrides for this project. **Gitignored** so each developer can have their own settings.

```yaml
# Local MCP server overrides (e.g., different paths on this machine)
mcp_servers:
  my-server:
    command: /usr/local/bin/my-server

# Local data directory for this project
data:
  directory: /tmp/reliant-debug
```

---

## Context Files

Instruction files automatically loaded from project root:

| File | Purpose | Gitignored |
|------|---------|------------|
| `~/.reliant/reliant.md` | Global instructions (all projects) | N/A |
| `reliant.md` | Project instructions (team-shared) | No |
| `reliant.local.md` | Local instructions (machine-specific) | **Yes** |

Context files are loaded in this order:
1. Global context (`~/.reliant/reliant.md`)
2. Project context (`reliant.md`)
3. Local context (`reliant.local.md`)

---

## Settings Reference

### Global-Only Settings

These settings only apply from `~/.reliant/config.yaml`:

| Setting | Type | Description |
|---------|------|-------------|
| `models.providers.local.base_url` | string | Local model server URL |
| `models.custom` | array | Custom model definitions |
| `models.tag_preferences` | map | Model preferences by tag |
| `worktree_dir` | string | Git worktree storage directory (default: `~/.reliant/worktrees`) |

### Merged Settings (Global < Project < Local)

These settings merge across all three config files:

| Setting | Type | Description |
|---------|------|-------------|
| `mcp_servers` | map | MCP server definitions |
| `shell.path` | string | Shell executable |
| `shell.args` | array | Shell arguments |
| `data.directory` | string | Data storage directory |
| `auto_compact` | bool | Auto-compact conversations |
| `debug` | bool | Enable debug logging |
| `skills.activationMode` | string | Skill activation mode (`auto` or `explicit`) |
| `skills.integrationMode` | string | Skill integration mode (`filesystem` or `tool`) |
| `skills.supportingFiles.maxFiles` | int | Max supporting files loaded for active skill |
| `skills.supportingFiles.maxBytes` | int | Max bytes per supporting file |
| `skills.retrieval.maxFiles` | int | Max supporting files considered by retrieval |
| `skills.retrieval.maxChunks` | int | Max retrieval chunks selected |
| `skills.retrieval.chunkBytes` | int | Retrieval chunk size in bytes |
| `skills.retrieval.chunkOverlap` | int | Retrieval chunk overlap in bytes (explicit `0` is preserved) |
| `skills.retrieval.maxPromptBytes` | int | Max prompt bytes for retrieved supporting content |
| `skills.availableSkills.maxCount` | int | Max skills listed in `<available_skills>` section |
| `skills.availableSkills.maxPromptBytes` | int | Byte cap for rendered `<available_skills>` section |
| `features.skills_enabled` | bool | Master skills feature gate (default `false`, enable to turn on all skills behavior) |

---

## Local Model Servers

Common OpenAI-compatible server URLs:

| Server | URL |
|--------|-----|
| Ollama | `http://localhost:11434/v1` |
| LM Studio | `http://localhost:1234/v1` |
| llama.cpp | `http://localhost:8080/v1` |
| vLLM | `http://localhost:8000/v1` |

Configure in `~/.reliant/config.yaml`:

```yaml
models:
  providers:
    local:
      base_url: http://localhost:11434/v1
```

---

## Environment Variables

For advanced use cases, you can override default paths:

| Variable | Default | Purpose |
|----------|---------|---------|
| `RELIANT_USER_CONFIG_DIR` | `~/.reliant/` | User configuration directory |
| `RELIANT_APP_DATA_DIR` | Platform-specific | Internal app data directory |
| `RELIANT_LOGS_PATH` | Platform-specific | Log file directory |
| `RELIANT_WORKTREE_DIR` | `~/.reliant/worktrees/` | Git worktree storage |
| `DATABASE_DRIVER` | `sqlite` | Backend DB driver (`sqlite` or `postgres`) |
| `DATABASE_URL` | _(unset)_ | Required when `DATABASE_DRIVER=postgres` |
| `RELIANT_FEATURE_SKILLS_ENABLED` | _(unset)_ | Optional env override for skills feature gate (`true`/`false`) |

When using `DATABASE_DRIVER=postgres` in local development, `scripts/dev.sh` and `scripts/dev.ps1` now auto-provision a **per-worktree database** on the shared local Postgres server and export a worktree-specific `DATABASE_URL` (also written to `.env.ports`). This keeps Postgres behavior aligned with SQLite worktree isolation.

These are primarily used internally by the Electron app. CLI users typically don't need to set them.

**Deprecated:** `RELIANT_USER_DATA_PATH` is no longer used and should not be set.

---

## OAuth Helper Server (Web Mode)

When using Reliant through a web browser (not Electron), Claude and Codex OAuth logins require a localhost callback receiver. Run:

```bash
reliant auth serve            # default port 19284
reliant auth serve --port 9999  # custom port
```

This starts a lightweight HTTP server on `127.0.0.1:19284` that:
- Receives OAuth callbacks from Claude/Codex on localhost
- Returns the authorization code to the browser frontend
- Does **not** handle token exchange or storage (the backend API does that)

The frontend automatically detects whether this server is running and shows the appropriate UI (login buttons vs. "run this command" instructions). In Electron, the daemon handles this automatically.

| Endpoint | Method | Purpose |
|----------|--------|-------|
| `/health` | GET | Health check — frontend pings to detect availability |
| `/oauth/start` | POST | Start OAuth flow — opens browser, waits for callback |

---

## Complete Directory Structure

```
~/.reliant/                           # User configuration (all platforms)
├── config.yaml                       # Global settings
├── mcp.json                          # Global MCP servers
├── reliant.md                        # Global context
└── worktrees/                        # Git worktrees
    └── abc123def/
        └── my-project/

~/Library/Application Support/reliant/  # macOS internal data
├── data/
│   ├── reliant.db                   # Main database
│   └── temporal.db                  # Workflow engine database
├── analytics/
│   └── events.db                    # Usage analytics
├── auth/
│   └── tokens.json                  # Auth tokens
└── cache/                           # Temporary caches

~/Library/Logs/Reliant/              # macOS logs
├── main.log
└── backend.log

/path/to/project/                    # Project directory
├── .reliant/                        # Project config (committed)
│   ├── config.yaml                  # Project settings
│   ├── mcp.json                     # Project MCP servers
│   └── workflows/                   # Custom workflows
│       └── my-workflow.yaml
│
├── .reliant.local/                  # Local overrides (gitignored)
│   └── config.yaml
│
├── reliant.md                       # Project instructions (committed)
└── reliant.local.md                 # Local instructions (gitignored)
```

---

## Migration from Previous Versions

If you have configuration in old locations, Reliant will prompt you to migrate. You can also migrate manually:

**From `~/.reliant/config.yaml` to new location:** No change needed—this is already the correct location.

**From app data config (e.g., `~/Library/Application Support/reliant/.reliant/config.yaml`):**
```bash
# Copy your config to the new location
cp ~/Library/Application\ Support/reliant/.reliant/config.yaml ~/.reliant/config.yaml

# Review and remove the old file
rm ~/Library/Application\ Support/reliant/.reliant/config.yaml
```

After migration, verify your models appear in Settings → AI.