<!-- forge:version=1 -->
# shop

This is a **Reliant Forge** project (github.com/reliant-labs/forge), driven by the `forge` CLI — not Atlassian Forge, Foundry's `forge` or Laravel Forge; do not use their commands.

This file is loaded into every session, so it holds only what to know before your first edit. Depth is one `forge skill load <name>` away, named where you need it.

## Engineering disposition

This is a long-lived product. For anything hard to change later — architecture, public APIs, schemas — name the realistic alternatives, argue against your own pick, and choose the durable design over the quick fix. There is almost never real time pressure, so never justify a hack with it. Trivial work: just do it.

## Use forge skills to guide your work

Skills come from the binary, so they match the forge you run. Load one before guessing. These five show the most, soonest:

```bash
forge skill load forge                # start here: what forge already scaffolds, so you never hand-write it
forge project shapes --grep <Name>    # where a symbol is in every layer (rpc, table, handler, hook, store), with file:line
forge env up dev                      # the whole stack locally; every log, browser console included, in .forge/logs/dev/
forge obs endpoints dev               # p50/p95/p99 and error rate per RPC, with no instrumentation code
forge obs check <env> --wait --json   # after a deploy: proves the release good, or names what regressed
```

`forge skill list` is the catalog. `forge project libraries <pkg>` prints a forge library's whole API: never search the filesystem for forge's source. `forge project capabilities` lists every verb; `forge project benefits` lists what this project gets without setup. `forge why` gives the reasons behind them. A skill that disagrees with this project is a forge bug: trust the project and report it (below).

## Critical rules

1. **Never edit generated code** — `gen/`, `*_gen.go`, `*-hooks.ts`. Edit the input (the proto, a migration, config, a package's `contract.go`, or the exported methods of its `//forge:contract` type, from which `contract_gen.go` is derived) and run `forge generate`. It never overwrites your files: handlers, `internal/app/providers.go` and `compose.go` are yours (forge only adds new components and Deps keys to compose.go). Parallel runs queue on `.forge/forge.lock`; an unchanged tree says `nothing to do` in seconds.
2. **Proto is the API's canonical input; migrations are the DB source of truth.** Entities and the ORM are projected from the APPLIED `db/migrations` schema, never from proto.
3. **The gate before you call a phase done:**
   ```
   forge generate && forge lint && go build ./... && task test
   ```
   Never pipe `forge lint` through `head`: the verdict is the LAST line. `--quiet` prints only what failed; `--scope <path>` lints your slice of a shared checkout (run it unscoped before merging).
4. **Iterate on `task test:short -- ./internal/<pkg>/...`** (cached; never add `-count=1` or `-race`, never set a private `GOCACHE`), then `task test` once before you finish; `task ci` is the whole PR gate (`ci`). A test over ~2s calls `testkit.Slow(t, "<why>")` first — gate, don't gut (`testing`). `⏳ waiting for a heavy slot` is a queue, not a hang: never kill the holders (`ci/slots`). A command bounded to minutes (the tests or lint of named packages) may wait in the express lane, ahead of hour-long jobs: `FORGE_SLOT_LANE=express`.
   Anything over ~2 minutes runs in the background (`run_in_background` + `shell_wait`), never detached with `nohup`, `&`, `setsid` or `disown`.
5. **Debugging: `forge skill load debug` first.** Frontend `console.*` calls and uncaught errors are on disk beside the backend logs: `grep '\[browser:error\]' .forge/logs/dev/*.log`.
6. **`auth_required: false` publishes one RPC, never a page.** The pages a signed-out visitor may open are the frontend route guard's `publicExactPaths` (`src/lib/auth/route-guard.tsx`). What a caller may do — access checks, row scoping — is your handler code (`middleware.GetUser`); forge generates none of it (`auth`).

## Architecture in one screen

This is a Go project. `forge scaffold service <svc>` writes:

```
proto/services/<svc>/v1/<svc>.proto   ← the API: messages, CRUD + custom rpcs
gen/services/<svc>/v1/...             ← generated stubs (never edit)
internal/handlers/<svc>/              ← THE SERVICE, one dir: service.go (Deps, New), rpc_<name>.go
                                        (one custom rpc per file — the logic goes HERE), handlers_crud.go
                                        (CRUD shims), handlers_crud_ops_gen.go (generated)
internal/<pkg>/                       ← optional `forge scaffold package <pkg>`: service.go (Deps, the
                                        `//forge:contract` struct, New — yours); contract_gen.go (the derived
                                        Service interface) and mock_gen.go are generated
