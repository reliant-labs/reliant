# forge — KCL module

The typed authoring surface and the lowering that forge projects import
(ADR 0002, `docs/adr/0002-one-workload-model.md`). A workload is declared
ONCE and each env binds it to a runtime:

```kcl
import forge
import forge.workloads as fw

# deploy/kcl/workloads.k — WHAT runs, complete on its own
api = fw.Workload {
    name = "api"
    build = forge.GoBuild {cmd = "./cmd/myapp", output_name = "myapp"}
    args = ["api"]                     # the subcommand, on every runtime
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    env = {
        LOG_LEVEL = "info"
        DATABASE_URL = forge.SecretRef {name = "myapp-secrets", key = "database_url"}
        WEB_URL = forge.WorkloadURL {workload = "web"}
    }
}

# deploy/kcl/dev/main.k — WHERE it runs, in this env
_k3d = forge.ClusterTarget {cluster = "k3d-myapp", namespace = "myapp-dev", registry = "localhost:5050"}

output = forge.render(forge.Bundle {
    project = "myapp"
    workloads = [
        wl.api | {runtime = forge.OnHost {runner = "air"}}
        wl.migrate | {runtime = forge.OnHost {}}
        wl.search | {runtime = forge.OnCluster {target = _k3d}}    # one workload elsewhere
    ]
    frontends = [forge.Frontend {name = "web", type = "vite", path = "frontends/web"}]
})
```

`output = forge.render(bundle)` is the ONE entrypoint; there is no other
public top-level var.

## What ships here

| Declaration                                                                                       | What it is                                                                                                                                                                                                                                                                                           |
| ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `fw.Workload`                                                                                     | Everything that runs: `kind` = `service` / `worker` / `job` / `cron` / `operator` / `tool`. Field names and types are the generated `v1alpha1.WorkloadSpec`'s (camelCase), plus `name`, `build`, `runtime`, `config_secrets`, `serves`.                                                              |
| `forge.OnHost` / `OnCompose` / `OnCluster` / `OnHosted` / `BuildOnly`                             | The runtime a workload binds to. Every workload in an env binds its own; there is no env default.                                                                                                                                                                                                    |
| `forge.GoBuild` / `DockerBuild` / `ShellBuild` / `RemoteBuild`                                    | How forge produces a workload's artifact. Unset = forge builds nothing. `ShellBuild.built_images` builds one image FROM another: forge builds the named workload first and hands the command its pushed `<repository>@sha256:<digest>`.                                                              |
| `forge.SecretRef` / `ConfigMapRef` / `FieldRef` / `ManagedSecret` / `DatabaseRef` / `WorkloadURL` | The non-literal values of `fw.Workload.env` (a map, name -> value).                                                                                                                                                                                                                                  |
| `forge.Bundle`                                                                                    | One environment: workloads, `infra` (`forge.HostInfra`), frontends, databases, gateways/routes, secrets, clusters, `manifests` (`forge.Manifests`, raw objects), `network_policy` (opt-in).                                                                                                          |
| `forge.Frontend`                                                                                  | A dev server or a static build forge publishes, bound per env to `forge.OnHost` / `OnHosted` / `OnBucket` / `OnFirebase` / `BuildOnly` (required, no default). The build facts — `public_dir`, `base_path`, `bundle`, `cache_control` — are on the frontend. A containerized frontend is a workload. |
| `forge.ManagedDatabase`                                                                           | A Postgres on a cluster (CloudNativePG) or on the control plane.                                                                                                                                                                                                                                     |

### What each runtime does with a workload

| Runtime                      | forge does                                                                                                                                                                         | spec.image                              |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------- |
| `OnHost {runner}`            | launches a process; the argv is derived from `build` + `args` (`go run <cmd> <args>`, `air`, `./bin/<out> <args>`, `dlv`)                                                          | `""`                                    |
| `OnCompose {service}`        | `docker compose up` of the compose file's service; literal env feeds the compose process env. `shared = True` runs it from the repo's primary checkout, whichever worktree deploys | `""`                                    |
| `OnCluster {target}`         | a `forge.dev/v1alpha1 Workload` record in `output.manifests`, expanded by `pkg/deploy.RenderWorkloads` (Full profile)                                                              | `<registry>/<image>:<tag>` or `@digest` |
| `OnHosted {}`                | publishes the spec to the control plane, which renders it (Restricted profile)                                                                                                     | the artifact / third-party image        |
| `BuildOnly {build_variants}` | builds and ships, never runs                                                                                                                                                       | `""`                                    |

A workload whose runtime cannot honour a field is REFUSED at render, naming
the workload, the field and the reason. That covers the hosted runtime's
Restricted profile (generated from `v1alpha1.FieldProfiles` into
`tiers/tiers_gen.k`) and forge's own host/compose/build-only rules
(`render.k` `_mask_violations`).

