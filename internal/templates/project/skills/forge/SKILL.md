---
name: forge
description: Start here for any project — static site, SPA, landing page, docs site, web/mobile app or API. The two paths (frontend-only sites, and apps with an API), the four commands that print forge's whole surface, the pre-flight checklist that stops you hand-writing what forge already scaffolds, and the rules; greenfield end to end and proto authoring are one load away.
---

# Forge — start here

The essentials for any forge project are here; depth is named at the point you
would need it — load it then, not before.

pnpm: `migrations/v1.0.0/pnpm`.

## Three ways in, all first-class

**A frontend with no backend of its own** — a landing page, marketing site,
docs site, SPA or PWA — is a **static site**, and it is a complete forge
project, not a stripped-down one. It gets dev/staging/prod envs from day 0,
`forge env up dev` with no database or cluster, build-once releases promoted by
digest, hosted static hosting (the platform owns the bucket, the CDN and the hostname;
one static site per person is free across the orgs they own without a plan, and
each env's site counts), custom domains, per-env runtime config without a
rebuild, and the component library. Adopt an existing Vite or Next.js app in
place with `forge project new --in-place --kind site`. Either
way, **load `deploy/static-site` now** — it is the whole path, and the service
sequence in `greenfield` does not apply. (For a PWA, forge builds, hosts and ships it;
the service worker and manifest are yours to add.)

**An app with an API** — services, a database, a frontend over generated
Connect RPC hooks — follows the sequence in `greenfield`.

**An app that already exists, in any language** — Express, Django, Rails,
Spring, with its own Dockerfile — is adopted as it is: you write `forge.yaml`
and `deploy/kcl/` declaring its Dockerfile, and none of its files change.
**Load `adopt`**: copy-paste templates, then tiers you can stop after.

## The scaffolded frontend is starter code

The dashboard grid, sidebar nav, sign-in screen and generated CRUD pages exist
to prove the stack is wired — Rails scaffolding, in React. They are all yours to
rewrite, and rewriting them is expected. Forge withholds a visual identity
deliberately: an invented one is harder to replace than an obviously neutral
one. Before showing any of it to a user, load `frontend/design` — it asks for a
brief first and will not invent an aesthetic without one.

A Next.js frontend builds as a **static export** (`output: static`, into
`out/`), the only shape hosted static hosting serves. So every route is
static: detail and edit are `/<entity>/view?id=…` and `/<entity>/edit?id=…`
(`src/lib/entity-routes.ts`). A hand-written `[id]` segment fails
`npm run build`. Depth: `frontend/serving`. If the project's pages predate
v0.1.44 and still use `[id]`, load `migrations/v0.1.44`.

## Where to go next

| You are about to… | Load |
|---|---|
| start an app with an API: `project new`, every service up front, evolve the schema, implement logic, run it, ship it | `greenfield` |
| write a service proto: entities, custom rpcs, two FKs to one parent; the shape in four lines | `authoring` |
| ship a frontend with no backend | `deploy/static-site` |
| deploy anything else | `deploy` |

## Four commands print forge's whole surface

Read these before deciding forge cannot do something. All four are enumerated
from forge's own command tree, recognizers, descriptors and package set, so they
cannot drift the way a document can.

| Command | Answers |
|---|---|
| `forge project capabilities` | **Every verb forge has**, with a one-line summary — plus every `forge lint` analyzer (including the ones hidden from `--help`) and every `// forge:*` marker. |
| `forge project annotations` | The entity-authoring vocabulary: how each proto field is born as a column, the `buf.validate` rules forge projects, and the `(forge.v1.service)` / `(forge.v1.method)` options. |
| `forge project libraries` | **Every `forge/pkg/*` library**, what each is for, and the absolute path to the source this project resolves. |
| `forge project audit --json` | What THIS project currently is: services, rpcs, entities, drift, stubs, ownership. |
| `forge project shapes` | **Where every symbol IS**: each rpc, message, enum, table, handler and hook with `file:line`. Start recon here. |

Add `--json` to any of them to query rather than read.

Start recon with `forge project shapes --grep <Name>`: one symbol across proto,
schema, handlers, hooks and stores with `file:line`. Use `--unimplemented` for
missing handlers and `--service <Name>` to narrow it. Full recipes:
`forge skill load shapes`.

For forge's OWN APIs, use `forge project libraries <pkg>` — every signature,
without hunting generator source. For your generated stores,
`go doc ./internal/db <Entity>Store` renders the full interface.

## Pre-flight: what forge already does for you

Run this list before writing code. Every row is something an earlier run
hand-wrote and then deleted.

| You are about to… | forge already does it | Load |
|---|---|---|
| add a **database entity** | mark the message `// forge:entity`, run bare `forge scaffold`. You get the create-table migration, the CRUD quintet, the managed fields, the entity struct + ORM, CRUD ops, a lifecycle test, the list/detail/new/edit pages, nav, and mock data | `db`, `proto` |
| add a **service** | `forge scaffold service <name>` — proto stub, `internal/handlers/<svc>/`, its compose.go entry (the `server` root runs it — no per-service subcommand or workload), and a test-harness entry | `services` |
| add a **custom rpc** | declare it in the proto, then `forge scaffold rpc <svc> <Name>` — a correctly-signed pb-through method plus a self-destructing test row. Never hand-write the stub | `api` |
| write a **query the generated CRUD can't express** | `internal/db/<entity>_repo_ext.go` is already there, with the repo delegates, the `db.Bun()` raw-SQL handle and `orm.Context` documented in its header | `db`, `db/crud-overrides` |
| build a **UI control** | check `src/components/` and `forge component search <thing>` first. A status badge, an enum select, and the foreign-key picker/name pair are already scaffolded and enum-aware; the library ships 70+ more | `frontend`, `frontend/pages` |
| write a **table-driven test** | `forge/pkg/tdd` + the scaffolded per-rpc `handlers_scaffold_<rpc>_test.go`; contract packages get a `mock_gen.go` you configure by field | `testing/patterns` |
| **port a utility** (errors, auth, middleware, test harness, money, retries) | `forge project libraries` lists every `forge/pkg/*` package with its purpose and source path. Adopt before porting — `forge/pkg/svcerr` is the most re-implemented thing in forge's history | `forge-libraries` |
| **read a forge library's API** | `forge project libraries <pkg>` prints every signature in one call — never `find`, never `grep`, and not `go doc <pkg>`, which shows no methods | `forge-libraries` |
| **see what a generated store offers** | `go doc ./internal/db <Entity>Store` (interfaces render in full), or `forge project shapes --kind store` for the list. Never forge's generator source | `db`, `service-layer` |
| accept **file uploads** | `forge/pkg/blobstore` presigns them into a `forge.Bucket` (MinIO in dev) | `storage/blobstore` |
| call an **LLM** from the backend | declare `forge.LLM` + `forge.llm_env()`, use the official anthropic-go / openai-go clients unmodified; agentic work goes to a Reliant workflow instead | `llm`, `deploy/llm` |
| bring an **existing app in another language** onto forge | nothing to scaffold: `adopt` has the `forge.yaml` + `deploy/kcl` templates (DockerBuild, managed Postgres, migrations job, secrets) and the tiers after them; `forge project new --in-place` refuses such a directory | `adopt` |
| need **dev data** | `forge db seed apply` derives FK-coherent rows from the applied schema; teach it your domain nouns in `db/seeds/vocab.yaml` | `db/seeding` |

`forge scaffold` also births a `worker`, `operator`, `crd`, `binary`, `adapter`,
`webhook`, `package`, `library`, `frontend`, `scenario` and `handler-file` —
`forge scaffold --help` for the arity of each, `services` for which to pick.
Everything scaffolded is yours from birth; re-running with nothing missing is a
clean no-op, and `--dry-run` prints the plan. Forge records none of it in
`forge.yaml`: services, workers, binaries and operators are discovered from the
code that declares them. Frontends are too: a directory under `frontends/` (or a
KCL `forge.Frontend` `path`), with per-frontend codegen knobs in the frontend's
own package.json `"forge"` block (`frontend` skill). Only packages take a config entry.

If the answer is not on this list, `forge project capabilities` has the full verb
set — check there before hand-rolling.

## Skills come from the binary

`forge skill load <name>` always matches the forge you are running. Forge never
writes skill bodies into a project. Non-reliant harnesses get a thin pointer
(`.claude/skills/forge/SKILL.md`, `.cursor/rules/forge.mdc`) that only tells the
agent to run `forge skill search <query>` / `forge skill load <name>`.

```bash
forge skill list              # the catalog: every skill, scope, one-line description
forge skill load db           # print one, authoritative
forge skill search migration  # find one by keyword
```

## Rules

- Load the skill before guessing — with `forge skill load`, not from a copy on disk.
- Check `forge project capabilities` before concluding forge has no verb for this.
- Check `forge project libraries` before writing a utility, and `go doc` to read one.
- Never hand-edit `gen/` or any `*_gen.go`. `internal/app/providers.go` and `internal/app/compose.go` are both yours to wire — forge reconciles compose.go's component set and Deps keys and leaves your value expressions alone (`forge project disown` to opt out).
- Run `forge generate` after any proto or migration change. It never touches business logic or migrations. Re-running it when nothing changed costs seconds, not minutes: a run that finds the tree byte-identical to one a full run verified as its own output says `nothing to do` and stops (`--no-cache` forces every step). So run it whenever in doubt; the minutes are spent only when something it reads actually moved.
- Parallel agents in one checkout may each run `forge generate` and `forge scaffold`. Every run takes the project's lock (`.forge/forge.lock`); a second run prints one `waiting for pid N` line and starts when the first finishes. The OS releases the lock when its holder exits, so never delete the file.
- Declare schema in migrations, never in proto; the ORM follows the schema.
- Use `forge scaffold` to scaffold — never copy-paste an existing directory.
- Hit a defect in forge itself — a command that fails, hangs or generates something wrong, or a wrong lint, skill or doc? Report it: `forge report-bug --severity <low|medium|high|critical> --title "<one line>" --actual "<exact error>" --evidence "<commands, output, paths>"` (or the `report_bug` tool of `forge mcp`). Not for bugs in the user's own code, their failing tests, or your own mistakes; never put credentials or file contents in it. It is the only way forge sends anything (`--dry-run` shows exactly what).
- Uncertain proto/migration/annotation syntax: draft it, run `forge generate`, read the error, fix. Never reverse-engineer forge's `internal/**` source.
- One service per proto package; its owned and generated files co-locate in one `internal/handlers/<svc>/`.
- Use `task test` (what CI runs), not raw `go test`; iterate on `task test:short -- ./internal/<pkg>/...`.
- Gate with `forge lint`, and never pipe it through `head`: the verdict is the LAST line. `forge lint --quiet` prints only what failed plus the verdict. `forge lint --scope internal/handlers/<svc> --scope proto/services/<svc>` lints only your slice of a shared checkout — the whole-project lanes (frontend lint, graph, component-drift, config-reach) are skipped and named, so run it unscoped before merging. Run one lane with its flag (`--read-only-fields`, `--computed-fields`, `--static-export`, `--conventions`, `--migration-safety`; `forge lint --help` lists them all), or name several (`--conventions --contract`) to run each under one verdict. An unwritten `forge:read-only` or `forge:computed` field fails lint, except while the service still holds forge's own unwired rpc stubs — then it warns `pending: implement <Rpc>`. A Next.js frontend that must be a static export (`output: static`, or bound to `forge.OnHosted`/`OnBucket`/`OnFirebase` in any env) fails `--static-export` on anything the export refuses — a `[id]` route, a server action, `cookies()`, next/image without `unoptimized` — each with its file:line and fix.