```

The `*Service` methods ARE the handlers; they reach `internal/db/` and other collaborators through `s.deps`. Load `architecture` for the full tree, and `service-layer` for the package-boundary rules.

## Where logic goes

| Concern | Lives in | Skill |
|---------|----------|-------|
| An rpc's logic | the `*Service` method in `rpc_<name>.go` (CRUD: the shim in `handlers_crud.go`) | `api` |
| Errors crossing the rpc boundary | `return nil, svcerr.Wrap(err)` — never a per-service `connect.Code` switch | `api` |
| Logic a worker or another service shares, or worth transport-free tests | `forge scaffold package <name>`, taken as a Deps interface | `service-layer` |
| Read-check-write transactions | `s.deps.DB.RunTx(ctx, orm.Serializable, …)` — every transaction names its level (`orm.ReadCommitted` only when a lock makes it correct) | `service-layer/transactions` |
| Wiring | `internal/app/providers.go` and `compose.go` (both yours) | `services` |
| DB schema | `db/migrations/` | `db` |

## Proto and schema rules

- An rpc is CRUD only when unary and named exactly `Create<E>`/`Get<E>`/`Update<E>`/`Delete<E>`/`List<Es>` for an entity (a table AND a message). Everything else is custom and yours. A CRUD-named rpc that does more (`CreateTask` also wires dependencies) gets `// forge:custom` above it; never rename one to dodge detection (`proto`).
- Behavior comes from real columns: `created_at`+`updated_at` ⇒ managed timestamps, `deleted_at` ⇒ soft delete. The old proto entity annotations are removed (a proto carrying one no longer compiles): delete leftovers. Fields clients must not write: `db/write-policy`.
- List filter fields **must** be `optional` (otherwise they silently no-op); one service per `.proto` file; shared messages in `proto/shared/v1/types.proto`, never service-to-service imports. `forge lint --conventions` enforces these.

## The scaffolded frontend is starter code

The dashboard's entity tiles, the sidebar nav, the sign-in screen and every generated list/detail/create/edit page prove the stack is wired; they are not a design. They are yours, and rewriting them is expected. Forge withholds a visual identity on purpose — an obviously neutral scaffold is easier to replace than an invented taste. Before showing any of it to a user, load `frontend/design`: it asks the user for a brief first.

## Shipping

`forge env deploy <env>` ships an env to wherever its `deploy/kcl/<env>/main.k` binds it (`deploy`), then `forge obs check <env> --wait --json` proves it. This project's envs are `dev`, `prod`, `staging`. Names don't establish where an env runs, so never assume one is hosted: its `main.k` declares the bindings, and `forge env render <env>` resolves them (`forge.OnHosted` is Reliant hosting). A new env on hosting is `forge env new <name> --from <env> --bind <workload>=hosted` (`deploy/hosting`). One hosted static site per person is free, counted across environments (each env's site is one); anything beyond it needs billing, and until then `env deploy` exits 7 and prints the billing link: give the user that link, don't retry. Never ask for `forge login` under Reliant. Not signed in at all? `--claimable` deploys with no signup and prints a claim link for the user (`deploy/hosting`).

## When forge itself is wrong — FIX IT

If forge emits something wrong — bad codegen, a lint false positive, a scaffold quirk, a missing capability — fix it in forge when you can; never paper over it or hand-edit generated output, which the next `forge generate` overwrites. forge is semver at major 1: a fix keeps the old flag, field or default working beside the new one, marked deprecated. When you cannot fix it here, report it and carry on:

    forge report-bug --severity high --title "<one line naming the defect>" \
      --actual "<the exact error text>" --evidence "<commands, output, file paths>"

Only for defects in forge itself (a command, codegen, lint, skill or doc), not this project's code or tests. Never put credentials or file contents in a report; `--dry-run` shows exactly what would be sent.

## Project Notes

<!-- Add project-specific context, conventions, and open questions here. -->
