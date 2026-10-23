---
name: activity
description: The typed activity stream (forge/pkg/activity). How to declare on an rpc which activity event it records (verb, target, env), how the one interceptor writes it, how to opt a mutation out, how to write transactionally or from a non-RPC actor, and how to store and list activity_events.
---

# Activity stream

"sean promoted release v1.7.44 in prod via cli" is one **activity event**: an
actor, a verb, a target, an env and a via. In forge you **declare** the event on
the rpc. One interceptor writes it, and no handler writes it by hand.

This is product history: the feed a user browses and that every object's
History panel links to. The audit log (`observability/audit-log`) is
different. It is the raw security trail of every call, failures included.
Keep both.

## Declare it on the rpc

```protobuf
rpc PromoteRelease(PromoteReleaseRequest) returns (PromoteReleaseResponse) {
  option (forge.v1.method) = {
    activity: {
      verb: "promote"
      target: { kind: "release", id: "release_id", label: "response.release.version" }
      env: "to_env"
      detail: ["from_env"]
    }
  };
}
```

| field | meaning |
|---|---|
| `verb` | A lower_snake action token (`promote`, `rotate`). It is stored as written; your UI phrases it as "promoted". |
| `target.kind` | A lower_snake literal (`release`, `secret`). |
| `target.id` | Required. The field path to the target's id. |
| `target.label` | The field path to a display label. If unset, the label is the id. |
| `env` | The field path to the environment. If unset, the event is project-level. |
| `scope` | The field path to the scope (an org or a project) that reads filter by. If unset, the scope is the caller's org. |
| `detail` | Field paths copied into the event's `detail` map, keyed by each path's last segment. |
| `writer` | `ACTIVITY_WRITER_HANDLER` makes the write transactional (see below). |
| `none` | `activity: { none: "<why>" }` means the rpc changes nothing worth an event. It cannot be combined with any other field. |

**Field paths** are dotted proto field names. They resolve against the
request. A path that starts with `response.` resolves against the response,
which is where a created object's id is. A path must end on a scalar or an
enum.

**CRUD needs nothing.** The following rpcs record activity without any option,
as long as their messages have forge's CRUD shape:

- `Create<E>`: `create` on `snake(E)`. The id is `response.<e>.id`.
- `Update<E>`: `update` on `snake(E)`. The id is `<e>.id`.
- `Delete<E>`: `delete` on `snake(E)`. The id is `id`.

The label is `name`, `title` or `display_name` when the entity has one. An
explicit option overrides the implied one, including `none`.

## Which rpcs must declare

Every **mutating** rpc must declare. An rpc counts as mutating unless:

- its name starts with a read verb: `Get`, `List`, `Watch`, `Search`, `Query`,
  `Count`, `Describe` or `BatchGet`; or
- it carries protobuf's read-only marker, `option idempotency_level = NO_SIDE_EFFECTS;`.

`forge lint` (the conventions lane, rule `forgeconv-activity-declared`) fails
a mutating rpc that declares nothing, and it fails any declaration whose paths
do not resolve. It reads the verdict `forge generate` records in
`gen/forge_descriptor.json`, so run `forge generate` first. A read rpc may
still declare an event. For example, revealing a secret can be a `reveal`.

`activity.Resolve` and `activity.IsMutating` are the only code that interprets
these rules. The interceptor and the lint both call them.

## Wire the interceptor

`serverkit.NewChain` already fills its `Activity` slot. By default, declared
events are **logged** (`activity.event`, `log_type=activity`), not stored. To
store them, give the slot a store:

```go
import "github.com/reliant-labs/forge/pkg/activity"

store := activity.NewStore(infra.ORM) // *orm.Client: writes join a RunTx
ic, err := activity.Interceptor(activity.Deps{
    Writer:    store,
    Principal: activity.ClaimsPrincipal(authn.ClaimsFromContext),
})
if err != nil {
    return err
}
chain, err := serverkit.NewChain(serverkit.ChainDeps{
    // ...Auth and the rest as scaffolded...
    Activity: ic,
})
```

### Who the caller is: the `Principal`

Every event names an actor and a scope, and only your app's auth knows them.
`activity.Deps.Principal` is that seam, a two-method interface:

```go
type Principal interface {
    Actor(ctx context.Context) activity.Actor  // who is calling
    Scope(ctx context.Context) string          // the org/project the call acts in
}
```

It is **required**: `activity.Interceptor` fails without one. A guessed
default fails silently. An app whose auth does not put forge's claims on the
context would record every event as `system:anonymous` with an empty scope,
and nothing would say so.

- **On `forge/pkg/authn`** (the scaffolded auth):
  `activity.ClaimsPrincipal(authn.ClaimsFromContext)`. The actor is a user
  named by email, or `system:anonymous` for an rpc the policy lets through
  without a token. The scope is the claims' `OrgID`.
- **Auth that keeps the caller its own way, or principals that are machines**
  (CI tokens, API keys): implement `Principal` over your auth context. This is
  control-plane's `activitystream`. A machine credential acting for a person is
  that person, a credential acting for nobody is `activity.Machine`, and the
  scope is the caller's org.

