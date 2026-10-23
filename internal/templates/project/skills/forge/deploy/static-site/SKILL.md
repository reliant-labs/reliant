---
name: deploy/static-site
description: Ship a landing page, marketing site, docs site or SPA as a static site — when forge is the right call for one, the static export, binding it to hosted static hosting (the platform owns bucket + CDN) or your own bucket, and the build-once → promote release flow.
---

# Static sites

A landing page, a marketing site, a docs site or a SPA over someone else's API
is a **frontend with no backend**. Forge ships it the same way it ships an app:
declared per env, built once, promoted by digest. What you get over dropping
files on a host is the part that is hard to retrofit later:

- **Environments from day 0**: a `staging` that promotes the exact bytes to
  `prod`, not a rebuild that happens to look the same.
- **Hosted static hosting.** `forge.OnHosted {}` publishes to the control
  plane, which owns the bucket, the CDN and a platform hostname. Custom
  domains are `forge domain`.
- **Config without a rebuild.** A hosted site's `config.js` is written per env
  by the platform, so one bundle serves every env.
- **A path to an app.** When the site grows a waitlist, checkout or login,
  `forge scaffold service <entity>` adds the Go API next to it. The site is
  already the frontend of a forge project and nothing has to move.

If none of that matters — a one-off page that will never have a second env —
a plain `index.html` is fine, and forge is the wrong shape for it.

## 1. Scaffold a static frontend

```bash
forge project new site --mod github.com/acme/site --disable orm,migrations \
  --frontend web --output static --routes none
cd site
```

- `npm run build` is a static export into `out/`. That is the scaffold default
  (`output: "export"` in `next.config.ts`). A static runtime publishes a directory,
  and `out/` is that directory.
- `--routes none` generates no CRUD pages, because a site with no backend has
  no entities. A site that has entities keeps its generated pages, which are
  static routes (`/<entity>/view?id=…`), so the export still builds.
- `--kind vite-spa` is the other static-capable shape (its build lands in
  `dist/`).

The scaffolded UI is starter code. Before designing the page, load
`frontend/design`; it asks for the brief first.

### Already have the site? Adopt it where it stands

An existing Vite SPA or Next.js static export (a landing page on Cloudflare
Pages, Netlify, Firebase) becomes a forge project without moving a file:

```bash
cd my-site                                    # the app's root: package.json + vite.config / next.config
reliant forge project new --in-place --kind site --name my-site
```

That writes `forge.yaml` (no `module_path`: a site has no Go module),
`deploy/kcl/kcl.mod` and `deploy/kcl/{dev,staging,prod}/main.k`, each
declaring the root app (`path = "."`, `type = "vite"`, `public_dir = "dist"`
or the literal `build.outDir` it found) bound to its dev server in dev and to
`forge.OnHosted {}` in staging and prod. The app's own files are untouched,
and `.gitignore` is merged. With no go.mod, every Go step (codegen, tidy,
golangci-lint, the contract linter) skips by itself, and the root app is in
the inventory `forge lint` and `forge build` load. A bare `--in-place` over
such a directory stops and names `--kind site` rather than scaffolding a Go
server beside the app.

Build-time keys (`VITE_*`, `NEXT_PUBLIC_*`) become `env_vars` on the frontend
in each env, and are baked in per release; values that must change without a
rebuild belong in `runtime_config`.

## 2. Bind it to a static runtime, per env

A frontend states where it runs in each env's `deploy/kcl/<env>/main.k`:

| `runtime =` | Where the bytes go |
|---|---|
| `forge.OnHosted {}` | the control plane's static hosting: the platform owns the bucket, the CDN and the hostname |
| `forge.OnBucket {bucket = ...}` | your own bucket: `releases/<digest>/` + `live/`, then CDN invalidation |
| `forge.OnFirebase {project, site}` | `firebase deploy` |

Hosted static hosting accepts only a static export, and **a static site alone
is free** (one site per org, no billing). Past that — a second site, or any hosted
workload or managed database beside it — the deploy needs billing and is
queued, not refused, until it is set up (exit 7; `deploy/hosting`).

A site with no backend needs nothing else in the env. This is a complete
hosted `prod`:

```kcl
import forge

output = forge.render(forge.Bundle {
    project = "site"
    control_plane = forge.ControlPlane {}    # Reliant cloud; needed by OnHosted
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        public_dir = "out"                    # where the static export lands
        image = "web"                        # platform resolves its registry/org/project
        runtime = forge.OnHosted {}
    }]
})
```

A hosted `image` names the artifact, not an invented external registry.
At build time forge resolves it under the platform registry, your credential's
organization and this project, then appends `static.v1`. The release records
that full pullable address; live deployment refuses an unresolved address.
Offline render/admission uses explicitly fake `preflight.forge.invalid`
release facts when no credential or release exists; those records cannot ship.

