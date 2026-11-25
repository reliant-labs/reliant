---
name: deploy/hosting/adopt
description: Moving an existing cluster-bound forge project onto Reliant hosting — the Bundle, `_hosted`, `_api` and frontend edits, which cluster-only fields to drop, and how to check the result.
---

# Adopting hosting in an existing project
Env files are scaffolded once and never rewritten, so a project scaffolded
before this default keeps its `_on_cluster` envs. To host one (try it beside
prod first: `forge env new cloud --from prod --bind <name>=hosted` derives the
env and adds the control plane):

1. In the Bundle: `control_plane = forge.ControlPlane {}`, `secret_provider =
   forge.HostedSecrets {}`, `databases = [_db]` with `_db =
   forge.ManagedDatabase {name = "<project>", runtime = forge.OnHosted {}}`
   (`<project>-db` when the project is named `api` or `migrate`: a workload
   and a database may not share a name).
2. In `_hosted`: `image = w.image[w.image.rfind("/") + 1:] if w.image else
   w.image`, and `DATABASE_URL = forge.DatabaseRef {name = _db.name}` in its
   env (or keep your own Postgres: `forge secret set --env <env> DATABASE_URL`).
3. Declare `_api` (above) and bind `_hosted(_api)` in place of every service
   and worker line; rebind the rest (`migrate`, other jobs) `_hosted(wl.<name>)`.
   A frontend `_on_bucket(...)` → hosted (`image = "<name>"`, `runtime =
   forge.OnHosted {}`, `runtime_config = {API_URL = forge.WorkloadURL
   {workload = "api"}}`). A project scaffolded hosted BEFORE `_api` existed
   binds each service `_hosted(wl.<name>)` and its frontend reaches only the
   first: make the same `_api` edit.
4. Drop the cluster-only fields (`cluster_target`, `network_policy`,
   gateways/routes, `ExternalSecrets`) once nothing is bound to the cluster.

`forge env render <env>` then `forge env new <env> --check` (renders, no
placeholder left, admissible to the control plane). The scaffold's current
shape: `forge project new` in a scratch dir, `deploy/kcl/prod/main.k`.