A forge-built `service` gets explicit `/readyz` + `/healthz` probes on its
`http` port in the spec (ADR 0002 §5). A `forge.WorkloadURL` is resolved at
render for host and cluster referrers (a cluster pod reaches a host process
through its target's `host_gateway`, default `host.k3d.internal`) and kept as
a reference for hosted ones. See `kcl/lib/workload_url.k`.

### What each runtime does with a frontend

A frontend binds a runtime exactly as a workload does (ADR 0002 §6). `OnHost`,
`OnHosted` and `BuildOnly` are the workload schemas of the same name; the
workload-only fields on them (`runner`, `listen_ports`, `build_variants`, ...)
are refused on a frontend, naming the field. `output.frontends[].runtime` is
`{type, ...}`, and the Go dispatch keys on `type`:

| Runtime                                                                             | `forge env deploy` does                                                                                                                                                    |
| ----------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OnHost {}`                                                                         | nothing — it is the dev server (`<dev_runner> dev` on `port`); `forge env up` runs it                                                                                      |
| `OnHosted {}`                                                                       | `forge env build <env> --push` pushes the site as an OCI release; the deploy publishes a StaticSite CR (needs `control_plane`)                                             |
| `OnBucket {bucket, cdn?, keep_releases}`                                            | builds, assembles, uploads `releases/<digest>/`, syncs `live/`, invalidates the CDN                                                                                        |
| `OnFirebase {project, site, target?, spa_fallback?, rewrites, keep_asset_releases}` | builds, assembles, carries the last `keep_asset_releases` releases' hashed assets forward, `firebase deploy` with a route-only SPA fallback and immutable/no-cache headers |
| `BuildOnly {}`                                                                      | builds it, so a sibling frontend's `bundle` can assemble its output; ships nothing                                                                                         |

`cache_control` is honoured on `OnBucket` (object headers) and `OnFirebase`
(firebase.json `headers`, replacing its default of an immutable `asset_dir`
and `no-cache` everywhere else), and refused elsewhere; `bundle` is refused
on `OnHost`. On `OnFirebase` a catch-all `** -> /index.html` rewrite is
refused: it answers a missing hashed asset with the page and a 200, so
declare `spa_fallback = "/index.html"` instead. `OnHosted` has no
`bucket` field at all, so "a hosted site that names a bucket" does not compile.

### Cross-repo sources

A `Frontend` declares its code EITHER as a `path` (a directory in this
repo) OR as a `source = forge.GitSource { repo, ref, subdir? }` — never
both. The `source` form exists because a filesystem path to a sibling
checkout has two failure modes: it does not exist in CI, and where it does
exist it silently ships whatever happened to be checked out, so identical
commits produce different artifacts on different machines.

`ref` is required — forge does not default to a repository's default
branch. Fetches are cached per repo+ref, and a machine-local
`.forge/source-overrides.yaml` (gitignored, so it can never un-pin CI)
maps a repo to a working copy for local iteration.

See `docs/cross-repo-sources.md` for the full model.

## Extending a workload — `schema MyService(fw.Workload)`

A project can use KCL-native inheritance to add its OWN typed/required
fields to a workload while forge renders the result EXACTLY like the base:

```kcl
import forge.workloads as fw

schema BillingService(fw.Workload):
    region: str               # extra REQUIRED field — enforced at parse time
    tier: "free" | "pro" = "free"

    check:
        region, "BillingService.region is required"

_svc = BillingService {
    name = "billing-api", region = "us-east-1"
    ports = [fw.Port {name = "http", port = 8080}]
}
```

This works because the lowering is typed on the BASE schema, which accepts
any subtype: the subtype renders the same `output` a plain `fw.Workload`
would. The app's extra fields ride on the typed value but are NOT part of
the rendered contract (they're yours, for your own KCL logic). An extra
field with no default — or a `check:` the value violates — fails at KCL
load, so your domain invariants are enforced the same way forge's are.

## Declared external prerequisites — `required_secrets` / `required_dns`

A deploy often depends on out-of-band facts forge does NOT (and must not)
create: the cert-manager `cloudflare-api-token` Secret, per-host DNS
A-records, the load-bearing `*.workspaces` wildcard. Left in a docstring,
`forge env deploy` renders green and THEN ACME / DNS hangs silently. Declare
them as first-class prerequisites on the Bundle so they're MODELED:

```kcl
_bundle = forge.Bundle {
    project = "acme"
    # ... workloads / gateways / ...
    required_secrets = [
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "cert-manager"      # often NOT the deploy namespace
            keys = ["api-token"]
            reason = "cert-manager DNS-01 Cloudflare API token"
        }
    ]
    required_dns = [
        forge.DNSRecord {
            host = "*.workspaces.example.com"
            reason = "DNS-01 wildcard cert + workspace-proxy traffic"
        }
    ]
}
```

What this buys (beyond a comment):

- **Render-time checklist** — `forge env deploy` prints the prerequisites
  every run; `forge project audit` surfaces them as the `prerequisites` category.
- **Deploy preflight BLOCK** — a declared `ExternalSecret` that's absent
  (or missing a declared key) on the live target FAILS the deploy before
  the first apply, in its OWN declared namespace, reusing the same
  SecretGetter the `secretKeyRef` preflight uses. DNS can't be verified
  authoritatively, so `required_dns` is a checklist note, not a block.
- **Cross-secret byte-match** — when ONE logical value is projected to N
  refs (the same token under two names), give each `ExternalSecret` a
  shared `value_group`. KCL rejects a group whose members declare
  different key sets at load; the preflight byte-compares the live values
  and BLOCKS on a divergence (a half-rotated credential).

`forge` never creates these resources — the declaration drives the
checklist + preflight only; nothing leaks into the rendered manifests.

## How projects consume this

Project's `deploy/kcl/kcl.mod`:

```toml
[package]
name = "myapp"
edition = "v0.11.0"
version = "0.0.1"