With ORM and migrations disabled and no backend components, the scaffold
contains no migration job, database, identity provider or cluster. Frontends
added later with `forge scaffold frontend docs --routes none` get the same
named declaration plus one binder line, so `forge env new preview --from prod
--bind docs=hosted` works for either entry point. Invalid bindings leave the
new environment unwritten (and `--force` leaves existing files unchanged).

## Run it locally

```bash
forge env up dev    # the frontend's dev server; prints its URL
```

No database or cluster is needed: the dev env declares only frontend dev
servers, and `env up` starts each in its actual frontend directory.

## 3. Ship it

```bash
forge lint --static-export                 # what the export can't serve, with file:line
forge env render prod                      # 0 cluster objects; hosted: web (static)
forge ci validate-kcl                      # the control plane would admit it
forge env build prod --release v0.1.0 --plan   # what would be pushed and recorded; builds nothing
forge env build prod --release v0.1.0      # build once, push, record the release
forge env deploy prod v0.1.0               # publish; the platform syncs it and writes config.js
forge env status prod                      # the release prod runs, and its health
```

`forge env render` (and every deploy and build) refuses a Next.js frontend on
a static runtime whose forge.yaml `output` is not `static`: its build would be
a Node server, with nothing in `out/` to publish. The error names the
frontend, the env and both fixes. `forge lint --static-export` runs offline and
reports, each with file:line, what `next build` would refuse for an export: a
`[id]` route without `generateStaticParams`, server actions, `next/headers`,
next/image without `unoptimized`. It also warns on what the export drops
silently, such as middleware and rewrites. The real `next build` in CI stays
authoritative. The lane only tells you sooner.

A hosted deploy ships only what a release froze, never a local build. Without a
release it refuses, and the error prints the cut-then-deploy command.
Promotion re-points another env at the same digest:
`forge env deploy staging v0.1.0` and then `forge env deploy prod v0.1.0` ship
byte-identical sites. Recovery rolls forward to a new release; there is no
rollback.

Signed in to Reliant means signed in to the control plane: there is no second
`forge login`. `reliant forge …` and Reliant agents' shells set
`$FORGE_CREDENTIAL_HELPER`, which mints forge a short-lived token from your
Reliant session. Your own hostname: load `deploy/domains`.

## Runtime config

A static bundle cannot read env vars at runtime, so per-env values (an API URL,
a public analytics key) go on the frontend's `runtime_config`, declared in
each env. The bundle reads them as `window.__FORGE_CONFIG__`:

```kcl
runtime_config = {
    API_URL = forge.WorkloadURL { workload = "api" }   # a workload's URL in the same env
    SITE_NAME = "acme"
}
```

On `OnHosted` the references are kept in the published spec, and the platform
writes `config.js` after every sync, so the release carries no `config.js` and
promotes across envs unchanged.

Secrets do not belong in a static site: everything in `config.js` is public.
A value that must stay secret belongs on a service (`forge secret set`).

## Growing a backend

`forge scaffold service <entity>` adds the Go API to the same project, and a
`fw.Workload` bound `forge.OnHosted {}` runs it beside the site. The frontend
reaches it through `runtime_config` (`workloadURL`). The CRUD pages generated
for the new entities are static routes, so the site still exports. Write your
own pages the same way: put the id in the query string and never use a `[id]`
segment (`frontend/pages`). A frontend scaffolded by forge ≤ v0.1.43 has
`[id]` pages; `forge skill load migrations/v0.1.44` converts them.

Two cases leave static hosting behind: server rendering, and request-time
server APIs such as server actions, middleware, and `cookies()`. For those,
switch `output` to `standalone` and ship the frontend as a workload instead
(`deploy`, `frontend/serving`).

## How hosted static hosting resolves a path

A request for `/books/view?id=…` is served `books/view.html`. The export
writes one `.html` file per route because `trailingSlash` is off, and the
hosted origin tries `<path>`, then `<path>.html`, then `<path>/index.html`.
Anything else route-shaped falls back to the site's root `index.html`. Every
link the scaffold generates resolves to the file for its own route.
`frontend/serving` covers hosts that only map directories to `index.html`.

A missing file that is not route-shaped (`/assets/app-1a2b.js`) is a 404,
never the HTML fallback. The frontend's content-hashed asset directory
(`asset_dir`: `_next/static` for Next.js, `assets` for Vite) is published as
the site's `assetDir`: files under it are served
`public, max-age=31536000, immutable`, everything else `no-cache`, and the
previous releases' copies stay serveable after a deploy, so a tab opened
before it can still lazy-load the chunks it was built against.
