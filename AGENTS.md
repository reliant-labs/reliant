<!-- forge:version=1 -->
# reliant

This is a **Reliant Forge** project (github.com/reliant-labs/forge), driven by the `forge` CLI — not Atlassian Forge, Foundry's `forge` or Laravel Forge.

This file holds the rules; depth is one `forge skill load <name>` away, named where you need it.

## Engineering disposition

A long-lived product: for anything hard to change later (architecture, public APIs, schemas), argue the alternatives against your own pick and choose the durable design, never a hack. Trivial work: just do it.

## Use forge skills to guide your work

Load a skill before guessing, `forge skill load forge` first (what forge already scaffolds, so you never hand-write it); `forge skill list` is the catalog. `forge project shapes --grep <Name>` finds a symbol in every layer with file:line, `forge project capabilities` lists every verb, and `forge project libraries <pkg>` prints a forge library's API.

## Critical rules

1. **Never edit generated code** (`gen/`, `*_gen.go`, `*-hooks.ts`): edit its input (a proto, a migration, config, a `//forge:contract` type) and run `forge generate`. Handlers, `internal/app/providers.go` and `compose.go` are yours.
2. **Proto is the API's canonical input; migrations are the DB source of truth.** The ORM is projected from the applied `db/migrations` schema, never from proto.
3. **Iterate on what you changed; run the full gate ONCE, before you finish.** While iterating: `forge lint --scope <changed path>` (~10s; the full lint takes minutes) and `task test:short -- ./internal/<pkg>/...` (cached; never add `-count=1` or `-race`). To finish: `forge generate && forge lint && go build ./... && task test`. Never pipe lint through `head`: the verdict is the LAST line (`--quiet` prints only failures).
4. **A test over ~2s calls `testkit.Slow(t, "<why>")` first** (`github.com/reliant-labs/forge/pkg/testkit`): it skips under `test:short`, so a green `test:short` never ran it (`testing`). Wait with `testkit.Eventually`/`testkit.Receive`, never `time.Sleep`. Run work over ~2 minutes in the background, never detached (`nohup`, `&`); `⏳ waiting for a heavy slot` is a queue: never kill its holders (`ci/slots`).
5. **Debugging: `forge skill load debug` first.** Frontend `console.*` calls and uncaught errors are on disk beside the backend logs: `grep '\[browser:error\]' .forge/logs/dev/*.log`.
6. **`auth_required: false` publishes one RPC, never a page.** Public pages are the route guard's `publicExactPaths`; access checks and row scoping are your handler code (`middleware.GetUser`), never generated (`auth`).

## Where logic goes

This is a Go project; `architecture` maps the tree. An rpc's logic is its `*Service` method in `internal/handlers/<svc>/rpc_<name>.go`, reaching collaborators through `s.deps` (`api`). Errors cross the rpc boundary as `return nil, svcerr.Wrap(err)` (`api`). Shared logic: `forge scaffold package <name>` (`service-layer`). Transactions: `s.deps.DB.RunTx(ctx, orm.Serializable, …)` (`service-layer/transactions`). Wiring: `internal/app/` (`services`). An rpc is CRUD only by its exact name, and list filters are `optional` (`proto`, `authoring`).

## The scaffolded frontend is starter code

The dashboard, nav, sign-in screen and CRUD pages prove the stack is wired; rewriting them is expected (they are neutral on purpose). Load `frontend/design` before showing any of it to a user.

## Shipping

`forge env deploy <env>` ships an env to wherever its `deploy/kcl/<env>/main.k` binds it, then `forge obs check <env> --wait --json` proves it (`deploy`). This project's envs are `demo`, `desktop`, `dev`, `preview`, `release`. Names don't establish where an env runs, so never assume one is hosted: its `main.k` declares the bindings, and `forge env render <env>` resolves them (`forge.OnHosted` is Reliant hosting). A new env on hosting is `forge env new <name> --from <env> --bind <workload>=hosted` (`deploy/hosting`). Without hosting billing a backend deploy exits 7 and prints the billing link: give it to the user, don't retry (what is free: `deploy/hosted-capacity`). Never ask for `forge login` under Reliant; signed out, `--claimable` deploys and prints a claim link (`deploy/hosting`).

## When forge itself is wrong — FIX IT

Bad codegen, a lint false positive, a scaffold quirk: fix it in forge when you can (semver at major 1: deprecate, never break), never by hand-editing generated output. Otherwise report it and carry on — forge's own defects only, not this project's code or tests. Never put credentials or file contents in a report:

    forge report-bug --severity high --title "<one line naming the defect>" \
      --actual "<the exact error text>" --evidence "<commands, output, file paths>"

## Project Notes

<!--
  The guide above is forge's framework guide for this project, exactly as
  `reliant forge project memory` prints it at the forge forge.yaml pins: the
  same text Reliant injects into every session ahead of reliant.md. It is a
  copy for agents that are not Reliant (Codex reads AGENTS.md; so do Claude
  Code without a CLAUDE.md, Cursor and others). On a forge bump, refresh it from
  that command (dev-docs/forge-upgrade.md).
-->

**Read `reliant.md` before your first edit.** It holds this project's own rules — semver at major 1 (no breaking change to a public surface), the architecture (api, worker and gateway server roles; only the daemon touches a user's filesystem), the merge and release pipeline, and the test tiers. Reliant loads it automatically; other agents must open it.

Corrections to the generic guide above, for this repo:

- **Shipping.** reliant declares no staging or prod env: `deploy/kcl/` holds `dev`, the deprecated `demo`, and `desktop`, which only files the desktop app's source maps. control-plane deploys it, and releases are control-plane's `rc-train` workflow, never a chat's (`reliant.md`, "Merging to main").
- **Layout.** Protos are `proto/reliant/v1/` (an owner decision), not `proto/services/<svc>/v1/`; the forge ORM is projected into `internal/store/`, behind the hand `internal/db` Repository facade (`internal/db/SCHEMA.md`).
- **The forge to run** is the one `forge.yaml` pins: every `task` runs it, and `reliant forge …` hands off to it.
