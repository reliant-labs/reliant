---
name: connect
description: Connect an agent or chat app to forge over MCP — the local server (npx -y @reliantlabs/forge mcp) for the repo tools, and the hosted Reliant Forge connector (https://api.reliantapi.com/forge/mcp, OAuth sign-in, no install) for the account tools — with the exact steps for Claude (claude.ai, Desktop, Code), ChatGPT, VS Code, Cursor, Goose and Reliant, and which tool lives where.
emit: both
applies: any
---

# Connect forge to your agent

forge speaks the Model Context Protocol from two places. Most people want both;
each takes about two minutes and neither needs the other.

| | **Local server** | **Hosted connector: "Reliant Forge"** |
|---|---|---|
| What it is | `forge mcp` on stdio, started by your agent | a remote MCP server Reliant runs |
| Address | `npx -y @reliantlabs/forge mcp` (no install) | `https://api.reliantapi.com/forge/mcp` |
| Sign-in | none (monitoring verbs it mentions use `forge login`) | OAuth: your Reliant account, in the browser |
| Needs the repo | yes: it acts on the directory it starts in | no: phones and chat apps work |
| Tools | the **repo** tools: `benefits`, `lint`, `generate`, `shapes`, `audit`, `capabilities`, `annotations`, `libraries`, `deploy_claimable`, `deploy_status`, `report_bug`, `explain_error`, `skill_list`, `skill_search`, `skill_load` | the **account** tools: `obs_status`, `obs_issues`, `obs_issue`, `obs_logs`, `obs_traces`, `obs_check`, `obs_changes`, `releases_list`, `release_show`, `envs_list`, `obs_init`*, `release_record`*, plus `benefits`, `explain_error`, `skill_list`, `skill_search`, `skill_load` |

\* changes something: your client asks before calling it.

A tool on both servers is the same tool: the same name and the same inputs,
except that a local tool also takes `dir` (the repo to act on). Hosted results
are the CLI's `--json` documents plus `console_url`, the page at
https://forge.reliantlabs.io that shows the same thing.

Rules of thumb: working in a repo, add the local server. Asking "what is broken
in prod", "did that release regress", "record this deploy" from anywhere,
including a phone, add the hosted connector.

## The hosted connector: what signing in grants

The first time a client connects, it sends you to Reliant to sign in. Reliant
then asks which **organization** the connector may read, optionally narrowed
to some **projects**, and which tools it may use. That choice is a connector
grant: it is the connector's whole authority (signing in only says who you
are), every call is recorded, and you narrow or revoke it at any time in
Reliant under Settings → Access tokens (Reliant Forge connectors). Revoking stops the next call.

It works only for a signed-in person: monitoring has no anonymous path. There is
nothing to install, and nothing on your machine holds a token.

## Per client

### Claude (claude.ai and Claude Desktop)

- **Hosted**: Settings → Access tokens (Reliant Forge connectors) → *Add custom connector*. Name it
  `Reliant Forge`, URL `https://api.reliantapi.com/forge/mcp`, then *Connect*
  and sign in. In a chat, enable it from the tools menu.
- **Local** (Desktop only): add to `claude_desktop_config.json`:
  ```json
  {"mcpServers": {"forge": {"command": "npx", "args": ["-y", "@reliantlabs/forge", "mcp"]}}}
  ```

### Claude Code

```bash
claude mcp add forge -- npx -y @reliantlabs/forge mcp                                  # local
claude mcp add --transport http reliant-forge https://api.reliantapi.com/forge/mcp     # hosted
```

Then run `/mcp` and pick `reliant-forge` → *Authenticate* to sign in.

### ChatGPT

Settings → Access tokens (Reliant Forge connectors) → *Create* (developer mode), URL
`https://api.reliantapi.com/forge/mcp`, authentication OAuth. Sign in when it
asks. ChatGPT runs no local servers.

### VS Code (Copilot agent mode)

`.vscode/mcp.json`:

```json
{
  "servers": {
    "forge": {"type": "stdio", "command": "npx", "args": ["-y", "@reliantlabs/forge", "mcp"]},
    "reliant-forge": {"type": "http", "url": "https://api.reliantapi.com/forge/mcp"}
  }
}
```

Start `reliant-forge` from the file's *Start* lens; VS Code opens the sign-in.

### Cursor

`.cursor/mcp.json` (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "forge": {"command": "npx", "args": ["-y", "@reliantlabs/forge", "mcp"]},
    "reliant-forge": {"url": "https://api.reliantapi.com/forge/mcp"}
  }
}
```

Cursor shows *Needs login* on `reliant-forge`: click it to sign in.

### Goose

`goose configure` → *Add Extension*:

- **Local**: *Command-line Extension*, command `npx -y @reliantlabs/forge mcp`;
- **Hosted**: *Remote Extension (Streamable HTTP)*, URL
  `https://api.reliantapi.com/forge/mcp`; Goose opens the sign-in on first use.

### Codex

```bash
codex mcp add forge -- npx -y @reliantlabs/forge mcp
```

### Reliant

forge is built in: agents' shells already have `reliant forge <verb>`, signed
in, and they load forge's skills without a server. Add the local server only if
you want forge's tools listed as MCP tools as well: Settings → MCP Servers →
Add Custom, transport stdio, command `reliant`, args `forge mcp`. The hosted
connector is for clients outside Reliant.

## Check it works

- Local: call `benefits`. It answers with what forge does for the repo.
- Hosted: call `envs_list` with your project name (the `<project>` in
  `https://forge.reliantlabs.io/forge/p/<project>`), then `obs_status` for one
  of its envs. No project yet? `obs_init` registers envs and returns the
  `OTEL_*` variables that send any app's telemetry (`observability/monitoring-only`).

A 403 that names a consent URL means you signed in but have not chosen what the
connector may read: open the URL, choose, retry. A refusal naming a project
means the grant does not cover it: widen it in Settings → Access tokens (Reliant Forge connectors).

## Where to go next

- In a repo with no `forge.yaml`: `skill_load name=adopt`.
- Monitoring an existing app: `observability/monitoring-only`.
- Recording deploys from your own CI: `deploy/record`.
