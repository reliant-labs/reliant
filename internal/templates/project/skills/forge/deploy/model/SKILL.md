---
name: deploy/model
description: The deploy model — every workload declared once in workloads.k and bound per env to a runtime (OnHost, OnCluster, OnHosted, OnCompose, BuildOnly, OnExternal); the scaffolded envs, their binders and the shared env base (deploy/kcl/base) they delegate to, `forge env new --from`, who applies an env, what forge writes into every spec (probes, grace, PDB, strategy, hardening), the hosted restricted profile, per-env config, cross-references, and derived features.
---

# The model: declare once, bind per env

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
| `forge.OnCompose {service}` | a docker-compose service | `docker compose up` of that service (`shared = True`: from the primary checkout; `optional = True`: `forge env up` skips it with one warning when no Docker daemon answers, instead of failing — a workload that only makes sense beside it says `requires = ["<name>"]`) |
| `forge.BuildOnly {}` | nowhere | builds and ships it (a CLI, an image others pull) |
| `forge.OnExternal {status, plan, deploy, health}` | wherever YOUR deploy tool puts it (Helm, Fly.io, Cloud Run, ECS, Argo CD) | builds, cuts and promotes, then runs your argv hooks with the frozen digest; no hooks = record-only (`deploy/external`) |

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

Under `forge env up`, host workloads start in `before` order (`migrate` to
completion first), each on the port its env declares; frontends run their dev
server with the declared port force-injected as `PORT`. The local loop's
flags: `dev`.

`output = forge.render(bundle)` is the ONE entrypoint. There is no separate
manifest stream: the cluster objects are Workload records only forge can
expand, so always go through `forge env render` / `forge env deploy`.

## The scaffolded envs

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
| `staging`, `prod` | `_hosted`, `_hosted_frontend`; `_on_cluster`, `_on_bucket` unused — each delegating to `deploy/kcl/base/env.k` | **hosted on Reliant**: a `forge.ManagedDatabase`, `forge.HostedSecrets` |

**Hosting elsewhere is a one-line rebind**: declare `_cluster` / `_bucket`
as the env shows, then edit the line to `_on_cluster(...)` /
`_on_bucket(...)`. An operator (hosting refuses it) is scaffolded
`_on_cluster`. One-workload API, billing, adopting in an older project:
`deploy/hosting`.

### A shared base, per-env deltas

The binders' bodies live ONCE, in `deploy/kcl/base/env.k` (`forge project new`
scaffolds it; yours). Each env imports it, `import ..base.env as envbase`, and
keeps only what is its own: its database, the cluster or bucket it declares,
its capacity (`_cluster_resources`, `_cluster_replicas`), and its binding
lines. Its binders are one-liners (`_hosted = lambda w ... {
envbase.hosted(w, appcfg.app_config, _db.name) }`), so a change in the base
reaches every env, and an env still overrides one by wrapping it. It is plain
KCL: `env render`, `env diff` and `env shape` read it like any import.

An older project adopts it by moving its binders' bodies into
`deploy/kcl/base/env.k` and leaving the one-liners. Anything else several
envs share goes in a module beside it (`deploy/kcl/base/<name>.k`); a large
project's library of such modules is the same idea at scale.

Derive another env — each binding is copied (an env on the base is copied as
just its deltas); a declared cluster's context, platform and namespace become
REPLACE_ME placeholders:

```
forge env new preview --from staging                        # same bindings as staging
forge env new cloud --from prod --bind item=cluster         # item on your cluster
forge env new cloud --check                                 # no placeholder left, renders, admissible
```

`lifecycle` (`"local"`/`"ephemeral"`), `applier = "forge"` and `hub_converges` declare
WHO APPLIES an env: scratch clusters (scaffolded `dev`), a real env forge applies for
now, and the delivery units the hub has taken over, so a real env moves onto Flux one
unit at a time. See deploy/shape; `applier = "flux"`: deploy/flux/self-hosted.

## What forge writes into every spec

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
- **`preStopSeconds`** for a server forge did not build (a `DockerBuild`, a
  third-party image): it reads no `PRE_STOP_DELAY`, so it closes its
  listener while load balancers still route to it, and every rollout drops
  requests. `preStopSeconds = 15` renders the main container's preStop
  `sleep` (a native Kubernetes 1.30+ action: no shell or `sleep` binary in
  the image) and adds 15 to the derived grace period. 0-120, long-running
  kinds, allowed on hosted; an explicit `terminationGracePeriodSeconds` must
  be at least `preStopSeconds + 5`. On the host runtime `forge env down`
  waits that long before SIGKILL; compose refuses it (the compose file's
  `stop_grace_period` owns the stop). A forge-built server needs none.
- **Rollout safety** above one replica: a PodDisruptionBudget and soft
  topology spread. On nodes shared with ranked pods, also declare
  `priorityClassName` — see `deploy/pod-priority`.
- **`strategy`**: unset is `RollingUpdate`, which surges a new pod before
  the old one stops, so `replicas = 1` still runs two during a rollout.
  When the count is a correctness bound (a sweeper not idempotent under
  concurrency), declare `strategy = "Recreate"` and accept the gap. Storage
  forces Recreate; `RollingUpdate` beside `storageGiB` is refused (the
  ReadWriteOnce volume would deadlock the surge pod).
- **Pod hardening**: non-root, read-only root filesystem with a `/tmp`
  emptyDir, no ServiceAccount token unless the workload declares RBAC.
- **No service-link env vars** (`enableServiceLinks: false`): a Service
  `temporal` cannot inject `TEMPORAL_PORT=tcp://…` into typed config. Use
  DNS or explicit env; `enableServiceLinks = True` on a cluster workload
  opts in.
- **`before` jobs** (`migrate` has `before = [fw.BEFORE_ALL]`) as an
  initContainer on every workload they gate.

## The hosted runtime is a restricted profile

A hosted workload shares nodes with other users, so the control plane admits
only what it can run safely there, and forge runs the same check at render: a
refusal names the workload and field in your env file. Bind a refused workload
to a cluster you operate instead. The allowed/refused table: `deploy/hosting`.

## Per-env config — KCL is the surface

Typed per-env config lives in `deploy/kcl/<env>/config.k` (an `AppConfig`
instance); `config_gen.appConfigEnvMap(cfg, w.config_secrets)` projects it
into a workload's `env` map. A `sensitive` field reaches only workloads that
list it in `config_secrets`, so one feature's missing Secret key cannot stall
every pod. `config_secrets` itself renders nothing: a name the final `env` does
not carry fails the render. A workload with no generated config (an app in any
other language) delivers each one itself, from the env's secret store:
`env = {JWT_SECRET = forge.ManagedSecret {name = "JWT_SECRET"}}`. A workload bound to a composition-graph root (`| config_gen.root_of("worker")`)
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
