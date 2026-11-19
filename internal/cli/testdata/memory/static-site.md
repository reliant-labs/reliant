<!-- forge:version=1 -->
# shop

This is a **Reliant Forge** project (github.com/reliant-labs/forge), driven by the `forge` CLI — not Atlassian Forge, Foundry's `forge` or Laravel Forge; do not use their commands. It is a **static site**: a frontend with no backend, shipped by forge with environments, build-once releases and hosted static hosting.

This file is loaded into every session, so it holds only what to know before your first edit. Depth is one `forge skill load <name>` away, named where you need it.

## Engineering disposition

This is a long-lived product. For anything hard to change later — architecture, public APIs, schemas — name the realistic alternatives, argue against your own pick, and choose the durable design over the quick fix. There is almost never real time pressure, so never justify a hack with it. Trivial work: just do it.

## What this project is

A static site — a landing page, a marketing or docs site, a SPA — is a complete forge project on its own. There is **no backend here**: no Go module, no server, no database, no proto. So the Go/proto/handler/migration rules forge projects with an API live by do not apply, `forge generate` has nothing to project (it skips every Go step by itself), and there is no `go build` to run. Do not add a `go.mod`, a `proto/` or a `cmd/` by hand to "make forge work": it already does.

```
package.json, src/, public/   ← the site: a plain Next.js or Vite app — yours, forge never rewrites it
forge.yaml                    ← name, forge pin, harness (no module_path: there is no Go module)
deploy/kcl/kcl.mod
deploy/kcl/dev/main.k         ← the frontend (path = ".") bound to its dev server
deploy/kcl/staging/main.k     ← … bound to hosted static hosting (forge.OnHosted {})
deploy/kcl/prod/main.k        ← … same
```

The frontend's `type` (`nextjs` or `vite`) and `public_dir` (where `npm run build` lands: `out/` or `dist/`) are declared in each env's `main.k`. The envs are the declaration: change a binding there, then check it with `forge env render <env>`.

**Start with `forge skill load deploy/static-site`** — the whole flow: envs, the static export, releases, hosting, runtime config, domains. Other skills a site uses: `frontend/design` (designing the page), `deploy/domains` (your own hostname), `deploy/hosting` (what hosted hosting is and costs), `debug`. `forge skill list` is the catalog. Skills come from the binary and are never written to disk.

## Run it

```bash
forge env up dev        # the dev server — no database, no cluster; prints the URL
```

`npm run dev` works too, but `env up` is what mirrors the browser into a log file (below) and applies the env's `env_vars`.

## Frontend logs are on disk

A site forge started (`forge project new --kind site`) mirrors every `console.*` call — plus every uncaught error and unhandled promise rejection — into `.forge/logs/<env>/frontend_<name>.log`, prefixed `[browser:<level>]`, while `env up` runs it. So "I can't see what the browser did" is almost always false:

```bash
grep '\[browser:error\]' .forge/logs/dev/*.log
```

An uncaught `TypeError` produces no `console` call anywhere in your source, so grepping the code finds nothing while the stack trace sits in that file. An adopted app has the same sink once it calls `installDevLogging()` from `@reliantlabs/forge-web-runtime` and serves the dev-server receiver; `forge skill load debug` covers both halves.

## Build it as a static export

Hosted static hosting serves files: `npm run build` must produce a directory of plain HTML/JS/CSS (`out/` for Next.js with `output: "export"`, `dist/` for Vite). For Next.js that rules out what needs a server at request time — a dynamic `[slug]` segment without `generateStaticParams()`, server actions, middleware, `cookies()`/`headers()`, a GET route handler that answers dynamically, and next/image without `unoptimized`. `forge lint --static-export` names each with its file:line; `npm run build` is the authority.

## Ship it

```bash
forge lint --static-export && forge env render prod
forge env build prod --release v0.1.0   # build ONCE, push, record the release
forge env deploy staging v0.1.0         # promote the same bytes … (a 2nd hosted env needs billing — below)
forge env deploy prod v0.1.0            # … to prod, by digest — no rebuild
forge env status prod                   # verify
```

