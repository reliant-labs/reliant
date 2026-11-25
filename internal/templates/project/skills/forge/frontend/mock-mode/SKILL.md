---
name: frontend/mock-mode
description: Connect RPC clients in a forge frontend and opt-in mock mode — the generated transport in src/lib/connect.ts, mock_api ("", "true", "hybrid") and the signed-in user each gives, the mock banner, where the mock pipeline lives (fixtures, service defaults, scenarios), dispatch order, generated defaults for every service (vendored ones too), overriding an RPC with forge scaffold scenario + mockService/patch, and fixture packs on the fixture transport (createFixtureTransport) for demos, previews and tests.
---

# Connect RPC clients + mock mode

Import the generated transport from `src/lib/connect.ts`; for direct calls `createClient(MyService, transport)`. It talks to the REAL backend through `src/lib/api-base.ts` (`frontend/serving`). Mock mode is **opt-in**: set `mock_api` in `deploy/kcl/<env>/config.k`, then `forge generate`.

| `mock_api` | RPCs | Signed-in user |
|---|---|---|
| `""` (default) | Real backend. | `GET /auth/session` |
| `"true"` | Fixtures only; "MOCK DATA" banner. | Fixture `dev@localhost`, no `/auth/session`, so guards pass. |
| `"hybrid"` | `?scenario=` handlers over the real backend. | Same fixture, UI only: forwarded RPCs carry just the real cookie. |

**Never remove the mock banner from the layout** — it stops a working-looking UI masquerading as a working stack.

## Where the mock pipeline lives

The engine is library code (`@reliantlabs/forge-web-runtime/mock-transport`, a subpath so production bundles shake it out). Your project keeps:

- **`src/lib/mock-transport_gen.ts`** (Tier-1) — gathers the per-entity `src/mocks/entities/*_gen.ts` and the per-service `src/mocks/services/*_gen.ts`.
- **`src/mocks/<entity>_gen.ts`** — the fixtures, the same rows `forge db seed apply` writes.
- **`src/mocks/services/<service>_gen.ts`** (Tier-1) — one `serviceDefaults(Service)` per service in the frontend's `src/gen/**/*_pb.ts`, whichever buf template wrote it, so a vendored contract is covered like your own. Two services with one short name are qualified by package (`acme-v1-report-service_gen.ts`, `acmeV1ReportServiceDefaults`).
- **`src/mocks/scenarios/*.ts`** — your scenarios; forge only regenerates the `index_gen.ts` barrel.

Dispatch: scenario handler → hybrid passthrough → fixtures → service defaults → `Unimplemented`. A Get miss is `NotFound`. Writes persist until reload. A fixture List mirrors the backend's: each SET `optional` request field filters by equality (enums by value), `search` matches strings and enum names, `order_by`/`descending` sort, `page_size` pages, and `total_count` is the filtered count.

## Service defaults: every RPC answers

Every RPC that no scenario, passthrough or fixture takes answers a deterministic, schema-valid message built at runtime from its descriptor, streams included (3 messages for a server or bidi stream). The same request always gets the same answer (seed: method + request bytes). What a value is:

| Field | Value |
|---|---|
| `id`, `*_id` | `<thing>-<8 hex>`; `trace_id`/`span_id` are 32/16 hex. Echoes the request's field of the same name, so a Get answers the id it was asked for (a bare `id` only on the top record). |
| Timestamp, `*_at`, `*_unix_nano` | recent; list items newest first; `start*`/`created*` before `end*`/`updated*`. |
| `email`, `url`, `host`, `name`/`title` | `user-3@example.com`, `https://example.com/...`, `host-3.example.com`, `<Message> <n>`; other strings `<field>-<n>`. |
| numbers | a range the name suggests (`*_ms`, `count`, `percent`, `*_bytes`, `port`, …), else 1–100. |
| enums | cycle the defined values across a list, skipping the zero `UNSPECIFIED`. |
| repeated / map | 3–10 items / 3–5 entries. |
| oneof, Duration, Struct, Value, wrappers | one member; seconds; a small JSON object; a string; the unboxed primitive. |
| `next_page_token` | always `""`, and a `total_count` beside one list is its length. |

Then protovalidate constrains it: `const`, `in`/`not_in`, gt/gte/lt/lte, len/min_len/max_len, prefix/suffix/contains, `pattern` (when the regex is simple), the formats (email, uri, uuid, hostname, ip, ulid), min/max_items, min/max_pairs, Duration and Timestamp bounds (`gt_now`). CEL rules are not evaluated. Defaults are shape-valid sample data, not domain data: what a page must SAY is an override.

