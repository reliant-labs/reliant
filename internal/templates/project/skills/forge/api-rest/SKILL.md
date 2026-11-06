---
name: api-rest
description: REST URLs on top of Connect handlers via connectrpc.com/vanguard — opt-in runtime transcoding (no parallel codegen), default annotations on CRUD RPCs, and composition with api.openapi.
---

# REST Mode (Vanguard Transcoding)

Forge's default API surface is Connect-RPC: JSON-over-POST at
`/<service>/<Method>`. Set `api.rest: true` in `forge.yaml` and the
server puts
[`connectrpc.com/vanguard`](https://github.com/connectrpc/vanguard-go) —
Buf's official REST layer for Connect — in front of the Connect mux. The
same handler now answers four protocol skins (Connect, gRPC, gRPC-Web,
REST) with one set of RPC implementations.

There is no parallel codegen pipeline. Vanguard reads
`google.api.http` annotations off the proto descriptors at runtime and
translates REST↔Connect on the fly.

## Enabling

```yaml
# forge.yaml
api:
  rest: true       # vanguard wraps the mux; CRUD RPCs gain default REST URLs
  # openapi: true  # sibling toggle — adds OpenAPI spec generation
```

Then re-run `forge generate`. Three things happen:

1. **The server serves `serverkit.RESTHandler(mux, mounted, logger)`**
   in place of the bare mux. A request the mux has a route for — every
   Connect / gRPC / gRPC-Web procedure, and any other route on it, such
   as the login broker's `/auth/*` — goes to the mux unchanged. Any
   other request is matched against the REST routes; a match is relayed
   to the same handler as a Connect call, in process, so its interceptor
   chain (auth included) runs exactly as for a Connect request, and it
   sees the REST caller's address, TLS state and Host. If the REST
   routes cannot be built, the error is logged and the bare mux serves:
   REST is lost, Connect is not.
2. **`buf.yaml`** gains the `buf.build/googleapis/googleapis` BSR dep
   so `google/api/annotations.proto` resolves.
3. **CRUD-prefixed RPCs** in `.proto` files (`Get<Entity>`,
   `List<Entity>`, `Create<Entity>`, `Update<Entity>`,
   `Delete<Entity>`) gain default `google.api.http` annotations on
   their next plan-mode emission.

`api.rest: false` (or absent) is the default — existing projects
regenerate byte-identically. The REST handler, the
googleapis dep, and the annotations all disappear.

## CRUD Defaults

When `api.rest: true`, the plan-proto scaffolder emits these
annotations on CRUD-prefixed RPCs:

| Method                | Annotation                                                         |
| --------------------- | ------------------------------------------------------------------ |
| `Get<Entity>`         | `option (google.api.http) = { get: "/v1/<entities>/{id}" };`       |
| `List<Entity>`        | `option (google.api.http) = { get: "/v1/<entities>" };`            |
| `Create<Entity>`      | `option (google.api.http) = { post: "/v1/<entities>" body: "*" };` |
| `Update<Entity>`      | `option (google.api.http) = { patch: "/v1/<entities>/{id}" body: "*" };` |
| `Delete<Entity>`      | `option (google.api.http) = { delete: "/v1/<entities>/{id}" };`    |

Pluralisation is naïve: lowercase the entity name and append `s`. So
`Patient` → `/v1/patients`. For irregular plurals (`person` →
`people`) edit the annotation after generation — `forge generate`
only writes the `.proto` during the initial plan emission, so
hand-edits stick.

## Hand-Written Annotations

Non-CRUD RPCs are emitted without annotations. To expose one over
REST, add the annotation by hand:

```proto
import "google/api/annotations.proto";

service ReportsService {
  rpc StreamReport(StreamReportRequest) returns (stream ReportChunk) {
    option (google.api.http) = {
      get: "/v1/reports/{report_id}/stream"
    };
  }

  // POST with a request body
  rpc GenerateReport(GenerateReportRequest) returns (GenerateReportResponse) {
    option (google.api.http) = {
      post: "/v1/reports:generate"
      body: "*"
    };
  }
}
```

Vanguard understands the full
[`google.api.HttpRule`](https://github.com/googleapis/googleapis/blob/master/google/api/http.proto)
surface — path patterns, body / response_body selectors, additional
bindings, and so on.

Streaming RPCs have a REST mapping only when the streamed side is a
`google.api.HttpBody`: a client stream's request body is the whole HTTP
request body, and a server stream writes one chunk per message to the
response body. Bidirectional streams have no REST mapping. Annotating a
streaming RPC whose streamed message is anything else fails the REST
routes at startup (logged as `init vanguard REST handler`), and the
server falls back to Connect only — leave such an RPC un-annotated.

## Composition With `api.openapi`

`api.rest` and `api.openapi` are independent toggles:

| `api.rest` | `api.openapi` | What you get                                                      |
| ---------- | ------------- | ----------------------------------------------------------------- |
| `false`    | `false`       | Connect / gRPC only (default).                                    |
| `true`     | `false`       | REST + Connect + gRPC. No spec emitted.                           |
| `false`    | `true`        | Connect / gRPC only. OpenAPI spec reflects Connect URLs.          |
| `true`     | `true`        | REST + Connect + gRPC. OpenAPI spec reflects the REST URLs.       |

Turning both on is the typical "expose a REST API with a published
spec" workflow.

## Troubleshooting

- **`google/api/annotations.proto: file does not exist`** — buf.yaml
  is missing the googleapis dep. `forge generate` should add it
  automatically when `api.rest: true`; if not, run `forge project upgrade` or
  add the dep by hand:
  ```yaml
  deps:
    - buf.build/googleapis/googleapis
  ```
- **REST request returns `404`** — vanguard only routes requests
  whose path matches an annotation. Check `forge project audit` (or `buf
  build`) confirms the annotation parsed, and that the server did not log
  `init vanguard REST handler` at startup (every REST route is then off).
- **A REST path shadowed by another route** — a route registered on the
  mux wins over a REST template that matches the same path, and a `/`
  catch-all on the mux would take every REST request. Keep REST templates
  under their own prefix (`/v1/…`).
- **REST request returns `405`** — method mismatch between request
  (e.g. `POST`) and annotation (e.g. `get:`).
- **Connect / gRPC requests still work?** — yes. Vanguard is purely
  additive; the original `/services.foo.v1.FooService/Bar` JSON-over-POST
  path stays available.

## Limits

Vanguard is runtime transcoding, not a code generator. Generated REST
clients are not produced. If you want typed REST clients, use the
OpenAPI spec emitted by `api.openapi: true` with your client
generator of choice.
