---
name: frontend-runtime/telemetry
description: Browser telemetry in forge frontends — the HyperDX SDK forge scaffolds and initialises (otel_gen.ts, SERVICE_NAME), the same-origin /_otel route and who forwards it, the env knobs, source maps extracted to a registry sidecar and never served, replay shipped off and always masked, reportException for caught errors, and interaction() spans timing a user operation from click to usable.
---

# Browser telemetry (HyperDX SDK)

Forge scaffolds, injects and initialises the **HyperDX browser SDK**
(`@hyperdx/browser`, pinned exactly) in Next.js and Vite frontends. It captures
uncaught errors and unhandled rejections, `console.*`, fetch/XHR spans, document
load, long tasks and web vitals, and ships them as OTLP to ClickStack. There is
no Sentry SDK and no Sentry-protocol adapter. React Native frontends get
nothing yet: there is no DOM to instrument and the browser SDK does not resolve
there.

You write none of the wiring. `src/lib/otel_gen.ts` (regenerated every
`forge generate`; do not edit) reads the env and calls
`initBrowserTelemetry` from `@reliantlabs/forge-web-runtime/telemetry`, and
`providers.tsx` (Next.js) / `main.tsx` (Vite) call its `initTelemetry()` once.
It is idempotent, a no-op without a DOM or while telemetry is off, and never
throws.

The frontend reports `service.name` `<project>-<frontend>` (e.g. `shop-web`),
exported from the same file as `SERVICE_NAME`. The generated frontend dashboard
and frontend alert defaults filter on exactly that value. Code that starts the
SDK itself instead of calling `initTelemetry()` must pass `SERVICE_NAME` as
`serviceName`: any other name is invisible to the generated observability. The SDK is the page's **only** tracer provider: it replaces the old
OTel web wiring rather than joining it, because two providers cannot both
register globally.

## Same-origin `/_otel`, and no secret in the bundle

The browser POSTs OTLP/HTTP to `<basePath>/_otel/v1/{traces,logs}` on its **own
origin**. Something in front of the page forwards that to a collector's OTLP/HTTP
port, and that hop owns any credential. The SDK insists on an `apiKey`, so it is
sent the public placeholder `forge-browser-public`; a collector that enforces
ingestion auth (ClickStack `collectorAuthenticationEnforced`) must have the proxy
strip that header or replace it with the real key. Same-origin also means a CSP
of `connect-src 'self'`, no CORS preflight, and no third-party host for an
ad-blocker to list.

| Where | Who routes `/_otel` | Upstream |
|---|---|---|
| `next dev` | `rewrites()` in `next.config.ts`, dev-only | `OTEL_EXPORTER_OTLP_ENDPOINT`, default `http://127.0.0.1:4318` |
| `vite` | `server.proxy` in `vite.config.ts` | same; the `/_otel` prefix is stripped |
| deployed | **the ingress** | a collector's OTLP/HTTP port |

A Next.js frontend builds as a **static export** (`output: static`), which has no
server, so `rewrites` do not exist in the built site; a built Vite SPA is plain
files. In a deployed environment the ingress must route `<basePath>/_otel/*` to
the collector. Until it does, leave the frontend's `otel_endpoint` unset and the
production bundle sends nothing, rather than POSTing to a 404.

## Turning it on, and the knobs

A value that differs per environment is read from the runtime config document
(`config.js`): a build variable is inlined at build time and follows that bundle
into every environment it is promoted to.

| Knob | Runtime key (`config.js`) | Declared by | Default |
|---|---|---|---|
| ingest path (`off` or empty disables) | `OTEL_ENDPOINT` | KCL `config.otel_endpoint` or `runtime_config` | `/_otel` in dev and on `forge.OnHosted`, **empty elsewhere** |
| `deployment.environment` | `ENVIRONMENT` | KCL `config.environment` or `runtime_config` | the forge env name |
| session replay | `OTEL_REPLAY = "true"` | `runtime_config` | **off** |
| `service.version` | — (a build fact: `NEXT_PUBLIC_APP_VERSION` / `VITE_APP_VERSION`) | the deploy | the **build id** a deploy injects (12-hex hash of the frontend source and build env), unless you set it |

