---
name: frontend-runtime/transport
description: The forge-web-runtime transport interceptor stack — buildRuntimeInterceptors (error-normalize → auth → brand → traceparent; React Query owns retries), always-on W3C traceparent tracing, and typed errors: ConnectClientError, the total .reason vocabulary, userMessage(err), normalizeError, never matching on message text.
---

# Transport interceptor stack

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

## Full-stack tracing (always on)

`traceInterceptor` attaches a valid **W3C `traceparent`** to every RPC — with
**no OTLP collector required**. The forge backend runs `otelconnect` +
`otelhttp` with a `TraceContext` propagator, so browser→backend calls **join
the same distributed trace**. When browser telemetry is also running (`frontend-runtime/telemetry`),
the active browser span's context is propagated instead, stitching the browser
and server spans into one trace.

Which trace a call joins (the caller's active span, else the open
`interaction()`) is decided when the call ENTERS the chain, before any stage
awaits. Every runtime interceptor makes that choice on entry if no earlier
one has, so keep a runtime interceptor outermost, or put
`interactionContextInterceptor()` first when your own awaiting stages lead.

## Typed errors

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
