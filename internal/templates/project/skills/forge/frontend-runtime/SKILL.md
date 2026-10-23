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

Per-env VALUES (API origin, feature flags) are not part of this package. They
come from the generated `src/lib/config_gen.ts`, which reads
`window.__FORGE_CONFIG__` from `<basePath>/config.js`. Dev, Firebase, own-bucket
and hosted deploys all serve the same `{KEY: "value"}` shape, so the
bundle cannot tell them apart. See `frontend` → "Runtime config and backend URLs".

Every `runtime_config` key must be one the bundle reads: a field of the
frontend's config message, or a forge runtime key (`API_URL`, `ENVIRONMENT`,
`OTEL_ENDPOINT`, `CONSOLE_CAPTURE`, `REPLAY_SAMPLE_RATE`). `forge generate`
lists them in `src/lib/config_schema_gen.json` beside `config_gen.ts`, and every
render checks the env's `runtime_config` against the frontend directory's copy
— so a project declaring a frontend from ANOTHER repo gets a did-you-mean on a
typo instead of a field silently left on its default. No schema file (no config
message) means no check.

Tailwind v4 does not scan `node_modules`, so your stylesheet carries an
`@source` directive pointing at the package. Keep it — without it every
utility only the runtime renders vanishes from the built CSS.

A frontend whose code is in ANOTHER repo (a sibling `path = "../other/web"` in
dev, or a source pin) never has a file written into that repo: forge renders its
`config.js` to `.forge/runtime-config/<frontend>/config.js` in the declaring
project, and `forge env up` hands the path to its dev server as
`FORGE_RUNTIME_CONFIG_FILE`. `forgeRuntimeConfig()` from
`@reliantlabs/forge-web-runtime/vite` serves it at `<base>config.js` (the
vite-spa scaffold includes it; it does nothing when the variable is unset).

## 1. Transport interceptor stack

`connect.ts` builds its Connect interceptors from the runtime:

```ts
import { buildRuntimeInterceptors } from "@reliantlabs/forge-web-runtime";

const interceptors = buildRuntimeInterceptors({
  getToken: () => _getToken(),      // optional; see the note below
  getBrand: () => currentBrand(),   // optional: X-Brand header
  onError: (e) => report(e),        // optional: typed-error sink
});
```

The chain (outermost → innermost): **error-normalize → auth → brand
headers → traceparent**. There is no retry layer in it: the app's React Query
client is the single retry owner, because it knows reads from writes — it
retries transient READ failures and never a mutation. Each React Query retry
re-runs the queryFn, so every attempt goes through the whole chain again
(fresh token, fresh traceparent). `retryInterceptor` is still exported,
idempotency-gated, for code that calls the raw transport outside React Query;
adding it to the default chain would stack two retry layers and retry
non-idempotent writes.

**Every field is optional, and the browser frontends supply no `getToken`.**
They sign in natively: the session is an HttpOnly cookie the browser attaches
to same-origin requests itself, so there is no token in JavaScript to hand
over and the auth interceptor simply adds no header. React Native is the tree
that does wire one, from its `AuthProvider` (see `auth/frontend`).

### Full-stack tracing (always on)

`traceInterceptor` attaches a valid **W3C `traceparent`** to every RPC — with
**no OTLP collector required**. The forge backend runs `otelconnect` +
`otelhttp` with a `TraceContext` propagator, so browser→backend calls **join
the same distributed trace**. When browser telemetry is also running (§4),
the active browser span's context is propagated instead, stitching the browser
and server spans into one trace.

### Typed errors

Failures are normalized to `ConnectClientError`, a **subclass of
`ConnectError`**: `err instanceof ConnectError` still holds, `.code` is still
the numeric `Code`, and `.metadata`, `.details`, `findDetails()` and
`.rawMessage` are the original error's. Code written against connect-es keeps
working when the runtime chain is put in front of it, so an existing app can
adopt the runtime one call site at a time. What the normalized error adds:

