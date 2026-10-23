---
name: serving
description: How a forge frontend is built and served — the three output shapes (static by default, standalone, server), static-exportable routes, how a static host resolves them, fenced build dirs, and mounting under a URL prefix with base_path and joinBasePath.
---

# Frontend build & serving shapes

## Production build shape (`output:`)

`forge scaffold frontend` and `forge project new --frontend` write the
frontend's `output:` into `forge.yaml` and render `next.config.ts` and the
`Dockerfile` from it. Pick a different shape at scaffold time with
`forge scaffold frontend dashboard --output standalone`. There are three
values:

| `output:`    | Production shape | Use when |
| ------------ | ---------------- | -------- |
| `static`     | Static export (`output: "export"`) into `out/` | **The scaffold default.** Ships to hosted static hosting (`forge.OnHosted {}`), `forge.OnBucket`, any CDN, or the scaffolded nginx image. No Node in prod. |
| `standalone` | Node server (`output: "standalone"`) | The frontend needs a server at request time: server actions, middleware, request-time `redirect()`/`cookies()`/`headers()`, route handlers that answer GET dynamically. Ships as a workload (Dockerfile), never as a static site. |
| `server`     | Full Next.js (no `output:`) | Custom server, ISR, managed host (Vercel). |

An entry with **no** `output:` is a frontend scaffolded before static became
the default (forge ≤ v0.1.43). It reads as `standalone`, because that is
what its `next.config.ts` says.

Reliant hosting serves only `static`: a frontend bound to `forge.OnHosted {}`
(or `OnBucket`) with another `output:` fails render. Hosted static sites need
billing like every hosted workload (a deploy that needs it is queued,
`deploy/hosting`). A `standalone` frontend ships as a workload, not a site.

`next dev` is the same in every mode.

## The dev loop is one origin (`/_api`, `/_otel`)

Under the dev server (`next dev`, or `vite` for the SPA) the browser talks
only to the dev server's own origin. Two dev-only proxies in
`next.config.ts` / `vite.config.ts` forward what is not a page:

| Path | Forwards to | Upstream |
| ---- | ----------- | -------- |
| `<basePath>/_api/*` | the API | `NEXT_PUBLIC_API_URL` / `VITE_API_URL`. `forge env up` sets it to the API it started; your shell or the frontend's KCL `env_vars` win over forge's |
| `<basePath>/_otel/*` | the collector's OTLP/HTTP port | `OTEL_EXPORTER_OTLP_ENDPOINT` |

`src/lib/api-base.ts` decides where API traffic goes, and both
`src/lib/connect.ts` (every RPC) and `src/lib/auth/native-login.ts` (sign-in)
ask it: `<origin><basePath>/_api` under the dev server, the runtime config's
`API_URL` (`config.js`) in every build. The config hands its decision to the
bundle as `FORGE_DEV_API_PROXY` (Next's `env`, Vite's `define`), so the
rewrite and the transport cannot disagree.

Why: the API's own URL, `http://localhost:<api port>`, only works for a
browser on the machine running the stack. A dev server opened through a
Reliant workspace preview URL (`reliant preview-url <port>`), a tunnel, or a
LAN or container IP would send every RPC and the sign-in POST to the viewer's
own machine. Through the page's origin there is nothing else to reach, no
CORS, and the session cookie is first-party to the host the page is on.

- **Which hosts may load the dev server:** `next dev` refuses its HMR socket
  and dev resources to any origin but localhost, and a refused page renders
  but never hydrates. `next.config.ts` therefore sets `allowedDevOrigins` to
  the Reliant preview host (`RELIANT_PREVIEW_URL_TEMPLATE` + `PORT`), this
  machine's own IPv4 addresses, and `FORGE_DEV_ALLOWED_ORIGINS` (comma-
  separated hostnames, for a tunnel or a custom dev DNS name).
  `vite.config.ts` puts the preview host and `FORGE_DEV_ALLOWED_ORIGINS` in
  `server.allowedHosts`. Vite already accepts any IP.
