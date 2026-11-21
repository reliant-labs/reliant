---
name: frontend/mock-mode
description: Connect RPC clients in a forge frontend and opt-in mock mode — the generated transport in src/lib/connect.ts, mock_api ("", "true", "hybrid") and the signed-in user each gives, the mock banner, where the mock pipeline lives (fixtures, service defaults, scenarios), dispatch order, generated defaults for every service (vendored ones too), and overriding an RPC with forge scaffold scenario + mockService/patch.
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

## A mock transport in production: demos and previews

The build-time `mock_api` is for development. To run a page on mocks in a PRODUCTION bundle (a public demo host, a sandbox preview), build the transport yourself, behind your own runtime switch, and import the engine only from that page's lazy chunk:

```ts
import { createMockTransport, resolveActiveScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

const scenario = resolveActiveScenario(scenarios, { name: "demo" }); // the app chooses; ?scenario= is not read
const transport = createMockTransport({
  scenario: { ...scenario, passthrough: liveUrl ? "first" : false },
  fallback: liveUrl ? createConnectTransport({ baseUrl: liveUrl }) : undefined,
  readOnly: true,                // refuse anything not `idempotency_level = NO_SIDE_EFFECTS`
  passthroughTimeoutMs: 4000,    // "first": the backend's deadline before the scenario answers
  onPassthroughFallback: () => showSampleDataBanner(),
});
```

- `readOnly` refuses every non-read with `PermissionDenied` BEFORE any handler, fixture or backend sees it. `true` reads the proto's idempotency; pass a function for a contract that does not annotate it.
- `passthrough: "first"` sends EVERY RPC to `fallback` first and answers from the scenario, then the fixtures, only when it fails or times out — live data that degrades to sample data, not to an error page. The deadline is enforced by the mock transport, not left to `fallback`. A server stream is read to its end within the deadline, then replayed, so a stream that fails midway still falls back; a stream that never ends always ends up on the mock. A caller's own abort is never masked. Swapping mocks for a real endpoint is then config, not code.
