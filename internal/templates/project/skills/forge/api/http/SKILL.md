---
name: api-http
description: Hitting Connect RPC handlers with plain HTTP+JSON — URL shape, Content-Type rules, streaming caveats, and the `forge api curl` helper for building copy-pasteable curl invocations.
---

# Plain HTTP + JSON against Connect RPCs

Forge generates Connect RPC handlers. Connect already speaks **plain HTTP/1.1 POST with `Content-Type: application/json`** for unary RPCs — no gRPC tooling required. `grpcurl`, `protoc`, and the connect-go client are convenient but optional. Any HTTP client (curl, HTTPie, Postman, the `fetch` in your browser console, a Lambda function) can call a forge backend directly.

## URL shape

```
POST /<fully.qualified.proto.package>.<ServiceName>/<MethodName>
```

The fully qualified service name is `<package>.<Service>`. Take it from your `.proto` file: the `package` directive plus the `service` block name.

```proto
// proto/services/users/v1/users.proto
package services.users.v1;

service UserService {
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
}
```

→ POST endpoint: `/services.users.v1.UserService/GetUser`

The leading slash is part of the URL. There is **no** `/api/`, `/v1/`, or any other prefix — Connect serves directly off the package + service path.

## Required headers

| RPC kind | Content-Type | Notes |
|----------|--------------|-------|
| Unary | `application/json` | The default — what `curl -X POST -H 'Content-Type: application/json'` gives you. |
| Server-streaming | `application/connect+json` | Frames are length-prefixed; curl will only show the first frame. |
| Client-streaming | `application/connect+json` | Same framing; rarely useful from curl. |
| Bidi-streaming | `application/connect+json` | Same framing. |

If you forget `Content-Type` entirely, the server responds with `415 Unsupported Media Type`. The body must be the JSON representation of the request message — not the response, not a query string.

## Request body shape

ProtoJSON: a JSON object with one key per proto field. Connect accepts both `snake_case` and `camelCase`; forge's proto definitions use `snake_case`, which is what the `forge api curl` skeleton emits.

```json
{
  "id": "abc123",
  "include_deleted": false
}
```

Empty input messages (`google.protobuf.Empty`) require an empty body: `{}`. Methods that take only optional fields can also send `{}` — fields default to their proto zero values.

## Curl example (unary)

```bash
curl -X POST \
  -H 'Content-Type: application/json' \
  -d '{"id":"abc123"}' \
  http://localhost:8080/services.users.v1.UserService/GetUser
```

Response (success):

```json
{"user":{"id":"abc123","name":"Alice"}}
```

Response (error — Connect maps RPC error codes to HTTP status):

```http
HTTP/1.1 404 Not Found
Content-Type: application/json

{"code":"not_found","message":"user not found"}
```

Common error codes: `invalid_argument` (400), `unauthenticated` (401), `permission_denied` (403), `not_found` (404), `already_exists` (409), `internal` (500). See `<https://connectrpc.com/docs/protocol/#error-codes>` for the full table.

## Auth headers

Every rpc requires a credential unless it declares `auth_required: false`. Pass a bearer token, or an API key once the project enables keys (`auth/api-keys`):

```bash
# JWT
curl -X POST -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <token>' \
  -d '{}' \
  http://localhost:8080/services.users.v1.UserService/ListUsers

# API key
curl -X POST -H 'Content-Type: application/json' \
  -H 'X-API-Key: <key>' \
  -d '{}' \
  http://localhost:8080/services.users.v1.UserService/ListUsers
```

Methods that declare `(forge.v1.method).auth_required = false` skip the auth interceptor and accept anonymous calls. The `auth` skill has the full table.

## Streaming RPCs

Server- and client-streaming RPCs use Content-Type `application/connect+json`. Each frame is wire-prefixed with a 5-byte header (1-byte flags + 4-byte length). curl can send the first frame but does not parse subsequent ones — for streaming work, reach for `grpcurl`, the connect-go client, or a small Go script using `connect.NewClient`.

Quick check that a streaming endpoint is reachable and accepts your input:

```bash
curl -X POST \
  -H 'Content-Type: application/connect+json' \
  -d '{"query":"all"}' \
  http://localhost:8080/events.v1.EventService/Subscribe
```

You will see the first response frame plus a connection that stays open until the server closes it.

## The `forge api curl` helper

`forge api curl <service.method>` builds a copy-pasteable curl command for you — no remembering the URL shape, no looking up the request message fields:

```bash
forge api curl services.users.v1.UserService.GetUser
# →
# curl -X POST \
#   -H 'Content-Type: application/json' \
#   -d '{"id":""}' \
#   http://localhost:8080/services.users.v1.UserService/GetUser
```

The body is a zero-value skeleton populated from the proto definition's fields, in declaration order. Edit it before sending.

Flags:

| Flag | Effect |
|------|--------|
| `--port <n>` | Port to hit; defaults to 8080 (e.g. when running behind a port-forward). |
| `--body '{...}'` | Skip the skeleton; use this JSON body verbatim. |
| `--host <name>` | Override the host (default: `localhost`). |

Short form works when the service name is unique across all proto packages:

```bash
forge api curl UserService.GetUser
```

The helper reads `gen/forge_descriptor.json` for the proto data — run `forge generate` first if you've just added an RPC.

## Common pitfalls

1. **Missing the leading slash** — the URL path *is* `/<pkg>.<Service>/<Method>`, not `<pkg>.<Service>/<Method>`. Without the slash you'll get a 404.
2. **Wrong Content-Type for streaming** — `application/json` only works for unary RPCs. Streaming returns `415` if you forget `+json`.
3. **Forgetting the body for empty-input methods** — Connect requires `{}` even for `google.protobuf.Empty`. An empty `-d ''` produces a parse error.
4. **Confusing the URL with the proto file path** — the URL is built from the `package` directive (`/services.users.v1.UserService/GetUser`), not the file path: the `proto/` root and the file name (`users.proto`) never appear in it.
5. **Hand-rolling the URL when `forge api curl` exists** — let the helper read the descriptor; it will catch service/method renames at the point of use.
