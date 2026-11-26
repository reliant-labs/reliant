---
name: deploy
description: Deploy, publish or host this app with Reliant Forge, with or without a Reliant account. Use when the user asks to deploy, ship, publish, host or put live a website, web app, API or service, wants a public URL to share, or asks for free hosting with no signup, or asks whether a deploy is healthy (that check needs a signed-in Reliant account). Works on a Vite or Next.js app, an app in any language with its own Dockerfile, or a Forge project.
---

# Deploy with Reliant Forge

Forge is an open-source framework and CLI that puts guardrails on any app and
deploys it with no account: `npx -y @reliantlabs/forge@1.11.0 env deploy prod --claimable`.

This plugin runs the Forge MCP server (`npx -y @reliantlabs/forge@1.11.0 mcp`). Prefer
its tools. Every step also works as a shell command, with nothing installed.

## Pick the path

- **The user has no Reliant account, or you do not know:** deploy with no
  account (below). Do not ask them to sign up first.
- **This machine is signed in to Reliant:** deploy to the account (below). A
  no-account deploy on a signed-in machine is refused with `error_reason`
  `claimable_signed_in`; switch to the account path when you see it.

## No account: a claimable deploy

Call the `deploy_claimable` tool. Pass `dir` when the app is not in the
working directory. Without the MCP server, run:

```bash
npx -y @reliantlabs/forge@1.11.0 env deploy prod --claimable
```

It deploys:

- a Vite or Next.js app at the directory's root,
- an app in any language with its own Dockerfile (and compose file),
- a Forge project (a directory with `forge.yaml`).

A directory with no `forge.yaml` is adopted in place first: Forge adds
`forge.yaml` and `deploy/kcl/` and never changes the app's own files. Tell the
user about the files it added.

The result is a live URL and a claim link. Show both to the user who asked for
the deploy, in your reply to them, and tell them:

- The claim link is a one-time link for them, good for 72 hours: the first
  person to open it signs up free and becomes the deploy's owner.
- Unclaimed, the deploy is live for 72 hours, then offline for 7 more days,
  then deleted.
- An unclaimed deploy is one static site, or one service (one replica) with
  one Postgres database and no backups. An unclaimed service has no outbound
  internet. Claiming it lifts these limits.

To show the user the URL and claim link again later, call `deploy_status`.

Because whoever opens the claim link first owns the deploy, it goes to that
user and nowhere else: never into a commit, a file in the repo, a log, an
issue, a pull request or any public place.

## Signed in: deploy to the account

First check for `forge.yaml` in the app's directory. Ordinary `env deploy`
does **not** adopt an app automatically. This also applies when switching here
from `claimable_signed_in`.

If `forge.yaml` is absent, adopt the existing app in place before deploying:

- **Vite or Next.js static site (no server):**

  ```bash
  npx -y @reliantlabs/forge@1.11.0 project new --in-place --kind site --name <name>
  ```

  Static-site adoption does not support `--dry-run`. Review the files it adds
  before deploying.

- **App with its own Dockerfile (including an app that needs a server):**

  ```bash
  npx -y @reliantlabs/forge@1.11.0 project new --in-place --kind app --name <name> --dry-run
  npx -y @reliantlabs/forge@1.11.0 project new --in-place --kind app --name <name>
  ```

Replace `<name>` with the project's name (lowercase, digits and hyphens).
For a Dockerfile app, review the dry-run output before running the second
command. Adoption adds `forge.yaml` and `deploy/kcl/` and appends Forge's block
to `.gitignore`; tell the user what
changed. Review the generated assumptions, prod hosting bindings and required
secrets before deploying. For a server app without a Dockerfile, load
`adopt/deploy` with `skill_load` for the prerequisites; do not adopt it as a
static site.

If `forge.yaml` already exists, skip adoption and preserve the project's
configuration. Check that `deploy/kcl/prod/main.k` exists and declares the
intended target; do not overwrite an existing project's environments. Use
`skill_load` with `deploy` if prod still needs configuring.

Once the project and prod environment are configured, deploy from the app's
directory:

```bash
npx -y @reliantlabs/forge@1.11.0 env deploy prod
```

Then prove the release is good:

```bash
npx -y @reliantlabs/forge@1.11.0 obs check prod --wait --json
```

Branch on its exit code:

- **0:** done.
- **1:** the release regressed. Run the commands in the document's
  `suggested[]`, fix, deploy again, check again.
- **2:** the check could not prove the release good. Report "not proven
  good", never "passed".
- **8:** the wait ran out before the check window closed. Check again later.

If `env deploy` exits 7, the deploy is queued on billing. Give the user the
billing link it printed and stop. Do not retry.

## When a deploy fails

Call `explain_error` with the `exit_code` and `error_reason` from the
`--json` output, or with the error text. It returns what happened and the
exact next command. The common ones:

- `claimable_throttled` (exit 4): too many new no-account deploys right now.
  Wait until `retry_at`, then run the same command once. Never retry in a
  loop.
- `claimable_ineligible`: the app does not fit an unclaimed deploy. The error
  lists each item and its fix. Re-check with `--plan-only` after fixing, or
  deploy to an account.
- Not a Vite, Next.js or Dockerfile app: start a site with
  `npx -y @reliantlabs/forge@1.11.0 project new <name> --kind site`, or add a
  Dockerfile.

## Rules

- Never create an account for the user, and never ask for their password or
  tokens. Signing in is theirs to do.
- Do not change the app's code to make a deploy pass without telling the user.

## Learn more

- `skill_load` with `deploy`, `deploy/static-site` or `deploy/hosting` (or
  `npx -y @reliantlabs/forge@1.11.0 skill load deploy`): the full guides.
- Deploying with no account: https://docs.reliantlabs.io/forge/deploy-without-an-account
- Reliant Labs: https://reliantlabs.io
