# Forge as an MCP server

`forge mcp` serves forge over the [Model Context Protocol](https://modelcontextprotocol.io)
on stdio, so any MCP-capable harness (Claude Code, Codex, Cursor, ...) can use
forge in **any repo**, with no memory file explaining how. The server introduces
itself as Reliant Forge (not Atlassian Forge, not Foundry's `forge`), then says
what forge does for the repo the agent is in, and sends the model to the
`benefits` tool and the `adopt` skill.

Each tool runs the same command the CLI does, in-process. There is no forge
binary to shell out to and nothing to keep in sync with the CLI.

## In any repo

Most of what forge offers works on the app as it is: forge need not build or
deploy it. In an existing app, in any language, the server leads with:

- **Monitoring.** After `forge login`, `forge obs init <env>…` (one or more
  envs) declares each env in its env document (`deploy/envs/<env>.yaml`),
  registers it, mints an ingest token per env and prints its
  `OTEL_*` variables. Set them where
  the app runs and any OpenTelemetry SDK sends its traces, logs and metrics to
  dashboards, issues and alerts at
  `https://forge.reliantlabs.io/forge/p/<project>/observability` (sign-in
  required).
- **A deploy history from the CI the repo already has.** One
  `forge release record` step after the deploy job records each release, so
  `forge obs changes` shows a deploy marker and `forge obs check` gives each
  release a verdict.
- **Proof before forge deploys anything.** `forge env verify` compares what
  forge would ship against what runs, read-only, for a workload bound to
  `forge.OnExternal` (the repo's own Helm, Fly, Cloud Run or script), and
  `forge project new --in-place --kind app` declares an app's Dockerfile so
  forge can deploy it.
- **Linters that run standalone.** `lint` runs migration safety over
  `db/migrations/*.up.sql` (a destructive `DROP COLUMN`, a `NOT NULL` column
  with no default, `CREATE INDEX CONCURRENTLY` beside other statements), or
  over the directory of the goose, atlas, flyway, dbmate or golang-migrate
  setup `database.migrations` names, and in a Go module golangci-lint, the
  tracing lane and timezone-safe date bucketing. Lanes that need a forge
  project skip and say why.
- **Playbooks.** `skill_load` serves the skills built into the forge you run:
  debugging, testing tiers, migrations, observability, deploys.
- **Runtime libraries.** `forge/pkg` for Go (`svcerr`, `observe`, `pgtest`,
  `testkit`, ...) and `@reliantlabs/forge-web-runtime`, whose `/node` entry
  (`initNodeTelemetry`) ships a Node process's logs, errors and spans over
  OTLP. `libraries` prints any package's API.
- **A deploy with no account.** `deploy_claimable` puts an existing Vite or
  Next.js app live and returns a URL plus a claim link (below).

Monitoring, recording and verifying are CLI commands the agent runs in the
user's terminal; the server's job is to say they exist and what they cost.

`benefits` lists exactly what applies to the directory, each with its first
step and the agent's time to first value, worded so the agent can quote it to
its user (`forge project benefits` is the CLI). Then `skill_load` with `name:
adopt`: the survey, the estimate rubric and every entry point.

Once the repo adopts forge's codegen, the forge-project tools apply too:
`skill_load` with `name: forge` is their entry point, and `capabilities`,
`annotations`, `audit`, `shapes` and `generate` work on the project.

## Tools

Every tool takes an optional `dir`: the repo to act on. It defaults to the
directory the server was started in (or `-C`). Those marked _codegen_
answer questions about the code forge generates, so they matter once the repo
adopts it; the rest are for any repo.

| Tool                                                             | CLI equivalent                                                | Writes files                                                                                                                     |
| ---------------------------------------------------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `benefits` (`all`, `category`)                                   | `forge project benefits --json`                               | no                                                                                                                               |
| `capabilities`                                                   | `forge project capabilities`                                  | no                                                                                                                               |
| `schema` (`name`)                                                | `forge project schema [env]`                                  | no                                                                                                                               |
| `annotations` (`kind`), _codegen_                                | `forge project annotations`                                   | no                                                                                                                               |
| `libraries` (`packages`)                                         | `forge project libraries [pkg...]`                            | no                                                                                                                               |
| `audit`, _codegen_                                               | `forge project audit --json`                                  | no                                                                                                                               |
| `shapes` (`grep`, `kind`, `service`, `unimplemented`), _codegen_ | `forge project shapes`                                        | no                                                                                                                               |
| `skill_list` (`group`, `all`, `include_migrations`)              | `forge skill list`                                            | no                                                                                                                               |
| `skill_search` (`query`)                                         | `forge skill search`                                          | no                                                                                                                               |
| `skill_load` (`name`)                                            | `forge skill load`                                            | no                                                                                                                               |
| `lint` (`scope`)                                                 | `forge lint --quiet [--scope PATH]...`                        | caches only; never `--fix`                                                                                                       |
| `generate` (`dry_run`, `check`), _codegen_                       | `forge generate [--dry-run \| --check]`                       | yes: generated files                                                                                                             |
| `report_bug` (`severity`, `title`, `summary`, ...)               | `forge report-bug`                                            | sends one report ([docs/report-bug.md](report-bug.md))                                                                           |
| `deploy_claimable` (`env`, `name`, `no_wait`)                    | `forge env deploy <env> --claimable --json`                   | adopts a Vite/Next.js app, or an app with its own Dockerfile, with no `forge.yaml` (adds `forge.yaml`, `deploy/kcl/`); publishes |
| `deploy_status` (`env`)                                          | `forge env status <env> --claimable --json`                   | no                                                                                                                               |
| `explain_error` (`exit_code`, `error_reason`, `error`, `env`)    | none: maps forge's exit codes and reasons to the next command | no                                                                                                                               |

The read-only tools are annotated `readOnlyHint`; `lint`, `generate`,
`report_bug` and `deploy_claimable` are not, so a harness can ask before running
them. `report_bug`, `deploy_claimable` and `deploy_status` are also
`openWorldHint`: they are the tools that talk to anything off the machine. `lint` and `audit` report findings in
the result rather than as a tool failure. Output past about 220 KB is cut in the
middle, keeping the end (lint's verdict is its last line).

Calls are serialized: a command's working directory, project override and stdio
are process-global. `generate` and `lint` queue for a host heavy slot like they
do from a shell.

A project pinned to another forge version is not handed over to that forge (the
exec would replace the server). `generate` refuses with the usual skew message
and the reinstall hint instead.

## Deploying with no account

`deploy_claimable` is the whole of "put this live" for an agent that has no
Reliant account and may never have heard of forge:

1. In a directory with no `forge.yaml` that holds a Vite or Next.js app at its
   root, it first runs `forge project new --in-place --kind site --name <dir>`;
   in one that holds an app in any other language with its own Dockerfile (or a
   compose service it builds), `forge project new --in-place --kind app
--claimable --name <dir>`, which sizes prod to the claimable plan. Both add
   `forge.yaml` and `deploy/kcl/` and never change the app's own files. A Go
   module with no Dockerfile, or a directory with no app, is refused with the
   command to run instead. An unclaimed service has no outbound internet (DNS
   and its own Postgres only) and takes no secret values.
   The CLI does the same: `forge env deploy <env> --claimable` in such a
   directory adopts the app first (one line says what it added), through the
   same code, so `npx -y @reliantlabs/forge env deploy prod --claimable` works
   in a fresh `npm create vite` app. Without `--claimable`, `env deploy` never
   adopts and needs an existing project.
2. It runs `forge env deploy <env> --claimable --json` (env `prod` by default):
   the availability check, the proof of work, `StartClaimableDeploy`, then the
   ordinary hosted publish as the anonymous account.
3. It returns the URL, the claim link, `expires_at` / `purge_after`, the
   plan's limits and `next_steps`: the text the agent relays to its user (give
   them the claim link, it expires in 72 h, treat it like a password).
   `rollout_verified` is true only when the call waited for the rollout and it
   came up healthy; only then does the result call the app live. With
   `no_wait` the deploy was only accepted, and `next_steps` says to confirm the
   URL answers first. `state` is the claim's lifetime, not the rollout's:
   `deploy_status` never verifies a rollout, and a `suspended`, `claimed` or
   `expired` claim is reported as offline or gone, never live.

The deploy takes minutes. Each line it prints is sent as a
`notifications/progress` message when the call carries a `progressToken`, and
cancelling the call (`notifications/cancelled`) cancels the deploy. Calls are
serialized, so other tools wait while a deploy runs.

A failure is a tool error whose result still has structure: `exit_code`,
`error`, `error_reason`, and `next_actions`, the exact commands to run, best
first. `explain_error` gives the same mapping for any forge failure an agent
saw elsewhere. When the code calls `api.openai.com` or `api.anthropic.com`
directly (or depends on their SDKs), `next_steps` explains that an unclaimed
backend has no outbound internet. Claim it, arrange backend billing, sign in,
store the provider key as a managed secret and redeploy without `--claimable`;
verify an actual server-side model request. Never ship a key in a static site.
The hosted gateway uses `forge.LLM` and `forge.llm_env()`, but unclaimed gateway
access is not guaranteed: missing credentials must disable the feature, not
crash startup. See the [recovery guide](https://docs.reliantlabs.io/forge/deploy-without-an-account#what-claiming-gives-you).

## Install

### With no install: npx

```bash
claude mcp add forge -- npx -y @reliantlabs/forge mcp
```

`@reliantlabs/forge` is a thin npm shim: on first run it downloads the forge
binary matching its version from the GitHub release, checks it against the
signed checksums the package pins, caches it and runs it. It writes nothing to
stdout itself, so the protocol is intact even on the first, downloading run.
Any harness config takes the same command:

```json
{
  "mcpServers": {
    "forge": { "command": "npx", "args": ["-y", "@reliantlabs/forge", "mcp"] }
  }
}
```

The official MCP registry listing is `npm/server.json` (name
`io.github.reliant-labs/forge`, verified by `mcpName` in `npm/package.json`) and
the Smithery config is `npm/smithery.yaml`; `scripts/release-forge.sh` keeps the
listing's version equal to the npm package's, and every release's
`.github/workflows/release-npm.yml` run publishes the listing to the registry
once npm serves the new version (`docs/releasing.md`, "Installers").

### With Docker

`Dockerfile.mcp` builds the forge binary from source into a small image whose
entrypoint is `forge mcp`. Mount the project at `/workspace`, where the server
starts:

```bash
docker build -f Dockerfile.mcp -t forge:mcp https://github.com/reliant-labs/forge.git
docker run -i --rm -v "$PWD:/workspace" forge:mcp
```

```json
{
  "mcpServers": {
    "forge": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-v", "${PWD}:/workspace", "forge:mcp"]
    }
  }
}
```

The image carries git and CA certificates only. `lint` and `generate` run the
project's own toolchain (Go, buf, ...), so for those use the npx or installed
forge; introspection, the skill catalog and `deploy_claimable` work as is.

### With forge installed

Put `forge` on your `PATH` (`go install github.com/reliant-labs/forge/cmd/forge@<version>`),
then register `forge mcp` with your harness. Start the harness in the project
directory, or pass `dir` per call.

### Claude Code

```bash
claude mcp add forge -- forge mcp
```

Everything after `--` is the server command. Add `--scope project` to write it
to a committed `.mcp.json` instead of your local config:

```json
{
  "mcpServers": {
    "forge": { "command": "forge", "args": ["mcp"] }
  }
}
```

### Codex

```bash
codex mcp add forge -- forge mcp
```

or in `~/.codex/config.toml` (or a trusted project's `.codex/config.toml`):

```toml
[mcp_servers.forge]
command = "forge"
args = ["mcp"]
```

### Cursor

`.cursor/mcp.json` in the project, or `~/.cursor/mcp.json` for every project:

```json
{
  "mcpServers": {
    "forge": {
      "command": "forge",
      "args": ["mcp"]
    }
  }
}
```

`"type": "stdio"` is implied by `command`.

## Under `reliant`

When forge is embedded, register the embedding binary's subcommand instead:
`claude mcp add forge -- reliant forge mcp`.

## The hosted connector: Reliant Forge

The same tools for what lives in your Reliant account are also served by a
remote MCP server, with nothing to install: **`https://api.reliantapi.com/forge/mcp`**
(streamable HTTP; legacy SSE at `/forge/sse`). It is for monitoring and release
questions from any client, including claude.ai, ChatGPT and phones, which run no
local server. Add it as a custom connector and sign in with OAuth; Reliant then
asks which organization (and optionally which projects and tools) the connector
may use. That grant is its authority, and Settings → Access tokens (Reliant Forge connectors) narrows or
revokes it.

| Tool                                                                                       | CLI equivalent                               | Changes anything                    |
| ------------------------------------------------------------------------------------------ | -------------------------------------------- | ----------------------------------- |
| `obs_status` (`project`, `env`, `since`, `services`, `limit`)                              | `forge obs status <env> --json`              | no                                  |
| `obs_issues` (… `status`, `since_release`, `sort`, `page_token`)                           | `forge obs issues <env> --json`              | no                                  |
| `obs_issue` (`project`, `env`, `id`, `events`)                                             | `forge obs issue <id> <env> --json`          | no                                  |
| `obs_logs` (… `query`, `severity`, `trace`, `oldest`, `page_token`)                        | `forge obs logs <env> <query> --json`        | no                                  |
| `obs_traces` (… `errors`, `slow`, `min_duration`, `route`, `page_token`)                   | `forge obs traces <env> --json`              | no                                  |
| `obs_check` (`project`, `env`, `release`, `promotion`, `window`)                           | `forge obs check <env> --json`               | no                                  |
| `obs_changes` (`project`, `env`, `since`, `limit`)                                         | `forge obs changes <env> --json`             | no                                  |
| `releases_list` (`project`, `env`, `since`, `limit`)                                       | `forge env releases list <env> --json`       | no                                  |
| `release_show` (`project`, `env`, `ref`)                                                   | `forge env releases show <env> <ref> --json` | no                                  |
| `envs_list` (`project`)                                                                    | the console's environments                   | no                                  |
| `obs_init` (`project`, `envs`, `service`, `token_name`, `ttl`)                             | `forge obs init <env…> --no-write --json`    | registers envs, mints ingest tokens |
| `release_record` (`project`, `env`, `version`, `commit`, `workloads`, `run_url`, `run_id`) | `forge release record`                       | records a release and its promotion |
| `benefits`, `skill_list`, `skill_search`, `skill_load`, `explain_error`                    | as above                                     | no                                  |

Every result is the CLI's `--json` document plus `console_url`, the page at
https://forge.reliantlabs.io that shows it, as structured content and as text.
A tool both servers serve has one name and one input schema; the local one adds
only `dir`. The hosted catalog tools serve the skills built into forge, with
commands written for `forge`. Repo tools (`lint`, `generate`, `shapes`, `audit`,
…) stay local: they need the repo.

`forge skill load connect` has the setup for each client (Claude, ChatGPT,
VS Code, Cursor, Goose, Codex, Reliant). A host embeds the same tools with
`github.com/reliant-labs/forge/cli.AddHostedTools`.
