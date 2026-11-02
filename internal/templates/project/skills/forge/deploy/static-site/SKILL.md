---
name: deploy/static-site
description: Ship a landing page, marketing/docs site, SPA or PWA — no backend needed. When forge is the right call for one, `project new --kind site` (new or adopted), the static export, hosted static hosting (platform owns bucket + CDN; one hosted site per org free, counted across envs) or your own bucket, custom domains, and the build-once → promote release flow.
---

# Static sites

A landing page, a marketing site, a docs site or a SPA over someone else's API
is a **frontend with no backend**. Forge ships it the same way it ships an app:
declared per env, built once, promoted by digest. What you get over dropping
files on a host is the part that is hard to retrofit later:

- **Environments from day 0**: a `staging` that promotes the exact bytes to
  `prod`, not a rebuild that happens to look the same.
- **Hosted static hosting.** `forge.OnHosted {}` publishes to the control
  plane: the platform owns the bucket, the CDN and the hostname. Every hosted
  site is served through the CDN edge: hashed assets cached at the edge, the
  entry document and `config.js` revalidated on every load, so a deploy is
  live on the next request. Custom domains are `forge domain`; one bound to a
  site is served through the same CDN once its DNS points at the records
  `forge domain show` prints, and `forge domain ls` / `forge env status` say
  which path (edge or ingress) each hostname takes.
- **Config without a rebuild.** A hosted site's `config.js` is written per env
  by the platform, so one bundle serves every env.
- **Browser logs on disk.** Under `forge env up dev` every `console.*` call
  and uncaught error lands in `.forge/logs/dev/frontend_<name>.log`.
- **A path to an API.** When the site grows a waitlist, checkout or login,
  the API is its own forge project, and the site reaches it by URL (see
  "Growing a backend").

If none of that matters — a one-off page that will never have a second env —
a plain `index.html` is fine, and forge is the wrong shape for it.

A PWA is a static site too: forge builds, hosts and ships it like any other.
The service worker and web manifest are yours to add; forge scaffolds neither.

## 1. Start a new site

```bash
forge project new site --kind site                          # Next.js static export (the default)
forge project new site --kind site --frontend-kind vite-spa # or a Vite single-page app
cd site
```

That is a frontend-only project: a starter app at the root (`package.json`,
`src/`) plus `forge.yaml` and `deploy/kcl/{dev,staging,prod}/main.k`. No
`go.mod`, `cmd/`, `proto/`, Dockerfile or server. `npm install` runs as part
of it (`FORGE_SKIP_NPM_INSTALL=1` skips it), and a fresh directory outside any
repository gets `git init` and an initial commit. `--in-place` in an empty
directory does the same there.

- **`--frontend-kind nextjs`** (default): `npm run build` is a static export
  into `out/` (`output: "export"` in `next.config.ts`), one HTML file per
  route — what a landing page or marketing site wants for search engines and
  link previews.
- **`--frontend-kind vite-spa`**: a single-page app; its build lands in
  `dist/`. (`vite-spa` is a value of `--frontend-kind`, not of `--kind`; the
  app frontend `forge scaffold frontend --kind vite-spa` writes is wired to a
  Go backend and is not a site.)
- `--frontend <name>` names the frontend in the envs (default `web`).

The starter page is a placeholder. Before designing the page, load
`frontend/design`; it asks for the brief first.

A new site and an adopted one (below) are the same project shape: the app at
the root, declared `path = "."` in every env. Everything after this section
applies to both.

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
| `forge.OnBucket {bucket = ...}` | your own bucket: `releases/<digest>/` + `live/`, the asset dir uploaded immutable and everything else `no-cache` (unless `cache_control` says otherwise), then CDN invalidation |
| `forge.OnFirebase {project, site}` | `firebase deploy` |

Hosted static hosting accepts only a static export. **One hosted static site
per org is free** (no billing, 1 GiB storage cap), and it is counted across
environments: each env's deployed site is a site. A new site binds both
`staging` and `prod` to hosting, so without billing one of them deploys free
(deploy `prod` first if that is the one that must be live) and the other is
queued with `STATIC_FREE_TIER_EXCEEDED`. Past the free site — a second env or
site, or any hosted workload or managed database beside it — the deploy needs
billing and is queued, not refused, until it is set up (exit 7, with the
billing link; `deploy/hosting`).

A site with no backend needs nothing else in the env. This is a complete
hosted `prod`:

```kcl
import forge

output = forge.render(forge.Bundle {
    project = "site"
    control_plane = forge.ControlPlane {}    # Reliant cloud; needed by OnHosted
    frontends = [forge.Frontend {
        name = "web"
        path = "."                            # the app at the project root
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

A site contains no migration job, database, identity provider or cluster.
`forge env new preview --from prod --bind web=hosted` adds another env. Invalid
bindings leave the new environment unwritten (and `--force` leaves existing
files unchanged).

A Go project with an API can also carry a static frontend
(`forge project new app --frontend web` declares it under `frontends/web`);
the binding and release flow below are the same.

## Run it locally

```bash
forge env up dev    # the frontend's dev server; prints its URL
```

No database or cluster is needed: the dev env declares only the frontend's dev
server, and `env up` starts it in the frontend's directory with `PORT` set. A
site forge started mirrors the browser console to
`.forge/logs/dev/frontend_<name>.log`; an adopted app does once it calls
`installDevLogging()` from `@reliantlabs/forge-web-runtime` (load `debug`).

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
byte-identical sites (two hosted envs are two sites: the second needs billing,
above). Recovery rolls forward to a new release; there is no rollback.

**No account at all?** `forge env deploy prod --claimable` ships the same site
with no signup and prints its URL plus a claim link to hand your user; unless
they open it within 72h the site goes offline (`deploy/hosting`, "No account:
`--claimable`").

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
promotes across envs unchanged. There an `API_URL` that references a workload
becomes `"/_api"`, which the platform serves on the site's own hosts
(`deploy/hosting`): the browser never calls the API's hostname.

Secrets do not belong in a static site: everything in `config.js` is public.
A value that must stay secret belongs on a service (`forge secret set`).

## Growing a backend

A site has no Go module, so `forge scaffold service` (and every other scaffold
that writes Go or proto) refuses in it, before writing anything, and says what
to do instead. Give the API its own forge project beside the site:

```bash
forge project new site-api --service waitlist   # a Go API, its own envs and releases
```

Deploy it, then point the site at its URL: a build-time `env_vars` entry
(`NEXT_PUBLIC_API_URL` / `VITE_API_URL`) or a `runtime_config` value on the
site's frontend in each env. The site keeps shipping as a static export.

A project that started as an app (`--service`, with `frontends/<name>`) grows
the other way: `forge scaffold service <entity>` adds to its API, and its
frontend reaches it through `runtime_config` (`forge.WorkloadURL`). The CRUD
pages generated for its entities are static routes, so it still exports. Write
your own pages the same way: put the id in the query string and never use a
`[id]` segment (`frontend/pages`). A frontend scaffolded by forge ≤ v0.1.43
has `[id]` pages; `forge skill load migrations/v0.1.44` converts them.

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
