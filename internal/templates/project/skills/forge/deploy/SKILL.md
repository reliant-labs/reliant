---
name: deploy
description: Ship code — declare workloads once, bind each one to a runtime per env (host, cluster, hosted), build, deploy, verify, roll forward. Static sites ship through the same model (deploy/static-site).
---

# Ship It

**New projects ship to Reliant hosting by default** — `staging` and `prod` bind
everything to it, and `forge env deploy prod` is the whole procedure. What it
provides, what it refuses, and the gates (billing, unset secrets):
`deploy/hosting`. A cluster you operate is the secondary path, below.

## The model: declare once, bind per env

Everything that runs is a `fw.Workload`, declared ONCE in
`deploy/kcl/workloads.k` with the facts that are true wherever it runs:

```kcl
item = fw.Workload {
    name = "item"
    kind = "service"
    build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
    args = ["item"]                                   # the subcommand, on every runtime
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    config_secrets = ["DATABASE_URL"]                 # the credentials it reads
}
```

Each env's `deploy/kcl/<env>/main.k` BINDS every workload to a runtime —
one binding per workload — and states only its own values. It never
re-describes a workload, and there is no env-level runtime: an env is a list
of bindings.

| Runtime | Where it runs | forge does |
|---|---|---|
| `forge.OnHost {runner}` | a local process | `forge env up` runs `go run ./cmd/<p> <args>` (or air / a built binary / delve) |
| `forge.OnCluster {target}` | a Kubernetes cluster you operate | renders a `forge.dev/v1alpha1 Workload` record and expands it through `pkg/deploy.RenderWorkloads` (Full profile) |
| `forge.OnHosted {}` | the forge control plane | publishes the spec as a Workload CR; the platform renders it (Restricted profile) |
| `forge.OnCompose {service}` | a docker-compose service | `docker compose up` of that service (`shared = True`: from the primary checkout) |
| `forge.BuildOnly {}` | nowhere | builds and ships it (a CLI, an image others pull) |

A binding is the same `|` an env uses for any refinement. Env NAMES mean
nothing to forge — any env may bind any workload to any runtime, and one env
may mix them. This one runs `item` on the forge control plane and keeps
`migrate` and `search` on a cluster it operates:

```kcl
_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {}              # needed because item is hosted
    workloads = [
        wl.migrate | {runtime = forge.OnCluster {target = _prod}}
        wl.search  | {runtime = forge.OnCluster {target = _prod}}
        wl.item    | {runtime = forge.OnHosted {}}
    ]
})
```

A workload with no runtime is a render error naming it.

`output = forge.render(bundle)` is the ONE entrypoint. There is no separate
manifest stream: the cluster objects are Workload records only forge can
expand, so always go through `forge env render` / `forge env deploy`.

### The scaffolded envs

Each scaffolded env declares small binders — lambdas that apply the env's
layer (env vars, capacity) and one runtime — and lists every workload with
its own:

```kcl
# deploy/kcl/prod/main.k
_workloads = [
    _hosted(wl.migrate)
    _hosted(_api)
]
```

| Env | Binders | Notes |
|---|---|---|
| `dev` | `_on_host` / `_on_host_job` (services, workers, jobs), `_on_k3d` (operators, crons: a host process cannot be one) | host-run postgres (and dev IdP, NATS, a Temporal dev server) via `forge.HostInfra` |
| `staging`, `prod` | `_hosted`, `_hosted_frontend`; `_on_cluster`, `_on_bucket` unused | **hosted on Reliant**: a `forge.ManagedDatabase`, `forge.HostedSecrets` |

**Hosting elsewhere is a one-line rebind**: declare `_cluster` / `_bucket`
as the env shows, then edit the line to `_on_cluster(...)` /
`_on_bucket(...)`. An operator (hosting refuses it) is scaffolded
`_on_cluster`. One-workload API, billing, adopting in an older project:
`deploy/hosting`.

Derive another env — each binding is copied; a declared cluster's context,
platform and namespace become REPLACE_ME placeholders:

```
forge env new preview --from staging                        # same bindings as staging
forge env new cloud --from prod --bind item=cluster         # item on your cluster
forge env new cloud --check                                 # no placeholder left, renders, admissible
```

