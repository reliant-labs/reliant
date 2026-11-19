---
name: v1.0.12
description: Upgrade a project to forge v1.0.12. Every sectioned generated file (contract mocks, config_gen.k, scenario-rpcs_gen.ts) names each section and puts its hash on the next line, so a part added beside a changed one merges with plain git, the read-only and dashboard grants become one constant script per role plus one row file per table (db/grants/readonly/, db/grants/dashboard/), each entity's store is its own internal/db/<entity>_store_gen.go with the aggregate Store now a struct, each entity's mock dispatch rows are their own src/mocks/entities/<slug>_gen.ts, and the Next.js nav and dashboard read the entities from generated per-entity modules instead of being re-rendered. Use when the project commits a mock_gen.go, deploy/kcl/config_gen.k, a scenario-rpcs_gen.ts, db/grants/ or internal/db/store_gen.go.
version: v1.0.12
detection: test -f db/grants/readonly_gen.sql -o -f deploy/kcl/config_gen.k -o -f internal/db/store_gen.go && exit 0; ls frontends/*/src/mocks/scenario-rpcs_gen.ts >/dev/null 2>&1 && exit 0; git grep -q -l 'forge:section forge:hash=' 2>/dev/null && exit 0; exit 1
---

# Upgrading to v1.0.12

The first `forge generate` on v1.0.12 rewrites everything below. Commit what it writes in one change. Nothing here needs a hand edit.

## 1. Sections are named, and the hash moves to the line after the name

**Use when** the project commits a contract mock (`mock_gen.go`), `deploy/kcl/config_gen.k` or a `src/mocks/scenario-rpcs_gen.ts`.

**What changed.** v1.0.10 and v1.0.11 divided these files into sections, each certified by its own hash, carried on the section's first line: `// forge:section forge:hash=…`. A part inserted into the file lands directly before the first line of the part after it, so a pull request that changed one part and another that added a part right before it touched neighbouring lines, and git conflicted. In a project with two services this is the first thing that happens: a new `orders` service sorts directly before a changed `users` service.

A section now begins with a header that names its part and never changes while the part exists, `forge:section <name>` (the mock type, the config block, the service), and the part's hash is on the line after it, `forge:hash=…`. The unchanged header is what keeps the two pull requests apart. Unique names also keep git from aligning one part's header with another's. Hand-edit detection is unchanged: a file is forge's untouched render only when every hash verifies.

In `config_gen.k`, the `<SCHEMA>_SENSITIVE_ENV` and `<SCHEMA>_OPTIONAL_SECRET_ENV` lists now hold one entry per line, grouped by config block like the env map, so two blocks gaining credentials no longer rewrite one shared line. Their values are unchanged.

**What generate does.** It restamps every sectioned file into the named form. Files in the old form still verify as forge's own render, so nothing reads as hand-edited during the upgrade.

## 2. The grants are one script per role and one row file per table

**Use when** the project commits `db/grants/readonly_gen.sql`.

**What changed.** The grants gathered every table into one file. Even with a section per table (v1.0.11), two pull requests that each add a table sorting between the same two neighbours insert at one point of that file, and line merging cannot order two insertions there. Each role's script (`db/grants/readonly_gen.sql`, `db/grants/dashboard_gen.sql`) is now CONSTANT: it depends only on the role. Each table is its own row file, `db/grants/readonly/<schema>.<table>_gen.sql` and `db/grants/dashboard/<schema>.<table>_gen.sql`. A row is the table's `(schema, table, readable columns)`, with its withheld columns (`forge:secret`, and credential-shaped columns nobody decided) noted beside it. The migrator splices the rows into the script in file-name order and applies one statement, as before. Two pull requests that add tables now add files, and dropping a table deletes its rows.

**What generate does.** It rewrites both scripts, writes the row files, and changes `db/embed_gen.go` to embed the whole `db/grants` directory (`//go:embed migrations/*.sql grants`). Commit both new directories. A binary built before this regeneration still applies the one-file script it embeds.

`forge lint --db-grants` now judges every dashboard row file too, and reports a row no table accounts for as stale.

## 3. One store file per entity, and the aggregate `Store` is a struct

**Use when** the project has entities (`internal/db/store_gen.go` exists).

**What changed.** Every entity's store interface (`<Entity>Store`) and its adapter lived in `store_gen.go`, and the aggregate `Store` interface listed one accessor per entity, so two pull requests that each added an entity edited one list and conflicted. Each entity's store is now its own `<entity>_store_gen.go`, which also declares the aggregate's accessor for it as a method (`func (s Store) Estimates() EstimateStore`). A Go interface's method set cannot span files, so the aggregate `Store` in `store_gen.go` is now a **struct** with `NewStore(handle)` and `WithTx(tx)`, and names no entity.

Code that calls the aggregate (`db.NewStore(client).Estimates().GetEstimateByID(…)`, a `Deps{DB db.Store}` field that compose fills) compiles unchanged. Code that treated it as an interface does not: a fake implementing `db.Store`, or a test using the `MockStore` forge generated for a `db.Store` Deps field (no longer generated, because a struct cannot be mocked). Move such a package to the per-entity stores (`Deps{Estimates db.EstimateStore}`), which forge mocks; `forge generate` names this fix when the build fails on it.

## 4. The mock dispatch table is one module per entity

**Use when** a frontend has `src/lib/mock-transport_gen.ts`.

**What changed.** The mock transport listed every entity's dispatch rows and imported every entity's schemas and fixtures, so two pull requests that each added an entity conflicted in it. Each entity's rows are now `src/mocks/entities/<slug>_gen.ts` (exporting `descriptors`, one row per service that serves the entity), and `mock-transport_gen.ts` names no entity: it gathers that directory with the bundler's own glob, `import.meta.webpackContext` under webpack (`next dev` and `next build`, which forge pins to webpack) and `import.meta.glob` under Vite and vitest. The rows and the fixtures are unchanged.

**What generate does.** It writes the entity modules and the new transport. Commit `src/mocks/entities/`. A Next.js frontend you moved to Turbopack has neither glob: keep it on webpack, or disown `mock-transport_gen.ts` and list the modules yourself.

## 5. The nav and the dashboard read the entities from generated modules

**Use when** a Next.js frontend has `src/components/nav.tsx` and `src/app/dashboard.tsx`.

**What changed.** forge re-rendered these two (they are yours) with every entity's link and tile inline, until your first edit. So every pull request that added an entity rewrote both, and their birth hashes under `.forge/scaffolded/`, and two such pull requests conflicted there. Each entity's sidebar route is now `src/lib/nav-routes/<slug>_gen.ts`, and each entity's tile is `src/components/dashboard-tiles/<slug>_gen.tsx`. `src/lib/nav-routes_gen.ts` (`ENTITY_ROUTES`) and `src/components/dashboard-tiles_gen.ts` (`DASHBOARD_TILES`) gather those directories with the bundler's own glob, like the mock transport (section 4). The scaffolded `nav.tsx` spreads `ENTITY_ROUTES` into `ALL_ROUTES`, and `dashboard.tsx` renders `DASHBOARD_TILES`, so neither changes when an entity is added, and a nav or dashboard you have edited still gains new entities.

**What generate does.** It writes the per-entity modules and the two registries. A `nav.tsx` or `dashboard.tsx` you never edited is refreshed once into the new form. One you edited is yours and left alone: to pick up new entities automatically, use `ENTITY_ROUTES` and `DASHBOARD_TILES` in it (`forge generate` stops reporting unlinked routes for a nav that names `ENTITY_ROUTES`).

### Still a conflict

Two pull requests that change the same part (one interface, one config block, one service, one table) still conflict there, because both changed it. Inside one sectioned file, line merging also cannot take two pull requests that each add a part between the same two neighbours, or one that deletes a part while the other changes the part beside it. `forge generate` on the merged inputs resolves each of them.
