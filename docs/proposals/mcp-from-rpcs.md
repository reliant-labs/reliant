# MCP from RPCs: serve an app's opted-in RPCs as MCP tools

**Status:** approved. The owner asked on 2026-11-25 whether forge should expose
an app's RPCs as an MCP server automatically, offer an easy opt-in, or not
bother. The recommendation given was **opt-in, not default**; this document
writes that design up. Nothing here is built. On 2026-11-26 the owner accepted
every recommendation of [section 9](#9-owner-decisions-decided-2026-11-26)
("take your recommendations on all"); they are the
[Settled](#settled-owner-2026-11-26) list. Slices land as their own pull
requests ([section 13](#13-rollout)).
**Date:** 2026-11-25
**Related:** [docs/mcp.md](../mcp.md) (forge's own MCP server),
control-plane#2133 ("forge APIs as tools", parked), control-plane#2531 (MCP
Apps, deferred), [start-anywhere.md](start-anywhere.md).

## Summary

A forge app's API is already a typed, documented, authenticated, traced set of
RPCs. That is exactly what an MCP server is, so forge can serve the RPCs a
developer picks as MCP tools with almost no new surface:

- **Opt-in per rpc or per service** with a new field on the existing
  `(forge.v1.method)` / `(forge.v1.service)` options: `mcp: true`. A mutating rpc
  needs a second, explicit acknowledgement, `mcp_mutating: true`, and is
  announced with MCP's `destructiveHint`. An rpc marked
  `idempotency_level = NO_SIDE_EFFECTS` is announced with `readOnlyHint`.
- **The app's own server mounts `/mcp`** (MCP streamable HTTP) beside its Connect
  routes. No second process, no second deploy target: every runtime that runs
  the API (host, compose, cluster, Reliant hosting) serves the tools.
- **Each tool call is an in-process Connect call** through the same mux and the
  same interceptor chain as a browser's call: authentication as the calling
  user (fail closed), authorization in the handler, protovalidate, deadline,
  rate limit, audit, the activity stream and the RPC's span. Nothing is
  re-implemented for MCP.
- **Tool descriptions come from the proto comments** and the input/output JSON
  Schemas from the message types plus their `buf.validate` rules, projected at
  `forge generate` time, the same way `api.openapi` projects the OpenAPI spec.
- `forge mcp app <env>` prints the URL and a config snippet for each MCP client.
  A warn-first lint lane asks every opted-in rpc for a description good enough
  for a model to choose it.

Everything is additive within v1: new optional proto fields, a new `pkg/`
library, a new zero-value-off field on `serverkit.ListenerDeps`.

## Settled (owner, 2026-11-26)

The owner accepted each recommendation of
[section 9](#9-owner-decisions-decided-2026-11-26) ("take your recommendations
on all"):

1. **Opt-in, per rpc or per service.** Nothing is exposed by default.
2. **Declaration:** `mcp` and `mcp_mutating` fields on the existing
   `(forge.v1.method)` and `(forge.v1.service)` options.
3. **Mutations are served in v1**, only with `mcp_mutating: true`, and are
   announced with `destructiveHint`.
4. **Schemas** come from the pinned `protoc-gen-connect-openapi` in
   `jsonschema` format at generate time. A forge-owned projection over the buf
   image is the fallback only if the plugin's shape fails clients.
5. **Public (`auth_required: false`) rpcs** are served only when they are also
   opted in.
6. **MCP OAuth:** tokens only first, then an OAuth façade in the login broker
   as its own slice.
7. **Per-env control:** a listener config field, default on, that an env can
   turn off.
8. **Dogfood:** control-plane's read-only `ObservabilityService` rpcs in
   dev/e2e, not published as a connector.
9. **control-plane#2133:** this proposal is its forge half (annotation,
   `/mcp`). Registration with an org tool registry and partners stay there.

These are not reopened below.

## 1. Problem and experience

Agents (Claude, ChatGPT, Cursor, Codex, an app's own workflow) increasingly
reach products through MCP. A team with a forge API that wants its app usable
from an agent today hand-writes an MCP server: re-declares every tool's schema,
re-does auth, forgets the audit trail and gets no telemetry. The proto already
holds all of that.

The experience this proposal aims for:

```proto
service OrdersService {
  // ListOrders returns the caller's orders, newest first, with cursor
  // pagination. Use status to narrow to open or shipped orders.
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
    option (forge.v1.method) = { mcp: true };
  }

  // CancelOrder cancels one open order and refunds its payment. It fails
  // with FailedPrecondition when the order has shipped.
  rpc CancelOrder(CancelOrderRequest) returns (CancelOrderResponse) {
    option (forge.v1.method) = { mcp: true, mcp_mutating: true };
  }
}
```

then `forge generate`, `forge env up dev`, and:

```
$ forge mcp app dev
orders (http://localhost:8080/mcp), 2 tools: orders_list_orders (read-only), orders_cancel_order (destructive)
Claude Code   claude mcp add --transport http orders http://localhost:8080/mcp --header "Authorization: Bearer $TOKEN"
Cursor        {"mcpServers": {"orders": {"url": "http://localhost:8080/mcp", "headers": {...}}}}
...
```

The agent sees two tools, with the comments as descriptions, schemas that say
`status` is one of `OPEN`/`SHIPPED`, and a client that asks before
`orders_cancel_order` runs. Each call shows up in `forge obs endpoints` under its
RPC, and in the activity stream as "cancelled order 42 via mcp".

## 2. Why opt-in

Auto-exposing every RPC is the tempting default, and the wrong one:

- **Tool count.** control-plane declares 469 rpcs and reliant 393
  (`grep -c '^\s*rpc '` over their `proto/`). MCP clients degrade sharply past a
  few dozen tools: selection accuracy falls, every tool's schema costs context
  on every turn, and some clients cap the count. An API's surface is shaped for
  a frontend, not for a model choosing among tools.
- **Safety.** Exposure would be implicit: an rpc added next month becomes
  callable by any agent holding a user's token, including mutations nobody
  reviewed for prompt-injected calls. The project's rule for this kind of
  decision is fail closed (`auth_required` is optional precisely so the default
  stays "auth required", `proto/forge/v1/forge.proto:298-302`).
- **Descriptions.** A tool is only as usable as its description. Most rpcs'
  comments were written for the next developer, not for a model deciding when
  to call. Opting in is the moment to write that comment, and the lint lane
  ([section 6](#6-descriptions-and-the-lint-lane)) can ask for it.

"Not bother" leaves the gap above to every team, and to control-plane#2133's
partners. An opt-in that costs one proto line per rpc keeps the default safe
and makes the useful path short.

## 3. Declaration

Two fields on each existing options message, all additive (`forge.proto`
extends `google.protobuf.MethodOptions` at 50100 and `ServiceOptions` at 50000,
`proto/forge/v1/forge.proto:91-98`; the next free field numbers are 8 on both):

```proto
message MethodOptions {
  ...
  // Serve this rpc as an MCP tool on the app's /mcp endpoint. Unset inherits
  // (forge.v1.service).mcp; false opts one rpc out of a service that opts in.
  optional bool mcp = 8;
  // Required, beside mcp, for an rpc that changes state. Without it a mutating
  // rpc is never served, whatever mcp says.
  bool mcp_mutating = 9;
}

message ServiceOptions {
  ...
  // Default for every unary rpc in this service (each still needs
  // mcp_mutating if it mutates).
  optional bool mcp = 8;
}
```

**Which rpcs mutate** is decided by the function forge already uses for the
activity stream: `activity.IsMutating` (`pkg/activity/resolve.go:86-100`), which
treats an rpc as a read when it declares protobuf's
`idempotency_level = NO_SIDE_EFFECTS` or its name starts with a read verb. One
definition, so the MCP surface, `forgeconv-activity-declared`
(`internal/linter/forgeconv/activity_declared.go:16-24`) and the frontend hook
classifier cannot disagree about an rpc.

**Tool annotations** (MCP `ToolAnnotations`, go-sdk `mcp/protocol.go:1960-1988`):

| rpc                                 | `readOnlyHint` | `destructiveHint` | `idempotentHint`                                                   |
| ----------------------------------- | -------------- | ----------------- | ------------------------------------------------------------------ |
| declares `NO_SIDE_EFFECTS`          | true           | n/a               | n/a                                                                |
| read-named, no `NO_SIDE_EFFECTS`    | false          | false             | `(forge.v1.method).idempotent`                                     |
| mutating, with `mcp_mutating: true` | false          | true              | `(forge.v1.method).idempotent` or `idempotency_level = IDEMPOTENT` |
| mutating, without `mcp_mutating`    | not served     |                   |                                                                    |

`readOnlyHint` needs the explicit protobuf marker, not just a read verb:
clients skip confirmation on it, so it must be a declaration, and
`forge lint --idempotency` already asks every read-named rpc to declare
`NO_SIDE_EFFECTS` (`internal/cli/annotations.go:323`). Every served mutation is
`destructiveHint: true` in v1; a finer action class (control-plane#2133's
read / reversible write / outward / money) can be a later additive field.

**Out of v1:** streaming rpcs (MCP tools are request/response; `forge generate`
refuses `mcp` on a streaming rpc with a message), MCP resources and prompts.

**Tool names** are `<service>_<rpc>` in snake case with the `Service` suffix
dropped (`orders_list_orders`), at most 64 characters of `[a-z0-9_]`, the
strictest shape current clients accept. Two rpcs that project to one name fail
`forge generate`.

## 4. Runtime: `/mcp` on the existing listener

### The bridge forge already has

`api.rest` serves REST URLs without a second server: vanguard decodes the
request and relays it as a Connect call over an in-process transport into the
same mux, "answered by the very handler — interceptor chain, auth and all —
that answers the Connect URL" (`pkg/serverkit/rest.go:3-12`, built at
`rest.go:59-75`, the transport at `rest.go:103`). MCP gets the same shape.
Vanguard itself cannot be reused, because MCP is JSON-RPC rather than REST, but
its bridge can be shared.

### Pieces

1. **Generated tool table.** `forge generate` writes `pkg/middleware/mcp_tools_gen.go`
   beside `procedures_gen.go`: one entry per served rpc with its name, Connect
   procedure constant, description, input and output schema, and annotations.
   Entries use connect's generated procedure constants for the same reason
   `UnauthenticatedProcedures` does: a renamed rpc breaks the build instead of
   leaving a dead string (`internal/templates/project/middleware-procedures.go.tmpl:35-37`).
2. **`pkg/mcpkit`** (new, compiled into the app): an `http.Handler` over the
   official go-sdk (`github.com/modelcontextprotocol/go-sdk v1.8.0`, already in
   `go.mod:39`) that answers `initialize`, `tools/list` and `tools/call`. A call
   decodes the arguments as the request's protojson, POSTs them as a Connect
   unary JSON request to the procedure over the in-process transport, and
   returns the response as `structuredContent`, plus its JSON as text for
   clients that predate structured output. The caller's `Authorization` header
   (or the cookie or header the project's `authn` policy reads) and
   `Forge-Via: mcp` are copied onto the inner request.
3. **Listener wiring.** `serverkit.ListenerDeps` gains `MCP *mcpkit.Config`
   beside `REST bool` (`pkg/serverkit/listener.go:202-204`), nil meaning off. The
   scaffolded API listener (`internal/templates/project/providers-graph.go.tmpl:412-446`)
   sets it whenever the generated table is non-empty. That is the only switch:
   the proto declarations are the declaration, with no `forge.yaml` toggle to
   drift from them (per-env control is decision 7).
4. **Transport.** Streamable HTTP in the SDK's stateless mode
   (`StreamableHTTPOptions.Stateless`, go-sdk `mcp/streamable.go:146`). No
   session state, so any replica answers any request and nothing needs
   affinity; v1 sends no server-to-client notifications. `Origin` is checked
   against the listener's CORS allowlist (the DNS-rebinding defence MCP
   requires), and the listener's payload caps and deadlines apply.

One listener serves one audience (`(forge.v1.service).audience`,
`proto/forge/v1/forge.proto:259-266`, checked at `listener.go:326`), so `/mcp` on the
user-audience listener lists only that listener's services. A daemon-audience
listener would serve its own `/mcp` with its own authenticator.

### Auth

The chain is the authority, so MCP adds no second validator:

- **Each tool call authenticates as the calling user** through the chain's
  `Auth` slot, exactly like the same request from the frontend
  (`pkg/serverkit/chain.go:5-11`). The handler's own access checks and row
  scoping (`middleware.GetUser`) apply unchanged.
- **The `/mcp` endpoint fails closed.** A request with no credential gets
  HTTP 401 with `WWW-Authenticate` naming the RFC 9728 metadata (go-sdk
  `auth.RequireBearerToken`, `auth/auth.go:97`), which is what starts an MCP
  client's sign-in. A present but invalid credential is judged by the chain:
  `mcpkit` sends it through an internal no-op procedure mounted with the chain
  (as the health rpc is, `listener.go:300-307`), so the project's
  `Authenticator` decides, whether forge's `authn` or the project's own.
- **Public rpcs.** An `auth_required: false` rpc is served only when it also
  opts in. When every served tool is public, `/mcp` answers without a
  credential; when tools are mixed, a caller without one sees only the public
  tools in `tools/list`.
- **Audit and activity** record the real caller with `via: mcp`
  (`pkg/activity/interceptor.go:28`; the hosted connector already names itself
  `mcp`, `internal/cli/mcp_hosted.go:55`).

### Errors

Handlers already return `svcerr.Wrap(err)` (`pkg/svcerr/svcerr.go:444`), which
maps service errors to Connect codes and keeps internals off the wire. `mcpkit`
returns a Connect error as a tool result with `isError: true` and the text
`<code>: <message>`, plus protovalidate's field violations for
`InvalidArgument`, so the model can correct its arguments. Only protocol
faults (an unknown tool, arguments that are not JSON) are JSON-RPC errors.

### Telemetry

The chain's trace slot already makes each in-process call the RPC's own span
with its metrics, so the call appears in `forge obs endpoints` and traces under
the RPC with no new instrumentation. `mcpkit` adds one parent span per MCP
request carrying the OpenTelemetry MCP attributes (`mcp.method.name`, the tool
name), so a trace shows the agent's call and the RPC it became.

### Rate limits

Two layers, each with a different job:

- **Per call:** the chain's `RateLimit` slot (`pkg/serverkit/chain.go:229-233`)
  applies to tool calls as to any call, keyed by caller.
- **Per MCP session or credential:** `mcpkit` bounds requests per minute,
  burst and concurrent calls, to stop a model that retries a refusal forever or
  is talked into a loop. Defaults follow what reliant's connector found
  workable (120/min, burst 30, 4 in flight; `reliant/internal/mcpserver/limits.go:15-34`),
  overridable in `mcpkit.Config`. A limited call returns `ResourceExhausted`.

## 5. Schemas

MCP requires an `inputSchema` (a JSON Schema object) per tool and allows an
`outputSchema`. Both must describe the **protojson** form the Connect JSON
codec speaks: 64-bit integers as strings, enums as names, `Timestamp` as
RFC 3339, `Duration` as `"1.5s"`, oneofs, maps and well-known types. Each
message and field description comes from its proto comment, and the
`buf.validate` rules become JSON Schema keywords (`required`, `minLength`,
`pattern`, `enum`, `minimum`). The schema is advisory: the chain's validate
slot (`chain.go:337`) still enforces every rule, including CEL rules a schema
cannot express (those are appended to the field description).

**The projection must run at generate time.** Comments live in the descriptor's
`SourceCodeInfo`, which `buf` has during `forge generate` and protoc-gen-go
strips from the descriptors it embeds. A runtime projection from
`protoregistry` would produce schemas with no descriptions.

**Sharing with `api.openapi`.** The OpenAPI emitter runs the pinned
`protoc-gen-connect-openapi` v0.30.0 as a buf step
(`internal/cli/generate_openapi.go:18-29`). That release already renders a
standalone JSON Schema (draft 2020-12) document (`format=jsonschema`, its
`internal/converter/options/options.go:28-31`) with protovalidate rules
(`internal/converter/protovalidate/`) and protojson naming. So the recommended
projection is the same pinned plugin in `jsonschema` format, written under the
ignored `gen/` tree and embedded into the tool table: one projection for both
the OpenAPI spec and the tool schemas, already provisioned by forge's tool
cache, with no new dependency. `api.openapi` stays an independent toggle; MCP
runs the step whether or not it is on.

The fallback, if the plugin's shape proves wrong for tools (for example, `$ref`
chains some clients do not resolve, which would need inlining), is a
forge-owned projection over the buf image using `github.com/google/jsonschema-go`
(already in `go.mod:33`). That is decision 4.

## 6. Descriptions and the lint lane

A model picks a tool from its name and description alone. New `forgeconv`
rules, each warn-first through the rollout table
(`internal/cli/lint/lint_rollout_table.go`; `IntroducedIn: v1.12.0`, the current
`NEXT_VERSION`, gating from v1.13.0):

| Rule                      | Finds                                                                                                                                                              |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `forgeconv-mcp-described` | an opted-in rpc with no leading comment, a comment under ~8 words, or one that only restates the name ("ListOrders lists orders"); request fields with no comment. |
| `forgeconv-mcp-mutating`  | an rpc with `mcp: true` that mutates and lacks `mcp_mutating: true` (it is not served; this says why), or `mcp_mutating` on an rpc that does not mutate.           |
| `forgeconv-mcp-shape`     | `mcp` on a streaming rpc; two rpcs projecting to one tool name; more than 40 tools on one listener (a warning only, with the tool-count guidance below).           |

`forge generate` refuses only what cannot be served (a streaming rpc, a name
collision). Everything about quality is lint, so a project pinning the release
sees warnings first, as the project rule for new lint requires.

**Tool-count guidance**, in the new `api-mcp` skill: serve the 5 to 30
operations an agent needs, not the API. Prefer coarse, task-shaped rpcs
(`SummarizeAccount`) over a CRUD quintet per entity; keep list rpcs paginated
(tool results are bounded); write the comment as when-to-call guidance with the
error cases. More than one tool group per app (`/mcp/<group>`) is a later
additive option, not v1.

## 7. `forge mcp app <env>`

A subcommand of the existing `forge mcp` (`internal/cli/mcp.go:66-123`, whose
bare form keeps serving forge's own tools on stdio). It reads the env's
resolved API URL (the same resolution `forge env status` uses), lists the
served tools from the generated table, and prints a ready snippet for Claude
Code, Codex, Cursor, VS Code and Claude/ChatGPT connectors, with the auth that
env supports: OAuth when the app advertises an authorization server
([section 8](#8-oauth-and-tokens)), otherwise a header with a token and the
command that mints one. `--json` returns the same as data, and `forge env up`
prints the dev `/mcp` URL among its other URLs.

## 8. OAuth and tokens

MCP's authorization spec makes the server an OAuth 2.1 **protected resource**:
it publishes RFC 9728 metadata naming its authorization server and accepts only
tokens issued for it (RFC 8707 audience). The authorization server publishes
RFC 8414 metadata and lets a client it has never seen obtain a client id, by
dynamic client registration (RFC 7591) or a client ID metadata document
(newer spec revisions). Clients such as Claude and ChatGPT connectors support
only that flow; Claude Code, Cursor, Codex and VS Code also accept a static
header.

The protected-resource half is small and belongs in `mcpkit` (go-sdk
`auth.ProtectedResourceMetadataHandler`, `auth/auth.go:188`). reliant's MCP
server made the same split and records why the authorization server is a
separate job (`reliant/internal/mcpserver/oauth.go:12-37`).

The authorization-server half is the question. Managed identity converges a
**fixed, declared** set of OIDC applications into the app's Zitadel instance
(`pkg/identity/doc.go:1-8`), so an MCP client nobody declared has no client id.
The options:

- **(a) Tokens only, in v1.** The bearer the app already accepts: a session
  token, an `apikey` key (`pkg/apikey`), or a token from the app's own flow,
  pasted into the client's header config. Works today for coding agents, not
  for connector-style clients.
- **(b) An MCP authorization-server façade in the login broker.** The app's
  broker (mounted by the API listener, `providers-graph.go.tmpl:424-438`)
  serves RFC 8414 metadata, registration (public clients, PKCE S256 only,
  registration rate-limited per IP), `/authorize` that signs the person in
  through managed identity, and `/token` issuing a token bound to the app's
  `/mcp` resource with a narrow scope. control-plane already does exactly this
  for reliant's connector (`control-plane/internal/cliauth/mcp.go:25-56`:
  public clients, one resource per token, one scope, 30 days, no refresh;
  metadata at `cliauth/handler.go:127-167`), which is the precedent to lift
  into a forge library. It works everywhere managed identity runs, dev
  included.
- **(c) The control plane as a shared authorization server** for every hosted
  app. Less code per app, but it ties app auth to Reliant hosting and leaves
  self-hosted and dev apps without it.
- **(d) Wait for Zitadel** to offer registration natively, and point the
  metadata at it.

Decided (owner, 2026-11-26, decision 6): (a) in the first runtime slice, (b) as its own slice, both
behind the same `mcpkit` resource metadata so a client needs no change when
OAuth arrives. A bring-your-own-issuer project (Auth0, Okta) points the
metadata at its issuer and uses that issuer's registration support.

## 9. Owner decisions (decided 2026-11-26)

**All settled (owner, 2026-11-26): each row is decided on its recommended
option** ("take your recommendations on all"), recorded as the
[Settled](#settled-owner-2026-11-26) list. The table keeps the options that
were weighed, as the record of why. Reopening a row is the owner's call.

| #   | Decision                             | Options                                                                                                                                                                                                             | Decided (owner, 2026-11-26)                                                                                                                                                                                  | Blocked                                    |
| --- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------ |
| 1   | Default exposure                     | (a) opt-in per rpc/service; (b) auto-expose every rpc; (c) don't build it.                                                                                                                                          | (a), as recommended to the owner on 2026-11-25, and confirmed.                                                                                                                                               | Everything.                                |
| 2   | Declaration shape                    | (a) `mcp` / `mcp_mutating` fields on the existing `(forge.v1.method)` and `(forge.v1.service)`; (b) a nested `mcp: { mutating: … }` message; (c) a new `(forge.v1.tool)` extension, as control-plane#2133 sketched. | (a): one proto line, beside `auth_required` and `activity` where reviewers already look. (b) stays possible for later fields (an action class).                                                              | Proto PR, codegen, lint.                   |
| 3   | Mutations in v1                      | (a) served with `mcp_mutating: true` and `destructiveHint`; (b) read-only tools only in v1.                                                                                                                         | (a). Agents that can only read are half the value; the explicit flag plus client confirmation is the guard, and the handler's own authorization still applies.                                               | `mcpkit` call path, lint rule.             |
| 4   | Schema projection                    | (a) the pinned `protoc-gen-connect-openapi` in `jsonschema` format at generate time; (b) a forge-owned projection over the buf image; (c) runtime from `protoregistry`.                                             | (a), falling back to (b) only if the plugin's shape fails clients. (c) has no comments, so no descriptions.                                                                                                  | Generated tool table.                      |
| 5   | Public (`auth_required: false`) rpcs | (a) served only when also opted in; (b) never served over MCP in v1.                                                                                                                                                | (a): opting in is the same explicit act, and a public rpc is already public over Connect.                                                                                                                    | `/mcp` auth path.                          |
| 6   | MCP OAuth                            | [Section 8](#8-oauth-and-tokens): (a) tokens only; (b) façade in the login broker; (c) control-plane-hosted; (d) wait for Zitadel.                                                                                  | (a) first, then (b) as its own slice.                                                                                                                                                                        | Connector-style clients (Claude, ChatGPT). |
| 7   | Per-env control                      | (a) on wherever the listener serves tools; (b) a listener config field (default on) an env can turn off; (c) off unless an env enables it.                                                                          | (b): the declaration stays in the proto, and prod can be kept off while a team tries it in dev.                                                                                                              | Template wiring, KCL field.                |
| 8   | Dogfood target                       | (a) control-plane's read-only `ObservabilityService` rpcs in dev/e2e; (b) Growth's API (control-plane#2133's first adopter); (c) a reliant service.                                                                 | (a): ~10 `NO_SIDE_EFFECTS` rpcs with good comments (`control-plane/proto/services/observability/v1/observability.proto:48-75`). Not published as a connector: the hosted forge connector already serves obs. | Slice 5.                                   |
| 9   | Relation to control-plane#2133       | (a) this proposal is #2133's forge half (annotation, `/mcp`); registration with an org tool registry and partners stay there; (b) keep #2133 as the design of record.                                               | (a). Nothing here blocks the registry: `forge env deploy` can register the generated `/mcp` later.                                                                                                           | Ticket bookkeeping only.                   |

## 10. Relation to existing work

- **`forge mcp`** (`internal/cli/mcp.go:66-122`) is forge's own tools over a
  repo, on stdio; the **hosted "Reliant Forge" connector**
  (`internal/cli/mcp_hosted.go:1-30`, `forge/cli.AddHostedTools`) is the
  account's monitoring and releases. Both are forge talking about the app. This
  proposal is the app serving its own domain, under the app's own name and
  identity. They share nothing at runtime (the CLI is `internal/`), only the
  go-sdk and naming conventions; `forge mcp app` is the one place the CLI
  touches it.
- **control-plane#2133** proposed `option (forge.tool) = { class: WRITE }`,
  `forge generate` mounting `/mcp` with the app's auth, rate limits and
  observability, and deploy-time registration. This is that forge piece, with
  the action class reduced to read versus mutating for v1.
- **control-plane#2531** (MCP Apps) is orthogonal: `_meta.ui` can be added to
  generated tools later.

## 11. Alternatives considered

| Alternative                                  | Why not                                                                                                                                                                                                                     |
| -------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Auto-expose every rpc                        | Hundreds of tools (469 in control-plane), mutations exposed without review, rpcs added later become agent-callable silently, and most comments are not model-grade. [Section 2](#2-why-opt-in).                             |
| A separate generated MCP server binary       | A second workload to build, deploy and scale on every runtime, a second auth path and its own telemetry. The in-process bridge gets the chain for free, as `api.rest` does.                                                 |
| An OpenAPI-to-MCP bridge (generic proxy)     | Needs `api.openapi` and a proxy process, forwards credentials across a hop, and cannot read `NO_SIDE_EFFECTS`, mutation status or the activity declaration. Still the right answer for non-forge apps (control-plane#2133). |
| One generic `call_rpc(procedure, json)` tool | Few tools, but no per-tool schema, description or annotation, so clients cannot ask before a destructive call and models guess arguments.                                                                                   |
| Do nothing                                   | Coding agents can already call the Connect JSON API with a shell; connector-style clients cannot, and every team rebuilds the same server by hand without audit or telemetry.                                               |

## 12. Semver

All additive within v1: optional fields with new numbers on `forge.v1`
options; a new `pkg/mcpkit`; `serverkit.ListenerDeps.MCP` whose zero value is
off; new generated file; new `forge mcp app` subcommand (bare `forge mcp`
unchanged); new lint rules, warn-first. A project that opts nothing in
generates byte-identical output except the new options in the descriptor. The
generated tool names and schemas become a public contract for the app's users
once served, so the `api-mcp` skill says to treat a tool rename as an rpc
rename (`proto-breaking`).

## 13. Rollout

Each slice ships alone.

1. **Declaration.** The proto fields, `forge project annotations` entries, the
   three lint rules with rollout rows, `forge generate` refusals. No runtime.
2. **Projection.** The jsonschema step and `mcp_tools_gen.go`, with golden
   tests over protojson edge cases (int64, enums, oneofs, maps, WKTs,
   `buf.validate` rules).
3. **Runtime.** `pkg/mcpkit` (stateless streamable HTTP, in-process dispatch
   sharing the REST bridge's transport, error mapping, annotations, limits, the
   parent span, `Forge-Via: mcp`), `ListenerDeps.MCP`, template wiring, token
   auth, RFC 9728 metadata for a configured issuer. Must pass `pkgguard`,
   `deadcodeguard` and the `apigate`.
4. **Developer path.** `forge mcp app <env>`, the `/mcp` URL in `forge env up`,
   the `api-mcp` skill, a benefits row (`status: in-progress` until slice 5).
5. **Dogfood** (decision 8): opt in control-plane's read-only obs rpcs in dev,
   drive them from Claude Code and Reliant, compare against the hosted
   connector's hand-written `obs_*` tools for description quality and tool
   choice.
6. **OAuth façade** (decision 6 (b)): the broker as an authorization server,
   lifted from control-plane's `cliauth` MCP flow into a forge library.
7. **Later, not scheduled:** tool groups, an action class field, MCP Apps
   (control-plane#2531), registration with an org tool registry on deploy
   (control-plane#2133).