[dependencies]
```

There is no `forge` dependency to declare, and no tag, registry or project-local
copy to pin. Every forge command that evaluates KCL supplies this module — the
copy embedded in the forge binary doing the evaluation — as an external package,
materialized once into a content-addressed user cache
(`<UserCacheDir>/forge/kcl/<hash>/`, overridable with
`FORGE_KCL_MODULE_CACHE`). A project pinned to a released forge therefore
renders against exactly that release's module wherever it runs: CI installs the
pinned forge, and the binary IS the module. No network, no git, nothing to
commit. Because kpm does not know about the module, the stock `kcl` CLI cannot
render a project on its own; render through forge (`forge env render <env>`).

Project's `deploy/kcl/dev/main.k` ends with the one entrypoint:

```kcl
import forge
import ..workloads as wl

output = forge.render(forge.Bundle {
    project = "myapp"
    workloads = [
        wl.api | {runtime = forge.OnHost {runner = "air"}}
        wl.migrate | {runtime = forge.OnHost {}}
    ]
    frontends = [forge.Frontend {name = "admin-web", path = "frontends/admin-web"}]
})
```

Then `forge env render dev` prints it, and every forge command reads it.

## Standard `-D` render options

The forge CLI drives every render with a standard set of top-level KCL
bindings (`kcl run -D <key>=<value>`). They carry per-invocation facts into
your `main.k`. Read them through the **typed `forge` accessors** (each wraps
`option(...)` with a default + doc) rather than raw `option()` so the whole
set is discoverable from the `forge` surface:

| `-D` key        | Accessor                   | Always passed? | What it is                                                                                    |
| --------------- | -------------------------- | -------------- | --------------------------------------------------------------------------------------------- |
| `env`           | `forge.env(default)`       | yes            | environment name (`dev`/`staging`/`prod`/…)                                                   |
| `image_tag`     | `forge.image_tag(env)`     | yes            | resolved image tag (override > per-env default > `latest`); `Bundle.image_tag` defaults to it |
| `namespace`     | `forge.namespace(default)` | yes            | k8s namespace to deploy into                                                                  |
| `image_digests` | `forge.image_digests()`    | when deploying | JSON name→digest map (pins each image to its digest)                                          |

The image **registry** is not a render option, and not an env field either: an
environment does not have a registry. A **workload** does, as part of its
`image` (`image = "ghcr.io/acme/api"` in `deploy/kcl/workloads.k`). forge
contributes only the tag and, after a push, the digest, composing them onto the
reference you wrote. `forge env build <env> --push`, `forge registry login <env>`
and `forge env deploy <env>` all follow the images — so two workloads in one env
can ride two different registries.

Per-env **config** is NOT passed via `-D`: it lives in the typed `AppConfig`
instance in `deploy/kcl/<env>/config.k` and is projected into each workload's
env by `config_gen.appConfigEnvMap` — read config there, never raw
`option()`.

### Per-env conditional manifests

Use `forge.env()` to conditionally include manifests — e.g. skip in-cluster
infra on `dev-host` envs where docker-compose already provides those
services:

```kcl
_is_dev_host = forge.env() == "dev-host"