- **Opting out:** start the dev server with `FORGE_DEV_API_PROXY=off`. The
  browser calls the configured API URL directly, cross-origin, as before.
  The proxy is also off when the dev server has no upstream (`npm run dev`
  without `NEXT_PUBLIC_API_URL`).
- **Long streams:** Next's rewrite proxy ends a request after 30s
  (`experimental.proxyTimeout`). Raise it, or opt out, for a server stream
  that must outlive that.
- **Production has neither proxy.** A static export has no server. The API
  URL comes from `config.js`, and `<basePath>/_otel` must be routed by the
  ingress.
- **Expo** has no page origin: `EXPO_PUBLIC_API_URL` must name an address
  the device can reach.

A frontend scaffolded before this has neither `src/lib/api-base.ts` nor the
`/_api` rewrite: `forge skill load migrations/v0.1.46/dev-api-proxy`.

## Static export: what a route may be

An export holds only the pages Next can enumerate at build time. That gives
three rules:

- **No dynamic segments.** `src/app/books/[id]/page.tsx` fails `npm run build`:
  `Page "/books/[id]" is missing "generateStaticParams()"`. Put the id in the
  query string. The generated pages are `/<entity>/view?id=…` and
  `/<entity>/edit?id=…`. Build those URLs with `entityViewHref` and
  `entityEditHref`, and read the id with `useEntityIdParam`, all from
  `src/lib/entity-routes.ts`.
- **`useSearchParams` sits under `<Suspense>`.** The export prerenders each
  page with no query string. A page that reads search params outside a
  Suspense boundary fails the build. The generated detail and edit pages
  render a Suspense shell around the component that reads the id.
- **No server at request time.** Static mode does not support server actions,
  `middleware.ts`, `next/headers`, next.config `rewrites`/`redirects`/`headers`,
  or route handlers that answer GET dynamically. `images: { unoptimized: true }`
  is set, because next/image's optimizer is a server route. The scaffolded
  `src/app/%5F_forge/log/route.ts` is a dev-only POST receiver, and the export
  leaves it out. Server-runtime redirects become a client component calling
  `useRouter().replace()` in a `useEffect`.

A frontend scaffolded before v0.1.44 has `[id]` pages. To convert it:
`forge skill load migrations/v0.1.44`.

## How a static host resolves the export

`trailingSlash` stays at Next's default (`false`). The export writes one file
per route: `out/books.html`, `out/books/view.html`, `out/books/edit.html`. A
request for `/books/view?id=b-1` is served `books/view.html`, and the page
reads `id` in the browser. The hosts forge ships to resolve that file as
follows:

- **Hosted static hosting** (`forge.OnHosted {}`) tries `<path>`, then
  `<path>.html`, then `<path>/index.html`. A route-shaped miss falls back to
  the site's `index.html`.
- **The scaffolded nginx image** uses `try_files $uri $uri.html $uri/ =404`.
- **A host that only maps directories to `index.html`** (a bare bucket website,
  `python -m http.server`) cannot find `books/view.html` from `/books/view`.
  Set `trailingSlash: true` in `next.config.ts` for such a host. The export
  then writes `books/view/index.html`, and Next adds the slash to every link.
  The entity-route helpers need no change.

Do not rely on the hosted SPA fallback for a route you exported. It serves
the root `index.html`, which is the dashboard and not your page.

**Build dirs are fenced.** In `standalone`/`server`, production builds write to
`.next-prod` while `next dev` keeps `.next`, so `npm run build` during a live
`forge env up` session can't clobber the dev cache; `next start` and the
Dockerfile read `.next-prod`. `static` keeps Next.js defaults, so avoid
production builds during a live dev session in that mode. `output:` is read at
scaffold time: `next.config.ts` and the `Dockerfile` are yours afterwards. To
switch an existing frontend, change `output:` and run
`forge project upgrade --force frontends/<name>/next.config.ts frontends/<name>/Dockerfile`.
That command overwrites the two files, so read `--check <path>` first.

