<!-- forge:version=1 -->
# shop

This is a **Reliant Forge** project (github.com/reliant-labs/forge), driven by the `forge` CLI — not Atlassian Forge, Foundry's `forge` or Laravel Forge. It is an **adopted app**: a server forge did not write, in any language, built from its own Dockerfile and shipped by forge with environments, build-once releases and Reliant hosting.

This file holds the rules; depth is one `forge skill load <name>` away, named where you need it.

## Engineering disposition

A long-lived product: for anything hard to change later (architecture, public APIs, schemas), argue the alternatives against your own pick and choose the durable design, never a hack. Trivial work: just do it.

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

This project's own commands, from its `package.json`: `npm test` (tests); `npm run lint` (lint); `npm run build` (build). Use them, not forge's defaults: while iterating, run only the tests your change reaches; run the whole suite once, before you finish. There is no Taskfile here, so no `task test`, and no Go module, so no `go build`. There is no `forge test` either. Before shipping, `forge lint` checks forge's own files and `forge env render <env>` proves each env still renders.

A run expected to take more than ~2 minutes goes in the background (`run_in_background` + `shell_wait`), never a long foreground call — and never detached by hand with `nohup`, a trailing `&`, `setsid` or `disown`.

## Critical rules

- **The app's files are yours.** forge generates no code into this project and never rewrites its source, its Dockerfile or its build config. What forge owns is `forge.yaml` and `deploy/kcl/`.
- **Never move the app into forge's layout**, and never translate its existing config into forge's, unless the user asks.
- **The Dockerfile must build on its own.** A deploy runs `docker build` with that file and context and nothing else: an image that `COPY`s a `dist/` built on your machine fails there. Prove it with `docker build .` before the first deploy.
- **Gate with `forge lint` once, before you finish**, not after every edit. Never pipe it through `head` — the verdict is the LAST line.
- **Ship releases, not builds.** Build once, then deploy that release to each env in turn.

## When forge itself is wrong — FIX IT

Bad codegen, a lint false positive, a scaffold quirk: fix it in forge when you can (semver at major 1: deprecate, never break), never by hand-editing generated output. Otherwise report it and carry on — forge's own defects only, not this project's code or tests. Never put credentials or file contents in a report:

    forge report-bug --severity high --title "<one line naming the defect>" \
      --actual "<the exact error text>" --evidence "<commands, output, file paths>"

## Project Notes

<!-- Add project-specific context, conventions, and open questions here. -->