## Overriding an RPC

Run `forge scaffold scenario <name>` and add handlers — do NOT edit `mock-transport_gen.ts` or a `_gen.ts` module. Activate with `?scenario=<name>`; it is read once at module init, so client-side navigation keeps it active until a full reload.

```ts
import { mockFactory, mockService, patch } from "@reliantlabs/forge-web-runtime/mock-transport";
import { ReportService, ReportSchema } from "@/gen/services/reports/v1/reports_pb";

export default defineScenario({
  name: "failing-reports",
  handlers: {
    ...mockService(ReportService, {
      // Typed by the service: a misspelled RPC or field fails tsc. `d` is the defaults.
      listReports: (req, d) => {
        const res = d.listReports(req);
        return patch(res, { reports: res.reports.slice(0, 3), summary: { failed: 2 } });
      },
      getReport: () => ({ sections: [] }), // a plain object is the whole response; omitted fields are zero values
    }),
  },
});
```

- `patch(message, changes)` deep-merges onto a default: nested messages merge field by field; lists, maps, oneofs and scalars are replaced.
- `mockService(ReportService, {}, { factories: [mockFactory(ReportSchema, ({ index }) => ({ owner: index % 2 ? "ops" : "sales" }))] })` reshapes one message type wherever the service builds it (the factory's result is patched onto the default), and answers every RPC of that service with it.
- `mockValue(Schema, { seed })` builds one message; `serviceDefaults(Svc, { now })` pins "recent" for a screenshot or a test.
- A key-string handler (`"pkg.v1.Svc/Method": (req) => ...`) still works; `mockService` is the typed spelling, and the only typed one for a vendored service.

## Fixtures per env: a demo or preview env promoted by digest

`mock_api` is BUILD-time sugar for development. A deployed env that should
serve sample data declares it as RUNTIME config on the frontend instead, so it
runs the same release digest as prod:

```bash
forge env new demo --from prod
```

```kcl
# deploy/kcl/demo/main.k, on the frontend
fixtures = forge.Fixtures { scenario = "shop" }   # read_only = True
```

`forge env deploy demo <prod's release>` then ships prod's bytes, and
`forge env render demo --json | jq '.frontends'` shows what reaches the browser:

| Runtime config key | Value |
|---|---|
| `FIXTURES_SCENARIO` | the scenario name (`src/mocks/scenarios/<name>.ts`) |
| `FIXTURES_READ_ONLY` | `"true"`, or `"false"` when `read_only = False`; anything but `"false"` is read-only |

- The scaffolded `src/lib/connect.ts` reads them through `src/lib/fixtures.ts`
  (`runtimeFixtures()`) and answers every RPC from the generated
  `createMockTransport(undefined, { scenario, readOnly })`, loaded lazily, so a
  live env's main chunk carries no fixture code. The scenario wins over
  `?scenario=`; dispatch below it is unchanged (scenario → fixtures → service
  defaults).
- Read-only is the default and fails closed: only rpcs declaring
  `option idempotency_level = NO_SIDE_EFFECTS;` answer, the rest fail
  `PermissionDenied`. Annotate your read rpcs (`forge lint --idempotency`,
  below), or a read-only demo renders their errors.
- `src/lib/auth/session-provider.ts` signs the UI in as the fixture user, as
  `mock_api = "true"` does. No mock banner is drawn: label sample data in your
  own UI.
- `mock_api` stays and means what it did: `"true"` is roughly fixtures with the
  `?scenario=` (or `default`) scenario and writes allowed, `"hybrid"` the
  scenario over the real backend. The schema refuses `fixtures` beside
  `config.mock`.
- A frontend whose `connect.ts` predates forge.Fixtures ignores the keys.
  Port the branch from a fresh scaffold, or read
  `window.__FORGE_CONFIG__.FIXTURES_SCENARIO` in your own transport layer.
- A bundle that must not contain the live app's code at all (no sign-in, no
  API client) is still a second build target: that is the escape hatch, not
  the default.

## Fixture data in production: demos and previews

The build-time `mock_api` is for development. To run the real app on sample data in a PRODUCTION bundle (a public demo, a hosted preview, a screenshot harness), serve a **fixture pack** through the fixture transport, behind your own runtime switch, imported only from a lazy chunk:

