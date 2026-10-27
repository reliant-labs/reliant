# Reliant

**Production apps, built and run by AI agents.**

<p align="center">
<img width="1200" height="630" alt="Reliant: production apps, built and run by AI agents" src="https://reliantlabs.io/og-image.png" />
</p>

<p align="center">
  <a href="https://reliantlabs.io">Website</a>
  ·
  <a href="https://docs.reliantlabs.io">Docs</a>
  ·
  <a href="https://github.com/reliant-labs/forge">Forge</a>
  ·
  <a href="https://join.slack.com/t/reliant-pn51441/shared_invite/zt-4c7wet6o2-W_Tksw80LaRGxZqUz9GQdA">Slack</a>
</p>

Reliant is an agent harness for the whole life of an app: agents that build it,
ship it, watch it and fix it when it breaks. Chat with an agent in the desktop or
web app, or hand the loop to workflows that run on their own: review every pull
request, turn a labelled issue into a tested PR, answer an alert at 2am with the
cause and a proposed fix.

Reliant works on any codebase. It works best on an app built with
[Forge](https://github.com/reliant-labs/forge), our open-source framework: every
Forge app has the same shape, tracing on every request, and linters that catch
whole classes of bugs, so agents know your app from the start and can see what it
is doing in production.

## Key Features

- **Agents that keep working** — workflows run on your laptop, a server you own, or a Reliant cloud machine, and keep going after you close the lid
- **Autopilot** — built-in workflows for on-call, issue → pull request, PR review, security review and review responses, set off by alerts, pull requests, labels and comments
- **Programmable workflows** — define repeatable multi-step processes in YAML under `.reliant/workflows/`, versioned with your code, and trigger them on a schedule, a webhook, or a GitHub or Slack event
- **Multi-agent patterns** — spawn parallel agents, race attempts in separate git worktrees, chain specialized roles
- **Build and ship with Forge** — `reliant forge` runs the Forge built into Reliant, already signed in; deploys land on Reliant hosting with dashboards and alerts
- **Any model, no token markup** — bring your own key (Anthropic, OpenAI, Google, Bedrock, Azure, Groq, OpenRouter and more) or your ChatGPT plan; your provider bills you at their prices
- **MCP support** — extend agents with Model Context Protocol servers

## Installation

### Homebrew (macOS/Linux)

```bash
brew install --cask reliant-labs/reliant/reliant
```

### Direct Download

| Platform              | Download                                                                                                      |
| --------------------- | ------------------------------------------------------------------------------------------------------------- |
| macOS (Apple Silicon) | [Reliant-latest-mac-arm64.dmg](https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg)                 |
| macOS (Intel)         | [Reliant-latest-mac-x64.dmg](https://downloads.reliantlabs.io/Reliant-latest-mac-x64.dmg)                     |
| Windows (x64)         | [Reliant-latest-win-x64.exe](https://downloads.reliantlabs.io/Reliant-latest-win-x64.exe)                     |
| Windows (ARM)         | [Reliant-latest-win-arm64.exe](https://downloads.reliantlabs.io/Reliant-latest-win-arm64.exe)                 |
| Linux (x86_64)        | [Reliant-latest-linux-x86_64.AppImage](https://downloads.reliantlabs.io/Reliant-latest-linux-x86_64.AppImage) |
| Linux (ARM64)         | [Reliant-latest-linux-arm64.AppImage](https://downloads.reliantlabs.io/Reliant-latest-linux-arm64.AppImage)   |

## Quick Start

1. **Sign in** at [app.reliantlabs.io](https://app.reliantlabs.io) or in the desktop app
2. **Pick a machine** — a Reliant cloud machine, or your own computer
3. **Pick a model** — your own API key, or Reliant's managed provider
4. **Start chatting**, or run a built-in workflow like Get It Right

See the [Quick Start](https://docs.reliantlabs.io/getting-started/quick-start) for the full walkthrough.

## Documentation

Full documentation at [docs.reliantlabs.io](https://docs.reliantlabs.io)

- [Quick Start](https://docs.reliantlabs.io/getting-started/quick-start)
- [Machines: your computer or the cloud](https://docs.reliantlabs.io/machines/overview)
- [Autopilot](https://docs.reliantlabs.io/guides/autopilot)
- [Workflows Overview](https://docs.reliantlabs.io/workflows/overview)
- [Multi-Agent Patterns](https://docs.reliantlabs.io/workflows/patterns)
- [Custom Workflows](https://docs.reliantlabs.io/workflows/custom-workflows)
- [Presets Reference](https://docs.reliantlabs.io/workflows/presets)
- [Forge: build, deploy and host apps](https://docs.reliantlabs.io/forge/overview)
- [MCP Servers](https://docs.reliantlabs.io/settings/mcp-servers)

## How It Works

When you send a message, Reliant runs a workflow. The default `agent` workflow:

```
Your Message → Call LLM → Run LLM Tool Calls → Loop while working → Response
```

The agent reads your codebase, plans changes, implements them, and verifies they work—all within this loop. A single message like "add input validation to the signup form" might trigger dozens of iterations as the agent explores, plans, and implements.

### Execution Modes

| Mode       | Description                                         |
| ---------- | --------------------------------------------------- |
| **Auto**   | Agent works autonomously, auto-approving tool calls |
| **Manual** | You approve each tool execution                     |
| **Plan**   | Read-only exploration, outputs a task plan          |

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

We support the latest models from:

| Provider       |
| -------------- |
| Anthropic      |
| OpenAI         |
| Google         |
| GitHub Copilot |
| AWS Bedrock    |
| Azure OpenAI   |
| Groq           |
| OpenRouter     |
| Bring Your Own |

You can also configure Reliant to point to your own ChatGPT compatible provider.

## Community

- [Slack](https://join.slack.com/t/reliant-pn51441/shared_invite/zt-4c7wet6o2-W_Tksw80LaRGxZqUz9GQdA) — Get help and share workflows
- [GitHub Issues](https://github.com/reliant-labs/reliant/issues) — Report bugs and request features
- [Documentation](https://docs.reliantlabs.io) — Guides and reference

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and the [`contributing/`](contributing/) directory for detailed guides.

## License

Copyright (c) 2026 Reliant Labs, Inc. Licensed under the [Business Source License 1.1](LICENSE).