`serverkit.NewChain` fills the `Principal` of its **default** slot from
`ChainDeps.Auth`. `serverkit.AuthFor(audience, interceptor)` recognises
forge's authn interceptor and reads the claims it stashes. Any other
interceptor leaves the authenticator's `Caller()` nil, and **`NewChain` then
refuses to build** rather than default `Activity` (or `Audit`) to an anonymous
caller. The error names the three ways out:

- `serverkit.AuthWithCaller(audience, interceptor, lookup)`, where `lookup`
  returns your caller as `*authn.Claims`. This keeps both defaults.
- `Activity:` the interceptor `activity.Interceptor(activity.Deps{Principal:
  yourPrincipal})` returns. This is the right choice when actors include
  machines.
- `serverkit.NoActivity` (and `serverkit.NoAudit`) for a chain whose rpcs
  record nothing.

The other `activity.Deps` fields default:

- `ViaFrom`: the `Forge-Via` request header (`console`, `cli`, `ci`), or `api`
  when the header is absent. A browser client that sends this header needs it
  in the CORS allow-list.
- `Writer`: the log writer. `activity.NewStore` persists.

`serverkit.NoActivity` drops the slot.

## When the write happens

**By default, the write comes after success and is best-effort.** The
interceptor writes once the handler has returned successfully, in a separate
statement. If the write fails, it is logged and counted
(`forge_activity_write_failures_total`), and the rpc still succeeds, because its
mutation has already committed. A failed call writes nothing.

The worst case is a missing row after a crash between the commit and the
insert. The interceptor never writes a row for a mutation that did not happen.

**When the event must commit with the mutation**, declare
`writer: ACTIVITY_WRITER_HANDLER` and record the event inside the handler's
transaction:

```go
err := s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
    v, err := rotate(ctx, s.deps.DB, req.Msg.GetSecretId())
    if err != nil {
        return err
    }
    // Verb, kind, actor, via, scope and the request paths come from the
    // declaration. Supply only what the handler alone knows.
    return activity.Record(ctx, s.deps.Activity, activity.Event{
        Target: activity.Target{Label: fmt.Sprintf("%s (v%d)", v.Name, v.Version)},
    })
})
```

The store writes through `*orm.Client`, so the insert joins the transaction and
rolls back with it. If such a handler succeeds without recording, the
interceptor writes the event itself and logs a warning. A streaming rpc must
use `ACTIVITY_WRITER_HANDLER` or `none`.

## When a call changes nothing

A declaration says what an rpc records when it changes something. An
idempotent "ensure" rpc that found its object already there, a dry run or a
no-op update calls `activity.Skip(ctx)`, and the interceptor writes no event for
that call.

## Actors that are not rpcs

A converger, an alert transition, or a CI ingestion job writes with the same
function. The event must be complete, and `Record` rejects one that is missing
the actor, verb, target kind or target id:

```go
activity.Record(ctx, store, activity.Event{
    Actor:  activity.System("converger"),
    Verb:   "observe",
    Target: activity.Target{Kind: "deploy_tree", ID: "acme/prod"},
    Env:    "prod",
    Via:    "converger",
    Detail: map[string]string{"state": "converged"},
})
```

## Store and list

Every migration is yours, and this table is no exception. Its DDL is
`activity.SchemaSQL`. Paste that into a migration, then run `forge generate`:

```bash
forge db migration new create_activity_events
```

`activity_events` holds one row per event: id, occurred_at, scope, the actor
(id, name, kind), verb, the target (kind, id, label), env, via, procedure,
`detail` (jsonb) and trace_id.

The read rpc is yours too. Who may read a scope is access control, and access
control is handler logic. Here is the canonical shape:

```protobuf
rpc ListActivity(ListActivityRequest) returns (ListActivityResponse);

message ListActivityRequest {
  string scope = 1 [(buf.validate.field).required = true];
  optional string actor_id = 2;
  optional string verb = 3;
  optional string target_kind = 4;
  optional string target_id = 5;
  optional string env = 6;
  optional string via = 7;
  google.protobuf.Timestamp since = 8;
  google.protobuf.Timestamp until = 9;
  int32 page_size = 10;
  string page_token = 11;
}
```

The handler checks that the caller may read `scope`, then delegates:

```go
page, err := s.deps.Activity.List(ctx, activity.Filter{
    Scope: req.Msg.GetScope(), Verb: req.Msg.GetVerb(), TargetKind: req.Msg.GetTargetKind(),
    TargetID: req.Msg.GetTargetId(), PageSize: int(req.Msg.GetPageSize()), PageToken: req.Msg.GetPageToken(),
})
```

**Scopes are paths.** Scope an event `org_1` for something org-wide and
`org_1/billing` for something in project billing (`activity.ScopeSeparator`).
`Filter.IncludeAncestors` then reads a project's feed together with its org's
events, and `Filter.IncludeDescendants` reads an org's feed together with every
project's. `Filter.Envs` matches any of several environments.

`List` returns the newest events first and pages by keyset, so a page never
repeats or skips a row while new events arrive. `Filter.Scope` is matched
exactly, including the empty scope, so always set it.
