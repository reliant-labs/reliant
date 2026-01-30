<p align="center">
  <a href="https://reliantlabs.io">Website</a>
  ·
  <a href="https://docs.reliantlabs.io/docs/">Docs</a>
  ·
  <a href="https://join.slack.com/t/reliant-pn51441/shared_invite/zt-3g6mhfnhx-~CWMzNRZUylWHevlJXO89A">Slack</a>
</p>

# Reliant

**Take control of your AI workflows.**

## About

This is an **issues-only** repo for **[Reliant](https://reliantlabs.io)** — an AI coding assistant built around **programmable workflows**.

Most AI coding tools are built around a single chat thread. That works for questions and quick edits, but longer tasks turn into manual babysitting: you prompt, copy outputs around, run commands yourself, and steer when things go off track. Reliant keeps the chat, but adds **workflows**—versioned, multi-step processes that mix model calls with real execution (commands, checks, branching, retries, approvals) and let you **compose multiple agents** into a single loop (plan → implement → test → review) instead of relying on one agent to do everything.

> Use issues here for bug reports, feature requests, workflow ideas, and integrations.

### Why Reliant?

- **Workflows as code**  
  Define repeatable AI processes in YAML. Commit them with your repo, review changes in PRs, and share them across your team.

- **Composable agents + roles**  
  Build workflows out of distinct roles (planner, implementer, tester, reviewer, debugger, etc.) and combine them into reliable loops.

- **Thread control (share / fork / fresh)**  
  Steps can share context, fork it when you want isolation, or start fresh when context becomes baggage—so workflows stay legible instead of turning into one giant soup.

- **Conditional routing + guardrails**  
  Route based on outcomes (e.g., back to implementation if tests fail, escalate to a reviewer/refactor step after repeated failures). Add optional approval/filter steps where you want tighter control.

- **Parallel execution with Git worktrees**  
  Run multiple approaches side-by-side in isolated worktrees/branches to avoid conflicts and compare results.

- **Pause / resume / branch**  
  Inspect any step, inject context, branch into a new thread/workspace, and continue from the failure point instead of restarting.

- **Config lives in your repo**  
  Workflows, presets, and settings live in `.reliant/` so they’re inspectable, versioned, and portable.

- **Extensible tool integration (MCP)**  
  MCP (Model Context Protocol) support lets you connect agents to your APIs, databases, and custom tools.

- **Provider-agnostic**  
  Bring your own keys (Anthropic, OpenAI, Gemini, Groq, OpenRouter, local models). Pay providers directly.

## Installation

### Homebrew (macOS/Linux)

```bash
brew install --cask reliant-labs/reliant/reliant
```

### Direct Download

| Platform    | Architecture             | Download                                                                                   |
| ----------- | ------------------------ | ------------------------------------------------------------------------------------------ |
| **macOS**   | Apple Silicon (M1/M2/M3) | [Download DMG](https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg)              |
| **macOS**   | Intel                    | [Download DMG](https://downloads.reliantlabs.io/Reliant-latest-mac-x64.dmg)                |
| **Windows** | x64                      | [Download EXE](https://downloads.reliantlabs.io/Reliant-latest-win-x64.exe)                |
| **Windows** | ARM64                    | [Download EXE](https://downloads.reliantlabs.io/Reliant-latest-win-arm64.exe)              |
| **Linux**   | x86_64                   | [Download AppImage](https://downloads.reliantlabs.io/Reliant-latest-linux-x86_64.AppImage) |
| **Linux**   | ARM64                    | [Download AppImage](https://downloads.reliantlabs.io/Reliant-latest-linux-arm64.AppImage)  |

### Arch Linux (AUR)

```bash
yay -S reliant-labs-bin
```

See our [installation docs](https://docs.reliantlabs.io/docs/getting-started/installation/) for detailed instructions.

## Quick Start

1. **Install** using one of the methods above
2. **Launch** and open a project folder
3. **Configure** your API key in Settings → AI
4. **Chat** — ask questions, request implementations, or run workflows

Free for 30 days. No credit card required.

## Resources

- [Documentation](https://docs.reliantlabs.io/docs/) — Full guides and reference
- [Quick Start](https://docs.reliantlabs.io/docs/getting-started/quick-start/) — Get running in minutes
- [Workflows Overview](https://docs.reliantlabs.io/docs/workflows/overview/) — How workflows work
- [Custom Workflows](https://docs.reliantlabs.io/docs/workflows/custom-workflows/) — Build your own

## Issues and Feature Requests

Please search through existing issues before filing a new one. We appreciate detailed bug reports and thoughtful feature requests.

## Community

- Join our [Slack](https://join.slack.com/t/reliant-pn51441/shared_invite/zt-3g6mhfnhx-~CWMzNRZUylWHevlJXO89A) to connect with other users
- Follow the [GitHub Community Guidelines](https://docs.github.com/en/github/site-policy/github-community-guidelines)
