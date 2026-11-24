---
name: deploy-frontends
description: Bind a frontend per environment to host, hosted static hosting, a bucket or Firebase; declare build facts, runtime config and domains.
---

# Frontend deployment

### Frontends bind a runtime too

A `forge.Frontend` states where it runs per env, like a workload — there is
no default, and a frontend with no `runtime` is a render error naming it:

| `runtime =`                                   | `forge env deploy` does                                                       |
| --------------------------------------------- | ----------------------------------------------------------------------------- |
| `forge.OnHost {}`                             | nothing (the dev server; `forge env up` runs it)                              |
| `forge.OnHosted {}`                           | publishes the site to the control plane's static hosting (below)             |
| `forge.OnBucket {bucket, cdn?, keep_releases}` | uploads to YOUR bucket: `releases/<digest>/` + `live/`, then CDN invalidation |
| `forge.OnFirebase {project, site, ...}`       | `firebase deploy`                                                             |
| `forge.BuildOnly {}`                          | builds it for a sibling frontend's `bundle`; ships nothing                    |

Build facts are the frontend's on every runtime: `public_dir` (default `out`
for Next.js, `dist` otherwise), `asset_dir`, `base_path`, `bundle`,
`cache_control` (OnBucket, OnFirebase). A Firebase SPA sets `spa_fallback`.
A static Next.js frontend needs `output: "export"` in its own next.config.ts
(forge reads the build shape from there; render refuses others; `forge lint --static-export` lists what an export
can't serve); a server build is a workload (`forge.DockerBuild`).

```kcl
_web = forge.Frontend {name = "web", path = "frontends/web", public_dir = "out"}

frontends = [_web | {runtime = forge.OnBucket {bucket = "acme-prod-web"}}]
```

`forge env new cloud --from prod --bind web=bucket` rebinds a scaffolded
frontend's line (`_hosted_frontend(_web_frontend)` → `_on_bucket(...)`).
`forge env deploy <env> <version> --frontends-only` ships only the bucket /
Firebase frontends of a release. It applies no bundle object, so it neither
renders nor compares the env's bundle, and computes no k8s plan: it ships a
release's frontend from any checkout, even one whose workloads have moved on.
A `--target` naming only such frontends behaves the same.

### Hosted static sites

A frontend on `forge.OnHosted {}` publishes into the platform's bucket and
CDN, as an OCI release with **no `config.js` in it**, so `forge env deploy`
moves one digest everywhere. `forge env build <env> --push` pushes it. The
frontend's `runtime_config` becomes the spec's `runtimeConfig`:

```yaml
runtimeConfig:
  API_URL: {workloadURL: {name: api}}   # resolved by the control plane to the hosted api's URL
  APP_NAME: {value: acme}
```

After every sync the control plane writes `config.js`, resolving references
against workloads in the same environment.

A site with no backend is a whole project: `deploy/static-site`.

### Custom domains

Every hosted site and exposed hosted port already answers on a hostname the
platform allocates. To ALSO serve your own, use the `forge domain` commands —
**a hosted domain is NOT spec**, and a hosted frontend or port carrying
`domains` is refused at render. `forge.OnCluster` keeps `Port.domains`.
Domains: `deploy/domains`. Hosted `forge env stop|start|delete`:
`deploy/lifecycle`.

PDB lint + stale-PDB prune: deploy/pdb.