`lifecycle` (`"local"`/`"ephemeral"`), `applier = "forge"` and `hub_converges` declare
WHO APPLIES an env: scratch clusters (scaffolded `dev`), a real env forge applies for
now, and the delivery units the hub has taken over, so a real env moves onto Flux one
unit at a time. See deploy/shape.

### What forge writes into every spec

- **Probes.** A service forge builds gets `/readyz` readiness and
  `/healthz` liveness on its `http` port, written explicitly into the spec
  on every runtime. That is what the scaffolded server serves. Declare
  `probes` only to change them. A third-party image with ports gets TCP on
  its first port; a worker or job gets none unless it declares one.
- **Grace period** from the drain env (`PRE_STOP_DELAY + SHUTDOWN_TIMEOUT`
  + 5s), so a rollout never SIGKILLs a pod mid-drain. Each is read the way
  the server reads it: unset or zero is the default (5s / 30s), negative is
  none (no pause; no shutdown window) and adds nothing;
  `terminationGracePeriodSeconds` overrides it. What the server does inside
  it: `services/shutdown`.
- **Rollout safety** above one replica: a PodDisruptionBudget and soft
  topology spread. On nodes shared with ranked pods, also declare
  `priorityClassName` — see `deploy/pod-priority`.
- **`strategy`** is how the Deployment replaces its pods. Unset is
  Kubernetes' `RollingUpdate`, which surges a new pod before the old one
  stops — so `replicas = 1` does NOT mean one process: two run for the
  length of every rollout. When the replica count is a correctness bound
  rather than a capacity choice (a sweeper not idempotent under
  concurrency), declare `strategy = "Recreate"` and accept the gap with no
  pod. Storage already forces Recreate, and `RollingUpdate` beside
  `storageGiB` is refused — the ReadWriteOnce volume would deadlock the
  surge pod.
- **Pod hardening**: non-root, read-only root filesystem with a `/tmp`
  emptyDir, no ServiceAccount token unless the workload declares RBAC.
- **`before` jobs** (`migrate` has `before = [fw.BEFORE_ALL]`) as an
  initContainer on every workload they gate.

### The hosted runtime is a restricted profile

A hosted workload shares nodes with other users, so the control plane admits
only what it can run safely there, and forge runs the same check at render: a
refusal names the workload and field in your env file. Bind a refused workload
to a cluster you operate instead. The allowed/refused table: `deploy/hosting`.

## Pre-flight checks

```
forge lint              # Go + proto + frontend linters; fixes formatting in place locally
forge lint --check      # report only: print the diffs, write nothing (what CI does, by itself)
task test               # full suite
forge ci validate-kcl   # every env renders to something applyable
forge doctor --signal deploy   # probes, resources, Secrets, migrations
```

Capacity: `deploy/hosted-capacity`.

## Build

```
forge build <env>                 # what <env> declares (host workloads need no image)
forge env build <env> --push          # and push each image to the reference its workload declares
forge registry login <env>        # hosted: nothing to pass; -u/--password-stdin for your own
forge registry ref <env>          # <image>@<digest> per image the last build pushed
forge build <env> --plan          # resolve + preflight the build set; build nothing
forge build --tag=<tag>           # override image tag (default: commit SHA)
forge build --debug               # with debug symbols for Delve
```

One project image carries every binary. Its ENTRYPOINT is the binary and
its CMD the default subcommand (`server`), so a workload's `args` select
what the pod runs, the same subcommand the host runtime runs.

### The hosted registry takes your control-plane credential

