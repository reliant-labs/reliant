---
name: dev
description: Local-cluster dev loop primitives — cluster lifecycle, status, logs, ingress URLs, host/cluster split. Compose with project-specific bash for sibling-repo deploys, helm bootstraps, and webhook listeners.
---

# Forge Dev Loop

For local-dev-against-a-cluster workflows. For local-go-only (no k8s) see the
`forge` skill.

## Commands

| Command | What it does |
|---|---|
| `forge cluster up [--wait]` | Create k3d cluster from `deploy/k3d.yaml`. Idempotent — no-op if already up. |
| `forge cluster down` | Delete the cluster. Idempotent — no-op if not present. |
| `forge cluster reset` | Down then up (default `--wait=true`). |
| `forge cluster reload` | Re-render `deploy/kcl/dev/` + kubectl apply + wait rollout. The inner-loop reload after editing code or KCL. |
| `forge cluster status [--json]` | Cluster up/down + kubectl context + config path + pods in the dev namespace + ingress URLs + sibling dev namespaces. |
| `forge cluster logs [--service x] [--tail N]` | Stream `kubectl logs -f` for one or all forge-managed pods in the dev namespace. |
| `forge cluster info` | Diagnostic dump — cluster, context, namespace, declared images, the component list (every server answers on the binary's one mux, `PORT`, default 8080) and declared frontend ports. |
| `forge cluster urls [--json]` | Print the ingress URL table for the dev env (one row per HTTP/GRPC route). |
| `forge cluster instances [--json]` | List every forge-managed dev namespace across every reachable k3d cluster (multi-worktree). |
| `forge env up <env> --target <workload> [--background]` | Single-workload runner: scopes the WHOLE run — build, deploy, host and frontend phases — to the named workload. A workload bound to `forge.OnHost` launches as a host process, dispatching on its `runner` (`go-run` / `air` / `binary` / `delve`). |
| `forge env options <env> [--json]` | List the `-D` render options that env's KCL declares (see below). |
| `forge env config <env> [--json] [--workload <name>]` | Print the resolved configuration `deploy/kcl/<env>/` hands each workload — the values `forge env up` passes to each process (see below). |
| `forge env down <env> [--all]` | Stop this project's stack for that env, tracked or orphaned. `--all`: all of them, machine-wide. Never stops the process running it or any ancestor — safe to run from a shell a forge-started server spawned (see below). |
| `forge env ps` | Every stack running here: project dir, env, process count. |
| `forge env up <env> [--no-build] [--no-deploy] [--target <name>] [-D name=value] [--background]` | The whole-loop orchestrator: build (host-bound workloads need no image) → cluster apply → host launch → frontend dev-serve. Reads each workload's runtime from `deploy/kcl/<env>/`. The cluster steps run only when something the env runs is bound to a cluster (a workload, route, chart, placed Secret or raw manifest); a `cluster_target` that only places the env's Namespace and Gateway needs no cluster, so the scaffolded dev env comes up with none and the run says which cluster it left alone. `--target` narrows WHICH entities each phase acts on; it never turns phases off. |
| `forge env deploy dev [--prune]` | Apply `deploy/kcl/dev/`'s cluster-bound workloads. `--prune` deletes orphan forge-managed Deployments. |
| `forge env deploy dev <version> --target <app>` | The same, scoped: applies ONLY that app's Deployment (repeatable, by workload/frontend name), keeping shared resources (Namespace, ConfigMap/Secret, RBAC). A scope flag needs a version — with none, the deploy would build and push everything to ship one app, so forge refuses and says this. `forge env up dev --target <app>` is the dev-loop spelling that needs no release. |

## Host vs cluster: where does each workload run in dev?

Every workload's runtime is DECLARED in the env, one binding per workload —
never asserted from the command line, and never an env-wide default. The
scaffolded `deploy/kcl/dev/main.k` binds each workload through a named binder:

```kcl
# deploy/kcl/dev/main.k
_workloads = [
    _on_host_job(wl.migrate)    # go run ./cmd/acme db migrate up, to completion, first
    _on_host(_api)              # air: ./tmp/acme server — EVERY service + worker, hot reload
    _on_k3d(wl.reaper)          # an operator: a pod in the local k3d cluster
]
```

**The API is one process.** `_api` (declared in the env) runs the binary's
`server`: every service on one Connect mux, every worker beside it. The
frontend's dev config resolves the API at the `<project>-dev-api` port, which
`_port_of` gives `_api`, so the browser reaches every service at one origin —
the same `_api` staging and prod host (`deploy/hosting`). A new service or
worker needs no line here.

`_on_host` binds `forge.OnHost {runner = ..., listen_ports = [...]}`. `server`
runs under **air** — rebuilt and restarted when a .go file changes;
`.air.toml`'s `entrypoint` is the same `server`, and `forge env up` refuses
to start, naming the fix, when air is not installed (`go install
github.com/air-verse/air@latest`, or `runner = "go-run"` to give up the
reload). Anything else runs `go run`, its argv DERIVED from the workload's
`build` + `args`. `_on_k3d` binds `forge.OnCluster {target = _k3d}`: the image
`forge build dev` pushes, with the same `args`.

Rebinding is editing one line: `_on_k3d(_api)` runs the API as a pod. To run
ONE service alone (to debug it), bind `_on_host(wl.item)` and give it a root
of its own (a `//forge:root` struct in internal/app — `architecture`, "Roots
and commands") that `server`'s root no longer holds, or both serve it. To vary HOW
something launches per run without an edit, declare a render option and read
it in the binding (`runner = option("host_runner") or "air"`, then `-D
host_runner=go-run`).

The decision rule:

| Workload shape | dev binding |
|---|---|
| Connect-RPC service, worker, one-shot job | `forge.OnHost` (`_on_host` / `_on_host_job`) |
| Operator (controller-runtime, watches CRDs) | `forge.OnCluster` (`_on_k3d`) — the host runtime refuses it |
| `kind = "cron"` (a run-to-completion CronJob) | `forge.OnCluster` — Kubernetes schedules it |
| Needs an Ingress, sidecars, or the cluster API | `forge.OnCluster` |

The host runtime REFUSES what a process cannot honour — `namespacedRBAC` /
`clusterRBAC`, sidecars, volumes, replicas > 1 — naming the workload and
field, so a binding that cannot work never silently drops a field.

`forge env up staging` and `forge env deploy prod` see whatever each env's
`main.k` binds — typically every workload on the env's cluster.

A host workload's env composes three layers: the bundle's
`secret_provider` values first, then the workload's `env` (the typed config
projection plus its own entries), then your shell, so an exported variable
wins. See `secrets` for the provider model.

## Inner loop: editing a host-bound workload

`forge env up dev` is the one-command inner loop — host infra up, cluster-bound
workloads applied, host-bound workloads launched (jobs to completion first),
every frontend dev-served — whatever runtime it binds, `up` serves it with
`<dev_runner> dev` on its `port` (a frontend's runtime decides what a DEPLOY
does with it; dev binds `forge.OnHost {}`). It
also keeps two gitignored prerequisites fresh, each gated on staleness (a no-op
in the steady state):

- **Generated code** — runs `forge generate` when `gen/` is missing or
  `proto/` is newer than the generated tree (`--no-generate` to skip).
- **Frontend deps** — runs `<dev_runner> install` for a frontend whose
  `node_modules` is missing or older than its lockfile/manifest
  (`--no-install` to skip).

After a batch of proto edits, `forge scaffold` catches the tree up (see
`forge` and `db`). `forge env up` also auto-seed a
fresh dev DB from the applied schema on first boot, so a clean checkout comes up
with FK-coherent demo data — dev only; `--no-seed` opts out, `forge db seed
status` inspects.

For fine-grained control:

```bash
# Terminal 1: long-running infra + cluster-bound workloads
# (only once something in dev is bound to the cluster — `_on_k3d(...)`;
# the scaffolded dev env runs everything on the host and needs neither)
forge cluster up --wait
forge env deploy dev

# Terminal 2: the API (every service; air reloads it as you edit)
forge env up dev --target api                  # foreground; Ctrl-C to stop
# or detach + tail logs separately:
forge env up dev --target api --background     # detach; PIDs tracked per env
forge env down dev                                     # later teardown
```

The host child process also inherits the host shell's env, so anything already
exported wins over both the injected `secret_provider` layer and the workload's `env`.

## Render options: varying a run without editing KCL

To change something per run rather than per commit — which runner a host
service launches under, whether to point at a remote dependency — use a
**render option**. An env declares one by *reading* it, so the call site is the
declaration:

```python
# deploy/kcl/dev/main.k
_host_runner = option("host_runner", type="str", default="air",
                      help="Host launch runner: air (default) or go-run")
```

```
forge env options dev            # what this env declares
forge env up dev -D host_runner=go-run
```

forge does **not** interpret the value: it checks the *name* against what the
env declares (a typo fails instead of silently doing nothing) and relays the
value verbatim as a string. Your KCL decides what it means. Declare
`type` / `default` / `help` on `option()` — they are what `forge env options`
shows the next reader.

- **Options forge derives are refused.** `env`, `namespace`, `image_tag`,
  `release_version`, `image_digests`, `worktree`, `branch` are computed by
  forge; a caller-supplied value would disagree with what was actually built
  and applied.
- **`-D` is accepted on `env up` only**, never `env deploy`. A cluster apply
  has to be reproducible from the repo alone.

Because the KCL resolves the option, it can do what a CLI flag could not — the
canonical case being leaving Air, where `HostOverrides` forbids `air_config`
unless the runner is `air`, and the Air config is usually the only place the
service's real entrypoint is written down:

```python
host = forge.HostOverrides {
    runner = _host_runner
    if _host_runner == "air":
        air_config = ".air.api.toml"
    else:
        # what the Air config was carrying
        command_override = ["go", "run", "./cmd/api", "server", "api"]
}
```

## Reading an environment's resolved configuration

`forge env config <env>` prints what each workload is actually configured
with — the same values `forge env up` hands the processes it launches, with
launch-resolved ports reported as launched rather than as rendered.

```bash
forge env config dev                      # every workload, grouped
forge env config dev --workload api       # just one
forge env config dev --json               # machine-readable
```

**This is how you find the database (or the broker, or the bucket) you are
working on.** Do not go looking for it in `docker ps`: on a machine running
several projects that finds *a* postgres, not necessarily *this* project's, and
the mistake reads as correct right up until the schema disagrees. Do not
evaluate the KCL template string by hand either — an ephemeral port resolved at
launch lives in the run state, not in the source.

There is no `db dsn`-style command, since a project may run two databases, none,
or reach its store over something that is not a DSN. Select what you need out of
`env config`:

```bash
# whatever this project calls its database
psql "$(forge env config dev --json | jq -r '.workloads[].env.DATABASE_URL // empty' | head -1)"

# every `forge db` subcommand accepts an explicit --dsn, and otherwise
# falls back to $DATABASE_URL — so export it once and they all follow
export DATABASE_URL="$(forge env config dev --json | jq -r '.workloads[].env.DATABASE_URL // empty' | head -1)"
forge db seed status
```

## Logs & the `forge env up` summary

`forge env up <env>` writes every host service's and frontend's output
to a stable, greppable location:

```
.forge/logs/<env>/<service>.log
.forge/logs/<env>/frontend_<name>.log
```

This holds in **both** modes — foreground tees the file alongside the live
`[name]`-prefixed terminal stream, `--background` uses it as the sole sink. The
directory is gitignored (`.forge/*`). The path is project-relative and
deterministic, so read one service's output directly instead of scraping
interleaved scrollback:

```bash
tail -f .forge/logs/dev/admin-server.log
grep -i "error\|panic" .forge/logs/dev/*.log
```

After the host + frontend phases start, `up` prints a summary box listing each
process, its URL and its log path. Host-service URLs are derived from each
service's KCL `PORT` env var; a service that declares no `PORT` is listed
without one. Cluster service routes (Gateway API) are not host-local — list
them with `forge cluster urls`.

## Composing with Taskfile (cloud-dev pattern)

```yaml
# Taskfile.yml
tasks:
  dev:
    desc: Bring up the cluster + its workloads, run host workloads locally
    cmds:
      - forge cluster up --wait
      - forge env deploy dev --prune       # cluster-bound workloads only
      - forge env up dev --target api --background

  dev-stop:
    cmds:
      - forge env down dev
```

## Safety: kubectl context pinning

Every `forge cluster` command runs against `k3d-<cluster-name>` (resolved from
`deploy/k3d.yaml` metadata.name, falling back to forge.yaml `name`), so you
cannot accidentally `forge cluster reload` into staging or prod.

`forge env deploy <env>` is DECLARATIVE-ONLY for cluster selection: the target
kubectl context comes SOLELY from the `cluster` of the `forge.ClusterTarget` a
workload binds (`forge.OnCluster {target = ...}`) in `deploy/kcl/<env>/main.k`,
threaded as `--context <declared>` on every kubectl call. It never reads or
falls back to your current kubectl context, and there is no CLI override. Dev
declares `k3d-<project>`; staging/prod declare their own:

```kcl
# deploy/kcl/prod/main.k
_cluster = forge.ClusterTarget {
    cluster = "gke_acme-prod_us-central1_cluster-1"
    namespace = "myapp-prod"
    platform = "amd64"
}
```

A ClusterTarget declares no registry: each workload declares its own, as part
of its `image` in `deploy/kcl/workloads.k`.

The deploy fails fast (even under `--dry-run`) if the declared cluster has no
matching kubectl context. Fix your kubeconfig (e.g. `gcloud container clusters
get-credentials ...`) or correct the ClusterTarget's `cluster` — there is no
`--context` escape hatch. `forge env deploy <env> --explain` prints the declared
context and whether it exists.

## Safety: stopping a stack never stops the process running the command

`forge env down` (both forms) and the `forge env up` pre-flight select their
targets by forge's ownership markers — environment variables every child of a
forge-started process inherits, including the shells an agent server spawns.
So the command walks its OWN parent chain first and never signals an ancestor
or the tree under it:

```
[up] skipped pid 1234 (reliant serve --port 3090): it is an ancestor of this command — stopping it would end the session running you
[up] frontend:web: stopping (pid 2345 + tree)
[down] left 1 process tree(s) and the host infrastructure for env=dev running: they host this command. Stop them from a shell outside that stack.
```

Everything else stops as usual and the command exits 0. `forge env up` refuses
outright (stopping nothing) when the stack it would replace hosts it. The
decision reads only the kernel's parent links — there is no flag or env var to
turn it off. Still never `pkill`/`killall` forge: a pattern kill has no such
guard.

## Multi-worktree / multi-namespace

For per-worktree namespacing — each worktree its own namespace, one shared
cluster — set the `namespace` field on each worktree's `forge.ClusterTarget`
in `deploy/kcl/dev/main.k` (or via the `FORGE_DEV_NAMESPACE` env override if
your bootstrap supports it). `forge cluster instances` then lists every dev
namespace on the host with its pod count.

## What forge does NOT own

Forge owns the universal cluster + ingress + status mechanics. These stay in
`scripts/`, called from `Taskfile.yml` and composed with the `forge cluster`
primitives above:

- Sibling-repo deploys (project-specific helm installs, manifest applies)
- Helm chart bootstraps (project-specific stack — Postgres, Redis, observability)
- Webhook listeners (Stripe `stripe listen`, GitHub `gh webhook forward`, etc.)
- Project-specific DB seeding (custom schema + fixtures)
- Cross-service smoke tests (project-specific business invariants)

## CI usage

```bash
# guard: did we forget to run forge generate?
forge generate --check

# build + push each image to the reference its own workload declares in
# deploy/kcl/workloads.k — the same reference `forge env deploy staging` pulls.
# --push takes no value: to push elsewhere, change the workload's image.
forge env build staging --push

# deploy with context guard
forge env deploy staging
```

## When this skill is not enough

- Production deploy → see `deploy` skill (`forge env deploy <env>`)
- Greenfield setup → see `forge`
- Multi-cluster operator workflows → see `operators`
- Observability stack queries → see `observability`
