# Reliant

**The whole stack for building with AI agents, from first prompt to production.**

<p align="center">
<img width="1200" height="630" alt="Reliant: the whole stack for building with AI agents" src="https://reliantlabs.io/og-image.png" />
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

Reliant is the agent harness for automation: a desktop and web app where you chat with agents, workflows that run on their own when something happens, and always-on machines that keep working after you close the lid. Paired with [Forge](https://github.com/reliant-labs/forge), our open-source Go + React framework, it is the whole stack in one place: like Cursor + Lovable + Vercel + Google Cloud + Datadog, built for your agents.

Reliant works on any codebase. Forge is what turns that from "an agent edits files" into "an agent builds, ships, watches and fixes a production app" (see [Better with Forge](#better-with-forge)).

## Workflows do anything

A workflow is a YAML file in `.reliant/workflows/`, versioned with your code. It starts from a trigger (a pull request, a label, a comment, a schedule, a webhook, an alert, a GitHub, Slack, Gmail or Twilio event, or another run finishing) and runs a graph of steps: agents, shell commands, approvals, and actions on outside services (`http/request@1` reaches any API). Edges between steps carry conditions, so you decide where a run goes next. That makes a workflow an LLM-integrated CI/CD pipeline if you want one.

It is not only for dev work. One of ours places stock trades every market day: a scheduled workflow, an agent that decides, and an app we built with Forge on the other end.

This one runs when a pull request opens, looks at its size, sends big ones to a deep review, and tells Slack:

```yaml
name: pr-triage
description: Look at every new pull request; send big ones to a deep review and tell Slack
inputs:
  repos: {type: github.repository, multi: true, required: true, min_items: 1}
  repo: {type: string, default: ""}
  pr_number: {type: integer, default: 0}

triggers:
  - name: pr-opened
    integration:
      integration: github
      events: [pull_request.opened]
      match: {repository: {input: repos}}
    inputs:
      repo: "{{ trigger.payload.attributes.repository }}"
      pr_number: "{{ trigger.payload.data.pull_request.number }}"

entry: [pr]
nodes:
  - id: pr
    type: action
    uses: github/pr.get@1
    with:
      owner: "{{ inputs.repo.split('/')[0] }}"
      repo: "{{ inputs.repo.split('/')[1] }}"
      pull_number: "{{ inputs.pr_number }}"

  - id: size
    type: compute
    outputs:
      big: "{{ nodes.pr.data.changed_files > 20 }}"

  - id: deep_review
    type: workflow
    ref: builtin://agent
    thread:
      mode: new
      inject:
        role: user
        content: "Review {{ inputs.repo }}#{{ inputs.pr_number }} in depth. It touches {{ nodes.pr.data.changed_files }} files."

  - id: tell_slack
    type: action
    uses: slack/message.post@1
    with:
      channel: C0123ABCD
      text: "Big PR: {{ inputs.repo }}#{{ inputs.pr_number }}, deep review done"

edges:
  - from: pr
    default: size
  - from: size
    cases:
      - to: deep_review
        condition: nodes.size.big == true
        label: big
  - from: deep_review
    default: tell_slack
```

Check a file with `reliant workflow validate path/to/workflow.yaml`. The [workflows overview](https://docs.reliantlabs.io/workflows/overview), [custom workflows](https://docs.reliantlabs.io/workflows/custom-workflows) and [automations](https://docs.reliantlabs.io/automations/overview) cover the rest. Built-ins such as Get It Right and Best of Three (parallel attempts in separate git worktrees, then a judge) ship with the app, and agents can spawn sub-agents of their own.

## Autopilot: a team out of the box

Autopilot is five built-in workflows for GitHub. You turn each on for the repositories you pick; each leaves its work as ordinary reviews, issues, comments and pull requests that you can read and overrule. None of them merges, approves, or pushes to your default branch.

| Role | Workflow | What sets it off |
| --- | --- | --- |
| Code reviewer | `autopilot-pr-review` | A pull request is opened or marked ready; the re-review label; `/autopilot re-review` |
| Security reviewer | `autopilot-security-review` | A pull request is opened, reopened, marked ready or gets new commits; `/autopilot security-review` |
| Engineer | `autopilot-issue-worker` | `agent:ready` added to an issue, or an `@reliant` mention. Writes a failing test and the fix, opens a PR |
| Author's assistant | `autopilot-review-responder` | A review or comment on an `agent:authored` pull request. Pushes the fix to that PR's branch and replies |
| On-call | `autopilot-oncall` | Your alerts, and new, regressed or spiking errors. Files an issue with severity, evidence, cause and a proposed fix |

On-call files issues for a human to promote with `agent:ready`, which hands them to the engineer, which opens a PR that the reviewer reads: a loop with a person at the merge. Details in the [Autopilot guide](https://docs.reliantlabs.io/guides/autopilot).

## Always-on machines

An agent runs where you tell it to: your desktop, a server you own, or a Reliant cloud machine (from $20/mo; it wakes on demand and suspends when idle, and your checkout and tools are waiting when it wakes). Runs continue with your laptop closed, and workflows that only talk to GitHub, Slack, Gmail or the web need no machine at all. See [machines](https://docs.reliantlabs.io/machines/overview).

## Better with Forge

[Forge](https://github.com/reliant-labs/forge) is an open-source, opinionated framework for Go + React apps (Next.js, Vite or Expo frontends) that ships what you would otherwise stitch together: SQL migrations with safety checks, multiple environments and secrets from the first commit, logs, traces, metrics and error tracking, dashboards and default alerts, releases promoted by digest, and deploys to your own Kubernetes or to Reliant hosting. Scaffolding and custom linters are guardrails against AI slop.

Reliant and Forge are built for each other:

- **Reliant already knows Forge.** Every Forge skill is available to the agent, so it uses the framework's conventions from the first prompt.
- **`reliant forge` is built in**, and already signed in. Agents' shells have it too.
- **Cloud machines are authenticated to Forge and Reliant hosting**, so an agent can deploy what it built.
- **On-call reacts to Forge's alerts and errors** and opens the fix, closing the loop from production back to the code.

Forge is best for new apps, but you can bring an existing app as-is and migrate over time (the built-in Move to Forge workflow does the porting). See the [Forge docs](https://docs.reliantlabs.io/forge/overview).

## Any model, no token markup

Bring your own key (Anthropic, OpenAI, Google, GitHub Copilot, AWS Bedrock, Azure OpenAI, Groq, OpenRouter, or any OpenAI-compatible endpoint) or sign in with your ChatGPT plan. Your provider bills you at their prices. Extend agents with [MCP servers](https://docs.reliantlabs.io/settings/mcp-servers).

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
2. **Pick a machine**: a Reliant cloud machine, or your own computer
3. **Pick a model**: your own API key or ChatGPT plan, or Reliant's managed provider
4. **Start chatting**, or turn on an [Autopilot](https://docs.reliantlabs.io/guides/autopilot) workflow

The [Quick Start](https://docs.reliantlabs.io/getting-started/quick-start) has the full walkthrough.

## Documentation

[docs.reliantlabs.io](https://docs.reliantlabs.io): [Quick Start](https://docs.reliantlabs.io/getting-started/quick-start) · [Machines](https://docs.reliantlabs.io/machines/overview) · [Autopilot](https://docs.reliantlabs.io/guides/autopilot) · [Workflows](https://docs.reliantlabs.io/workflows/overview) · [Automations](https://docs.reliantlabs.io/automations/overview) · [Integrations](https://docs.reliantlabs.io/integrations/overview) · [Forge](https://docs.reliantlabs.io/forge/overview)

## Community

- [Slack](https://join.slack.com/t/reliant-pn51441/shared_invite/zt-4c7wet6o2-W_Tksw80LaRGxZqUz9GQdA): get help and share workflows
- [GitHub Issues](https://github.com/reliant-labs/reliant/issues): report bugs and request features

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and the [`contributing/`](contributing/) directory for detailed guides.

## License

Copyright (c) 2026 Reliant Labs, Inc. Licensed under the [Business Source License 1.1](LICENSE).