A hosted env's images go to the PLATFORM registry, authenticated with the same
`rlat_` forge reaches the control plane with — no registry password, and
`--push` / `forge env deploy` log themselves in before their first push.
`forge registry login <env>` REFUSES `--username`/`--password-*` for that host.
A bare `image = "api"` resolves under `<registry_host>/<org>/<project>/` (org = your credential's).

Load `deploy/hosted-registry` for credential sources, the CI shape, and
denied-push triage.

A non-hosted env declares no registry: each workload — and each hosted
frontend, via `forge.Frontend.image` — names its own.

### Docker build contexts

A `forge.DockerBuild` sends the PROJECT ROOT as its build context unless it
sets `context` (a project-root-relative directory) — set that when the
Dockerfile expects to run from its own directory, or an un-prefixed `COPY
package.json ./` fails with `failed to compute cache key`. Separately,
`docker.build_contexts` declares NAMED contexts for files outside the project
tree, consumed via `COPY --from=<name>`. For both, and which one a Dockerfile
needs: load `deploy/build-contexts`.

## Deploy

Environment is a positional arg — `forge env deploy dev`, not `forge env deploy --env dev`.

```
forge env render prod             # every object, and the cluster it lands on — changes nothing
forge env shape prod              # what the env IS, not its objects — see deploy/shape
forge env deploy prod             # SHIP THIS CHECKOUT: build → push → cut → plan → confirm → apply → wait
forge env deploy prod --yes       # the same, in CI: "I read the plan" (no TTY to prompt on)
forge env deploy prod v1.7.1      # deploy a release that already exists; builds nothing
forge env deploy prod --dry-run   # render/print without applying
forge env deploy prod v1.7.1 --target item   # one workload of a release that exists
```

**Two forms, and the difference is whether you name a version.** With NO
version, deploy does all the deployment bits: it builds the env's artifacts at
the current checkout exactly as `forge env build` does, pushes them, cuts a
release named `<YYYYMMDD>.<HHMMSS>-<tree12>` for this tree, records the shape,
plans, confirms, then promotes and waits. If a release already holds this
checkout — whatever it is named, including one `forge env build --release` cut
— it is deployed and NOTHING is built: it must record this clean tree and this
forge version, cover every artifact the env declares, and still resolve every
image it pins. The first line says which: `reusing release <v>` or
`cutting new release <v> because …`. Naming a version builds
nothing — it deploys a release that is already cut, which is also how you make
a spec-change deploy: name the version the env already runs. A version nobody
cut errors with `forge env deploy <env>` as the fix, never `--no-build`.

**Nothing is written until you confirm.** A promotion IS the deploy — the
converger picks it up within minutes — so the plan is printed and approved
BEFORE the write. Three ways through the gate: **interactively**, where the
default is no; **`--yes` in CI**, meaning "I read the plan" (the plan is still
printed into the job log above the write); and **`--plan-only`**, the first
stage of a two-stage pipeline. With no terminal and no `--yes` the command
refuses with exit **5** (`plan_unconfirmed`), having built, pushed and cut but
written no promotion — so approving it afterwards needs no rebuild. A CI deploy
missing `--yes` is the most common cause of exit 5.

**The plan diffs against the last SUCCEEDED apply, never the declaration**,
and whoever applies the env computes it: the control plane for an env it
converges, forge for one it applies itself (a file-ledger env, or a
`forge.ControlPlane` env that `converges nothing`). Every successful apply
records the bundle it shipped, so an unchanged config plans clean. Reading a
plan — `object_removed`, field paths, secret presence, `no recorded config`:
`deploy/plan`.

**A scoped deploy needs a release.** `--frontends-only` and `--target` ship
part of the env, so forge refuses them when no version is named: a no-version
deploy cuts a release over every artifact, and cutting one that ships only
some would record a release that does not describe what is running. Name the
version (`forge env deploy prod v1.7.1 --frontends-only`) or deploy
everything. `--dry-run` / `--explain` cut nothing and are unaffected.

`build` and `deploy` also record that shape on the control plane, so a console
can read an env with no daemon online.

**Where releases and promotions are recorded.** An env declaring
`forge.ControlPlane` records on that control plane; every other env in this
machine's ledger (`$FORGE_LEDGER_HOME`, default `~/.forge/ledger`).
`forge ledger where <env>` says which, and why. A hermetic script or test that
renders or imports a real env sets **`FORGE_LEDGER=machine`**: every forge it
starts stays off the declared control plane, and `forge ledger where <env>
--json` reports `"override": "FORGE_LEDGER=machine"` to assert it.
`$FORGE_LEDGER_HOME` alone only relocates the machine ledger — a declared env
still reads and writes its control plane. Control plane down: `deploy/offline-ledger`.

Each workload deploys through its runtime: cluster workloads are applied to
the kubectl context the ClusterTarget names (forge refuses when it is
missing rather than using the current one), after a live preflight that
every referenced Secret key and image exists. Hosted workloads publish to
the env's control plane, admitted there under the Restricted profile.

Every refusal that does not depend on the apply runs in that preflight,
BEFORE the promotion is recorded and before anything ships: the Secret/image
check, a pinned env, a frontend that cannot ship (mock build, a forge-owned
dotenv, no `firebase` CLI installed), and what the control plane will converge.
The plan's `converges` line says the last one: `nothing` for an env that
declares `forge.ControlPlane` but binds no tier to it (forge's own apply is
the whole deploy), or its hosted tiers, naming any this deploy publishes for
the first time or leaves unpublished. If a deploy still fails part-way, its
report lists each stage that shipped (`shipped …` / `PARTLY …`); it never
claims nothing shipped once a cluster or frontend host has changed.

### Migrations run BEFORE the rollout

A standalone `kind = "job"` workload is **pre-rollout by default**: `forge env
deploy` applies it and waits for it to COMPLETE before any Deployment in the
same cluster group. If it fails the deploy stops with no workload changed,
printing the exact `kubectl logs` command. The scaffolded `migrate` goes
further — `before = [fw.BEFORE_ALL]` runs it as an initContainer on every
workload, so no new pod serves against an old schema. A job needing this
release's workloads running declares `deployPhase = "post-rollout"`.

### Delivery units: how a Flux-applied bundle is ordered

A bundle a reconciler applies states its order as trees per cluster × unit × phase. Declare units with `Bundle.delivery_units`, place objects with `delivery_unit`: `deploy/delivery-units`.

### Frontend runtimes and domains

Every frontend must bind a runtime: `OnHost`, `OnHosted`, `OnBucket`,
`OnFirebase`, or `BuildOnly`; missing runtime is a render error. Static Next.js
frontends require `output: static`. Hosted sites ship one OCI digest without
`config.js`; the control plane resolves `runtime_config` after sync. Everything
hosted needs billing. Hosted custom domains use `forge domain`, never spec
`domains` (cluster port domains stay spec).
Load `forge skill load deploy/frontends` for bindings, build facts, runtime
config and domain links; a standalone site starts at `deploy/static-site`.

PDB lint + stale-PDB prune: deploy/pdb.

Raising a StatefulSet's declared volume size (immutable `volumeClaimTemplates`) is
converged by a pre-rollout Job forge renders; a shrink is refused:
deploy/statefulset-volumes.

## Pod priority on shared nodes

Declare `priorityClassName` on nodes shared with ranked pods, or yours get
preempted: `deploy/pod-priority`.

## forge env up — the local loop

```
forge env up dev              # build + cluster apply + host launch + frontend dev
forge env up dev --target item
forge env up dev -D name=value    # a render option the env's KCL declares (forge env options dev)
```

Host workloads start in `before` order (`migrate` to completion first),
each on the port its env declares; frontends run their dev server with the
declared port force-injected as `PORT`.

## Verify + recover — roll forward, never back

After every deploy confirm the env runs what it claims: `forge env status
<env>` — bound release, running digests, rollout, health, gates, ledger.
Exit 2 + `ledger BEHIND` = pull first.

**There is no rollback command, and that is deliberate.** A rollback claims to
undo a release and it cannot: by the time you would run it the release has
applied its migrations and written rows in the new shape. So forge has no
rollback flag, no `rollback_cmd`, no `kubectl rollout undo` path. Recovery is
always a **new release that rolls forward** — `forge env deploy prod` with the
fix in the tree, or `--plan-only` first to read the plan.

A release deploy compare-and-sets against the plan's read: exit **3** = the env
moved meanwhile, nothing written — never retry blind. CI recipe:
`forge env deploy --help`.

A failed deploy changes nothing to undo: the pre-rollout gate stops a bad
migration before any workload changes, and a workload that never becomes ready
leaves the previous ReplicaSet serving. Binding an env to an OLDER release is
an ordinary promote labelled `direction BEHIND`; see `db/deploy-migrations`.

## Rules

- Declare a workload once, in `workloads.k`. An env file that re-states a
  workload's command, ports or build is a second copy that will drift.
- Never `kubectl apply` a hand render — everything through `forge env deploy`.
  To read a VALUE KCL declares, `forge kcl eval`; see `deploy/kcl-eval`.
- Image tags are immutable — commit SHA by default, digest-pinned on deploy.
- Secrets never live in KCL — it is checked in. Reference them
  (`config_secrets`, `forge.SecretRef`, `forge.ManagedSecret`).
- EVERY image on a CLUSTER or COMPOSE workload names its registry host
  (`ghcr.io/acme/api`). A hostless one is refused at render, naming the
  workload: no env registry exists to complete it. A `forge.OnHosted` workload
  may name a bare image — forge composes the address from the env.

## Per-env config — KCL is the surface

Typed per-env config lives in `deploy/kcl/<env>/config.k` (an `AppConfig`
instance); `config_gen.appConfigEnvMap(cfg, w.config_secrets)` projects it
into a workload's `env` map. A `sensitive` field reaches only workloads that
list it in `config_secrets`, so one feature's missing Secret key cannot stall
every pod. A workload bound to a composition-graph root (`| config_gen.root_of("worker")`)
takes `config_gen.workloadEnvMap(cfg, w)` instead: exactly the config its root
reaches — the scaffolded envs already call it (`architecture`, "Roots and
commands"). Anything else is a literal in the workload's `env`:

```kcl
wl.item | {env = {LOG_LEVEL = "debug"}}
```

## Cross-references — declare once, read twice

When two fields must agree (a route's port and the service's, an issuer in two
places), declare the value on the schema that OWNS it and reference it from the
other. A `forge.WorkloadURL {workload = "item"}` in an env or a frontend's
`runtime_config` is resolved by forge (host, cluster) or by the control plane
(hosted), so a URL is never written twice. A port mismatch is invisible at
render and fatal at runtime — make it impossible.

## Routes and per-route traffic policy

A route names its backend as a `service` OR as a `workload`; a `workload` is
resolved per env to that workload's Service, or to the host process when that
env runs it on the host, so ONE declaration follows it everywhere. `traffic`
carries the typed retry / timeout / health-check / outlier-detection policy.
For the resolution table, the port-inference rules and a worked example: load
`deploy/routes`.

## Extra Kubernetes objects

Anything that is not a workload — a CRD, a ClusterIssuer, a vendored chart's
output — goes in `Bundle.manifests`, placed explicitly:

```kcl
manifests = [forge.Manifests {name = "issuer", cluster = "gke_acme_prod", objects = [_issuer]}]
```

A hosted env never accepts raw objects: the platform owns the render.

To change one FIELD of an object forge already renders — replicas, a
container's resources, a Namespace's PSA label — do not re-declare it here.
Name it in `Bundle.overrides` and forge patches its own render:
`overrides = {"Deployment/api" = {spec.replicas = 10}}`. Keys, patch semantics
and every failure: load `deploy/overrides`.

Own app chart, kustomize/jsonnet output: `deploy/byo-yaml`.

## Reaching another cluster's API server

A workload needing a kubeconfig for ANOTHER cluster declares
`forge.KubeconfigSecret`, minted on every deploy: `deploy/cluster-access`.
To have the control plane deploy INTO your cluster: `deploy/cluster-connect`.

## Features are derived, and pinned only to override

Subsystems turn on from the repo: `.proto` files → `codegen`; a server's
sources (`pkg/app`, `internal/handlers`, `proto/services`) →
`observability`/`hot_reload`; `deploy/kcl` → `deploy`; non-library →
`ci`/`build`; `db/migrations/` → `migrations`/`orm` (off under
`//forge:no-orm`); a frontend → `frontend`; a `forge.Gateway` in `deploy/kcl` →
`ingress`; `internal/operators/<name>/` → `operators`. When a derived default is
wrong for the project, pin it in forge.yaml, e.g. `features: {observability:
false}`. No later change to the tree turns a pinned feature back on. `orm` has
no key. `forge project features` explains each one.

## k3d local-registry mirror

`deploy/k3d-registry`: the `localhost:5050`↔`registry.localhost:5000` mirror.
