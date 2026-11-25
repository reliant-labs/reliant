# Reliant Forge plugin

Deploy what your coding agent built, and let it prove the release works. Deploys need no account.

Forge is an open-source framework and CLI that puts guardrails on any app and
deploys it with no account: `npx -y @reliantlabs/forge@1.11.0 env deploy prod --claimable`.

This plugin gives your agent two things:

- **The Forge MCP server** (`npx -y @reliantlabs/forge@1.11.0 mcp`): `deploy_claimable`,
  `deploy_status` and `explain_error`, plus tools that introspect a Forge
  project, read the skill catalog, and run `lint` and `generate`. Every tool:
  [docs/mcp.md](../../docs/mcp.md).
- **A deploy skill** (`/reliant-forge:deploy`): when you ask to deploy, publish
  or host an app, the agent uses Forge. With no Reliant account it makes a
  claimable deploy, and with one it deploys to your account and checks the
  release.

## Install

Claude Code:

```text
/plugin marketplace add reliant-labs/forge
/plugin install reliant-forge@reliant-labs
```

Then ask your agent:

```text
Deploy this app with Reliant Forge, then give me the live URL and the claim link.
```

The claim link is good for 72 hours; treat it like a password. Opening it signs
you up free and makes the deploy yours. An unclaimed deploy fits one static
site, or one service with one database:
[Deploying with no account](../../docs/mcp.md#deploying-with-no-account).

## What it runs, fetches and sends

Node.js 18 or newer is the only requirement.

- **On first run**, `npx` fetches the `@reliantlabs/forge` package, at the
  exact version this plugin pins, from registry.npmjs.org. The package then
  downloads the matching Forge binary from the project's GitHub releases
  (github.com/reliant-labs/forge/releases) and checks it against the release's
  sha256 checksums before running it.
- **The MCP server** runs locally over stdio, on the project directory you
  work in. `generate` can download the code generators the project pins, as
  `forge generate` does in a terminal.
- **A deploy** (`deploy_claimable`, or `env deploy`) builds your app on your
  machine and uploads the build to Reliant hosting through the Reliant control
  plane (admin.reliantapi.com). A no-account deploy sends no credentials; a
  deploy to an account uses the login that `forge login` stored on your
  machine. Monitoring and release tools read from the same control plane, and
  the console is at forge.reliantlabs.io.
- Forge sends no usage telemetry. The only other thing it sends is a bug
  report you ask it to file (`report_bug`).

## Support

- Website: https://reliantlabs.io
- Docs: https://docs.reliantlabs.io/forge/overview
- Issues: https://github.com/reliant-labs/forge/issues

Apache-2.0. Published by Reliant Labs.