| field | what it is |
| --- | --- |
| `.reason` | **stable machine code** the backend stamps on every error (`"not_found"`, `"duplicate"`, `"reference_in_use"`, …), or `null` |
| `.code` | the Connect `Code` (numeric, as on any `ConnectError`) |
| `.codeName` | its canonical wire name (`"not_found"`, `"unavailable"`, …); `codeName(code)` spells one |
| `.status` | approximate HTTP status, for telemetry and generic UI |
| `.retryable` | true for transient failures (unavailable, deadline-exceeded, aborted, resource-exhausted) |
| `.traceId` | the trace the failed request was recorded in, or `null` |

A failure that never reached a server is a `ConnectClientError` too: a
network fault (`TypeError`) is `Code.Unavailable` and retryable, an aborted
fetch is `Code.Canceled`, anything else `Code.Unknown`. Its `.name` is
`"ConnectError"`, which is what connect-es's `instanceof` requires.

The default chain does not retry: React Query owns retries, keyed on
`.retryable`. `retryInterceptor` is exported for a raw transport used outside
React Query (idempotent unary rpcs only — streams are never retried).

```ts
import {
  ConnectClientError,
  FORGE_ERROR_REASON_HEADER, // "x-forge-error-reason" — the metadata key .reason is read from
  normalizeError,
  userMessage,
} from "@reliantlabs/forge-web-runtime";
```

**The rule: branch on `.reason` (or `.code` / `.codeName`), render with `userMessage(err)`,
never match on message text.** Message prose is display-only — it is written
for humans, it gets reworded, and it is localized eventually; a `switch` on it
is a bug that ships green.

```tsx
const create = useCreateItem();               // hooks type their error as ConnectClientError
// …
if (create.error) {
  switch (create.error.reason) {
    case "duplicate":         return <FieldError name="sku">That SKU is taken.</FieldError>;
    case "reference_missing": return <Banner>Pick a category that still exists.</Banner>;
    default:                  return <Banner>{userMessage(create.error)}</Banner>;
  }
}
```

`.reason` is genuinely end-to-end and does not depend on a handler
remembering to set it: `forge/pkg/crud` — which produces most of a generated
app's errors — routes every failure through one exit that cannot construct an
error without a reason, and `CORSMiddleware` exposes the header
cross-origin. The vocabulary is **total** (`not_found`, `duplicate`,
`reference_missing`, `reference_in_use`, `required_field_missing`,
`constraint_violated`, `invalid_format`, `unknown_field`,
`invalid_page_token`, `invalid_order_by`, `page_token_order_conflict`,
`internal`), so a `switch` never falls through to `null` and back to sniffing
prose. Your own handlers join it with
`svcerr.Wrap(svcerr.WithReason(err, "no_active_subscription"))`.

`userMessage(err)` is the only correct way to SHOW an error. It strips the
transport framing that must never reach a user — the `[code]` prefix
connect-es adds — and falls back to generic copy when there is nothing
presentable. What remains is the message the backend wrote, shown verbatim.
Never render `err.message`.

Generated hooks (`use<Rpc>` in `src/hooks/`) declare their error as
`ConnectClientError`, so `.reason` is reachable at the call site with no cast.
Outside a hook — a raw transport call, an error boundary, a `catch` — run the
value through `normalizeError(err)` first.

## 2. App-shell providers, error boundary, toast host

The dependency points **app → runtime, one way**. The runtime never imports
`@/lib/auth/*`, `@/lib/event-context`, `@/lib/use-event`, or `@/components/ui/*` — those are yours
to rename, restyle, or delete, and a forge-owned file may not depend on them.
Everything app-shaped is a **prop**, wired once in your `providers.tsx`:

