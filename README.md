# Reliant

**AI coding assistant with programmable workflows**

<p align="center">
 <img width="2184" height="1390" alt="Reliant Screenshot" src="https://github.com/user-attachments/assets/cecdb7c2-1ee6-4c03-bc45-6b1feb44a8dd" />
</p>

Reliant is a desktop AI coding assistant that goes beyond chat. Interact with your codebase through natural conversation, then extend that interaction with programmable workflows that automate complex, multi-step development tasks.

## Key Features

- **Chat with your codebase** — AI understands your project structure, reads files, and maintains context across conversations
- **Programmable workflows** — Define repeatable multi-step processes in YAML, version them with your code
- **Multi-agent patterns** — Spawn parallel agents, implement debate patterns, or chain specialized agents together
- **Git worktrees** — Isolate work in separate branches without switching contexts
- **Any LLM provider** — Anthropic, OpenAI, Google Gemini, GitHub Copilot, AWS Bedrock, Azure, Groq, OpenRouter, and more
- **MCP support** — Extend capabilities with Model Context Protocol servers
- **Config as code** — Workflows and presets live in your repo as `.reliant/` files

## Installation

### Homebrew (macOS/Linux)

```bash
brew install --cask reliant-labs/reliant/reliant
```

### Direct Download

| Platform | Download |
|----------|----------|
| macOS (Apple Silicon) | [Reliant-latest-mac-arm64.dmg](https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg) |
| macOS (Intel) | [Reliant-latest-mac-x64.dmg](https://downloads.reliantlabs.io/Reliant-latest-mac-x64.dmg) |
| Windows (x64) | [Reliant-latest-win-x64.exe](https://downloads.reliantlabs.io/Reliant-latest-win-x64.exe) |
| Windows (ARM) | [Reliant-latest-win-arm64.exe](https://downloads.reliantlabs.io/Reliant-latest-win-arm64.exe) |
| Linux (x86_64) | [Reliant-latest-linux-x86_64.AppImage](https://downloads.reliantlabs.io/Reliant-latest-linux-x86_64.AppImage) |
| Linux (ARM64) | [Reliant-latest-linux-arm64.AppImage](https://downloads.reliantlabs.io/Reliant-latest-linux-arm64.AppImage) |

## Quick Start

1. **Launch Reliant** and open a project folder
2. **Configure an API key** in Settings → AI
   - Have a Claude Code subscription? Run `claude setup-token` and use that key with Anthropic
3. **Start chatting** — ask questions, request changes, explore your codebase

## Documentation

Full documentation at [docs.reliantlabs.io](https://docs.reliantlabs.io)

- [Quick Start Guide](https://docs.reliantlabs.io/using-reliant/quick-start/)
- [Workflows Overview](https://docs.reliantlabs.io/workflows/overview/)
- [Multi-Agent Patterns](https://docs.reliantlabs.io/workflows/patterns/)
- [Custom Workflows](https://docs.reliantlabs.io/workflows/custom-workflows/)
- [Presets Reference](https://docs.reliantlabs.io/workflows/presets/)
- [MCP Servers](https://docs.reliantlabs.io/settings/mcp-servers/)

## How It Works

When you send a message, Reliant runs a workflow. The default `agent` workflow:

```
Your Message → Call LLM → Execute Tools → Loop while working → Response
```

The agent reads your codebase, plans changes, implements them, and verifies they work—all within this loop. A single message like "add input validation to the signup form" might trigger dozens of iterations as the agent explores, plans, and implements.

### Execution Modes

| Mode | Description |
|------|-------------|
| **Auto** | Agent works autonomously, auto-approving tool calls |
| **Manual** | You approve each tool execution |
| **Plan** | Read-only exploration, outputs a task plan |

### Workflows as Code

Workflows live in `.reliant/workflows/*.yaml` and are versioned with your code:

```yaml
name: review-pr
description: Review a pull request

nodes:
  - id: analyze
    workflow: builtin://agent
    inputs:
      prompt: "Review the changes in this PR for bugs, security issues, and style"
      mode: plan

  - id: summarize
    workflow: builtin://agent
    inputs:
      prompt: "Summarize the review findings"
```

## Supported Providers

| Provider | Models |
|----------|--------|
| Anthropic | Claude 4 Opus, Claude 4 Sonnet, Claude 3.5 Sonnet, Claude 3.5 Haiku |
| OpenAI | GPT-4.1, GPT-4o, O1, O3, O4-mini |
| Google | Gemini 2.5 Pro, Gemini 2.5 Flash, Gemini 2.0 Flash |
| GitHub Copilot | Access to Claude, GPT, Gemini, and O-series models |
| AWS Bedrock | Claude models via AWS |
| Azure OpenAI | GPT models via Azure |
| Groq | Llama, Mixtral |
| OpenRouter | 100+ models from various providers |

## Community

- [Slack](https://join.slack.com/t/reliant-pn51441/shared_invite/zt-3g6mhfnhx-~CWMzNRZUylWHevlJXO89A) — Get help and share workflows
- [GitHub Issues](https://github.com/reliant-labs/reliant/issues) — Report bugs and request features
- [Documentation](https://docs.reliantlabs.io) — Guides and reference

## Development

For Postgres-based local dev, run with `DATABASE_DRIVER=postgres` (for example `npm run dev:pg`).
Reliant will use a shared local Postgres server while auto-provisioning a **per-worktree database** during startup, mirroring SQLite worktree isolation.

To inspect the currently resolved DB context quickly (computed worktree DB name + active `DATABASE_URL`), run:

```bash
npm run db:info
```

See [docs/RELEASE_SETUP.md](docs/RELEASE_SETUP.md) for release process documentation.

For WSL/Linux developer setup, see [docs/WSL_DEVELOPMENT_SETUP.md](docs/WSL_DEVELOPMENT_SETUP.md).
On native Windows, use `npm run dev` for day-to-day development.
Use `make` targets from Linux/macOS/WSL where Unix tooling (bash/sqlite3/goose) is available.

## Star History

<picture>
  <source
    media="(prefers-color-scheme: dark)"
    srcset="
      https://api.star-history.com/svg?repos=reliant-labs/reliant&type=Date&theme=dark
    "
  />
  <source
    media="(prefers-color-scheme: light)"
    srcset="
      https://api.star-history.com/svg?repos=reliant-labs/reliant&type=Date
    "
  />
  <img
    alt="Star History Chart"
    src="https://api.star-history.com/svg?repos=reliant-labs/reliant&type=Date"
  />
</picture>

## License

Copyright (c) 2025 Reliant Labs. All rights reserved.