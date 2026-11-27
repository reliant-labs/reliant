---
name: frontend-runtime
description: The forge frontend runtime, @reliantlabs/forge-web-runtime — the web twin of forge/pkg. Transport interceptor stack (error normalization, auth, brand, a client span per RPC with redacted failure forensics, W3C traceparent; React Query owns retries), app-shell providers (session, error boundary, toast host), the generic <Resource> data-table container, and browser telemetry (the HyperDX SDK: errors, console, network, traces — to ClickStack through a same-origin /_otel route).
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

`forge generate` raises the declared range to the floor its generated code
needs, but the floor is a version, and the lockfile can still install an
older runtime than the generated code was written against. So after it settles
each frontend, generate checks named imports in its generated TypeScript
against the INSTALLED runtime's public entry points. It also checks option
keys it can trace to the telemetry and mock transport API declarations. If
an import or option is missing, generate prints a
`⚠️ frontend <name>: the installed @reliantlabs/forge-web-runtime X does not
ship …` line that names each feature and the file using it. Fix it with
`npm update @reliantlabs/forge-web-runtime` (or `pnpm update`, `yarn up`). If
that changes nothing, your forge is ahead of the last runtime release (a pin of
an untagged forge commit), and the next release publishes the feature.

You compose it from **your** owned files:

- `src/lib/connect.ts` — builds the transport interceptors from the runtime.
- `src/app/providers.tsx` — mounts `RuntimeShell` and inits telemetry.

Import everything from the barrel:
`import { … } from "@reliantlabs/forge-web-runtime"`.

These subpaths are deliberately OUTSIDE the barrel, each for its own reason:

| Subpath | Why it is not in the barrel |
|---|---|
| `/interceptors` | DOM-free transport layer, for React Native / non-react-dom renderers. |
| `/mock-transport` | Dev-only. The fixture dispatch engine must be tree-shakeable out of a production bundle. |
| `/fixture-transport` | Fixture packs on Connect's in-memory router, for demos, previews and tests (`frontend/mock-mode`). An app imports it from a lazy chunk, so its main bundle never carries it. |
| `/transport-scope` | `TransportScopeProvider`, `createTransportScope`, `scopedClient`, `useClient`: a subtree with its own transport and QueryClient (`frontend/mock-mode`, "A fixture subtree"). Imports `@tanstack/react-query`, an optional peer, like `/service-hooks`. Reads need TanStack Query 5.66.0+. |
| `/telemetry` | Drives the HyperDX SDK (`@hyperdx/otel-web`, ~430 KB, loaded lazily as its own chunk) and records session replay with the runtime's own rrweb recorder (a second lazy chunk, ~180 KB, loaded only when replay runs). Both are exact OPTIONAL PEERS of the runtime (0.14.0 on; dependencies before): an app importing `/telemetry` declares them at the runtime's versions, which every web scaffold does and `forge generate` adds. A React-Native frontend or a `/node` consumer installs neither. |

Nothing you write imports `/mock-transport` or `/telemetry` directly (`/fixture-transport` is yours to import): forge generates
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
| Transport interceptor stack | `buildRuntimeInterceptors` in `connect.ts`: error-normalize → auth → brand → rpc span → traceparent; React Query owns retries. One client span per RPC (`<rpc.service>/<rpc.method>`, joined to the server span), with a redacted `forge.forensics` request shape on failure (`RPC_FORENSICS` turns it off per env). Typed errors: branch on `.reason`, render with `userMessage(err)`, never match on message text | `frontend-runtime/transport` |
| App-shell providers | `RuntimeShell` in `providers.tsx`: `useSession()` / `useSignedIn()`, `RuntimeErrorBoundary`, `RuntimeToastHost`, `RouteGuard` (a render gate, not a security boundary). The session port is a structural `SessionAuth` prop; `fixedSession(user)` is the one for a bundle with no auth code (demos, previews, tests), importable alone from the `./session` subpath | `frontend-runtime/shell` |
| `<Resource>` | the data-table container: loading / error / empty / data ladder, server-side filter, cursor pagination, sorting | `frontend-runtime/resource` |
| Browser telemetry | the HyperDX SDK, initialised for you: errors, console, network, traces to ClickStack through a same-origin `/_otel` route; source maps, replay, `reportException`, `interaction()` click-to-usable spans; cookieless product analytics by default (`identified`: nothing stored on the device; `full` only with `ANALYTICS_MODE = "full"` + `grantAnalyticsConsent()`), automatic `page_viewed` events with referrer/utm/click-id props and `page_engaged` (engaged time, scroll depth) per view and signed-in user identity (`PAGE_VIEWS` / `IDENTIFY_USER` turn them off per env), `trackExposure` + an OpenFeature hook | `frontend-runtime/telemetry` |
| Runtime config | `config_gen.ts`, `config.js`, accepted and required `runtime_config` keys, a frontend in another repo | `frontend-runtime/config` |
