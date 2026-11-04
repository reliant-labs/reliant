# Forge as an MCP server

`forge mcp` serves forge over the [Model Context Protocol](https://modelcontextprotocol.io)
on stdio, so any MCP-capable harness (Claude Code, Codex, Cursor, ...) can
introspect a project, read the skill catalog and run lint and generate without a
memory file in the project explaining how. The server introduces itself as
Reliant Forge (not Atlassian Forge, not Foundry's `forge`) and tells the model
to load the `forge` skill first.

Each tool runs the same command the CLI does, in-process. There is no forge
binary to shell out to and nothing to keep in sync with the CLI.

## Tools

Every tool takes an optional `dir`: the forge project to act on. It defaults to
the directory the server was started in (or `-C`).

| Tool                                                  | CLI equivalent                          | Writes files                                           |
| ----------------------------------------------------- | --------------------------------------- | ------------------------------------------------------ |
| `capabilities`                                        | `forge project capabilities`            | no                                                     |
| `annotations` (`kind`)                                | `forge project annotations`             | no                                                     |
| `libraries` (`packages`)                              | `forge project libraries [pkg...]`      | no                                                     |
| `audit`                                               | `forge project audit --json`            | no                                                     |
| `shapes` (`grep`, `kind`, `service`, `unimplemented`) | `forge project shapes`                  | no                                                     |
| `skill_list` (`group`, `all`, `include_migrations`)   | `forge skill list`                      | no                                                     |
| `skill_search` (`query`)                              | `forge skill search`                    | no                                                     |
| `skill_load` (`name`)                                 | `forge skill load`                      | no                                                     |
| `lint` (`scope`)                                      | `forge lint --quiet [--scope PATH]...`  | caches only; never `--fix`                             |
| `generate` (`dry_run`, `check`)                       | `forge generate [--dry-run \| --check]` | yes: generated files                                   |
| `report_bug` (`severity`, `title`, `summary`, ...)    | `forge report-bug`                      | sends one report ([docs/report-bug.md](report-bug.md)) |

The read-only tools are annotated `readOnlyHint`; `lint`, `generate` and
`report_bug` are not, so a harness can ask before running them. `report_bug` is
also `openWorldHint`: it is the only tool that sends anything off the machine. `lint` and `audit` report findings in
the result rather than as a tool failure. Output past about 220 KB is cut in the
middle, keeping the end (lint's verdict is its last line).

Calls are serialized: a command's working directory, project override and stdio
are process-global. `generate` and `lint` queue for a host heavy slot like they
do from a shell.

A project pinned to another forge version is not handed over to that forge (the
exec would replace the server). `generate` refuses with the usual skew message
and the reinstall hint instead.

## Install

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
