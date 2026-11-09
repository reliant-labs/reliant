---
name: migration-v1.0.0-service-links
description: Upgrade to v1.0.0's enableServiceLinks false on every forge-rendered pod — find code that read the Kubernetes-injected <SERVICE>_SERVICE_HOST / _SERVICE_PORT / tcp:// _PORT variables, replace them with DNS names or explicit env, opt a workload back in with enableServiceLinks = True, and delete config shadows that only existed to dodge the collision.
relevance: migration
---

# v1.0.0 — pods no longer get service-link env vars (`enableServiceLinks: false`)

Use when a workload reads `<SERVICE>_SERVICE_HOST`, `<SERVICE>_SERVICE_PORT` or
a `<SERVICE>_PORT` / `<SERVICE>_PORT_<n>_TCP*` variable that no `env` entry
declares, or when you shadowed a service-link name by hand (`TEMPORAL_PORT = "7233"`
to stop it parsing `tcp://10.0.0.1:7233`).

## What changed

Kubernetes injects, into every pod, an env var set per Service in the
namespace unless the pod says `enableServiceLinks: false`: a Service
`temporal` becomes `TEMPORAL_PORT=tcp://10.20.15.230:7233`,
`TEMPORAL_SERVICE_HOST` and `TEMPORAL_SERVICE_PORT`. Those names are not
scoped to anything, so they collide with typed config. A config loader that
parses `TEMPORAL_PORT` as an integer failed at boot
(`invalid value "tcp://10.20.15.230:7233" for config field port (from env
TEMPORAL_PORT)`) and CrashLooped a prod gateway; `ZITADEL_PORT` did the same
before it. A collision is always latent: it needs only a Service whose name
matches a config key.

forge now renders `enableServiceLinks: false` on **every** pod template it
writes: Deployment (service, worker, operator), Job (including one that gates
others, whose init container runs in the gated pods), CronJob, and the node
agent's DaemonSet. `kcl/lib/jwks.k`'s Deployment too. Behavior change: the next
deploy rolls every forge-rendered pod once (the pod template changed), and a
standalone Job gets a new spec-hash name.

## Steps

1. Search your own code and `deploy/kcl` for the variables a pod used to
   receive: `rg '_SERVICE_HOST|_SERVICE_PORT|_PORT_[0-9]+_TCP' --glob '!node_modules'`.
   forge's own code reads none.
2. Replace each with a DNS name or an explicit env entry:

   ```python
   env = {"TEMPORAL_HOSTPORT": "temporal.${namespace}.svc:7233"}
   ```

3. A workload that really does read the injected variables (an image you do
   not control) opts back in, per workload. Full profile only; refused on
   hosted:

   ```python
   legacy = fw.Workload {name = "legacy", enableServiceLinks = True}
   ```

4. Delete workarounds that shadowed a service-link name only to stop the
   collision (`TEMPORAL_PORT` set to a bare port number).

## Verify

`forge env render <env>` shows `enableServiceLinks: false` under every pod's
`spec.template.spec` (and `jobTemplate` for a CronJob), and `true` only on the
workloads you opted in. A pod's `kubectl exec <pod> -- env | grep -E '_SERVICE_HOST|_PORT='`
lists nothing from sibling Services.
