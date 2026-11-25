---
name: deploy/static-site
description: Ship a landing page, marketing/docs site, SPA or PWA with Reliant Forge — no backend needed. When forge is the right call for one, `project new --kind site` (new or adopted), the static export, hosted static hosting (platform owns bucket + CDN; one hosted site per person free across their unplanned orgs, counted across envs) or your own bucket, custom domains, and the build-once → promote release flow.
applies: any
---

# Static sites

<!-- @benefits glance site -->

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
reliant forge project new --in-place --kind site --name my-site --dry-run   # every file it would write; writes nothing
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

Because it never edits the app, adoption ends with what the app still needs:

- **Next.js**: it runs the `--static-export` checks and prints each finding
  with file:line. An app that is not yet a static export needs
  `output: "export"` in its `next.config`, and `create-next-app`'s Next 16
  default `cacheComponents: true` must go (`next build` refuses an export with
  it). Make both edits before the first hosted build.
- **Vite**: a stock `vite.config` ignores the `PORT` that `env up` sets, so
  the dev env declares the port Vite really listens on (its literal
  `server.port`, else 5173) instead of a resolved one. To let forge pick a
  free port per checkout, read `process.env.PORT` in `vite.config` and switch
  the dev `port` back to `plugin.resolve_port(...)`; the comment on that line
  shows both.

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
per person is free**, across every org they own without a plan (no billing,
1 GiB storage cap), and it is counted across environments: each env's deployed
site is a site. A new site binds both `staging` and `prod` to hosting, so
without billing one of them deploys free (deploy `prod` first if that is the
one that must be live) and the other is queued with
`STATIC_FREE_TIER_EXCEEDED`. Past the free site — a second env or site, or any hosted workload or managed database beside it — the deploy needs
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
forge env up dev --watch   # an agent: run it in the background; SIGTERM stops it
```

No database or cluster is needed: the dev env declares only the frontend's dev
server, and `env up` starts it in the frontend's directory with `PORT` set (an
adopted Vite app that ignores `PORT` declares its own fixed port; see above). A
site forge started mirrors the browser console to
`.forge/logs/dev/frontend_<name>.log`. An adopted app does the same once it
calls `installDevLogging()` from `@reliantlabs/forge-web-runtime/devlog` in the
browser, and its dev server receives the lines. That subpath (the release after
0.13.20) needs no other package; the package root also needs
`@bufbuild/protobuf` and `@connectrpc/connect` installed. On Vite, the receiver is the
`forgeDevLog()` plugin from `@reliantlabs/forge-web-runtime/vite` (load
`debug`).

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
next/image without `unoptimized`, `cacheComponents: true` (create-next-app's
Next.js 16 default). It also warns on what the export drops
silently, such as middleware and rewrites. The real `next build` in CI stays
authoritative. The lane only tells you sooner.

A Vite site has no server features to lose, so the lane checks the seam with
the deploy instead: `build.outDir` in `vite.config.*` (or `--outDir` in the
build script) must be the env's `public_dir` (`dist` when unset), and `base`
must match where the env mounts the site (`/` at the root, `<base_path>/`
under a `base_path`). Either mismatch fails with file:line. A relative base
(`./`) with a path-based router warns, because deep links to nested routes
then request their assets under the route's path. A value the lane cannot
read statically, such as a computed `outDir` or a `mergeConfig`, is named in a
note rather than guessed. Every frontend the lane does not check gets a
`<name> (<type>): not applicable — <reason>` line.

A hosted deploy ships only what a release froze, never a local build. Without a
release it refuses, and the error prints the cut-then-deploy command.
Promotion re-points another env at the same digest:
`forge env deploy staging v0.1.0` and then `forge env deploy prod v0.1.0` ship
byte-identical sites (two hosted envs are two sites: the second needs billing,
above). Recovery rolls forward to a new release; there is no rollback.

**No account at all?** In a bare Vite app or Next.js static export, with no
`forge.yaml` yet, one command does it:

```bash
npx -y @reliantlabs/forge env deploy prod --claimable
```

With no `forge.yaml` it first adopts the app in place (the same as `forge
project new --in-place --kind site`), says in one line that it added
`forge.yaml` and `deploy/kcl/` and left the app's own files untouched, then
ships the site with no signup and prints its URL plus a claim link to hand your
user; unless they open it within 72h the site goes offline. Run it at the app's
root. A Go module with no Dockerfile, or a directory with no app, is refused
with the command to run instead, and a plain `forge env deploy` (no
`--claimable`) never adopts: it needs an existing project. The MCP tool
`deploy_claimable` does the same (`deploy/hosting`, "No account:
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

A site that calls several backends of its own names the others with
`apis = {billing = "billing-api"}`: each is served on the site's own origin at
`/_api/@<name>` (dev and hosted alike), so the session cookie reaches every
one. Only a workload of the same env may be named. See `frontend/serving`,
"Several APIs on one origin".

Secrets do not belong in a static site: everything in `config.js` is public.
A value that must stay secret belongs on a service (`forge secret set`).

### A demo or preview env on fixtures, promoted by digest

A frontend that calls an API can run a whole env on sample data instead:
a public demo, a sales sandbox, a preview of prod's release with nothing
behind it. Fixtures are runtime config like the rest of this section, so the
env serves **prod's release, by digest**, with no second build:

```bash
forge env new demo --from prod --bind web=hosted
```

```kcl
# deploy/kcl/demo/main.k — the frontend as prod declares it, plus:
fixtures = forge.Fixtures { scenario = "shop" }   # read_only = True
```

```bash
forge env render demo --json | jq '.frontends[].runtime_config'   # FIXTURES_SCENARIO, FIXTURES_READ_ONLY
forge env deploy demo v1.8.2                     # the release prod runs; nothing is rebuilt
```

- forge renders `fixtures` into the frontend's runtime config as
  `FIXTURES_SCENARIO = "shop"` and `FIXTURES_READ_ONLY = "true"`. The
  scaffolded `src/lib/connect.ts` sees them and answers every RPC from the
  generated mock transport with that scenario; a live env's bundle never
  downloads it (a lazy chunk). The scaffolded session is the fixture user,
  since nothing answers `/auth/session`.
- `read_only` defaults to `True`: only an rpc declaring
  `option idempotency_level = NO_SIDE_EFFECTS;` answers, and every other one
  fails `PermissionDenied`. Set `read_only = False` only for a sandbox whose
  writes may land in the browser's in-memory store.
- The scenario is `src/mocks/scenarios/<name>.ts` (`forge scaffold scenario
  <name>`; `frontend/mock-mode`). An unknown name answers from `default`,
  with a console warning.
- Drop what the demo does not run: prod's backend workloads, and any
  `runtime_config` `forge.WorkloadURL` naming one (render refuses a reference
  to a workload the env does not declare). A demo of a hosted site is a
  second hosted site, so it needs billing (above).
- forge cannot see from the env whether the frontend's code reads the keys.
  A frontend scaffolded before forge.Fixtures, or one with a transport layer
  of its own, reads `window.__FORGE_CONFIG__.FIXTURES_SCENARIO` itself; the
  scaffolded `src/lib/fixtures.ts` shows how. Until it does, the env serves
  the live build, calling whatever API its runtime config names.
- `fixtures` is for a web frontend (`nextjs`, `vite`), not on
  `forge.BuildOnly`, and not beside the build-time `config.mock`.

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
has `[id]` pages; `frontend/serving` converts them.

Two cases leave static hosting behind: server rendering, and request-time
server APIs such as server actions, middleware, and `cookies()`. For those,
switch `output` to `standalone` and ship the frontend as a workload instead
(`deploy`, `frontend/serving`).

## How hosted static hosting resolves a path

A request for `/books/view?id=…` is served `books/view.html`. The export
writes one `.html` file per route because `trailingSlash` is off, and the
hosted origin tries `<path>`, then `<path>.html`, then `<path>/index.html`.
Every link the scaffold generates resolves to the file for its own route.
`frontend/serving` covers hosts that only map directories to `index.html`.

What a path no file answers gets is declared by the build itself, by
Cloudflare Pages' rule:

- **The build ships a `404.html`** at the site's root (a Next.js export
  always does; a Vite site adds `public/404.html`): every miss is that page
  with status **404** and `Cache-Control: no-store`. There is no SPA
  fallback, so every page must be a file — a static export, or a SPA that
  prerenders each route as `<path>.html`. This is what keeps a mistyped URL
  out of search results as a copy of the homepage.
- **No `404.html`**: the site is a single-page app. A route-shaped miss
  falls back to the root `index.html` with 200 for the client router to
  draw; a miss with an extension is a plain 404.

## Response headers: `_headers`

A `_headers` file at the root of the build (Vite: `public/_headers`; Next.js:
`public/_headers`) declares response headers, in the Netlify / Cloudflare
Pages format. It is part of the release, so the policy ships with the code it
describes and a promoted digest serves the same headers in every env. The
file itself is never served.

```
# every response
/*
  Content-Security-Policy: default-src 'self'; frame-ancestors 'none'
  Permissions-Policy: camera=(), microphone=(), geolocation=()
  X-Frame-Options: DENY
# a narrower rule adds to (and, for a repeated name, joins with) the wider one
/embed/*
  Content-Security-Policy: frame-ancestors https://partner.example
```

- A pattern is an absolute request path; `*` matches anything, `/` included.
  `:placeholders`, per-host rules and `! Name` removal are not supported.
- Every matching rule applies, in file order; a name several rules set is
  joined with `, ` (two CSPs are both enforced, so a rule can only tighten).
- The platform always sends `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: strict-origin-when-cross-origin` (yours replaces it) and
  `Strict-Transport-Security: max-age=31536000` (`includeSubDomains` is a
  per-domain flag: `deploy/domains`). It sends NO anti-framing header:
  framing is the site's policy. A scaffolded app frontend (Next.js, Vite)
  ships `public/_headers` with `frame-ancestors 'self'` and
  `X-Frame-Options: SAMEORIGIN`; a site created before that, or a site
  without the file, can be framed by any origin until it declares one.
- Refused, and logged: the headers the platform owns (`Set-Cookie`,
  `Cache-Control`, `Content-Type`, `Strict-Transport-Security`,
  `X-Content-Type-Options`, `Access-Control-Allow-Credentials`, HTTP
  message-framing headers such as `Content-Length` and `Transfer-Encoding`,
  and hop-by-hop headers), invalid names or values, and anything past the caps —
  100 rules, 50 headers per rule, 4 KiB per value, 64 KiB per file, 16 KiB of
  file headers per response. A bad line is skipped; the rest still apply.
- On an unclaimed preview, CORS stays the platform's (`*`) and the
  preview sandbox policy is added beside your CSP.

A missing file that is not route-shaped (`/assets/app-1a2b.js`) is a 404,
never the HTML fallback. The frontend's content-hashed asset directory
(`asset_dir`: `_next/static` for Next.js, `assets` for Vite) is published as
the site's `assetDir`: files under it are served
`public, max-age=31536000, immutable`, everything else `no-cache`, and the
previous releases' copies stay serveable after a deploy, so a tab opened
before it can still lazy-load the chunks it was built against.