_bundle = forge.Bundle {
    project = "acme"
    workloads = [...]
    manifests = [] if _is_dev_host else [forge.Manifests {objects = [
        # in-cluster NATS, Temporal, LiteLLM, etc.
    ]}]
}
```

## Labels forge stamps — and `forge.dev/env`

Every k8s object the render emits carries the k8s-recommended set
(`app.kubernetes.io/name`, `.../managed-by=forge`, `.../part-of`) **plus a
forge-owned ownership tag**:

| Label               | Value                       | What it answers                            |
| ------------------- | --------------------------- | ------------------------------------------ |
| `forge.dev/env`     | the environment name        | _which env rendered this object_           |
| `forge.dev/cluster` | a kubectl context (`k3d-…`) | _which cluster this manifest is pinned to_ |

`forge.dev/env` exists because two envs can legitimately resolve to the SAME
namespace (a `dev` and a `dev-k8s` both rendering into `myapp-dev`). Deploying
one then silently replaces the other's workloads, and without the tag nothing
on the surviving objects says whose render won. With it:

```sh
kubectl get all -n myapp-dev -l 'forge.dev/env!=dev'   # what dev does NOT own
```

**It is automatic.** The value defaults to the `-D env=<name>` binding the CLI
passes on every render, so a project gets it without editing any KCL.

**Override it with a KCL drill-in** — a schema field, not a CLI flag:

```kcl
_bundle = forge.Bundle {
    project = "acme"
    env = "dev-k8s"        # this render's ownership tag
    cluster_target = _target
    workloads = [...]
}
```

A single object can also opt out by naming the label itself —
the stamp merges UNDER labels you already set, so an explicit
`metadata.labels."forge.dev/env"` always wins.

The stamp writes **label maps only**: object `metadata.labels`, and the pod
template's labels for the workload kinds (so pods are selectable too). It
never touches a selector — `spec.selector.matchLabels` is immutable in
Kubernetes, and every forge builder derives its selector from a separate
one-key `app.kubernetes.io/name` literal. Rendering with no `-D env=` binding
and no drill-in emits no label at all, so a plain `kcl run` is byte-identical
to the pre-stamp output. See `lib/labels.k`.

Raw objects go in `Bundle.manifests` (`forge.Manifests`), which the same
gate stamps. Cluster workloads' Kubernetes objects are rendered by Go from
their Workload records and carry the record's labels.

## Secrets — the `ConfigSecretRef` config-contract override

For a SENSITIVE config field (`sensitive: true` in the config proto), forge's
config codegen types the field as a `ConfigSecretRef` on the generated
`AppConfig` schema, defaulting to the `<project>-secrets` Secret and a
`<env_var lowercased>` key; `config_gen.appConfigEnvMap(cfg, config_secrets)`
projects it to a `forge.SecretRef` entry of the workload's `env` map (a
`secretRef` on a cluster or host workload, a `managedSecret` of the same
store key on a hosted one). To bind a field to a DIFFERENT existing cluster
Secret/key, set its `ConfigSecretRef` in the per-env `deploy/kcl/<env>/config.k`:

```kcl
# deploy/kcl/prod/config.k
app_config: config_gen.AppConfig = {
    internal_service_secret = config_gen.ConfigSecretRef {
        name = "control-plane-internal", key = "secret"
    }
}
```

Use a kebab-case `key` for cluster secrets whose keys don't match forge's
lowercase-env-var default (e.g. `key = "database-url"`). The Secret itself is
provisioned out-of-band (ESO / sealed-secrets / `kubectl create secret`). A
hand-written credential is `env = {X = forge.SecretRef {name = ..., key = ...}}`.

## Versioning

The module version IS the forge version doing the render — there is nothing
separate to pin, refresh or go stale. Pin forge (go.mod's
`github.com/reliant-labs/forge vX.Y.Z`, and the matching `go install` in CI) and
the module is pinned with it.

When a release changes this module's schemas in a breaking way, forge ships a
migration skill for that release; `forge project upgrade list` surfaces the
ones your project still needs.

See `docs/adr/0003-kcl-module-from-the-binary.md` for why the binary is the
only source (and `docs/adr/0001-always-vendor-forge-kcl.md` for the vendored
copy it replaced).

## Layout

```
kcl/
  kcl.mod              # module declaration
  README.md            # you are here
  workload.k           # fw.Workload, env references, the runtimes
  schema.k             # the Bundle and every env-level declaration
  render.k             # the lowering: Bundle -> output (forge.render)
  core.k               # env-map helpers for [EnvVar] lists (frontends)
  base.k               # -D option accessors, env groups, helpers
  workloads/schema.k   # `forge.workloads` (fw): re-exports for authors
  tiers/tiers_gen.k    # GENERATED from pkg/deploy/v1alpha1 (+ the hosted mask)
  lib/
    capabilities.k     # kind x capability matrix
    images.k           # cluster image reference resolution
    workload_url.k     # forge.WorkloadURL resolution
    gateway.k          # Gateway API builders
    labels.k           # label sets and the forge.dev/env stamp
    crd.k, jwks.k, annotations.k
  example/dev/main.k   # one env using every runtime
  tests/               # KCL-level invariant tests (go test ./internal/templates -run TestKCLModule)
```