```tsx
import { RuntimeShell } from "@reliantlabs/forge-web-runtime";
import ToastNotification from "@/components/ui/toast_notification";
import { useAuth } from "@/lib/auth/use-auth";
import { useEventBus } from "@/lib/use-event";

function RuntimeLayer({ children }: { children: React.ReactNode }) {
  const auth = useAuth();
  const bus = useEventBus();

  const subscribe = useCallback((sink: RuntimeToastSink) => {
    const offShow = bus.on("toast:show", (p) => sink.onShow(p));
    const offDismiss = bus.on("toast:dismiss", (p) => sink.onDismiss(p?.id));
    return () => { offShow(); offDismiss(); };
  }, [bus]);

  const render = useCallback<RuntimeToastHostProps["render"]>(
    ({ toasts, onDismiss }) => <ToastNotification toasts={toasts} onDismiss={onDismiss} />,
    [],
  );

  return (
    <RuntimeShell auth={auth} toast={{ subscribe, render }}>
      {children}
    </RuntimeShell>
  );
}
```

`RuntimeShellProps`: `auth` (required — any value shaped like `SessionAuth`:
`{ user, isAuthenticated, isLoading, getToken }`), `toast` (optional — omit and
no host is mounted), `onError` (optional — forwarded to the error boundary).

`SessionAuth` is a structural PROP, not an import, so the app adapts whatever
it has into that shape — which is what lets `src/lib/auth/*` change without
touching a forge-owned file. The browser scaffolds do exactly that in
`providers.tsx`: they map their `identity` onto `user` and pass a `getToken`
that answers null, because the real token is in an HttpOnly cookie no script
can read. `useSession()` therefore reports the identity and no `claims`.

`RuntimeShell` supplies three batteries:

- **`SessionProvider` / `useSession()`** — the signed-in user, from the `auth`
  prop, plus any JWT claims decodable from a bearer token when one is supplied.
  ```ts
  const { userId, email, name, claims, isAuthenticated, isLoading } = useSession();
  ```
- **`RuntimeErrorBoundary`** — a React error boundary with a designed
  fallback. Next's `app/error.tsx` / `app/global-error.tsx` catch route
  crashes; use this to isolate a subtree so one crashing widget degrades to a
  small fallback instead of taking the route down:
  ```tsx
  import { RuntimeErrorBoundary } from "@reliantlabs/forge-web-runtime";
  <RuntimeErrorBoundary compact><RiskyWidget /></RuntimeErrorBoundary>
  ```
- **`RuntimeToastHost`** — owns the toast QUEUE (ids, add, dismiss-one,
  dismiss-all) and nothing else. `subscribe` feeds it your bus events; `render`
  draws them with your own component. The QueryClient's mutation-error
  chokepoint already emits `toast:show`; this host is what makes those toasts
  actually appear.

### Route guarding (render-gate only)

```tsx
import { RouteGuard } from "@reliantlabs/forge-web-runtime";

<RouteGuard loading={<Spinner />} fallback={<SignInPrompt />}>
  <AdminPanel />
</RouteGuard>
```

`RouteGuard` gates on **signed-in state only**: `loading` while the session
resolves, `fallback` when nobody is signed in, `children` otherwise. This is a
**render convenience, not a security boundary** — a client can always call the
RPC directly, so the handler's own checks are the only thing that actually
decides the outcome.

The runtime ends at identity. **Authorization is application code** — forge
does not generate it, and what a caller may do is yours to design and enforce.
`useSession().claims` hands you the decoded JWT; deciding what a claim entitles
someone to render is your call. See the `security-review` skill for the review
bar.

## 3. `<Resource>` — the data-table container

`<Resource>` encapsulates the loading / error / empty / data tristate ladder,
a debounced server-side filter, and cursor pagination in one owned-once
component. Pair it with `useQueryResource` and a generated list hook. Do not
hand-roll the tristate ladder, and do not client-side-filter a single page cap.
Pass the query's error straight through: the error rung renders
`userMessage(error)`, so there is nothing to pre-format.

