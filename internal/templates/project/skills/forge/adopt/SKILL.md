---
name: adopt
description: Bring your app — start anywhere. What forge does for an EXISTING repo (any language, any CI, any cloud) with no rewrite and no required order — standalone linters, playbooks, runtime libraries, hosted monitoring (forge obs init), deploys recorded from your own CI (forge release record), parity with your deploy tooling (forge.OnExternal + forge env verify), forge-driven deploys (--kind app) and hosting, your own migration tool, ownership knobs, polyrepo, previews, gates, secrets, codegen — plus the survey checklist, the estimate rubric and the phrasing template, so you can tell the user "easy, ~N min" before touching anything. Load first in a repo with no forge.yaml.
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

`forge project benefits` lists what applies to the repo you are in: outside a
forge project, only what works with no forge.yaml, each with its first step and
an estimate (`--json` to query it, `--all` for everything). Run it during the
survey; the MCP server's `benefits` tool returns the same list.

## Everything here ships

Every command in this skill and the sub-skills it names runs on the forge you
have; there is no "coming soon" item to avoid. You act on a skill literally, so
if a command here disagrees with `forge <command> --help`, trust the binary and
report the skill with `forge report-bug`.

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
| **Dashboards, issues and alerts for the app as it runs** | `forge obs init` mints an ingest token; three `OTEL_*` variables send any app's telemetry to dashboards at `https://forge.reliantlabs.io/forge/p/<project>/observability` (sign-in required); forge need not build or deploy it | `monitoring-any-language`, `obs-check` |
| **Deploys with a memory** | one `forge release record` step after the user's own deploy job: a deploy marker and an `obs check` verdict per release, a Releases page | `record-deploys`, `release-ledger` |
| **Proof before forge touches a deploy** | `forge.OnExternal` drives their own Helm, Fly, Cloud Run or script; `forge env verify` shows no diff first and changes nothing | `external-deploy-tooling`, `env-verify-parity` |
| **Their migration tool, forge's checks** | goose, atlas, flyway, dbmate, golang-migrate or their own command stays; forge lints what it applies | `keep-your-migration-tool` |
| **Forge-driven deploys** | one command declares their Dockerfile; build once, promote by digest, managed Postgres, secrets | `adopt-existing-project`, `build-once-promote-by-digest` |

A project built wholly on forge gets much more (proto→API codegen, an ORM
projected from SQL, scaffolded CRUD, per-worktree dev stacks). Mention it once,
last, as "later, if you want". It is not the pitch.

## The menu: start anywhere

Pick from what the user cares about, not the list order; every row stands on
its own. With no preference, run the linters first (the cheapest proof that
forge works on this repo), then follow the journey: **monitoring → record
deploys → `env verify` shows no diff → forge drives deploys → the rest**
(migrations, secrets, envs, codegen, CI). Each step makes the next one cheaper
to trust, and the user can stop after any of them.

