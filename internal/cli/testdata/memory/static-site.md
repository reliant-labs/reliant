<!-- forge:version=1 -->
# shop

This is a **Forge** project managed by the `forge` CLI. Forge here is **Reliant Forge** (github.com/reliant-labs/forge) — not Atlassian Forge, Foundry's `forge` (Solidity) or Laravel Forge; do not use their commands. It is a **static site**: a frontend with no backend, shipped by forge with environments, build-once releases and hosted static hosting.

This file is auto-loaded into LLM context every session. Treat it as the authoritative quick-reference. For depth, load the named skills.

## Engineering disposition

This is a long-lived product. For consequential decisions — architecture, public APIs, schemas, lifecycle, anything that ships and is hard to change later — reason to the production-grade, durable solution first, not the quick fix. Before building a non-trivial design, name the realistic alternatives and the trade-off, and devil's-advocate the chosen approach **including your own proposals**; bias toward the coherent design that won't hit a ceiling.

**Don't manufacture urgency.** There is almost never real time pressure — user frustration usually means you haven't converged on the right thing, not "go faster / cut corners." Commit to the right design and ship it decisively; never rationalize a hack with invented pressure. When tempted by a shortcut, the question is "is this correct?", not "is this faster?"

Skip this depth for trivial/mechanical work — there, just do it.

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

**One of our biggest priorities is dogfooding forge and making sure it is
incredibly robust for all of our use cases.** When forge is the thing standing
in your way, improving forge IS the task, not a detour from it.

So if forge emits something wrong — bad codegen, a lint false-positive, a
scaffold quirk, a missing capability — reproduce it, then **fix it in forge**.
Do not paper over it with a workaround and a TODO, and do not hand-edit
generated output to compensate: the next `forge generate` overwrites your
patch and the defect comes back.

Every workaround is a bug report that never got filed and a defect that ships
to users instead. Hitting forge's rough edges is the single most valuable
signal we get about what to fix — routing around them throws that signal away
and leaves the next person to rediscover it.

Bias toward the durable fix over the minimal one: if a default is wrong, change
the default; if a behavior needs a knob, add the flag; if the design is wrong,
redesign it. Forge is semver at major 1, so a fix never breaks the projects
that pin it: keep the old flag, field, API or default working beside the new
one, mark it deprecated, and leave its removal to the next major, which only
forge's owner cuts.

When you cannot fix it here — forge is not this checkout, or the fix is out of
scope — **report it**, then carry on:

    forge report-bug --severity high --title "<one line naming the defect>" \
      --actual "<the exact error text>" --evidence "<commands, output, file paths>"

(`forge mcp` exposes the same as its `report_bug` tool.) Use it for defects in
forge itself — a forge command that fails, hangs or generates something wrong,
or a lint, skill or doc that is wrong — not for bugs in this project's code,
its failing tests, or your own mistakes. Never put credentials or file contents
in a report. A repeat of the same title within 30 minutes returns the first
report's id and sends nothing. forge sends nothing unless someone runs this;
`--dry-run` prints exactly what would go.

## Project Notes

<!-- Add project-specific context, conventions, and open questions here. -->