```ts
import { createFixtureTransport, defineFixturePack, fixtureJson } from "@reliantlabs/forge-web-runtime/fixture-transport";
import tracesJson from "./data/traces.json";

export const shopPack = defineFixturePack({
  name: "shop",
  idPrefix: "sample.",                       // ids the pack mints: ctx.id("t1") is "sample.t1"
  extends: [basePack],                       // later layers win, per method
  data: { traces: fixtureJson(ListTracesResponseSchema, tracesJson) }, // strict fromJson: an unknown field fails the load
  routes: (router, ctx) => router.service(TraceService, {           // standard typed ServiceImpls
    listTraces: () => ctx.data.traces,
    async *watch() { yield { id: ctx.id("e1"), observedAt: timestampFromMs(ctx.now()) }; },
  }),
  scenario: demoScenario,                    // an existing createMockTransport scenario still works
  defaults: [TraceService],                  // descriptor-derived answers for the rest
});

const transport = createFixtureTransport({
  pack: shopPack,
  readOnly: true,                            // only `idempotency_level = NO_SIDE_EFFECTS` passes
  readOnlyMessage: "Sample data is read-only",
  now: () => loadedAt,                       // a test passes a fixed instant: one pack, two clocks
});
```

- It is Connect's own `createRouterTransport`: requests and responses really serialize (a fixture that cannot travel fails here) and your interceptors (`interceptors: [...]`) run.
- Dispatch: `readOnly` guard, then the method's route or scenario handler (latest layer wins; within one layer a route beats a scenario handler), then `defaults`, then `Unimplemented`. `readOnly` has no name heuristic: an unannotated read is refused, and the fix is the proto annotation.
- In the project's tests, `expect(await checkFixturePack(shopPack, { services: [TraceService] })).toEqual([])` calls every handler once and names the method of each output with a misspelled field, a wrong value type, another message type or a failed serialization, plus every `data` entry that does not decode.
- The signed-in user of such a bundle is a **fixed session**, not your auth module: mount `<SessionProvider auth={fixedSession(APP_USER)}>` from `@reliantlabs/forge-web-runtime/session` (react is its only import), with `APP_USER` the same user the pack's current-user rpc answers, so the shell and the data agree. Signed in, never loading, `getToken` resolves null. Depth: `frontend-runtime/shell`.

Live data that degrades to sample data is still `createMockTransport` (`/mock-transport`):

```ts
const transport = createMockTransport({
  scenario: { ...scenario, passthrough: "first" },
  fallback: createConnectTransport({ baseUrl: liveUrl }),
  readOnly: true,
  passthroughTimeoutMs: 4000,                // the backend's deadline before the scenario answers
  onPassthroughFallback: () => showSampleDataBanner(),
});
```

`passthrough: "first"` sends EVERY RPC to `fallback` first and answers from the scenario, then the fixtures, only when it fails or times out. The deadline is enforced by the mock transport. A server stream is read to its end within the deadline, then replayed, so a stream that fails midway still falls back. A caller's own abort is never masked. There, `readOnly` also takes a function, for a contract that does not annotate idempotency.

### Reads declare it in the proto; `forge lint --idempotency` keeps it true

`readOnly: true` takes the proto at its word, with no name heuristic: an rpc is a read only if it declares `option idempotency_level = NO_SIDE_EFFECTS;`. Everything else is refused (fail closed), so a demo page that calls an undeclared read shows an error, and a write wrongly declared a read reaches the fixtures (and becomes a cacheable GET in production).

