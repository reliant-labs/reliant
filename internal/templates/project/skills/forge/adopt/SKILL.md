---
name: adopt
description: Bring your app — start anywhere. What forge does for an EXISTING repo (any language, any CI, any cloud) with no rewrite and no required order — standalone linters, playbooks, runtime libraries, monitoring, forge-driven deploys and hosting, migrations, secrets, codegen — plus the survey checklist, the estimate rubric and the phrasing template, so you can tell the user "easy, ~N min" before touching anything. Load first in a repo with no forge.yaml.
emit: both
applies: any
---

# Bring your app — start anywhere

You are in a repo that already exists: its own layout, CI, deploy method and
database tool. **Forge is a set of independent pieces. Each one is usable on its
own, in any language, and none requires the others.** Nothing here asks the
user to rewrite code, move files or give up their CI. Every step is additive,
and every step can be the last.

Already adopted (a `forge.yaml` and `deploy/kcl` declaring the app's
Dockerfile)? Your path is `adopt/deploy`, the tiers after Tier 1.

This skill has two jobs: to let you tell the user, truthfully and briefly, what
forge would do for this repo and what it costs; and to send you to the
sub-skill that does the work. Survey before you quote a number.

## What ships, and what does not

You act on a skill literally, so read this rule first. Everything in this skill
and its sub-skills (`adopt/linters`, `adopt/monitoring`, `adopt/libraries`,
`adopt/deploy`) runs on the forge you have. Items under **Coming soon** do not
exist yet: never run them, never write their config keys, never promise them.
You may tell the user they are coming. If a command here disagrees with
`forge <command> --help`, trust the binary and report the skill with
`forge report-bug`.

## Why it is worth the user's time

Say these in the user's terms. Each names its entry in forge's benefits registry
(`docs/benefits.md#<id>`), where the evidence behind it is checked.

| Outcome | What the repo gets, as it is | Benefit ids |
|---|---|---|
| **Migrations that can't take prod down** | `forge lint --migration-safety` refuses `DROP COLUMN`, `ADD COLUMN … NOT NULL` with no default, `CREATE INDEX CONCURRENTLY` beside other statements, each with a fix | `migration-safety` |
| **Requests that stay in one trace** (Go) | `forge lint --tracing` names each log call without its context and each hand-built HTTP client; `--fix` rewrites the log calls | `lint-rules-tracing` |
| **Charts that don't move with the server's timezone** (Go + SQL) | `forge lint --time-bucketing` flags a two-argument `date_trunc` over a `TIMESTAMPTZ` | `time-bucketing-lint` |
| **A fast test tier that stays fast, and passes that prove something** (Go) | `--slow-tests`, `forge ci test-budget`, `forge ci verify-test-run` over `go test -json` | `slow-test-enforcement`, `test-budget`, `skipped-pass-detector` |
| **Real Postgres in tests, no Docker** (Go) | `pgtest.New()` from `forge/pkg` | `pgtest-real-postgres` |
| **Errors that never leak internals** (Go, Connect) | `svcerr.Wrap(err)`: a classified code out, the driver's SQL and DSN stay in the log | `svcerr-redaction` |
| **Telemetry with no SDK to configure** (Node) | `initNodeTelemetry` ships logs, uncaught errors and spans over OTLP/HTTP | `node-telemetry` |
| **Playbooks that match the tool** | 180+ skills served by the forge binary, and its MCP tools | `skills-in-the-binary`, `mcp-server` |
| **A static site or SPA live, with environments** | an existing Vite or Next.js app adopted in place; hosted, one site free | `static-site-first-class`, `free-static-site`, `claimable-deploys` |
| **Deploys with a memory, and dashboards** | once forge deploys the app: build once, promote by digest, a release ledger, `obs check` verdicts, dashboards at `https://forge.reliantlabs.io/forge/p/<project>/observability` (sign-in required) | `build-once-promote-by-digest`, `release-ledger`, `obs-check`, `hosted-observability-store` |

A project built wholly on forge gets much more (proto→API codegen, an ORM
projected from SQL, scaffolded CRUD, per-worktree dev stacks). Mention it once,
last, as "later, if you want". It is not the pitch.

## The menu: start anywhere

Pick from what the user cares about, not the list order. If they have no
preference, **start with the linters** (the cheapest proof that forge works on
this repo), then monitoring.