Staging and prod are already bound to Reliant hosted static hosting: the platform owns the bucket, the CDN and a hostname. A hosted deploy ships only a recorded release, never a local build; recovery rolls forward to a new release. Another env is `forge env new preview --from prod`. Your own bucket or Firebase instead: change the frontend's `runtime` in that env's `main.k` (`deploy/static-site`).

**Billing.** One hosted static site per person is free, across every org they own without a plan — no billing, 1 GiB storage cap — and it is counted across environments: each env's deployed site is one site. Staging and prod are both bound to hosting, so without billing ONE of them deploys free (deploy prod first if the user wants it live) and the second queues with `STATIC_FREE_TIER_EXCEEDED`. Anything beyond the free site — a second env or site, any hosted workload or managed database — needs billing; until it is set up the deploy is queued, not refused: `env deploy` exits 7 and prints the billing link. Give the user that link; don't retry. Never ask for `forge login` when running under Reliant: you are already signed in.

**Custom domains:** `forge domain add <host> --env prod` (`deploy/domains`).

## Config

- **Build-time** (`NEXT_PUBLIC_*`, `VITE_*`): `env_vars` on the frontend in each env's `main.k`. Baked into the release that env builds.
- **Per env, without a rebuild**: `runtime_config` on the frontend. The platform serves it as `config.js` beside the bundle (`window.__FORGE_CONFIG__`); load it with a plain blocking `<script src="/config.js">` in `<head>` before your bundle.
- Everything in a static site is public. A secret belongs on a backend, never in either of these.

## Design

The starter page is a placeholder that proves the site builds and deploys — not a design, and not something to hand a user. Ready to show means a brief (what the site is for, who it is for, how it should feel — only the user can give you that), real copy, and a layout built for it. Load `frontend/design` first; it asks for the brief and will not invent an aesthetic without one. `forge component search <thing>` searches forge's component library before you hand-write a control.

A PWA is a static site too: forge builds, hosts and ships it like any other. The service worker and web manifest are yours to add — forge scaffolds neither.

## Adding an API later

When the site needs a backend — a waitlist, checkout, login — that is a Go API, and this project has no Go module, so `forge scaffold service` (and every other Go scaffold) refuses here and says why. Give the API its own forge project beside the site (`forge project new <name>-api --service <entity>`), deploy it, and point the site at its URL through `env_vars` or `runtime_config`. `deploy/static-site` (Growing a backend) has the details.

## Critical rules

1. **The site's files are yours.** forge generates no code into a site and never rewrites `package.json`, `src/` or the build config. What forge owns is `forge.yaml` and `deploy/kcl/` — declarations you edit, not generated output.
2. **Gate with `forge lint`.** Never pipe it through `head` — the verdict is the LAST line; `forge lint --quiet` prints only failures plus that verdict. `--static-export` is the lane that matters most here.
3. **`npm run build` before you call it done.** It is the only proof the export builds; `forge env render prod` is the proof the envs still declare a shippable site.
4. **Ship releases, not builds.** Build once (`env build <env> --release <v>`), then deploy that release to each env in turn.

## When forge itself is wrong — FIX IT

If forge emits something wrong — bad codegen, a lint false positive, a scaffold quirk, a missing capability — fix it in forge when you can; never paper over it or hand-edit generated output, which the next `forge generate` overwrites. forge is semver at major 1: a fix keeps the old flag, field or default working beside the new one, marked deprecated. When you cannot fix it here, report it and carry on:

    forge report-bug --severity high --title "<one line naming the defect>" \
      --actual "<the exact error text>" --evidence "<commands, output, file paths>"

Only for defects in forge itself (a command, codegen, lint, skill or doc), not this project's code or tests. Never put credentials or file contents in a report; `--dry-run` shows exactly what would be sent.

## Project Notes

<!-- Add project-specific context, conventions, and open questions here. -->