| Entry point | The user gets | Needs | Agent / user time | Load |
|---|---|---|---|---|
| **Linters** | findings with `file:line` and a fix; a step in their CI | nothing (Go lanes: a Go module) | ~10 min / ~2 min | `adopt/linters` |
| **Playbooks** | debugging, testing, migration and deploy guidance; MCP tools for your agent | nothing | ~2 min / ~1 min | below |
| **Runtime libraries** | `forge/pkg` (Go), `@reliantlabs/forge-web-runtime` (Node, browser) | a Go module, or Node ≥ 22 | ~15–30 min / ~2 min | `adopt/libraries` |
| **Monitoring: instrument** | logs, traces and RED metrics in forge's shape, to any OTLP backend | nothing | ~30–60 min / ~5 min | `adopt/monitoring` |
| **Monitoring: hosted dashboards** | dashboards, issues, alerts, `forge obs` for agents, an `obs check` verdict per `service.version` — for any app, wherever it runs | sign-in | ~15 min with an OTel SDK in place, ~30–60 min without / ~10 min | `observability/monitoring-only` |
| **Record deploys** | a deploy marker and an `obs check` verdict per release, a Releases page; their pipeline unchanged | a forge.yaml with `name:`; a CI token | ~15 min / ~5 min | `deploy/record` |
| **Parity: forge drives their tool** | `forge env verify` proves no diff against what runs, read-only; then forge builds, records and promotes while their Helm, Fly, Cloud Run, ECS, Argo or script deploys | a forge env (`adopt/deploy` Tier 1) | ~1 hour / ~5 min | `deploy/external` |
| **Forge drives deploys** | dev/staging/prod, build once, promote by digest, plans, managed Postgres, secrets | a Dockerfile; sign-in; billing for hosted workloads | ~1 hour / ~10 min | `adopt/deploy` |
| **Static site or SPA** | envs, CDN hosting, custom domains | a Vite or Next.js app | ~10–15 min / ~5 min | `deploy/static-site` |
| **Migrations: safety lane** | the safety lane on their SQL | `db/migrations/*.up.sql` | ~15 min / ~3 min | `adopt/linters`, `db/orm-and-safety` |
| **Migrations: keep their tool** | the safety lane on whatever their tool applies, in their directory; `forge db migrate up` runs their tool | goose, atlas, flyway, dbmate, golang-migrate, or a command | ~10 min / ~2 min | `db/own-migration-tool` |
| **Ownership knobs** | forge stays out of what they own: areas, single files, agent files, CI workflows, lint lanes | a forge.yaml | ~10 min / ~2 min | `migration/ownership`, `ci/extending`, `ci/lint-rollout` |
| **Several repositories** | a shared envs repo imported by git pin, or one deploy repo building N app repos' Dockerfiles; no monorepo, no Go | a forge project per repo, or one deploy repo | ~1 hour, +15 min per further repo / ~10 min | `deploy/polyrepo` |
| **Envs: base, previews, gates** | one shared env base; a preview env per PR that expires; promotions refused without passing checks or an approval | forge envs | base ~10, previews ~30, gates ~15 min / ~5 min | `deploy/model`, `deploy/preview`, `deploy/delivery-policy` |
| **Secrets** | a reference per env; values never in config | a forge env (`adopt/deploy` Tier 1) | ~10 min + 2 per secret / 1 min per secret | `secrets` |
| **Hosting** | Reliant hosting, domains, managed Postgres | a forge env | inside `adopt/deploy` | `deploy/hosting` |
| **Codegen** | forge's whole Go stack | a port, per service | a few hours per service | `migration`, `greenfield` |
| **CI** | forge's lanes beside their own jobs | nothing | ~15 min / ~5 min | `adopt/linters`, `ci` |

### The first command of each

```bash
forge project benefits                                   # what applies to this repo, with estimates
forge login && forge obs init prod staging               # hosted monitoring (`obs init [env…]`: one or more envs): registers each env,
                                                         #   mints a token per env, prints its OTEL_* variables: endpoint,
                                                         #   headers (the token, shown once), attributes
forge cloud token create --name ci --scopes deploy:write # record deploys: the CI secret FORGE_CONTROL_PLANE_TOKEN, then after the deploy job:
forge release record --env prod --version "$TAG" --workload api --image "ghcr.io/acme/api@$DIGEST"
forge obs check prod --release "$TAG" --wait             #   …and its verdict: exit 0 good, 1 regressed, 2 not proven, 8 too early
forge project new --in-place --kind app --dry-run        # deploys by forge: what it would write from the Dockerfile; writes nothing
forge env verify prod --image api=ghcr.io/acme/api@sha256:<what runs>   # parity, read-only; exit 0 match, 1 drift, 2 unverified
forge lint --migration-safety                            # migrations; with database.migrations: {dir: migrations, tool: goose} in forge.yaml for their tool
forge env new preview --from staging && forge env preview create --pr 123   # previews from a committed template env
```

What each one cannot do yet, so you say it honestly:

- **Hosted monitoring** needs sign-in, and the ingest token is a server-side
  secret. With no recorded deploy, `obs check` judges the newest
  `service.version` by when telemetry first showed it, and says `marker
  INFERRED`: a weaker marker than a recorded deploy.
