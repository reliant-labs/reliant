<!-- forge:version=1 -->
# shop

This is a **Reliant Forge** project (github.com/reliant-labs/forge), driven by the `forge` CLI — not Atlassian Forge, Foundry's `forge` or Laravel Forge. It is a **static site**: a frontend with no backend, shipped by forge with environments, build-once releases.

This file holds the rules; depth is one `forge skill load <name>` away, named where you need it.

## Engineering disposition

A long-lived product: for anything hard to change later (architecture, public APIs, schemas), argue the alternatives against your own pick and choose the durable design, never a hack. Trivial work: just do it.

## What this project is

A static site — a landing page, marketing or docs site, SPA or PWA — is a complete forge project on its own, with **no backend**: no Go module, server, database or proto. `forge generate` skips every Go step by itself and there is no `go build`; never add a `go.mod`, `proto/` or `cmd/` to "make forge work". The site (`package.json`, `src/`, `public/`) is a plain Next.js or Vite app, and yours. forge owns `forge.yaml` and `deploy/kcl/<env>/main.k`, one per env (dev, prod, staging), which declares the frontend's `type` and `public_dir`; check a change with `forge env render <env>`.

**Load `forge skill load deploy/static-site`** for the whole flow: envs, the static export, releases, hosting, runtime config, domains, adding an API. Also `frontend/design`, `deploy/domains`, `deploy/hosting`, `debug`.

## Run, build, ship

```bash
forge env up dev                          # the dev server; mirrors the browser console to .forge/logs/dev/
forge lint --static-export && forge env render <env>
forge env build <env> --release v0.1.0    # build ONCE, push, record the release
forge env deploy <env> v0.1.0             # promote the same bytes to each env in turn, by digest
```

Browser `console.*` calls, uncaught errors and unhandled rejections land in `.forge/logs/<env>/frontend_<name>.log` as `[browser:<level>]`: `grep '\[browser:error\]' .forge/logs/dev/*.log` before guessing.

`npm run build` must emit plain files (`out/` for Next.js `output: "export"`, `dist/` for Vite); `forge lint --static-export` names what a static export refuses, with file:line. This project's envs are `dev`, `prod`, `staging`. Names don't establish where an env runs, so never assume one is hosted: its `main.k` declares the bindings, and `forge env render <env>` resolves them (`forge.OnHosted` is Reliant hosting). A new env on hosting is `forge env new <name> --from <env> --bind <frontend>=hosted` (`deploy/hosting`). A hosted deploy ships only a recorded release; recovery rolls forward. Build-time config is `env_vars` in each env's `main.k`; per-env config without a rebuild is `runtime_config` (`config.js`). Everything in a static site is public: never put a secret in either.

**Billing.** Free static hosting covers one organization per person (one they founded), with no limit on its sites; each env's site is one, within 1 GiB stored per site (`deploy/hosting`). A site in another organization, or any backend or database, needs hosting billing; until then `env deploy` exits 7 and prints the billing link. Give the user that link; don't retry. Never ask for `forge login` under Reliant.

**Custom domains (on Reliant hosting):** `forge domain add <host> --env <env>`.

## Design

The starter page is a placeholder, not a design. Ready to show means a brief only the user can give, real copy and a layout built for it: load `frontend/design` first. `forge component search <thing>` before you hand-write a control.

## Adding an API later

A backend (waitlist, checkout, login) is its own forge project beside the site (`forge project new <name>-api --service <entity>`); Go scaffolds refuse here. Point the site at its URL through `env_vars` or `runtime_config`.

## Critical rules

1. **The site's files are yours.** forge generates no code into a site; what it owns is `forge.yaml` and `deploy/kcl/`.
2. **Iterate with `forge lint --static-export`; run the full gate ONCE, before you finish:** `forge lint && npm run build && forge env render <env>`. Never pipe lint through `head`: the verdict is the LAST line (`--quiet` prints only failures).
3. **Ship releases, not builds.** Build once (`env build <env> --release <v>`), then deploy that release to each env in turn.

## When forge itself is wrong — FIX IT

Bad codegen, a lint false positive, a scaffold quirk: fix it in forge when you can (semver at major 1: deprecate, never break), never by hand-editing generated output. Otherwise report it and carry on — forge's own defects only, not this project's code or tests. Never put credentials or file contents in a report:

    forge report-bug --severity high --title "<one line naming the defect>" \
      --actual "<the exact error text>" --evidence "<commands, output, file paths>"

## Project Notes

<!-- Add project-specific context, conventions, and open questions here. -->
