---
name: frontend-runtime
description: The forge frontend runtime, @reliantlabs/forge-web-runtime — the web twin of forge/pkg. Transport interceptor stack (error normalization, auth, brand, W3C traceparent; React Query owns retries), app-shell providers (session, error boundary, toast host), the generic <Resource> data-table container, and browser telemetry (the HyperDX SDK: errors, console, network, traces — to ClickStack through a same-origin /_otel route).
---

# Frontend Runtime

Every generated web frontend — Next.js and Vite SPA alike — is built on
**`@reliantlabs/forge-web-runtime`**, the frontend analog of `forge/pkg`. It is a
set of pull-out-of-the-box batteries the app wires through thin, owned
composition.

## Ownership

The runtime is a **package**, not project code: there is nothing to edit and
nothing to regenerate. Fixes to it (e.g. tracing behavior) arrive with a
version bump.

You compose it from **your** owned files:

- `src/lib/connect.ts` — builds the transport interceptors from the runtime.
- `src/app/providers.tsx` — mounts `RuntimeShell` and inits telemetry.

Import everything from the barrel:
`import { … } from "@reliantlabs/forge-web-runtime"`.

Three subpaths are deliberately OUTSIDE the barrel, each for its own reason:

| Subpath | Why it is not in the barrel |
|---|---|
| `/interceptors` | DOM-free transport layer, for React Native / non-react-dom renderers. |
| `/mock-transport` | Dev-only. The fixture dispatch engine must be tree-shakeable out of a production bundle. |
| `/telemetry` | Imports the HyperDX browser SDK (`@hyperdx/browser`, exact-pinned, ~600 KB with rrweb and the OTel web stack, loaded lazily). It is an OPTIONAL peer: the Next.js and Vite scaffolds install it; a React-Native frontend declares `@opentelemetry/api` alone and imports the barrel. |

Nothing you write imports `/mock-transport` or `/telemetry` directly: forge generates
the thin project files (`src/lib/mock-transport_gen.ts`, `src/lib/otel_gen.ts`)
that do, carrying the per-project table and the `NEXT_PUBLIC_*` env reads
respectively.

Presentation stays yours and is deliberately NOT in the package: the component
library under `src/components/ui`, `globals.css`, the nav and the layout are
all scaffolded once for you to fork.

Tailwind v4 does not scan `node_modules`, so your stylesheet carries an
`@source` directive pointing at the package. Keep it — without it every
utility only the runtime renders vanishes from the built CSS.

Per-env VALUES (API origin, feature flags) are not part of this package: they
come from the generated `src/lib/config_gen.ts`, and every `runtime_config` key
must be one the bundle reads. Load `frontend-runtime/config`.

## What the runtime provides, and where each is documented

| Battery | What it is | Load |
|---|---|---|
| Transport interceptor stack | `buildRuntimeInterceptors` in `connect.ts`: error-normalize → auth → brand → traceparent; React Query owns retries. Typed errors: branch on `.reason`, render with `userMessage(err)`, never match on message text | `frontend-runtime/transport` |
| App-shell providers | `RuntimeShell` in `providers.tsx`: `useSession()`, `RuntimeErrorBoundary`, `RuntimeToastHost`, `RouteGuard` (a render gate, not a security boundary) | `frontend-runtime/shell` |
| `<Resource>` | the data-table container: loading / error / empty / data ladder, server-side filter, cursor pagination, sorting | `frontend-runtime/resource` |
| Browser telemetry | the HyperDX SDK, initialised for you: errors, console, network, traces to ClickStack through a same-origin `/_otel` route; source maps, replay, `reportException`, `interaction()` click-to-usable spans; cookieless product analytics by default (`identified`: nothing stored on the device; `full` only with `ANALYTICS_MODE = "full"` + `grantAnalyticsConsent()`), automatic `page_viewed` events with referrer/utm/click-id props and signed-in user identity (`PAGE_VIEWS` / `IDENTIFY_USER` turn them off per env), `trackExposure` + an OpenFeature hook | `frontend-runtime/telemetry` |
| Runtime config | `config_gen.ts`, `config.js`, accepted and required `runtime_config` keys, a frontend in another repo | `frontend-runtime/config` |
