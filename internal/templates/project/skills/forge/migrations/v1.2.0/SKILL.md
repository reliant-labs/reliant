---
name: v1.2.0
description: Upgrade a project to forge v1.2.0. In mock mode every RPC of every service a frontend has generated TypeScript for — custom reads, streams, vendored contracts — now answers a deterministic, schema-valid default instead of Unimplemented; forge generate writes src/mocks/services/<service>_gen.ts per service and mock-transport_gen.ts gathers them. Hand-written scenario handlers that only filled in a response shape can become mockService(Service, { rpc: (req, d) => patch(d.rpc(req), …) }) overrides, typed for vendored services too. Use when a browser frontend has src/lib/mock-transport_gen.ts.
version: v1.2.0
detection: for f in frontends/*/src/lib/mock-transport_gen.ts; do [ -f "$f" ] && exit 0; done; exit 1
---

# Upgrading to v1.2.0

## 1. Every RPC answers in mock mode

**Use when** a Next.js or Vite frontend has `src/lib/mock-transport_gen.ts`.

### What changed

`forge generate` writes one Tier-1 module per Connect service found in the
frontend's `src/gen/**/*_pb.ts` — the project's own protos and any vendored
contract another buf template generated there:

```
src/mocks/services/<service>_gen.ts   export const <service>Defaults = serviceDefaults(<Service>);
```

`src/lib/mock-transport_gen.ts` gathers them and passes them to the engine as
`services`. The dispatch order gains one rung, after the entity fixtures:
scenario handler → hybrid passthrough → fixtures → **service defaults** →
`Unimplemented`. A default is built at runtime from the service's descriptor
(field names, enums, maps, oneofs, well-known types, protovalidate rules) and is
the same for the same request; the `frontend/mock-mode` skill lists what each
field becomes.

The new modules import `serviceDefaults` and `isServiceDefaults` from
`@reliantlabs/forge-web-runtime/mock-transport`; `forge generate` raises the
frontend's `@reliantlabs/forge-web-runtime` range to the release that has them.
Run `npm install` in the frontend after generating.

### What to check

- A mocked page that showed an error for a custom RPC now shows sample data.
  A test or a scenario that ASSERTED `Unimplemented` for an unmocked RPC must
  name that RPC now (a scenario handler that throws
  `new ConnectError("…", Code.Unimplemented)`).
- In hybrid mode (`passthrough: true`), unmatched RPCs still go to the backend:
  defaults rank below passthrough.

### Optional: shrink hand-written handlers

A scenario handler that built a whole response by hand to make a page render
can start from the default and change only what the scenario is about:

```ts
import { mockService, patch } from "@reliantlabs/forge-web-runtime/mock-transport";

handlers: {
  ...mockService(ReportService, {
    listReports: (req, d) => patch(d.listReports(req), { reports: [] }),
  }),
},
```

`mockService` is typed by the service descriptor, so a vendored service's
handlers no longer need the untyped `"pkg.v1.Svc/Method"` escape hatch.