- Declare it in the body of every read: `rpc GetOrder(GetOrderRequest) returns (GetOrderResponse) { option idempotency_level = NO_SIDE_EFFECTS; }`. `forge scaffold entity` writes it on Get and List; `forge scaffold rpc` on a read-named rpc.
- `forge lint --idempotency` (part of every `forge lint`; warns for one release, then gates) flags a read-named unary or server-streaming rpc without it (`forge-idempotency-read-undeclared`; read verbs are the ones forge already treats as reads: Get, List, Search, Find, Check, Has, Is, Count, Exists, Watch, Query, Describe, BatchGet) and a write-named rpc (Create, Update, Delete, Set, Add, Remove, Upsert, Insert, Patch, Replace, Cancel, Send, Grant, Revoke, Archive, Restore, Purge, Move, Rename) or a `// forge:mutation` one that declares it (`forge-idempotency-write-marked-read`). A noun name (`Health`) is not judged.
- A read whose response carries a secret or a per-call value (an access token, a presigned URL, an OAuth state, an invite or claim link, a checkout URL, a one-time code, a nonce, a challenge, or any `debug_redact` field) must NOT declare it: NO_SIDE_EFFECTS lets Connect send the rpc as an HTTP GET, which caches and proxies may store and replay to the next caller. `forge-idempotency-secret-response-marked-read` flags one that does, and takes the place of the read-undeclared finding on one that does not; mark it `// forge:mutation: <what each call issues>` instead. A read-only fixture transport then refuses it, like any other rpc that is not a read. The rule reads string and bytes fields at any depth of the response by name (token, secret, password, credential, nonce, challenge, otp, one_time, oauth_state, api_key; a signed, presigned, upload, download, export, management, checkout, portal, authorize, reset, invite, claim or magic url or link) and any `[debug_redact = true]` field. Pagination tokens (`page_token`, `next_page_token`, `resume_token`, `cursor`), masked or hashed forms (`masked_api_key`) and counts (`input_tokens`) never fire. A `// forge:secret` field is skipped: the marker says reads never return it, so it also settles a write-only field of a shared message for every rpc that returns it. Any other misread takes `// forge:lint-disable-next-line forge-idempotency-secret-response-marked-read: <why>` above the rpc.
- A read-named rpc that really writes says so, with the reason, which also makes its generated hook a useMutation:

```proto
// forge:mutation: creates the cart on first call
rpc GetOrCreateCart(GetOrCreateCartRequest) returns (GetOrCreateCartResponse);
```

- Anything else: `// forge:lint-disable-next-line <rule>: <why>` above the rpc.

## A fixture subtree: TransportScope

When only PART of a signed-in page reads fixtures (a sample app in a product tour, a preview panel) and the rest stays live, give that subtree its own transport and its own query cache with `@reliantlabs/forge-web-runtime/transport-scope`. Never route by request content instead (an interceptor that recognises sample ids): the first request it misses goes to the real backend.

```tsx
import { createTransportScope, TransportScopeProvider, useClient } from "@reliantlabs/forge-web-runtime/transport-scope";

const sampleScope = createTransportScope({
  id: "sample",                                         // for logs; never part of a query key
  transport: createFixtureTransport({ pack: shopPack, readOnly: true }),
  readOnly: true,                                       // a flag YOUR UI reads; the runtime enforces nothing with it
});

<TransportScopeProvider scope={sampleScope}>             {/* mounts sampleScope.queryClient + the scope */}
  <ConsolePages />
</TransportScopeProvider>
```

- **The scope's QueryClient carries its transport.** React context cannot be read in a `queryFn`, so a generated hook resolves its client from the QueryClient it runs on (`context.client`): `const clientOf = scopedClient(Svc, connectClient(Svc))`, then `clientOf(ctx).method(...)`. Reads, `sampleScope.queryClient.fetchQuery(svcQueries.getX(req))` outside React, mutation hooks and `svcMutations(sampleScope.queryClient)` all go to the scope's transport. Generated hook files do this with no change to your `src/lib/connect.ts`: `connectClient(Svc)` stays the fallback, so outside a scope every call is what it was. A hook file generated by an older forge holds one module client and ignores the scope: run `forge generate`. It needs a runtime that ships `/transport-scope`; `forge generate` says so when the installed one does not.
- **Each scope owns its QueryClient**, created for it unless you pass `queryClient` (never the app's). The cache is isolated by construction, so `xServiceKeys.*` stay module constants and `invalidateQueries({ queryKey: xServiceKeys.all })` works inside and outside the scope. Call `sampleScope.queryClient.clear()` when the subtree goes away for good.
- **Hand-written calls** take the scope with hooks: `useClient(Svc, moduleClient)` and `useTransport(moduleTransport)` return the nearest scope's, else the fallback; `useTransportScope()` returns the scope or null. Outside React, `clientFor(Svc, transport)` is memoized per transport and `transportOf(queryClient)` names a scope client's transport.
- Do not mount another `QueryClientProvider` inside a scope: hooks below it would leave the scope.
- Needs TanStack Query **5.66.0** for reads (`context.client` on a `queryFn`). Mutation hooks fall back to their own `useQueryClient()` where TanStack before 5.89.0 passes no mutation context. On an older TanStack a read falls back to the module client.
