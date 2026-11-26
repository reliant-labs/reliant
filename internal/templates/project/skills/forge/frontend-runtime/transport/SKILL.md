---
name: frontend-runtime/transport
description: The forge-web-runtime transport interceptor stack — buildRuntimeInterceptors (error-normalize → auth → brand → rpc span → traceparent; React Query owns retries), always-on W3C traceparent tracing, one client span per RPC with redacted failure forensics, and typed errors: ConnectClientError, the total .reason vocabulary, userMessage(err), normalizeError, never matching on message text.
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

The chain (outermost → innermost): **error-normalize → [per-service] → auth →
brand headers → rpc span → traceparent**. There is no retry layer in it: the app's React Query
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

## Interceptors for one service

`serviceInterceptors` scopes interceptors to ONE service's RPCs, keyed by its
fully-qualified proto name (the generated descriptor's `typeName`). Add it to
the `buildRuntimeInterceptors` call in your `connect.ts` (its comment shows
where); it needs a runtime newer than 0.13.21, the first that exports
`forService`:

```ts
const serviceInterceptors: Record<string, Interceptor[]> = {
  [ReportService.typeName]: [quotaGuard, pollHeader],
};
const interceptors = buildRuntimeInterceptors({ serviceInterceptors, onError });
```

Each list runs in order, only for that service, directly inside
error-normalize and ahead of auth, the rpc span and traceparent: what it throws
reaches callers as a `ConnectClientError` (and `onError`), a header it sets is
on the wire, and time it spends waiting (a scheduler, a quota guard) is outside
the RPC's span. Other services' calls pass straight through. `forService(svc,
...interceptors)` is the same scoping as a plain Connect `Interceptor`, for a
chain you assemble yourself (`svc` is a name, a descriptor, or a list of them).

What ONE call carries is the call site's: the generated hooks' `callOptions`
(`proto/hooks`). A call site marks a read (`meta: { poll: true }`, `callOptions:
({ meta }) => ({ contextValues: createContextValues().set(pollKey, meta?.poll
=== true) })`) and the service's interceptor reads `req.contextValues.get(pollKey)`
and sets the header, so neither the hooks nor the transport are wrapped.

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

## One client span per RPC

`rpcSpanInterceptor` records a `CLIENT` span for every RPC, unary and
streaming, named `<rpc.service>/<rpc.method>` (`pkg.v1.ChatService/GetChat`)
with `rpc.system = connect_rpc`, `rpc.service`, `rpc.method`,
`forge.rpc.client = true`, and on failure `rpc.connect_rpc.error_code` and an
ERROR status (a cancelled call keeps its code but is not an error). A stream's
span ends when the stream does. It sits directly outside the traceparent
stage, so the server's otelconnect span is its descendant in the same trace,
and `rpc.service` + `rpc.method` join the two. With no tracer provider
registered it records nothing and costs nothing; a React Native app that
registers its own OpenTelemetry provider gets the spans from `/interceptors`.

A failed call's span carries one **`forge.forensics`** event:
`forensics.reason = error` and `forensics.arg.<field>` for the request
message, classified deny by default — numbers, bools, enums and id-named
strings (`*_id`, `*Id`, `*ID`, `*Ids`) as values, except that a string or
number with a secret name is `"[len=N]"` only (a number's digit count),
id-shaped or not. A name is secret when, lowercased with `_`/`-` removed, it
contains `token`, `secret`, `passw`, `credential`, `session`, `cookie`,
`apikey`, `privatekey`, `signingkey` or `otp` (`session_id`, `apiKeyId`), or
when one of its words, or two adjacent words joined, is `pin`, `otp`, `cvv`,
`cvc`, `ssn`, `tan`, `passcode`, `securitycode`, `mfa` or `totp`, plural or
not (`pin`, `userPIN`, `cvv2`, `security_code`; not `spinner`, `shipping`,
`tank`). A number whose first or last word is `max`, `min`, `limit`,
`count`, `total`, `size`, `len` or `length`, or whose first word is `num`, is
a quantity and keeps its value (`max_tokens`, `session_count`,
`num_sessions`); a trailing `num` is not (`pin_num` records its digit count),
nor is an id-shaped secret name (`max_session_id` records its digit count),
and a string named like a quantity still records its length. `uuid`, `*_uuid` and `*Uuid` are id-shaped like `id`. A secret-named
bool or enum keeps its value (`mfa_enabled`). The
server records the same names the same way. Every other string and
bytes as `"[len=N]"`; repeated and map fields as `"[count=N]"`; times
(`Timestamp`, `Duration`, the `google.type` dates) as `"[type=<Name>]"`, never
their value; nested messages walked three levels; `[debug_redact = true]`
fields never. Once per
procedure and error code per page load. Mark a request field that must never
leave the browser with `[debug_redact = true]` in the proto.

Off switches: `buildRuntimeInterceptors({ rpcForensics: false })` (this chain),
`telemetry = forge.BrowserTelemetry {rpc_forensics = False}` in the env's KCL
(renders `RPC_FORENSICS = "false"`, page-wide), `rpcSpans: false` (no span
stage). `disableTelemetry()` stops both. A hand-built chain puts
`rpcSpanInterceptor()` directly outside `traceInterceptor()`.

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

## Failed RPCs in the dev log

`errorNormalizeInterceptor` reports every failed RPC except a cancelled one
(navigation, unmount, a superseded query) to a sink. In dev,
`installDevLogging` installs one that writes an error line into
`.forge/logs/<env>/frontend_<name>.log`, worded like the server's own
`rpc failed` line so one grep of the trace id finds both:

```
[browser:error] rpc failed procedure=/pkg.v1.Svc/Method code=not_found reason=not_found message="no such task" trace_id=<32 hex>
```

It is written whatever the page does with the error, so a toast, an inline
banner or a swallowed failure all leave the same line. With no sink —
production, React Native, dev logging not installed — nothing is written:
browser telemetry already records the failed request. `setRpcFailureSink(fn)`
installs your own (it receives `{procedure, codeName, reason, message,
traceId}`; `formatRpcFailure` spells the line); a sink that throws is ignored.
