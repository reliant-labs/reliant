# @reliantlabs/forge

Deploy what your coding agent built, and let it prove the release works. Deploys need no account.

[Forge](https://github.com/reliant-labs/forge) is guardrails for apps. It is an
open-source (Apache-2.0) framework, CLI and MCP server that supercharges any
agent harness: Claude Code, Codex, Cursor, a harness you wrote, or CI. It
scaffolds Go services, React frontends and SQL migrations into the right places,
lints for your architecture and for unsafe migrations, tests against a real
Postgres, and builds each release once and promotes it by digest. Use it piece
by piece in any repo, in any language, with no rewrite. Deploy with no account:
in a Vite or Next.js app, or an app with its own Dockerfile, run
`npx -y @reliantlabs/forge@latest env deploy prod --claimable` to get a live URL
and a claim link that is good for 72 hours.

This package runs forge without a Go toolchain.

## Deploy with your agent

```sh
# No account: a live URL plus a 72-hour claim link
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

Or give your agent the MCP server, whose `deploy_claimable` tool does the same:

```sh
claude mcp add forge -- npx -y @reliantlabs/forge mcp   # Claude Code
codex mcp add forge -- npx -y @reliantlabs/forge mcp    # Codex
gemini extensions install https://github.com/reliant-labs/forge   # Gemini CLI
```

Cursor: add this to `.cursor/mcp.json` (or `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "forge": { "command": "npx", "args": ["-y", "@reliantlabs/forge", "mcp"] }
  }
}
```

Then ask:

```text
Deploy this app with Reliant Forge. Run
`npx -y @reliantlabs/forge@latest env deploy prod --claimable` (or call the forge
MCP server's deploy_claimable tool), then give me the live URL and the claim link.
```

The claim link expires in 72 hours: treat it like a password. Opening it signs
you up free and makes the deploy yours. It fits one static site, or one service
with one database; the limits are in
[docs/mcp.md](https://github.com/reliant-labs/forge/blob/main/docs/mcp.md#deploying-with-no-account).

## In the repo you are in

Forge helps the repo you are in today, in any language, with no rewrite:
`forge lint` catches unsafe SQL migrations (and, in a Go module, cut traces and
timezone-dependent date buckets), the built-in skills are playbooks for
debugging, testing, migrations and deploys, and its runtime libraries (`forge/pkg`
for Go, `@reliantlabs/forge-web-runtime` for web and Node) import as they are.
An existing Vite or Next.js app goes live with no account. Monitoring and a
recorded history of every deploy come once forge deploys the app, which the
`adopt` skill walks without rewriting it.

For an agent, register the MCP server and call its `benefits` tool: what applies
to the repo, with the first step and the agent time to each.

```sh
claude mcp add forge -- npx -y @reliantlabs/forge mcp
npx @reliantlabs/forge project benefits   # the same list, in a terminal
```

Monitoring and releases in your Reliant account are also a hosted MCP connector
with nothing to install: add `https://api.reliantapi.com/forge/mcp` as a custom
connector (claude.ai, Claude Desktop, ChatGPT, VS Code, Cursor) and sign in.
`npx @reliantlabs/forge skill load connect` has the steps for each client and
says which tools live where.

As a CLI:

```sh
npx @reliantlabs/forge version
npx @reliantlabs/forge project new my-app
```

or install it globally:

```sh
npm install -g @reliantlabs/forge
forge version
```

This package is a thin shim. On first run it downloads the prebuilt `forge`
binary for your OS and CPU from the matching
[GitHub release](https://github.com/reliant-labs/forge/releases), verifies its
sha256 against the release checksums pinned into this package when it was
published (pinned only after the release's Sigstore signature verified), caches
it, and runs it. `FORGE_REQUIRE_SIGNATURE=1` refuses to run from a copy that
carries no pin. There is no
postinstall script, so `--ignore-scripts` is fine. The package version is the forge
version.

- Cache: `~/.cache/forge/npm/<version>` (Linux), `~/Library/Caches/forge/npm/<version>` (macOS),
  `%LOCALAPPDATA%\forge\npm\<version>` (Windows), one subdirectory per pinned
  digest (`sha256-<digest>`; `unverified` for a copy with no pin, which a
  pinned or `FORGE_REQUIRE_SIGNATURE=1` run never uses). Override with `FORGE_NPM_CACHE_DIR`.
- Needs Node 18+ and `tar` on `PATH` (present on Linux, macOS and Windows 10+).
- Platforms: linux, darwin, windows on amd64 and arm64.

Other install methods (curl | sh, Homebrew, `go install`) are listed in the
[main README](https://github.com/reliant-labs/forge#install).
