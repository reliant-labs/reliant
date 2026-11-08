"""
What app RUNS — every workload, declared ONCE.

YOURS. forge scaffolds this file once and never rewrites it. `forge
scaffold service|worker|binary|operator` APPENDS a new declaration to the
end; nothing else forge does touches what is already here. Edit it
freely — reorder it, comment it, delete a workload you no longer ship.

DECLARE ONCE, BIND PER ENV
==========================
A declaration here states the facts that are true WHEREVER the workload
runs: how it is built, the subcommand it runs (`args`), its ports, and the
credentials it reads (`config_secrets`). It says nothing about WHERE it
runs. Each environment's `deploy/kcl/<env>/main.k` binds it to a runtime:

    forge.OnHost {runner = "go-run"}      a local process (the dev loop)
    forge.OnCluster {target = _prod}      a Kubernetes cluster you operate
    forge.OnHosted {}                     the forge control plane
    forge.OnCompose {service = "nats"}    a docker-compose service
    forge.BuildOnly {}                    built and shipped, never run

The same `args` select the subcommand on every runtime: forge derives
`go run ./cmd/app <args>` on the host, and a pod runs the
image's ENTRYPOINT (`/app/app`) with `<args>`. Nothing about a
workload is re-stated per runtime, so the dev loop and prod cannot drift.

Probes are not declared here, and do not need to be: forge writes a
`/readyz` readiness and a `/healthz` liveness probe on the `http` port into
the spec of every service it builds, on every runtime. That is exactly what
the scaffolded server serves. Declare `probes` only to change them.

WHAT A WORKLOAD IS
==================
One deployable unit: an executable, the args that select what it does,
and where it runs. In Kubernetes terms, what becomes ONE Deployment.

A workload is NOT a proto service. One binary can mount a dozen Connect
services on a shared mux and still be ONE workload, because it is one
process with one image and one set of args. `args = ["server"]` runs
every service, worker and operator in one process; `args = ["<name>"]`
runs exactly one.

A subcommand of your own that mounts SEVERAL components (an `admin-api`
serving every admin service, say) is one workload too. List what it
runs in its `serves`, so `forge lint` knows those components are
deployed and `forge scaffold` does not declare a second workload for one:

    serves = ["billing-admin", "user-admin"]

  kind = "service"   Deployment + Service (named ports)
  kind = "worker"    Deployment, no Service (nothing dials it)
  kind = "cron"      CronJob on `schedule` (a process that runs to completion)
  kind = "job"       one-shot; `before` names who waits on it
  kind = "operator"  controller Deployment + cluster RBAC
  kind = "tool"      built, never scheduled

A field a kind or a runtime cannot honour is REJECTED at render, naming
the field and the reason, rather than silently dropped: `replicas` on a
job, `namespacedRBAC` on a host process, `clusterRBAC` on the hosted
runtime.

REFINE PER ENV
==============
An environment refines a declaration with KCL's `|` merge and states only
the difference:

    wl.tasks | {replicas = 3}
    wl.tasks | {runtime = forge.OnCluster {target = _k3d}}

`|` preserves the schema type, so every check re-runs on the merged
result: a bad override fails at that env, naming the field.

ADDING SOMETHING THAT ISN'T GO CODE
===================================
Anything can be a workload. Declare it in the env that HAS it rather than
here, and only that env gets it. Every image names its own registry — a
third-party one included, because there is no env registry to fall back on:

    # in deploy/kcl/dev/main.k
    _nats = fw.Workload {
        name = "nats"
        image = "docker.io/library/nats:2.10"
        ports = [fw.Port {name = "client", port = 4222}]
        runtime = forge.OnCluster {target = _k3d}
    }
"""

import forge
import forge.workloads as fw

# One NAMED declaration per deployable unit. The name is what each
# environment refines (`wl.tasks | {...}`), which is why these are
# bindings rather than entries in one anonymous list.

tasks = fw.Workload {
    name = "tasks"
    kind = "service"
    image = "REPLACE-ME-REGISTRY.invalid/app"
    build = forge.GoBuild {cmd = "./cmd/app", output_name = "app"}
    args = ["tasks"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    config_secrets = ["DATABASE_URL"]
}

# The deploy-time schema migration — YOURS to change.
#
# `before = [fw.BEFORE_ALL]` is the BROADCAST form: it gates EVERY workload
# in whichever environment deploys it, without naming any of them. On a
# cluster that is an initContainer on each dependent, so the schema is
# current BEFORE any new pod serves a request and a failed migration stalls
# the rollout with the old pods still serving. On the host runtime forge
# runs it to completion before starting anything it gates, and on the
# hosted runtime the control plane does the same. Concurrent replicas are
# safe — golang-migrate takes a postgres advisory lock.
#
# Do NOT replace the wildcard with a list of workload names. The list would
# go stale the next time someone adds a workload, and the failure is silent:
# it renders, it passes every check, and the new workload just is not gated.
#
# An environment that migrates OUT OF BAND (a DBA-run pipeline, a managed
# database console, a separate release train) drops this workload from the
# list it deploys, in deploy/kcl/<env>/main.k:
#
#     workloads = [w for w in wl.ALL if w.name != "migrate"]
#
# To run a different tool entirely, set `command` (the argv, verbatim).
migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    image = "REPLACE-ME-REGISTRY.invalid/app"
    build = forge.GoBuild {cmd = "./cmd/app", output_name = "app"}
    args = ["db", "migrate", "up"]
    before = [fw.BEFORE_ALL]
    config_secrets = ["DATABASE_URL"]
}

ticker = fw.Workload {
    name = "ticker"
    kind = "worker"
    image = "REPLACE-ME-REGISTRY.invalid/app"
    build = forge.GoBuild {cmd = "./cmd/app", output_name = "app"}
    args = ["ticker"]
    config_secrets = ["DATABASE_URL"]
}

# Every workload this project ships. `forge scaffold` appends to this
# list, and each environment starts from it.
#
# An environment is NOT obliged to deploy all of them — `deploy/kcl/<env>/main.k`
# names what IT runs, so an env can drop one, refine one, or add a workload
# only it has. This list is "what exists", not "what every env deploys".
ALL: [fw.Workload] = [migrate, tasks, ticker]
