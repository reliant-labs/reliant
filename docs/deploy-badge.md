# The "Deploy with Reliant Forge" badge

A badge for your README that tells readers the repo deploys with Reliant Forge,
and links to the page that shows the one command to do it.

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

**What it is not:** a one-click deploy button. GitHub renders a README as
static markdown, so a badge can only be a link. Deploying runs on the reader's
machine (or in their agent), from a checkout of your repo. The badge links to
[Deploy without an account](https://docs.reliantlabs.io/forge/deploy-without-an-account),
which explains that command; put the command itself under the badge.

## Paste this

Your repo must already deploy with forge: it has a `forge.yaml` and a
`deploy/kcl/` (`reliant forge project new --in-place --kind app` for an app
with a Dockerfile, `--kind site` for a Vite or Next.js static site; see the
[examples](../examples/README.md)). Then add:

````markdown
[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

Deploy your own copy, no account needed (live for 72 hours unless you claim it):

```bash
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```
````

`--claimable` publishes with no account and prints a live URL plus a claim
link. Whoever opens the claim link first owns the deploy, so the reader keeps
it to themselves. An unclaimed deploy goes offline after 72 hours and is
deleted 7 days later; it is limited to one service with its migration job, one
Postgres database and one static site, sized 125m CPU / 128 MiB, with no secret
values and no outbound internet until claimed. `--plan-only` checks a repo
against those limits without creating anything:

```bash
npx -y @reliantlabs/forge@latest env deploy prod --claimable --plan-only
```

Readers who are signed in (`reliant forge login`, or inside Reliant) deploy to
their own organization with `forge env deploy prod`; `--claimable` is refused
for a signed-in user.

## The image

The SVG is [`docs/assets/deploy-with-reliant-forge.svg`](assets/deploy-with-reliant-forge.svg)
in this repository, served from:

```
https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg
```

It is a dark, neutral pill (196×28) that reads on GitHub's light and dark
themes. Copy it into your own repo if you would rather not depend on this one's
`main` branch.

### shields.io alternative

If your README already uses shields.io badges and you want a matching style:

```markdown
[![Deploy with Reliant Forge](https://img.shields.io/badge/deploy%20with-Reliant%20Forge-18181b?style=flat)](https://docs.reliantlabs.io/forge/deploy-without-an-account)
```

`style=` takes `flat`, `flat-square`, `for-the-badge` or `plastic`.