| Entry point | The user gets | Needs | Agent / user time | Load |
|---|---|---|---|---|
| **Linters** | findings with `file:line` and a fix; a step in their CI | nothing (Go lanes: a Go module) | ~10 min / ~2 min | `adopt/linters` |
| **Playbooks** | debugging, testing, migration and deploy guidance; MCP tools for your agent | nothing | ~2 min / ~1 min | below |
| **Runtime libraries** | `forge/pkg` (Go), `@reliantlabs/forge-web-runtime` (Node, browser) | a Go module, or Node ≥ 22 | ~15–30 min / ~2 min | `adopt/libraries` |
| **Monitoring: instrument** | logs, traces and RED metrics in forge's shape, to any OTLP backend | nothing | ~30–60 min / ~5 min | `adopt/monitoring` |
| **Monitoring: hosted dashboards** | dashboards, issues, alerts, `obs check` per release | sign-in; forge deploys the app | Tier 2 of `adopt/deploy` | `adopt/deploy` |
| **Forge drives deploys** | dev/staging/prod, build once, promote by digest, plans, managed Postgres, secrets | a Dockerfile; sign-in; billing for hosted workloads | ~1 hour / ~10 min | `adopt/deploy` |
| **Static site or SPA** | envs, CDN hosting, custom domains | a Vite or Next.js app | ~10–15 min / ~5 min | `deploy/static-site` |
| **Migrations** | the safety lane on their SQL; their migration tool unchanged | `db/migrations/*.up.sql` | ~15 min / ~3 min | `adopt/linters`, `db/orm-and-safety` |
| **Secrets** | a reference per env; values never in config | a forge env (`adopt/deploy` Tier 1) | ~10 min + 2 per secret / 1 min per secret | `secrets` |
| **Hosting** | Reliant hosting, domains, managed Postgres | a forge env | inside `adopt/deploy` | `deploy/hosting` |
| **Codegen** | forge's whole Go stack | a port, per service | a few hours per service | `migration`, `greenfield` |
| **CI** | forge's lanes beside their own jobs | nothing | ~15 min / ~5 min | `adopt/linters`, `ci` |

Coming soon (do not run or promise; say "coming"):

- **Monitoring with no forge deploy**: an `obs init` command, user-minted ingest
  tokens and a public OTLP endpoint, so an app forge does not deploy reaches the
  hosted dashboards; and `obs check` on an "inferred" deploy marker.
- **Record deploys from the user's CI**: a `release record` command.
- **Parity**: `forge.OnExternal` release hooks and an `env verify` command,
  which shows NO DIFF against what is live before forge drives anything.
