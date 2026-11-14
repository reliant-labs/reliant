---
name: greenfield
description: An app with an API, from an empty directory to a deployed release — `forge project new` with every service named up front and the flags that matter, evolving the schema with migrations, where business logic goes, running it locally (`forge env up`), and shipping it to Reliant hosting.
---

# Greenfield: an app with an API, end to end

The sequence for **an app with an API** — services, a database, a frontend
over generated Connect RPC hooks. A frontend-only site follows
`deploy/static-site` instead. Authoring each service proto: `authoring`.

## Greenfield, end to end

```bash
forge project new my-app --mod github.com/acme/my-app \
  --service customers --service jobs --service estimates --frontend web
cd my-app
# author each service proto: entity messages + custom rpcs (rules: `authoring`)
forge scaffold --dry-run     # print the plan
forge scaffold               # birth entities, then generate
forge lint --quiet           # only failures, then the verdict as the last line
go build ./...
forge env up dev             # host services + frontends, on a fresh seeded DB
```

### Name every service up front

`forge project new --service a --service b --frontend web` composes every
service into one graph; `<bin> server`, its default root, serves them all on
one mux. `main.go`'s `cmd.Execute(...)` builds the command tree, with no
command per service: every env runs `server` as `_api`. A later
`forge scaffold service <name>` joins the same way.

Batch later ones: `forge scaffold service sales jobs billing` generates once.

A bare `forge project new` scaffolds **zero services**: binary shell, `internal/app`
composition root, buf/proto scaffolding, Taskfile/CI/deploy. The binary is a
deployment unit, not a domain entity, so forge never invents a service from its name.

### The `project new` flags that matter

| Flag | Purpose |
|---|---|
| `--mod` | Go module path (`github.com/acme/my-app`). Defaults to the project name; set it to the path you will publish under |
| `--service <name>` | repeatable or comma-separated. Name after a domain entity, never after the binary |
| `--frontend <name>` | repeatable or comma-separated Next.js frontends |
| `--in-place` | scaffold into the current directory; takes no positional name |
| `--kind site` | `--in-place` only: adopts the Vite or Next.js app at the directory root as a frontend-only project — writes `forge.yaml` and the `deploy/kcl` envs, no Go (`deploy/static-site`) |
| `--name` | names an `--in-place` project whose directory is a worktree or branch name rather than the product |
| `--path <dir>` | parent directory for the project (default `.`) |

Everything else has a working default — `--kind` (`service` / `cli` / `library`,
each with its own `features:` block), `--harness` (`none` writes no agent file at
all — no AGENTS.md, pointers or .mcp.json — for a repo that keeps its own),
`--disable`, `--go-version`, `--force`: `forge project new --help`.

## Evolving the schema

Migrations lead; projections follow.

```bash
forge db migration new add_login_tracking    # creates the .up.sql only — migrations roll forward, there is no down
# write plain postgres DDL — postgres-pinned, so ::casts, JSONB and TEXT[] all work
forge generate                               # re-project the entity struct/ORM
forge db migrate up --dsn "$DATABASE_URL"    # apply against a live database
```

Generated conversions map the intersection of wire fields and columns by name: a
DB-only column never leaks onto the wire, a wire-only field never reaches the DB.
Add either side freely. Forge stores no ownership of its own — if rows belong to
someone, that is a column you write in the migration, and adding it later costs a
backfill. Depth: `db`.

## Implementing business logic

Write it in `internal/handlers/<svc>/`, on the `*Service` methods. A custom rpc is
a method on `*Service` working with the wire (pb) types and the `internal/db/` ORM
through `s.deps`. CRUD rpcs delegate to `pkg/crud` via the owned shims in
`handlers_crud.go`. There is no `contract.go`/interface split inside a handler
package — the `*Service` methods **are** the handlers. When an rpc's logic
deserves transport-free tests or cross-service reuse, scaffold a standalone
package (`forge scaffold package <name>`) and call it via `s.deps`. Depth: `api`,
`service-layer`, `architecture`.

## Running it

| Command | What it does |
|---|---|
| `forge env up` | Host services + frontends; auto-seeds a fresh dev DB on first boot |
| `forge env up dev` | Full stack: host infra (postgres, dev IdP) + the API (air, hot reload) + frontends |
| `forge env up <env>` | Build + deploy + host launch + frontend dev — reads `deploy/kcl/<env>/` |
| `forge env deploy dev` | Deploy to local k3d (or whatever dev's KCL targets) |
| `forge generate` | Re-project from protos + applied migrations. Safe anytime; never touches business logic |
| `forge lint` | Go + proto + frontend linters. `--quiet`: only failures, verdict last. `--scope <path>`: one slice. `--<lane>` (e.g. `--read-only-fields`): one linter |
| `forge build` | Binaries + frontends. Docker images only with `--docker` or `--push` (`forge env build <env> --push` pushes each image to the reference its own workload declares — a workload's `image` is the only place a registry is set) |
| `task test` / `task test:e2e` | Unit + frontends / E2E (needs a stack up); `task test:all` adds integration |

Auth is enforced in every mode — present a real bearer token (`auth`). There is
no per-service port: one process, the binary's `server` (`_api` in every env),
serves every service on one mux (`PORT`; 8080 in a container), so the frontend
reaches all of them at one origin. Dev runs it under air (hot reload) on the
env's `<project>-dev-api` port, printed in the launch banner. Depth: `dev`,
`deploy`.

## Ship it

Asked to publish or deploy? **Reliant hosting is the default**: a new project's
`staging` and `prod` already bind its services, workers, jobs, managed Postgres
and static frontend to it:

```bash
forge env deploy prod      # build → push → release → plan → apply
forge env status prod      # then verify
```

One static site per person is free (counted across the orgs they own without
a plan); a second (another env's included) or any
compute (service, worker, job, managed DB, image build) needs billing.
Without it the deploy is **queued, not refused**: `env deploy` exits **7** with
the billing link; give it to the user, don't retry. Under Reliant never ask for
`forge login`. Depth: `deploy`, `deploy/hosting`, `deploy/static-site`.
