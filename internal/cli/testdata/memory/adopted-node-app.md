<!-- forge:version=1 -->
# shop

This is a **Forge** project managed by the `forge` CLI. Forge here is **Reliant Forge** (github.com/reliant-labs/forge) — not Atlassian Forge, Foundry's `forge` (Solidity) or Laravel Forge; do not use their commands. It is an **adopted app**: a server forge did not write, in any language, built from its own Dockerfile and shipped by forge with environments, build-once releases and Reliant hosting.

This file is auto-loaded into LLM context every session. Treat it as the authoritative quick-reference. For depth, load the named skills.

## Engineering disposition

This is a long-lived product. For consequential decisions — architecture, public APIs, schemas, lifecycle, anything that ships and is hard to change later — reason to the production-grade, durable solution first, not the quick fix. Before building a non-trivial design, name the realistic alternatives and the trade-off, and devil's-advocate the chosen approach **including your own proposals**; bias toward the coherent design that won't hit a ceiling.

**Don't manufacture urgency.** There is almost never real time pressure — user frustration usually means you haven't converged on the right thing, not "go faster / cut corners." Commit to the right design and ship it decisively; never rationalize a hack with invented pressure. When tempted by a shortcut, the question is "is this correct?", not "is this faster?"

Skip this depth for trivial/mechanical work — there, just do it.

## What this project is

An app forge did not write — a server in any language, with its own Dockerfile — adopted as a forge project without changing a file of it. forge builds the image from that Dockerfile and deploys it: environments, build-once releases promoted by digest, plans, rollout waits, managed Postgres and secrets on Reliant hosting.

**It keeps its own language, layout and tooling.** forge owns only `forge.yaml` and `deploy/kcl/` (declarations you edit, not generated output), plus whatever the user opts into later. So:

- **Never move existing code into forge's layout** — `internal/handlers/`, `proto/`, `cmd/`, `db/migrations/` — and never rename, restructure or "forge-ify" it, unless the user asks.
- forge's Go-service rules — handlers, proto as the API, the generated ORM, `forge generate` — describe projects forge built, not this one. Do not add a `go.mod`, a `proto/` or a `Taskfile.yml` to "make forge work": it already does.
- Keep the app's existing deploy YAML, Helm charts, compose files and CI as they are. Translating them into KCL is the user's decision, never a cleanup; forge can deploy hand-written manifests and charts where they stand (`deploy/byo-yaml`).
- **The next pieces are a menu, not a migration.** `forge skill load adopt` lists what forge can take on next — deploys, environments, releases, secrets, migrations, monitoring — each on its own, in any order, with what it takes. Offer one when it would help; start it only when the user asks.

```
<the app>, as it was               ← yours, in its own language; forge never rewrites it
forge.yaml                         ← name, forge pin, harness
deploy/kcl/kcl.mod
deploy/kcl/workloads.k             ← the app, declared ONCE: forge.DockerBuild on the Dockerfile, its port, its probe
deploy/kcl/<env>/main.k            ← where it runs, one per env: dev, prod, staging
```

## Ask forge before guessing

- `forge skill list` is the catalog, `forge skill load <name>` prints one (`adopt` first), and `forge skill search <word>` finds one. Skills come from the binary and are never written to disk.
- `forge project capabilities` lists every verb forge has: check it before concluding forge cannot do something.
- Debugging: `forge skill load debug` names the evidence forge keeps; every process `forge env up` runs is teed to `.forge/logs/<env>/<name>.log`, so grep there before guessing.

## Environments and deploys

The envs are the declaration: `deploy/kcl/<env>/main.k`. Change a binding or an env value there, then check it with `forge env render <env>`. `deploy`, `deploy/hosting` and `secrets` are the skills.

```bash
forge env up dev                     # run dev as declared: the app's dev command as a host process, with PORT (and DATABASE_URL) set
forge env render <env>               # check an env's declaration
forge env deploy <env>               # build ONCE, push, cut a release, promote it
forge obs check <env> --wait --json  # prove the release good; branch on its exit code
forge env deploy <next-env> <release> # promote the same bytes, by digest — no rebuild
```

The dev command is the `command` in `deploy/kcl/dev/main.k`: replace it with your watch / hot-reload loop.

A hosted deploy needs billing on the Reliant organization; until it is set up `env deploy` exits 7 and prints the billing link. Give the user that link; don't retry.

- The app reads its config from environment variables. A per-env value is `env` on the app in that env's `main.k`; a credential is `env = {KEY = forge.ManagedSecret {name = "KEY"}}` there, with its value set once by `forge secret set --env <env> KEY` (stdin). forge never sees a secret's value.
- A managed database (if declared) reaches the app as `DATABASE_URL` through `forge.DatabaseRef`.
- `forge obs logs <env>` and `forge obs workloads <env>` (restarts, crash loops, failed jobs) work for any app. Request rates, error rates and traces exist only if the app exports OpenTelemetry (OTLP to `OTEL_EXPORTER_OTLP_ENDPOINT`, which hosting sets on every workload): until it does, `forge obs check` is INCONCLUSIVE with `reason_code: no_request_telemetry` — not proven good — while restarts, alerts and issues still count. Load `observability/query` for the details.

## Database migrations

The schema migrations are prisma's (`prisma/schema.prisma`), applied with `npx prisma migrate deploy`. They stay with prisma. Never convert them to forge migrations, rename or move them unless the user asks. On a forge-deployed env, run them before each rollout as a `kind = "job"` workload from the app's image with `before = [fw.BEFORE_ALL]` (`adopt/deploy` has the template).

## Tests and the gate

This project's own commands, from its `package.json`: `npm test` (tests); `npm run lint` (lint); `npm run build` (build). Use them, not forge's defaults, and run the tests once before you finish. There is no Taskfile here, so no `task test`, and no Go module, so no `go build`. There is no `forge test` either. Before shipping, `forge lint` checks forge's own files and `forge env render <env>` proves each env still renders.

A run expected to take more than ~2 minutes goes in the background (`run_in_background` + `shell_wait`), never a long foreground call — and never detached by hand with `nohup`, a trailing `&`, `setsid` or `disown`.

## Critical rules

- **The app's files are yours.** forge generates no code into this project and never rewrites its source, its Dockerfile or its build config. What forge owns is `forge.yaml` and `deploy/kcl/`.
- **Never move the app into forge's layout**, and never translate its existing config into forge's, unless the user asks.
- **The Dockerfile must build on its own.** A deploy runs `docker build` with that file and context and nothing else: an image that `COPY`s a `dist/` built on your machine fails there. Prove it with `docker build .` before the first deploy.
- **Gate with `forge lint`.** Never pipe it through `head` — the verdict is the LAST line.
- **Ship releases, not builds.** Build once, then deploy that release to each env in turn.

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