- **Deploy-only adoption of a Dockerfile app**: `forge project new --in-place
  --kind app` (forge#2085), writing only `forge.yaml` and `deploy/kcl`. Today
  `adopt/deploy` has the templates you write by hand.
- **The migrations hook**: forge running the user's own migration tool
  (`database.migrations`), and migration-safety rules dialled outside a forge
  project.

## Survey: read, change nothing

Record each answer; they feed the rubric.

1. **Languages and frameworks.** Read the manifests (`go.mod`,
   `package.json`, `pyproject.toml`/`requirements*.txt`, `pom.xml`/
   `build.gradle`, `Gemfile`, `Cargo.toml`). Name the HTTP framework per service.
2. **Services.** How many deployable units: Dockerfiles, `cmd/*/main.go`,
   `services/*`, compose services, workers, cron jobs.
3. **Existing observability.** `rg -n "opentelemetry|@opentelemetry|otel|dd-trace|newrelic|sentry|prom-client|pino|winston|zap|slog|structlog|logrus"`
   over manifests and source. An OTel SDK already? A JSON logger? A middleware
   layer where one more middleware is a small change?
4. **Migrations.** Tool (goose, golang-migrate, Flyway, Alembic, Prisma,
   Rails, Django, Atlas, dbmate, raw SQL), directory, and file names. forge's
   lane reads `db/migrations/*.up.sql` only.
5. **CI.** `.github/workflows/`, `.gitlab-ci.yml`, `Jenkinsfile`,
   `.circleci/`. Which job builds, which deploys.
6. **Deploy.** Helm, kustomize, Argo/Flux, Fly, Cloud Run, ECS, compose, a
   PaaS, a script. A Dockerfile per service?
7. **Secrets today.** CI secrets, Vault, a cloud secret manager, `.env`.
8. **Environments.** Which exist and how they differ.
9. **Test command**, and whether it is fast. forge never replaces it.
10. **Who must click.** Which steps need a human: `forge login`, a CI secret,
    a DNS record, a billing link.

## The estimate rubric

Minutes are **agent time**, then **user hands-on time**. Most of every step is
yours; the user mostly approves a diff or pastes a token. State both; never fold
yours into theirs.

| Step | Agent | User | Scaling |
|---|---|---|---|
| Linters: first findings | 5 | 1 | +5 to add the CI step; +5 if migrations need a `--scope` list (legacy `.down.sql` files) |
| Playbooks / MCP | 2 | 1 | none |
| Libraries | 15 | 2 | +10 per further service |
| Monitoring: instrument | 25 for the first service | 5 (set env vars where the app runs) | **+10 per further service in the same framework**, **+25 per further language or framework**, −10 if an OTel SDK is already there, −5 if a middleware layer exists, +10 with no structured logger |
| Monitoring: verify | 10 | 0–2 | +5 per service you must exercise |
| Forge deploys (`adopt/deploy` Tier 1) | ~60 | 10 (sign in, approve the first plan, maybe a billing link) | +15 per further workload; +15 if the Dockerfile does not build on its own |
| Static site | 10 | 5 | +10 per further site (billing) |
| Secrets | 10 + 2 per secret | 1 per secret (the user pastes values) | +10 with an external store |
| Codegen | a few hours per service | 20 review | scope it with the user first |

Rules: sum only the steps the user chose; round to 5 minutes; give a range when
two modifiers are uncertain. Never quote a number for a Coming-soon item. Never
say "free" beyond the one free static site: a hosted workload or database needs
billing (the deploy queues, exits 7 and prints the link). If the survey breaks
an assumption (no Dockerfile, migrations outside `db/migrations`, a polyrepo),
say so and revise the number out loud. **When the honest estimate is long, say
so.** "Easy" is a claim the numbers must back.

**Worked example.** Two Express services, one already on `pino`; GitHub
Actions deploys with Helm; Postgres with goose files under `db/migrations`
named `*.up.sql`; the user wants monitoring, and linters too.

- Linters: 5 + 5 (CI step) = **~10 min**
- Monitoring: 25 + 10 (second service) − 5 (Express middleware) = **~30 min**,
  verify **~15 min**
- Total **~55 min of agent time; ~10 min of yours** (set two env vars in the
  Helm values; read two diffs).

## The phrasing template

Present the plan before changing anything, then wait for a yes.

```
I looked at the repo: [N services, language/framework, CI, deploy method,
migration tool, existing observability].

Adding [piece] here is easy — about [T] min of my time and [U] min of yours
(you mostly [approve a diff / set an env var / paste a token]):

  1. [step] (~[m] min) — [what you can see afterwards]
  2. [step] (~[m] min) — [...]

It is additive: [one small PR / these N files]; nothing in your layout moves,
and you can stop after any step. To undo it: [the exit for this piece].
[Not available yet, so I won't touch it: …]

Shall I start with step 1?
```

Never write "easy" without the numbers, or the numbers without the undo line.
After each step, verify (each sub-skill says how), report what you saw, and only
then offer the next piece.

## Playbooks: forge's skills and MCP server in any repo

```bash
forge skill list                     # in a repo with no forge.yaml, APPLIES TO YOUR REPO comes first
forge skill search migration         # find by keyword; matches bodies too
forge skill load debug               # print one; it always matches the binary you run
claude mcp add forge -- npx -y @reliantlabs/forge mcp   # MCP tools, no install
```

The playbooks marked `applies: any` (`debug`, `testing`, `forge-libraries`,
`observability/tracing-lint`, `db/orm-and-safety`, `deploy/static-site` and
this one) are worth reading in any repo. The rest describe a forge project:
adapt them, do not obey them blindly.

## Leaving is easy too

Trust is part of the pitch. Say these, with the commands:

- **Lint lanes, libraries and playbooks** leave nothing behind but a CI step
  and ordinary imports (Apache-2.0).
- **Telemetry** is standard OTLP and the standard `OTEL_*` environment: point
  it elsewhere by changing `OTEL_EXPORTER_OTLP_ENDPOINT`.
- **Kubernetes YAML**: `forge env render <env>` prints every object as plain
  YAML and contacts no cluster.
- **Deploy history**: `forge ledger where` names the backend, `forge ledger
  export` writes it as files, `forge release verify` proves each artifact still
  exists.
- **Code**: `forge project disown <file>` makes a generated file yours for good.
- **Secrets, the honest exception**: hosted secrets are write-only by design;
  the user keeps their own copy, or uses an external provider.
- **Removing forge**: delete `forge.yaml`, `deploy/kcl/` and `.forge/`; nothing
  in the app imports forge unless they chose its libraries or codegen.

## Rules for the agent

1. **Never rewrite the user's layout.** New forge files go in their own paths;
   changes to theirs are the smallest diff that works.
2. **Add, don't replace.** Keep their CI, test command, migration tool and
   deploy command; add a step beside them.
3. **One entry point at a time.** Finish, verify, show what they gained, then
   offer the next. Never bundle pieces into one PR.
4. **Say the estimate, the split and the undo** before you start.
5. **Never run or write a Coming-soon item.**
6. **Never print, log or commit a secret.** The user places tokens and values.
7. **Report forge's defects** (a command that fails, a skill that disagrees
   with the binary) with `forge report-bug`; never the user's own bugs.
8. **Ask forge before guessing:** `forge project capabilities` lists every
   verb, `forge project libraries <pkg>` prints a package's API.