For a deployed environment, declare `otel_endpoint = "/_otel"` in the
frontend's KCL `config` block. Every web frontend's `config.js` names its
environment, so `ENVIRONMENT` needs no declaration. A literal
`{NEXT_PUBLIC_,VITE_}OTEL_ENDPOINT` / `ENVIRONMENT` / `OTEL_REPLAY` in the
frontend's `env_vars` is carried into the runtime config as well, so older
declarations keep working (on `forge.OnHosted`, only the ingest path: the
control plane writes that document's `ENVIRONMENT`). A build variable only exported in the shell at
`npm run build` is a **deprecated** fallback: used when `config.js` lacks the
key, with one `console.warn` per page load naming the KCL field to move it to;
it is removed in the next major.
The API origin (`API_URL`) feeds `tracePropagationTargets`, resolved as
`connect.ts` resolves it, so a cross-origin API still gets a `traceparent`.
`environment` is emitted as both `deployment.environment.name` and the legacy
`deployment.environment` that HyperDX's default sources key on.

## Source maps are extracted, never served

A deploy builds with `FORGE_SOURCEMAPS=1` (the scaffolded `vite.config.ts` /
`next.config.ts` emit maps only then), then the static stage moves every map out
of the tree it publishes into `<staging>.sourcemaps/` (same relative paths, plus
`manifest.json` naming the project, frontend and build id) and strips the
`sourceMappingURL` trailers. The release digest excludes them, and a guard fails
a deploy whose served tree still holds one. Firebase asset retention never
carries a `.map` forward.

The sidecar is filed in the platform registry
(`<org>/<project>/<frontend>-sourcemaps:<build id>`), where the issues worker
symbolicates errors from. `telemetry = forge.BrowserTelemetry {sourcemaps =
"public"}` is the escape hatch for an app that wants its maps served: the tree
keeps its maps and trailers, and the maps are still copied to the sidecar and
filed, so its errors symbolicate too. `"off"` keeps maps nowhere, and its errors
stay minified.

## Replay is shipped, off, and always masked

The SDK bundles an rrweb recorder. It stays disabled unless the replay knob is
`true`, and when it is on, **all text and all inputs are masked** and canvas is
not recorded — a replay records the DOM, which for most apps is their most
sensitive data. There is no knob that records unmasked. Reliant's own chat UI
never enables it. Network bodies (`advancedNetworkCapture`) are also off: request
and response bodies are user data.

## Reporting a caught error

Uncaught errors, rejections and `console.error` are recorded for you. React
error boundaries and Next's `error.tsx` swallow errors before `window.onerror`
sees them, so they call `reportException(error, attrs)` from the barrel (the
scaffolded `error.tsx`, `global-error.tsx` and `RuntimeErrorBoundary` already
do). It returns `false` when no SDK is running so the caller can fall back to
`console.error`, and it skips an error the SDK has already recorded — one failure
is one record. Use it in your own catch blocks the same way.

```ts
import { reportException } from "@reliantlabs/forge-web-runtime";

try {
  await checkout();
} catch (err) {
  if (!reportException(err, { "checkout.step": "payment" })) console.error(err);
  throw err;
}
```

## Interactions: click to usable

A fetch span times one RPC. To time what the user waits for — several RPCs,
a stream's first frame, a render — open an `interaction` (barrel) on intent
and end it when the screen is usable:

```ts
const op = interaction("chat.open", { "chat.id": id }); // in the click handler
useInteractionEnd(op, loaded); // endAfterPaint() on the first ready commit
```

It is one root span (`interaction.name|scope|outcome`), and every RPC made
while it is open is its child, server spans included — no `context.with`,
which a browser loses at the first `await`. The choice is made when the call
enters the interceptor chain, so a call held by auth's `getToken()` stays in
its own interaction; a chain whose outermost stages are the app's own and
await puts `interactionContextInterceptor()` first. A span the caller has
active when it makes the call still wins. One interaction is open per `scope` (default
`foreground`); a new one ends the old as `superseded`. `endAfterPaint()` waits
two animation frames; ends are idempotent; unended ones end as `timeout` after
60s; a page hidden while open stamps `page.hidden`. `mark(name)` adds a phase
event; `currentInteraction()` finds the open one. With telemetry off it records
nothing but its RPCs still share one trace id; it never throws.

Custom spans: `getTracer("checkout")` from `@/lib/otel_gen`.

Distributed tracing (the traceparent on every RPC) is independent and always
on — it does **not** depend on this module.
