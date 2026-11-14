---
name: adopt/deploy
description: Forge deploys an existing app in any language (Express, Django, Rails, Spring, …) from its own Dockerfile, without rewriting it, in tiers you can stop after — 1 ship it (`forge project new --in-place --kind app` writes forge.yaml + deploy/kcl from its Dockerfile and compose file — DockerBuild, managed Postgres, migrations job, secrets — then the dev loop and deploy; the templates it writes are here to review or write by hand), 2 see it (OpenTelemetry → dashboards, alerts, obs check verdicts), 3 harden it (promotion, CI, health endpoints), 4 move in (port to forge's Go stack only when it pays).
emit: both
applies: any
---

# Adopt: forge deploys your app, in any language

This is the "forge drives deploys" entry point of `adopt`, the menu of every
way into forge; load that first if you have not chosen one. An app that
already exists keeps its language, its framework and every file.
forge builds it from its own Dockerfile and deploys the image: environments,
build-once releases promoted by digest, plans, rollout waits, managed Postgres
and secrets on Reliant hosting work as they do for code forge generated.

`forge project new --in-place --kind app` writes the declarations from what it
reads in the app (Tier 1); the templates below are what it writes, and what
you review and edit so they fit the app. Work through the tiers in order and **stop after any
of them**: each one is complete, and each later one costs more.

| Tier | You get | Cost |
|---|---|---|
| 1 Ship it | dev, staging and prod envs; the app on Reliant hosting with managed Postgres and secrets; releases you can redeploy | ~an hour, no app code changed |
| 2 See it | traces, metrics, dashboards, alerts and real `obs check` verdicts | an OpenTelemetry SDK in the app |
| 3 Harden it | gated promotion, CI deploys, real health checks | a little config per env |
| 4 Move in | forge's Go stack (generated API, ORM, typed frontend hooks) | a port; only when it pays |

Not this skill: a Vite or Next.js app with no server is a **static site**
(`forge project new --in-place --kind site --name <name>`, then `deploy/static-site`).
A Go server with its own protos is Tier 4 from the start (`migration`).
Never run `forge project new --in-place` with no `--kind` in the app: in a
directory with a Dockerfile and no go.mod it refuses and points here.

## Tier 1: Ship it

**One command writes the declarations; you review them.** In the app's
directory:

```bash
forge project new --in-place --kind app --dry-run     # what it found, and every file it would write; writes nothing
forge project new --in-place --kind app --name myapp  # write them
```

It writes `forge.yaml` and `deploy/kcl/` (kcl.mod, workloads.k, and the dev,
staging and prod envs below) and nothing else: no Go module, Taskfile, CI
workflow or memory file, and none of the app's files change except forge's
block appended to `.gitignore`. It refuses in a directory that already has a
`forge.yaml`. What it reads, and what it makes of it:

| It reads | It writes |
|---|---|
| a `Dockerfile` at the root and in each top-level directory, or each compose service with a `build:` | one workload each, built with `forge.DockerBuild` from the same Dockerfile and context |
| the compose service's published port, else `EXPOSE`, else a `--port` / `-b` in the start command, else `ENV PORT` | its port, `PORT` in its env, a tcp probe |
| a compose service on a Postgres image, or a Postgres driver in the app's dependencies | a managed Postgres on staging and prod, `DATABASE_URL` (or the variable the compose file put its URL in) from it |
| a compose service that migrates, a `migrate` script, prisma / knex / sequelize-cli / drizzle-kit, `alembic.ini`, Django's `manage.py`, Rails' `db/migrate` | a `migrate` job from the app's image, before every rollout |
| compose env: literals, and credentials (`*_SECRET`, `*_TOKEN`, `*PASSWORD*`, `*_KEY`, `${…}`) | literals in workloads.k; each credential a `forge.ManagedSecret` on staging and prod, set with `forge secret set` |
| a compose file with a built service | dev runs that compose file (`forge.OnCompose`), exactly as `docker compose up` did |
| no compose file | dev runs each workload's dev command (`package.json` `dev`/`start`, the Procfile's `web`, else the Dockerfile's `CMD`) on the host, beside a forge host-native Postgres |
| Redis / Valkey / Memcached in compose | a hosted workload of that image; dev tools (adminer, mailpit) and other services stay dev-only |

It prints what it **detected**, what it **assumed**, what it **left out**,
and the next commands. Every assumption is also a `# check:` line in the file
it shaped: read each one before the first deploy. `--claimable` sizes prod
for a deploy with no account (one service, its migration job and one
database; no secret values; no outbound internet until claimed).
`--harness <name>` adds that harness's agent files; without it there are none
(Reliant reads the project's memory from forge.yaml).

**Before you trust it**, read the app the way the summary cannot: what each
env var falls back to when it is unset (a `JWT_SECRET || "superSecret"` is a
production hole if the secret is never delivered), and whether its start
command reads `PORT`.

### 1. The Dockerfile must build on its own

A deploy runs `docker build` with the Dockerfile and its context and nothing
else. An upstream Dockerfile that `COPY`s a `dist/` built on your machine
fails there: build inside it (a multi-stage `npm ci && npm run build`, `pip
install`, `mvn package`). If the app migrates its schema, keep the migration
tool in the final image, because the migration job runs that image. Prove it
with `docker build .`. Add `deploy`, `.forge` and `secrets` to
`.dockerignore` (a KCL-only edit then leaves the image digest alone).

### 2. The files, for review and edits

The command writes the files below, with the values it detected. They are the
reference for reading and editing what it wrote, and for writing them by hand
where it cannot read the app (no Dockerfile yet, a build it does not
understand). By hand: replace `myapp` with the project's name (lowercase,
digits, hyphens), `api` with the workload's, `3000` with the app's port, and
the two commands marked `# yours`. No database? Delete the `migrate` workload
and every line marked `# db`.

```yaml
# forge.yaml
name: myapp
forge_version: v0.0.0  # yours: the version `forge version` prints
```

```toml
# deploy/kcl/kcl.mod
[package]
name = "myapp-deploy"
edition = "v0.11.0"
version = "0.0.1"

[dependencies]
```

```python
# deploy/kcl/workloads.k
"""What myapp runs, declared once. Each env's main.k binds it."""
import forge
import forge.workloads as fw
import forge.tiers

api = fw.Workload {
    name = "api"
    kind = "service"
    # Bare: hosting puts the build under <registry>/<org>/myapp/api.
    image = "api"
    build = forge.DockerBuild {dockerfile = "Dockerfile", context = "."}
    ports = [fw.Port {name = "http", port = 3000, expose = True}]
    env = {PORT = "3000"}
    # The app serves no /readyz of forge's. Up once it accepts connections; with a
    # health endpoint, probe it: tiers.Probes {readinessPath = "/health"}.
    probes = tiers.Probes {tcp = True}
}

# Schema migrations, from the same image, before every rollout.
migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    image = api.image
    build = api.build
    command = ["npx", "prisma", "migrate", "deploy"]  # yours: the app's migrate command, as run in the image
    before = [fw.BEFORE_ALL]
}
```

```python
# deploy/kcl/dev/main.k
"""dev: the app as a host process, beside a host-native Postgres."""
import forge
import forge.workloads as fw
import kcl_plugin.forge as plugin
import ..workloads as wl

_port = plugin.resolve_port("myapp-dev-api", 3000)
_pg_port = plugin.resolve_port("myapp-dev-postgres", 5432)  # db
_database_url = "postgres://postgres:postgres@localhost:${_pg_port}/myapp?sslmode=disable"  # db

_on_host = lambda w: fw.Workload, command: [str] -> fw.Workload {
    w | {
        command = command
        env = w.env | {
            PORT = str(_port)
            DATABASE_URL = _database_url  # db
        }
        runtime = forge.OnHost {listen_ports = [_port] if w.kind == "service" else []}
    }
}

_bundle = forge.Bundle {
    project = "myapp"
    lifecycle = "local"
    workloads = [
        _on_host(wl.migrate, ["npx", "prisma", "migrate", "deploy"])  # db  # yours
        _on_host(wl.api, ["npm", "run", "dev"]) | {env.JWT_SECRET = forge.ManagedSecret {name = "JWT_SECRET"}}  # yours: your dev command; one secret per credential
    ]
    infra = [forge.HostInfra {name = "postgres", port = _pg_port, database = "myapp"}]  # db
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
}
output = forge.render(_bundle)
```

```python
# deploy/kcl/staging/main.k
"""staging: on Reliant hosting, with a managed Postgres."""
import forge
import forge.workloads as fw
import ..workloads as wl

# Its name may not be a workload's. RETAIN: deleting it never drops the data.
_db = forge.ManagedDatabase {name = "myapp", runtime = forge.OnHosted {}}  # db

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {
        image = w.image[w.image.rfind("/") + 1:]
        env = w.env | {DATABASE_URL = forge.DatabaseRef {name = _db.name}}  # db
        runtime = forge.OnHosted {}
    }
}

_bundle = forge.Bundle {
    project = "myapp"
    control_plane = forge.ControlPlane {}
    workloads = [
        _hosted(wl.migrate)  # db
        _hosted(wl.api) | {env.JWT_SECRET = forge.ManagedSecret {name = "JWT_SECRET"}}  # yours: one per credential
    ]
    databases = [_db]  # db
    secret_provider = forge.HostedSecrets {}
}
output = forge.render(_bundle)
```

`deploy/kcl/prod/main.k` is staging's file with `prod` in its docstring: same
bindings, its own database and secrets.

With a compose file the command binds dev to it instead of to host processes.
The compose file owns each container (build, ports, healthcheck, env), so dev
takes only the workload's name, and the compose Postgres stays the dev
database:

```python
# dev with a compose file (deploy/kcl/dev/main.k)
import forge
import forge.workloads as fw
import ..workloads as wl

_compose = lambda w: fw.Workload, service: str -> fw.Workload {
    fw.Workload {name = w.name, runtime = forge.OnCompose {file = "docker-compose.yml", service = service}}
}

_bundle = forge.Bundle {
    project = "myapp"
    lifecycle = "local"
    workloads = [
        fw.Workload {name = "db", runtime = forge.OnCompose {file = "docker-compose.yml", service = "db"}}
        _compose(wl.api, "api")
    ]
}
output = forge.render(_bundle)
```

Append to `.gitignore` (`.forge/*`, never `.forge/`: git cannot re-include
under an ignored directory, and `forge lint` gates on it):

```
.forge-kcl/
secrets/
.forge/*
!.forge/disowned.json
!.forge/hashes/
!.forge/scaffolded/
```

**Credentials.** The app reads them from env vars, so deliver each one in its
`env`, as `JWT_SECRET` above (`forge.ManagedSecret`), and set the value once per
env: `forge secret set --env staging JWT_SECRET` (the value on stdin). Dev
delivers the same names from the gitignored `secrets/dev.yaml`
(`JWT_SECRET: dev-only`). `config_secrets`
delivers nothing to an app forge did not write; a name listed there that `env`
does not deliver fails the render. Read each fallback in the app: one that
silently defaults (`JWT_SECRET || "superSecret"`) is a production hole if the
secret is never delivered.

**A third-party image** (Redis, a worker someone else ships): declare it with
no `build`, its registry host included, `image = "docker.io/library/redis:7"`.
On hosting forge copies it into your org's registry at release cut and pins it
by digest. A one-line `FROM` Dockerfile with a `forge.DockerBuild` works too,
when you want to change the image.

### 3. Run it, ship it

```bash
npm ci                                 # the app's own install, when dev runs it on the host (no compose file)
forge env render dev                   # host: migrate (job), api (service)
forge env up dev                       # Postgres → migrations → the app; output in .forge/logs/dev/api.log
forge env status dev                   # App Health: "accepts connections (its declared tcp probe)"
forge lint                             # the verdict is the LAST line
forge env render staging               # the hosted spec, with the image under your org's registry
forge secret set --env staging JWT_SECRET < secret.txt
forge env deploy staging --plan-only   # builds, pushes, cuts a release, prints the plan; promotes NOTHING
forge env deploy staging --yes         # publish: managed DB, migrate job, rollout
forge env status staging               # its URL
forge obs check staging --wait --json  # branch on exit_code
forge env deploy prod <release>        # the same bytes, by digest
```

**Verify.** `curl` a real endpoint in dev and on the staging URL, one that
reads and writes the database. `forge obs workloads staging` shows 0 restarts
and no failed job runs. `forge obs logs staging --service api` shows the app's
output.

**Gotchas, all seen in practice:**

- `--plan-only` is not a dry run: it builds, pushes and cuts a release (no
  promotion). `env render` is the dry run.
- A hosted deploy needs billing on the org. Until it is set up, `env deploy`
  exits 7 and prints the link: give the user that link, and do not retry.
- `forge_version` is the forge you run. If it names another build, forge
  runs that build (it says so on stderr); keep the pin current.
- `obs check` is INCONCLUSIVE (exit 2, `reason_code: no_request_telemetry`)
  until Tier 2: it judges requests from the app's own traces, never from
  the ingress. That means "not proven good", never "passed".
- Each env's `DATABASE_URL` is per env. The migration job runs before every
  rollout, so a migration that breaks the running release breaks it during
  the deploy. Keep migrations backward-compatible (expand, then contract).
- Adoption writes an empty `deploy/kcl/kcl.mod.lock` (what kpm writes for a package with no dependencies); commit it with the rest, so a later render or deploy finds a clean tree.

**Stop here when** the app runs on staging and prod and you can redeploy any
release: that is the whole hosting story, with no app change.

## Tier 2: See it

**You get:** request rates, error rates and latency per endpoint, traces, logs
correlated with them, generated dashboards and default alerts
(`observability/alerts`), `obs check` verdicts that PASS or FAIL a release
instead of INCONCLUSIVE, and the evidence an agent on call needs
(`forge obs status`, `forge obs issue`).

**Steps.** Add the language's OpenTelemetry SDK with auto-instrumentation for
its HTTP framework and database client. Configure nothing: hosting sets
`OTEL_EXPORTER_OTLP_ENDPOINT` (OTLP/HTTP), `OTEL_EXPORTER_OTLP_PROTOCOL`,
the auth header and `OTEL_SERVICE_NAME` on every workload, and the SDKs read
them.

| Language | Package | Start it |
|---|---|---|
| Node | `@opentelemetry/api`, `@opentelemetry/auto-instrumentations-node` | `node --require @opentelemetry/auto-instrumentations-node/register dist/main.js` |
| Python | `opentelemetry-distro`, `opentelemetry-exporter-otlp`, then `opentelemetry-bootstrap -a install` | `opentelemetry-instrument gunicorn app:app` |
| Java / Kotlin | the `opentelemetry-javaagent.jar` | `java -javaagent:opentelemetry-javaagent.jar -jar app.jar` |
| Ruby | `opentelemetry-sdk`, `opentelemetry-exporter-otlp`, `opentelemetry-instrumentation-all` | `OpenTelemetry::SDK.configure { \|c\| c.use_all }` in an initializer |
| .NET | `OpenTelemetry.Extensions.Hosting` + the ASP.NET Core / HttpClient instrumentations | `AddOpenTelemetry().WithTracing(...).UseOtlpExporter()` |
| Go | `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | wrap the handler in `otelhttp.NewHandler` |

Put the start command in the Dockerfile's `CMD`. Dev exports nothing until
its Bundle declares `observability` with a local collector (the local
ClickStack, which needs Docker): `observability` has the declaration.

**Verify.** After a deploy and some traffic: `forge obs status staging` lists
`api` with a request rate, `forge obs traces staging --service api` shows
spans, and `forge obs check staging --wait` exits 0 or 1, not 2.

**Stop here when** a bad release shows up as a FAIL you would act on.

## Tier 3: Harden it

**You get:** releases that reach prod only after staging proved them,
deploys from CI instead of a laptop, and probes that mean "ready".

- **Gated promotion.** In `deploy/kcl/prod/main.k`, `delivery_policy =
  forge.DeliveryPolicy {health_gate = forge.HealthGate {after = ["staging"]}}`:
  the control plane refuses a release on prod until staging's verdict for it
  is PASS (needs Tier 2). Add `required_gates = forge.RequiredGates {names =
  ["e2e"]}` to refuse a promote that does not carry your CI's checks as passed
  (`--gate name=e2e,status=passed`), and `approval_hook = forge.ApprovalHook
  {url = "https://…"}` to ask your own approval system first; both are
  enforced with or without a control plane. `deploy/delivery-policy`.
- **More environments.** `forge env new preview --from staging`; `forge env
  diff`. `deploy/model`.
- **CI.** forge generates no workflow for a project with no Go module; write
  one job that installs forge (`curl -fsSL
  https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh`),
  runs `forge lint`, `forge env deploy staging --yes` and `forge obs check
  staging --wait`, with `FORGE_CONTROL_PLANE_TOKEN` a token with
  deploy:read,deploy:write that a person mints in the web app
  (Settings → API tokens, https://forge.reliantlabs.io/settings/tokens). `ci`.
- **Health endpoints.** Serve a readiness path that checks the database and a
  cheap liveness path, then `probes = tiers.Probes {readinessPath = "/readyz",
  livenessPath = "/healthz"}` in `workloads.k`.
- **Migrations.** Keep the app's tool and the pre-rollout job. If the
  migrations move to forge's `db/migrations/`, which is the start of Tier 4,
  `forge lint --migration-safety` gates them (`db/deploy-migrations`).

**Stop here when** nobody deploys by hand and prod only ever runs what
staging proved.

## Tier 4: Move in

**Only when it pays:** a new service you would write anyway, or a part of the
app whose hand-written API, data layer or frontend client costs more to keep
than to regenerate. forge's Go stack generates the Connect API from proto, the
ORM from SQL migrations, typed frontend hooks and tests (`greenfield`,
`authoring`).

- **A new piece, beside the app.** Its own forge Go project
  (`forge project new <name>-api --service <entity>`). It deploys next to the
  adopted app and is reached through `forge.WorkloadURL`.
- **A Go server already here.** Adopt it in place with `forge project new
  --in-place --kind service`, and mark every proto service whose handlers
  still live elsewhere `// forge:unmanaged`. Then move one service per PR
  (`migration`, `migration-service`).
- **Rewriting a non-Go service** is a rewrite, not an adoption: do it one
  endpoint group at a time behind the same route, never all at once.

**Stop** as soon as the remaining code is cheaper to keep than to move.