```tsx
import { Resource, type ResourceColumn } from "@reliantlabs/forge-web-runtime";
import { useQueryResource } from "@/hooks/use-query-resource";

const columns: ResourceColumn<Item>[] = [
  { header: "Name", cell: (i) => i.name },
  { header: "Created", cell: (i) => fmtDate(i.createdAt) },
];

export function ItemsPage() {
  const [cursor, setCursor] = useState<string[]>([]);
  const [filter, setFilter] = useState("");
  const q = useQueryResource(useListItems({ pageToken: cursor.at(-1), filter }));
  return (
    <Resource
      title="Items"
      status={q.status}
      data={q.status === "success" ? q.data.items : undefined}
      error={q.status === "error" ? q.error : undefined}
      columns={columns}
      rowKey={(i) => i.id}
      filter={filter}
      onFilterChange={setFilter}
      onNextPage={() => setCursor((c) => [...c, nextToken])}
      onPrevPage={() => setCursor((c) => c.slice(0, -1))}
      hasNextPage={Boolean(nextToken)}
      hasPrevPage={cursor.length > 0}
    />
  );
}
```

## 4. Browser telemetry (HyperDX SDK)

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
throws. The SDK is the page's **only** tracer provider: it replaces the old
OTel web wiring rather than joining it, because two providers cannot both
register globally.

### Same-origin `/_otel`, and no secret in the bundle

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

### Turning it on, and the knobs

| Knob | Next.js | Vite | Default |
|---|---|---|---|
| ingest path (`off` or empty disables) | `NEXT_PUBLIC_OTEL_ENDPOINT` | `VITE_OTEL_ENDPOINT` | `/_otel` in dev, **empty in a production build** |
| `service.version` | `NEXT_PUBLIC_APP_VERSION` | `VITE_APP_VERSION` | the **build id** a deploy injects (12-hex hash of the frontend source and build env), unless you set it |
| `deployment.environment` | `NEXT_PUBLIC_ENVIRONMENT` | `VITE_ENVIRONMENT` | none |
| session replay | `NEXT_PUBLIC_OTEL_REPLAY=true` | `VITE_OTEL_REPLAY=true` | **off** |

For a deployed environment, declare `otel_endpoint = "/_otel"` in the
frontend's KCL `config` block. A project with a typed frontend config message
(the scaffolded `OTEL_ENDPOINT` / `API_URL` fields) receives it at runtime
through `config.js`, so one build promotes across environments; otherwise forge
passes it as the build-time variable above. `api_url` feeds
`tracePropagationTargets`, so a cross-origin API still gets a `traceparent`.
`environment` (KCL `config.environment`) is the build-time variable and is
emitted as both `deployment.environment.name` and the legacy
`deployment.environment` that HyperDX's default sources key on.

### Source maps are extracted, never served

A deploy builds with `FORGE_SOURCEMAPS=1` (the scaffolded `vite.config.ts` /
`next.config.ts` emit maps only then), then the static stage moves every map out
of the tree it publishes into `<staging>.sourcemaps/` (same relative paths, plus
`manifest.json` naming the project, frontend and build id) and strips the
`sourceMappingURL` trailers. The release digest excludes them, and a guard fails
a deploy whose served tree still holds one. Firebase asset retention never
carries a `.map` forward.

### Replay is shipped, off, and always masked

The SDK bundles an rrweb recorder. It stays disabled unless the replay knob is
`true`, and when it is on, **all text and all inputs are masked** and canvas is
not recorded — a replay records the DOM, which for most apps is their most
sensitive data. There is no knob that records unmasked. Reliant's own chat UI
never enables it. Network bodies (`advancedNetworkCapture`) are also off: request
and response bodies are user data.

### Reporting a caught error

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

Custom spans: `getTracer("checkout")` from `@/lib/otel_gen`.

Distributed tracing (the traceparent on every RPC) is independent and always
on — it does **not** depend on this module.