- **`release record`** refuses an env that `deploy/kcl/<env>/main.k` declares
  (forge deploys that one). Signed out, or with `FORGE_LEDGER=machine`, it
  writes this machine's ledger, which monitoring cannot see. A **version-only**
  record (no `--image`) needs a control plane that knows record-only releases;
  one that predates them refuses it with an upgrade hint, and accepts a record
  with image digests, unlabelled. Prefer digests: they are what ran.
- **`env verify`** reports a workload with no `status` or `plan` hook as
  **unverified** (exit 2), never as "no diff".
- **The migrations hook** lints every SQL file the tool applies; the rules about
  forge's own file naming and roll-forward policy are reported as `skipped
  (tool: …)`. The ORM projection from it is for a Go project.

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
| Monitoring: hosted (`forge obs init`) | 10 (init, then verify with `forge obs status`) | 10 (sign in; store the token as a secret; set the variables) | +10–30 per language with no OTel SDK; +10 per further service |
| Record deploys (`forge release record`) | 10 | 5 (mint the CI token, store it) | +5 per further pipeline or env |
| Parity (`forge.OnExternal` + `env verify`) | ~50 on top of Tier 1 | 5 | +15 per further workload; less when their tool has a dry run (`helm diff`) |
| Forge deploys (`adopt/deploy` Tier 1) | ~60 | 10 (sign in, approve the first plan, maybe a billing link) | +15 per further workload; +15 if the Dockerfile does not build on its own |
| Static site | 10 | 5 | +10 per further site (billing) |
| Migrations: keep their tool (`database.migrations`) | 10 | 2 | +10–20 to declare the migrate job when forge deploys |
| Ownership knobs | 10 | 2 | none |
| Several repositories | 60 for the first repo | 10 | +15 per further repo |
| Previews / gates / env base | 30 / 15 / 10 | 5 | +10 for the CI recipe that creates and deletes previews |
| Secrets | 10 + 2 per secret | 1 per secret (the user pastes values) | +10 with an external store |
| Codegen | a few hours per service | 20 review | scope it with the user first |

Rules: sum only the steps the user chose; round to 5 minutes; give a range when
two modifiers are uncertain. Never
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
- Hosted dashboards on top: `forge obs init prod` and a check **~10 min**, and the
  variables go in the same Helm values; then `forge release record` after the
  Helm job **~10 min**, so each deploy gets a verdict.
- Total **~75 min of agent time; ~15 min of yours** (sign in, set the
  variables and one CI secret in the Helm values and the repo; read the diffs).

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

The playbooks marked `applies: any` are worth reading in any repo: `debug`,
`testing`, `forge-libraries`, `observability/tracing-lint`,
`observability/monitoring-only`, `deploy/record`, `deploy/external`,
`deploy/polyrepo`, `deploy/static-site`, `db/orm-and-safety`,
`db/own-migration-tool`, `migration/ownership` and this one. The rest describe a forge project:
adapt them, do not obey them blindly.

## Leaving is easy too

Trust is part of the pitch. Say these, with the commands:

- **Lint lanes, libraries and playbooks** leave nothing behind but a CI step
  and ordinary imports (Apache-2.0).
- **Telemetry** is standard OTLP and the standard `OTEL_*` environment: point
  it elsewhere by changing `OTEL_EXPORTER_OTLP_ENDPOINT`. `forge obs tokens
  revoke` cuts an ingest token off within minutes.
- **Recorded deploys**: delete the `forge release record` step; their
  pipeline never depended on it. `forge cloud token revoke` retires the CI token.
- **External hooks**: `forge.OnExternal` runs their own tool; dropping the
  binding leaves that tool exactly as it was.
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
5. **The user signs in and places credentials.** `forge login`, the ingest
   token `obs init` prints once, the CI token: you run nothing that writes to
   their account before they say yes.
6. **Never print, log or commit a secret.** The user places tokens and values.
7. **Report forge's defects** (a command that fails, a skill that disagrees
   with the binary) with `forge report-bug`; never the user's own bugs.
8. **Ask forge before guessing:** `forge project capabilities` lists every
   verb, `forge project libraries <pkg>` prints a package's API.
