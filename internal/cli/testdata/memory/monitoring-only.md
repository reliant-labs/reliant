<!-- forge:version=1 -->
# shop

This is a **Forge** project managed by the `forge` CLI. Forge here is **Reliant Forge** (github.com/reliant-labs/forge) — not Atlassian Forge, Foundry's `forge` (Solidity) or Laravel Forge; do not use their commands. It is an **adopted project**: an existing app, in its own language and layout, that uses only the pieces of forge it opted into.

This file is auto-loaded into LLM context every session. Treat it as the authoritative quick-reference. For depth, load the named skills.

## Engineering disposition

This is a long-lived product. For consequential decisions — architecture, public APIs, schemas, lifecycle, anything that ships and is hard to change later — reason to the production-grade, durable solution first, not the quick fix. Before building a non-trivial design, name the realistic alternatives and the trade-off, and devil's-advocate the chosen approach **including your own proposals**; bias toward the coherent design that won't hit a ceiling.

**Don't manufacture urgency.** There is almost never real time pressure — user frustration usually means you haven't converged on the right thing, not "go faster / cut corners." Commit to the right design and ship it decisively; never rationalize a hack with invented pressure. When tempted by a shortcut, the question is "is this correct?", not "is this faster?"

Skip this depth for trivial/mechanical work — there, just do it.

## What this project is

An existing app, adopted onto forge as it is. forge is here for the pieces this project opted into — monitoring — not to rebuild it.

**It keeps its own language, layout and tooling.** forge owns only `forge.yaml` (declarations you edit, not generated output), plus whatever the user opts into later. So:

- **Never move existing code into forge's layout** — `internal/handlers/`, `proto/`, `cmd/`, `db/migrations/` — and never rename, restructure or "forge-ify" it, unless the user asks.
- forge's Go-service rules — handlers, proto as the API, the generated ORM, `forge generate` — describe projects forge built, not this one. Do not add a `go.mod`, a `proto/` or a `Taskfile.yml` to "make forge work": it already does.
- Keep the app's existing deploy YAML, Helm charts, compose files and CI as they are. Translating them into KCL is the user's decision, never a cleanup; forge can deploy hand-written manifests and charts where they stand (`deploy/byo-yaml`).
- **The next pieces are a menu, not a migration.** `forge skill load adopt` lists what forge can take on next — deploys, environments, releases, secrets, migrations, monitoring — each on its own, in any order, with what it takes. Offer one when it would help; start it only when the user asks.

```
<the app>, as it was               ← yours, in its own language; forge never rewrites it
forge.yaml                         ← name, forge pin, harness, and the switches it turns on
```

## Ask forge before guessing

- `forge skill list` is the catalog, `forge skill load <name>` prints one (`adopt` first), and `forge skill search <word>` finds one. Skills come from the binary and are never written to disk.
- `forge project capabilities` lists every verb forge has: check it before concluding forge cannot do something.
- Debugging: `forge skill load debug` names the evidence forge keeps.

## Environments

forge.yaml `envs:` names `prod`, `staging`. forge knows of them but does not deploy them: the app's own CI and infrastructure do, unchanged. Moving an env into forge's hands is a step on the `adopt` menu, not something to start unasked.

## Monitoring

forge monitors this app in `prod`, `staging`: the app exports OpenTelemetry (traces, logs, metrics) to Reliant with a per-env ingest token, and forge reads it back. It needs a signed-in forge: under Reliant it already is; standalone, `forge login` (or a token in CI). Dashboards: https://forge.reliantlabs.io/forge/p/shop/observability.

```bash
forge obs status <env>          # what is wrong now
forge obs issues <env>          # grouped errors, new and regressed
forge obs logs <env>            # logs; `obs traces` and `obs metrics` beside it
forge obs check <env> --json    # judge the newest service.version; branch on its exit code
```

With no deploy recorded, `obs check` dates a release from the first telemetry carrying its new `service.version`, and labels that marker INFERRED. Load `observability`, and `observability/query` for flags, documents and exit codes.

## Tests and the gate

No test command was found (no Taskfile, package.json scripts or Makefile): ask the user how this project is tested and built before calling work done. Its CI configuration (`.github/workflows/`, …) shows what CI runs. There is no `forge test` either.

A run expected to take more than ~2 minutes goes in the background (`run_in_background` + `shell_wait`), never a long foreground call — and never detached by hand with `nohup`, a trailing `&`, `setsid` or `disown`.

## Critical rules

- **The app's files are yours.** forge generates no code into this project and never rewrites its source or its build config. What forge owns is `forge.yaml`.
- **Never move the app into forge's layout**, and never translate its existing config into forge's, unless the user asks.

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