## Checking a static export before CI does

`next dev` serves everything an export cannot, so a frontend drifts out of
being exportable without anything noticing. Two checks catch it:

- **`forge env render` refuses the binding.** A frontend bound to
  `forge.OnHosted`, `forge.OnBucket` or `forge.OnFirebase` whose forge.yaml
  `output` is not `static` fails every render, deploy and build. The error
  names the frontend, the env and both fixes. forge.yaml is the one
  declaration of the build shape, and the KCL never repeats it.
- **`forge lint --static-export`** runs as part of every `forge lint`. It covers
  each Next.js frontend that is `output: static` or bound to a static runtime
  in any env, and reports each finding with file:line and a fix.
  - Errors are what `next build` refuses for an export:
    - a `[id]`, `[...x]` or `[[...x]]` route with no `generateStaticParams`
    - a GET route handler that is not `force-static`
    - `'use server'`
    - `next/headers`
    - `dynamic = "force-dynamic"` or `revalidate = 0`
    - next/image without `images.unoptimized`
    - a next.config that does not export, or that disagrees with forge.yaml
  - Warnings are what the export drops silently:
    - `middleware.ts` / `proxy.ts`
    - non-GET route handlers that are not dev-only
    - `rewrites` / `redirects` / `headers` not gated to development
    - `revalidate = N`
    - an export bound to `forge.OnBucket` without `trailingSlash: true`: a
      bucket resolves no `.html` (above), so every route but `/` 404s

  A dev-only handler says so in its first statement:
  `if (process.env.NODE_ENV === "production") return …`, as the scaffolded
  dev log route does. Suppress a single finding with
  `// forge:lint-disable-next-line <rule>: <reason>`.

The real `next build` stays authoritative. CI's
`NODE_ENV=production npm run build`, `forge build` and `forge env deploy` all
run it. The lint is a text scan: it does not follow a re-exported
`generateStaticParams` or a computed next.config, and stays silent rather
than guess.

## Serving under a path prefix (`base_path`)

To mount a frontend under a URL prefix, declare `base_path: /admin` in
`forge.yaml` (or `--base-path /admin`; must start with `/`, no trailing `/`).
What it drives:

- `next.config.ts` sets **both `basePath` AND `assetPrefix`** — `assetPrefix` is
  required or some RSC chunk URLs skip the prefix and React never hydrates.
- **ONE env var**: `NEXT_PUBLIC_BASE_PATH`. A second variant
  (`ADMIN_WEB_BASE_PATH` etc.) is silently ignored.
- `src/lib/basepath_gen.ts` (regenerated) exports `BASE_PATH` +
  `joinBasePath(path)`.
- Static-export builds **fail loudly** if `NEXT_PUBLIC_BASE_PATH` is emptied
  while `forge.yaml` declares a prefix.

Internal navigation (`<Link href="/tasks">`, `router.push("/tasks")`) keeps
app-relative paths — Next.js prepends the basePath, so do NOT wrap these in
`joinBasePath`. Hand-built URLs Next.js can't see — `window.location.origin`-based
return URLs, OAuth `redirect_uri`, share links, raw `fetch()`/`<a>` paths — MUST
go through it:

```typescript
import { joinBasePath } from "@/lib/basepath_gen";
const successUrl = window.location.origin + joinBasePath("/billing/success");
```

Anti-patterns lint catches: bare `"/admin" + path` literals, bare `/route`
strings in hand-built URLs, and reading any env var other than
`NEXT_PUBLIC_BASE_PATH`. `src/lib/admin-url.ts` (`adminUrl` /
`absoluteAdminUrl`) wraps the same helper for strings handed to an external
system that round-trips back.

See also: `frontend` for the rest of the frontend surface, `frontend/pages` for
the route shape, `deploy/static-site` for shipping the export.
