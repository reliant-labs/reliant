---
name: v0.1.44
description: Convert a Next.js frontend's generated CRUD routes from dynamic `/<slug>/[id]` pages to static `/<slug>/view?id=…` and `/<slug>/edit?id=…` pages, so `output: static` (the new scaffold default, and the only shape hosted static hosting serves) can build. Use when `next build` fails with "missing generateStaticParams()", or before binding a frontend to `forge.OnHosted {}`. Separately, keep the version-stamping code generators (protoc-gen-es, protoc-gen-go) out of `.github/dependabot.yml`, whose bumps fail Verify Generated Code every time. Separately, move local observability from the Grafana LGTM stack (and the opt-in Alloy collector) to ClickStack, now ON by default in `forge env up`: use when docker-compose.yml still has an `lgtm` or `alloy` service, deploy/kcl/dev/main.k still declares an `lgtm` workload, or deploy/alloy-config.alloy exists. Separately, move a web frontend from `initClientTelemetry` and the `@opentelemetry/*` browser SDK packages to the HyperDX browser SDK shipped by `@reliantlabs/forge-web-runtime` 0.4.0: use when providers.tsx or main.tsx still calls `initClientTelemetry`, or `@opentelemetry/sdk-trace-web` is in a frontend package.json.
version: v0.1.44
detection: for d in frontends/*/src/app/*/[[]id[]]; do test -d "$d" && exit 0; done; test -f deploy/alloy-config.alloy && exit 0; grep -qsE 'grafana/otel-lgtm|name = "lgtm"' docker-compose.yml deploy/kcl/dev/main.k && exit 0; grep -qs 'initClientTelemetry' frontends/*/src/app/providers.tsx frontends/*/src/main.tsx && exit 0; grep -qs '"@opentelemetry/sdk-trace-web"' frontends/*/package.json && exit 0; f=.github/dependabot.yml; test -f "$f" || exit 1; if grep -q 'package-ecosystem: *npm' "$f" && ! grep -qF '@bufbuild/protoc-gen-es' "$f"; then exit 0; fi; grep -qF protoc-gen-go buf.gen.yaml && ! grep -qF google.golang.org/protobuf "$f"
---

# Static-exportable CRUD routes

> This release also changed the scaffolded `.github/dependabot.yml`, and moved
> local observability from Grafana LGTM to ClickStack. Both are independent of
> the routes below: see
> [local observability](#also-in-v0144-local-observability-moves-from-lgtm-to-clickstack)
> and
> [Keep code generators out of Dependabot](#also-in-v0144-keep-code-generators-out-of-dependabot)
> at the end.

Use this when a Next.js frontend scaffolded by forge v0.1.43 or earlier still
has `src/app/<slug>/[id]/page.tsx` and `src/app/<slug>/[id]/edit/page.tsx`,
and you want it to build as a static export: to ship it on hosted static
hosting (`forge.OnHosted {}`), on a bucket (`forge.OnBucket`), or behind a
CDN. A frontend that stays `output: standalone` can keep its `[id]` pages.
Nothing here is required for it.

## What changed

New frontends are scaffolded `output: static`. `npm run build` is a static
export into `out/`, and the generated CRUD pages are static routes:

| Page   | Before (dynamic)          | Now (static)                          |
|--------|---------------------------|---------------------------------------|
| list   | `src/app/<slug>/page.tsx` | unchanged                             |
| create | `src/app/<slug>/new/…`    | unchanged                             |
| detail | `src/app/<slug>/[id]/page.tsx` → `/<slug>/<id>` | `src/app/<slug>/view/page.tsx` → `/<slug>/view?id=<id>` |
| edit   | `src/app/<slug>/[id]/edit/page.tsx` → `/<slug>/<id>/edit` | `src/app/<slug>/edit/page.tsx` → `/<slug>/edit?id=<id>` |

A static export can contain only the pages it can enumerate at build time.
An entity id exists only at runtime, so a `[id]` segment fails the build:

```
Error: Page "/books/[id]" is missing "generateStaticParams()" so it cannot be used with "output: export" config.
```

The new pages are one static file each (`out/<slug>/view.html`) that read
`?id=` in the browser, through `src/lib/entity-routes.ts`:

- `entityViewHref(slug, id)` and `entityEditHref(slug, id)` build the URLs.
- `useEntityIdParam()` reads the id. It calls `useSearchParams`, so the
  page renders the component that calls it inside `<Suspense>`.

Your pages are scaffold-once, so forge never rewrites them. While
`src/app/<slug>/[id]/` exists, `forge generate` keeps it as that entity's
detail/edit route and scaffolds no view/edit pages beside it.

## Steps

Run from the project root. `web` stands for your frontend's name, and the
paths assume it lives at `frontends/web`.

### 1. Add the route helper

```bash
forge project rescaffold frontends/web/src/lib/entity-routes.ts frontends/web/src/lib/entity-routes.test.ts
```

### 2. Replace each entity's `[id]` pages

**Pages you never edited.** Delete the dynamic pages AND the list and
create pages that link to them. Then re-scaffold the list and create pages.
`rescaffold` runs `forge generate`, which writes the new `view/` and
`edit/` pages because they were never scaffolded before:

```bash
for slug in books authors; do   # every entity slug under src/app
  rm -rf "frontends/web/src/app/$slug/[id]" \
         "frontends/web/src/app/$slug/page.tsx" \
         "frontends/web/src/app/$slug/new/page.tsx"
  forge project rescaffold "frontends/web/src/app/$slug/page.tsx" \
                           "frontends/web/src/app/$slug/new/page.tsx"
done
```

To see whether a page is still the one forge wrote, diff it against a
fresh render. Scaffold a throwaway project from the same protos, or read
`git log -p` on the file since its birth commit.

**Pages you edited.** Keep your code and move it:

1. `git mv 'frontends/web/src/app/<slug>/[id]/page.tsx' frontends/web/src/app/<slug>/view/page.tsx`
   and `git mv 'frontends/web/src/app/<slug>/[id]/edit/page.tsx' frontends/web/src/app/<slug>/edit/page.tsx`,
   then remove the empty `[id]/` directory.
2. In each moved page, replace `useParams()` with `useEntityIdParam()`.
   Move the body into a child component that takes `id`, and render that
   child inside `<Suspense>` from the default export. A static export
   prerenders the page with no query string, and Next refuses to build a
   page that reads search params outside a Suspense boundary.

   ```tsx
   export default function BookDetailPage() {
     return (
       <Suspense fallback={<SkeletonLoader variant="list-item" count={5} />}>
         <BookDetailRoute />
       </Suspense>
     );
   }

   function BookDetailRoute() {
     const id = useEntityIdParam();
     if (id === undefined) return /* a "which book?" state with a link back */;
     return <BookDetail id={id} />;
   }

   function BookDetail({ id }: { id: string }) {
     // your existing page body, unchanged
   }
   ```

3. Rewrite every link to the old shape: list row clicks, the create page's
   redirect after save, the detail page's Edit action, the edit page's
   Cancel, breadcrumb and post-save redirect, plus any hand-written link.

   ```bash
   rg -n '`/[a-z0-9-]+/\$\{' frontends/web/src   # `/books/${id}` and `/books/${id}/edit`
   ```

   `` `/books/${id}` `` becomes `entityViewHref("books", id)`, and
   `` `/books/${id}/edit` `` becomes `entityEditHref("books", id)`.

### 3. Keep the query string across sign-in

If the frontend has a sign-in gate, `src/lib/auth/route-guard.tsx` must send
the query string in `returnTo`. Otherwise a visitor bounced off
`/books/view?id=…` comes back to `/books/view` with no id. If you never
edited the file, adopt the new template:

```bash
forge project upgrade --force frontends/web/src/lib/auth/route-guard.tsx
```

If you edited it, change one line in its redirect effect:
`encodeURIComponent(pathname)` → `encodeURIComponent(pathname + window.location.search)`.

### 4. Switch the build to a static export

Set `output: static` on the frontend in `forge.yaml`. Then re-render the
two files that read it. Both are scaffold-once, so this is explicit:

```bash
forge project upgrade --check frontends/web/next.config.ts   # read the diff first
forge project upgrade --force frontends/web/next.config.ts frontends/web/Dockerfile
```

If you edited `next.config.ts`, apply the static branch by hand instead.
It needs `output: "export"` gated on `NODE_ENV === "production"`,
`images: { unoptimized: true }`, and no `distDir`. Remove anything that
needs a server at request time: `rewrites`, `redirects`, `headers`,
`middleware.ts`, `next/headers`, server actions, and route handlers that
answer GET dynamically. The scaffolded `src/app/%5F_forge/log/route.ts` is
POST-only and dev-only, and the export leaves it out.

### 5. Verify

```bash
forge generate
(cd frontends/web && npm run build && ls out/*/view.html out/*/edit.html)
find frontends/web/src/app -type d -name '\[*\]'   # prints nothing once no dynamic segment is left
forge project upgrade apply v0.1.44
```

Before you rely on it, serve `out/` with something that resolves
`/<route>` to `<route>.html`, the way the hosted origin does. Then click
list → row → Edit → Cancel.

Record the migration only once every part of it that applies is done:
`forge project upgrade list` stops offering v0.1.44 after `apply`.

# Also in v0.1.44: local observability moves from LGTM to ClickStack

`forge env up` now runs ClickStack (ClickHouse, the HyperDX UI and an OTLP
collector in one container) instead of Grafana LGTM, and it is on by default.
Host processes get `OTEL_EXPORTER_OTLP_ENDPOINT` and
`OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` from `forge env up`; logs are read
from `.forge/logs/<env>/*.log`. Nothing to register, no key to copy.

Your `docker-compose.yml` and `deploy/kcl/dev/main.k` are yours, so forge did
not rewrite them. Until you do, the project keeps its old LGTM wiring. Take the
new shape from a fresh scaffold: `forge project new scratch --service x` in a
temporary directory, and copy from it.

## Steps

1. **`docker-compose.yml`.** Delete the `alloy` and `lgtm` services and the
   `lgtm_data` volume. Add the `clickstack` service, its three
   `clickstack_*` volumes, and the `otel-collector` network alias, exactly as in
   the scaffold. In `app` and `app-debug`, replace
   `OTEL_EXPORTER_OTLP_ENDPOINT: http://lgtm:4317` with
   `http://otel-collector:4318` and add `OTEL_EXPORTER_OTLP_PROTOCOL: http/protobuf`;
   change `depends_on: lgtm` to `clickstack`.
2. **`deploy/kcl/dev/main.k`.**
   - Replace the `_observability` block and `_otlp_port` with the scaffold's:
     `_observability = True` plus `_otlp_http_port`, `_otlp_grpc_port` and
     `_hyperdx_port`, each from `plugin.resolve_port`.
   - Delete the `_telemetry = {OTEL_EXPORTER_OTLP_ENDPOINT = ...}` env layer and
     its use in `_env`. `forge env up` exports the endpoint now.
   - Replace the `lgtm` workload in `_telemetry_workloads` with the `clickstack`
     one, passing the three `CLICKSTACK_*_PORT` values.
3. **New files.** Copy `deploy/observability/otel-collector.yaml` (the log
   pipeline) and `deploy/observability/dashboards/` (the dashboard provisioner
   directory) from the scaffold.
4. **Remove the dead files.** `deploy/alloy-config.alloy` and
   `deploy/observability/grafana/` are no longer written. `forge generate`
   deletes the untouched ones and lists any you edited by hand;
   `forge generate --force-cleanup` deletes those too.
5. `forge generate && forge env render dev`, then `forge env up dev`. The summary
   lists the HyperDX UI; `forge env status dev` reports traces, metrics and logs
   by service name.

## Things that changed under you

- **Grafana, Prometheus, Tempo, Loki and Pyroscope are gone from the default
  path.** `/metrics` on your app is untouched. Dashboards that lived in Grafana
  are not carried over; put HyperDX dashboard JSON in
  `deploy/observability/dashboards/`.
- **Profiles are not collected locally yet.** pprof stays on `127.0.0.1`.
- **Memory.** ClickStack is about 0.8 GB resident, in the same range as LGTM.

## To keep it off

`_observability = False` in `deploy/kcl/dev/main.k`. No container starts and no
endpoint is exported.

## Existing OTLP configuration wins

`forge env up` fills `OTEL_EXPORTER_OTLP_ENDPOINT` and `_PROTOCOL` as a pair,
and only when neither is set (an empty value counts as unset; so does the
per-signal `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` family). If your shell,
`config.k` or a workload's `env` sets either one, forge adds neither, so a
project that already points at its own collector, say a gRPC one on :4317,
is never paired with a protocol it did not choose.

# Also in v0.1.44: keep code generators out of Dependabot

Use this when `.github/dependabot.yml` lets Dependabot bump a code generator
that writes its own version into the files it generates. It applies to every
project with a frontend, and to every service project whose `buf.gen.yaml`
runs `protoc-gen-go`. It is independent of the routes above.

## What changed

New projects' `.github/dependabot.yml` ignores these packages:

| Entry | Ignored | Generator | Stamped into |
|-------|---------|-----------|--------------|
| npm (each frontend) | `@bufbuild/protoc-gen-es`, `@bufbuild/protobuf` | protoc-gen-es | every `*_pb.ts`: `// @generated by protoc-gen-es v2.16.0` |
| gomod | `google.golang.org/protobuf` | protoc-gen-go, which `forge tools install` installs at the version go.mod resolves | every `*.pb.go`: `// protoc-gen-go v1.36.12` |

Dependabot edits `package-lock.json` or `go.mod` and cannot run
`forge generate`. So a bump of one of these changes the header of every
generated file in CI's regenerate and fails Verify Generated Code, every
time. control-plane #645 was that PR: an npm group moved protoc-gen-es
2.14.0 → 2.16.0, and merging it red left main red. `@bufbuild/protobuf` is
ignored with protoc-gen-es because protoc-gen-es declares an exact peer
dependency on it, so the two only move together.

`connectrpc.com/connect` and `golang.org/x/tools` (goimports) also version
tools `forge generate` runs, but neither stamps a version, so their bumps
stay with Dependabot.

`dependabot.yml` is yours, written once at scaffold time, so `forge generate`
does not update it.

## Steps

**If you never edited the file**, take the current template:

```bash
rm .github/dependabot.yml
forge project rescaffold .github/dependabot.yml
git diff .github/dependabot.yml   # only the ignore entries should be new
```

**If you edited it**, add the entries by hand, merging into an existing
`ignore:` list where an entry already has one. Under each `npm` entry:

```yaml
    ignore:
      - dependency-name: "@bufbuild/protoc-gen-es"
      - dependency-name: "@bufbuild/protobuf"
```

Under the `gomod` entry, if `buf.gen.yaml` runs `protoc-gen-go`:

```yaml
    ignore:
      - dependency-name: google.golang.org/protobuf
```

Then close any open Dependabot PR that bumps one of them. From now on, bump
them by hand and commit the regenerated output in the same PR:

```bash
# protoc-gen-es (per frontend)
(cd frontends/web && npm install -D @bufbuild/protoc-gen-es@X && npm install @bufbuild/protobuf@X)
forge generate

# protoc-gen-go
go get google.golang.org/protobuf@vX && (cd gen && go get google.golang.org/protobuf@vX)
forge tools install --force && forge generate
```

A bump of something else can still raise `google.golang.org/protobuf`
through a shared requirement. That PR needs the same regenerate.

## Verify

```bash
grep -n 'protoc-gen-es\|bufbuild/protobuf\|google.golang.org/protobuf' .github/dependabot.yml
forge project upgrade list   # offers v0.1.44 only while some part of it still applies
```

# Also in v0.1.44: browser telemetry moves to the HyperDX SDK

Use this when a Next.js or Vite frontend scaffolded before
`@reliantlabs/forge-web-runtime` 0.4.0 still calls `initClientTelemetry()` (in
`src/app/providers.tsx` or `src/main.tsx`), or lists `@opentelemetry/sdk-trace-web`
in its `package.json`. It is independent of the routes and the Dependabot
change above.

## What changed

Frontends now start the **HyperDX browser SDK** from the generated
`src/lib/otel_gen.ts`. It records uncaught errors, unhandled rejections,
`console.*`, fetch/XHR spans, page loads and web vitals, and sends them to a
**same-origin `/_otel`** route, so the bundle holds no collector address and no
secret. Every RPC still carries `traceparent`, so a browser span joins the
backend's trace. Session replay is off.

web-runtime 0.4.0 is a **breaking** release:

- the `@reliantlabs/forge-web-runtime/otel` subpath and its eight
  `@opentelemetry/*` SDK peers are gone. HyperDX registers the page's one
  global tracer provider, and a second registration silently loses;
- `initClientTelemetry` is gone; `initTelemetry()` (from `src/lib/otel_gen.ts`)
  replaces it and the old tracing init;
- `reportException()` is new. `RuntimeErrorBoundary`, `error.tsx` and
  `global-error.tsx` call it.

`forge generate` does this part for you: it rewrites `src/lib/otel_gen.ts`,
raises `@reliantlabs/forge-web-runtime` to `^0.4.0`, and adds `@hyperdx/browser`
at the runtime's exact pin. Everything else below is a file you own.

## Steps

1. **Upgrade the runtime first.** `forge generate`, then `npm install` in each
   frontend. This needs `@reliantlabs/forge-web-runtime@0.4.x` to be published.
2. **Next.js: `src/app/providers.tsx`.** Delete `initClientTelemetry` from the
   import, delete `const teardownRum = initClientTelemetry();`, and delete
   `return teardownRum;`. Keep `initTelemetry();`. The effect becomes:

   ```tsx
   useEffect(() => {
     initTelemetry();
     if (process.env.NODE_ENV !== "production") {
       installDevLogging({ dev: true });
     }
   }, [isDev]);
   ```

3. **Vite: `src/main.tsx`.** Add `import { initTelemetry } from "@/lib/otel_gen";`
   and call `initTelemetry();` once, before `createRoot(...)`. Delete any
   `initClientTelemetry` call.
4. **The dev proxy (`next.config.ts` / `vite.config.ts`).** Both are yours, so
   forge did not touch them. Take the `/_otel` block from a fresh scaffold
   (`forge project new scratch --service x --frontend web` in a temporary
   directory): a `rewrites()` gated to `NODE_ENV === "development"` for Next.js,
   or `server.proxy["/_otel"]` for Vite, both aimed at
   `OTEL_EXPORTER_OTLP_ENDPOINT`. `forge env up` sets that variable for the dev
   server. Without the block the browser's POSTs to `/_otel` 404 and nothing
   is sent.
5. **Optional: `error.tsx` and `global-error.tsx`.** Call
   `reportException(error)` from `@reliantlabs/forge-web-runtime` before the
   `console.error` fallback, as the scaffold does, so a boundary-caught error
   reaches the SDK.
6. **Drop the old SDK packages** from `package.json` if nothing else in the app
   imports them: `@opentelemetry/auto-instrumentations-web`, `core`,
   `exporter-trace-otlp-http`, `instrumentation`, `resources`,
   `sdk-trace-base`, `sdk-trace-web` and `semantic-conventions`. Keep
   `@opentelemetry/api`. Then `npm install`.
7. **Any other caller of the removed API** (`initBrowserTracing`, imports from
   `@reliantlabs/forge-web-runtime/otel`) moves to `initTelemetry` and the
   re-exported `trace`, `context`, `propagation` and `getTracer` in
   `src/lib/otel_gen.ts`.

## Verify

```bash
forge generate
(cd frontends/web && npm install && npx tsc --noEmit && npm run build)
grep -rn 'initClientTelemetry\|forge-web-runtime/otel' frontends/*/src   # prints nothing
forge project upgrade list   # offers v0.1.44 only while some part of it still applies
```

Then `forge env up dev`, open the app, and trigger an error in the console. It
appears in HyperDX under the frontend's service, with `deployment.environment.name`
set to the env, and a browser span shares a trace id with the API call it made.
