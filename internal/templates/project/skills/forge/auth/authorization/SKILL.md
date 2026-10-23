---
name: authorization
description: Turning an IdP identity into an application principal — the enrichClaims DB-backed seam and its rejection codes, why request validation belongs to protovalidate instead, app-owned provisioning via a public Register RPC, and declaring an owner column so forge fails a build that ships unscoped.
---

# From identity to principal

The token says who the caller is; your database says what they are. `enrichClaims(ctx, claims)` in `pkg/middleware/middleware.go` runs AFTER validation and BEFORE any handler sees the claims — the one chokepoint where an IdP identity becomes an application principal. Hydrate by `claims.Subject`/`UserID`, the stable IdP identifier, never by email (users change it, and two IdPs can assert the same one):

```go
func enrichClaims(ctx context.Context, claims *Claims) (*Claims, error) {
    row, err := userStore.BySubject(ctx, claims.UserID)
    if errors.Is(err, sql.ErrNoRows) {
        // Authenticated by the IdP but unknown here — see Provisioning below.
        return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no local account"))
    }
    if err != nil { return nil, err }          // a DB outage is not a 401
    if row.SuspendedAt != nil {
        return nil, connect.NewError(connect.CodePermissionDenied, errors.New("account suspended"))
    }
    claims.OrgID, claims.Roles = row.OrgID, row.Roles
    return claims, nil
}
```

**`Enrich` can REJECT, and it is the earliest place you can.** Return a `connect.Error` and its code is preserved verbatim (`PermissionDenied` stays `PermissionDenied`); return a plain error and it becomes `Unauthenticated` wrapped as `identity enrichment failed: …`. Rejecting here stops the request before any handler runs, which is right for whole-principal facts (suspended, no local account, org disabled) and wrong for per-resource decisions — "may this user edit THAT row" needs the row, so it belongs in the handler.

Enrichment runs on every authenticated request, so a query here is on the hot path: index the subject lookup, and cache if it shows up in traces.

**Request VALIDATION is not `Enrich`'s job.** Field-level input checking is already handled, separately and earlier in the chain, by protovalidate from the `buf.validate` rules on your proto messages:

```proto
string email = 1 [(buf.validate.field).string.email = true];
int32  qty   = 2 [(buf.validate.field).int32.gte = 1];
```

That interceptor rejects a malformed request with `InvalidArgument` before your handler runs. Putting field checks in `enrichClaims` would run them on requests that have none of those fields and report a bad email as an authentication failure.

## Provisioning is application code

Your IdP authenticates; it does not populate your `users` table. That first-contact write is yours, and the honest place for it is a **public** RPC the client calls once with an IdP-issued token in hand:

```proto
rpc Register(RegisterRequest) returns (RegisterResponse) {
  option (forge.v1.method) = { auth_required: false };
}
```

`auth_required: false` here does NOT mean unauthenticated — it means the allow-list lets the call through so the handler can do the credential work itself, because `enrichClaims` would reject the caller for having no local row and the request would never arrive. The handler validates the presented token (the same validator `SetupAuth` built), takes the **`sub` the IdP asserted**, and inserts on that. Trust the `sub` from the verified token; never a `user_id` from the request body, which is a caller-supplied string and an account-takeover primitive. Make the insert idempotent on `sub` — a retried or double-clicked registration must not create a second account.

## Authenticated is not scoped

`auth_required: true` gets you a caller. It does not get you *their* rows. A
generated CRUD delegation reads no claims at all, so the interceptor proves the
caller is signed in and the handler then serves them everyone's data identically.
That is the gap, and it is not hypothetical: a measured downstream app shipped 63
of 74 authenticated RPCs this way, where any authenticated user could read,
update, delete and create rows belonging to every other one.

Forge diagnosed it exactly — and exited 0, because the `unscoped_auth` audit
category was advisory. It was advisory for a real reason: a fresh scaffold is
entirely unscoped by construction, so failing on "unscoped" would mean forge's
own output could never pass forge's own gate.

The way out is that **"unscoped" and "unsafe" are different claims**, and only
you can tell forge which one applies. Declare the owner column and the gate
arms:

```sql
COMMENT ON COLUMN crews.company_id IS 'forge:owner';
```

- **No table declares it** → `unscoped_auth` stays `warn`, exit 0. Fresh
  scaffolds and apps whose rows belong to nobody in particular are untouched, and the finding is
  still reported.
- **A table declares it** → an authenticated RPC over *that entity* that never
  resolves the caller is an `error`, and `forge project audit` fails. Declaring
  an owner on `crews` does not fail an unscoped RPC over a global product
  catalog; the gate arms per table.

`forge:owner` changes no codegen. Forge ships no ownership of its own and will not inject a `WHERE` clause for
you, because only your app knows how a caller's claims map onto that column's
values — a direct user id, a company id read from a membership table, an org
resolved from a subdomain. The marker is a declaration, and what it buys is that
forge stops letting you ship the handler unfinished.

### Scoping a generated CRUD op

**Where the wrapper comes from depends on when you declared the owner column,
and on most projects it will NOT be written for you.** Read this before you go
looking for generated code that is not there.

The handler file is scaffold-once: forge writes
`internal/handlers/<service>/handlers_crud.go` when it does not yet exist, and
after that only ever appends blocks for RPCs the file has no method for. Its own
header says so — *"yours: scaffolded once, never touched again"*. So there are
two cases:

- **The owner column was declared before that file was born** (the table and its
  marker existed at the entity's first `forge generate`). Forge scaffolds the
  whole wrapper — the `GetUser` call, the op-seam override, the column named in
  the predicate — leaving one placeholder expression.
- **The owner column was declared later** — which is the normal order of work,
  because `forge:owner` is a `COMMENT ON COLUMN` in a migration and you usually
  discover the need for scoping after the handlers exist. The wrapper is **not**
  written, and re-running `forge generate` will not add it: the file is already
  there, and the method is already in it. This is not a bug you can generate your
  way out of.

In the second case `forge project audit` hands you the code instead. The gating
finding carries the whole wrapper for each failing RPC, rendered from the same
template the scaffolder uses, and prints it under the failure:

> ── DocumentsService.GetDocument — paste into internal/handlers/documents/handlers_crud.go ──
>
> the `GetUser` call, `owner := claims.UserID`, and the `op.Fetch` override
> carrying `orm.WhereEq("org_id", owner)`

Paste it over the existing delegation for that method. `forge project audit
--json` carries the same text per finding under
`details.owner_scoped_unscoped_rpcs[].remediation`, plus a
`details.owner_scoping_hint` explaining the scaffold-once rule above.

Either way the placeholder is the same, and it is the only thing left to do:

```go
// FORGE_SCAFFOLD: `claims.UserID` is forge's placeholder for the value of
// Crew.company_id that belongs to this caller. Only your app knows that
// mapping — replace the expression, then remove this comment.
owner := claims.UserID
```

Replace that expression with your real mapping (a direct user id, an org id read
from a membership table, an org resolved from a subdomain) and delete the marker.
The marker fails `forge lint --scaffolds` and `forge project audit` until you do,
so the gate cannot be satisfied by ignoring it — but satisfying it is one line,
not ten.

One gap worth knowing: a **custom** RPC (`TransferOrder`, `ArchiveWorkspace`)
delegates to no generated op, so there is no `Fetch`/`Filters`/`Persist` seam to
wrap and forge offers no wrapper for it. Such an RPC is reported as a warning and
the scoping is yours to write from scratch.

What the wrapper looks like, per shape:

```go
// List — the predicate goes in the QUERY, never on the returned page. The
// response's total_count comes from a COUNT over these same opts, so
// post-filtering a page reports rows the caller cannot see.
filters := op.Filters // nil when the RPC declares no filter fields
op.Filters = func(ctx context.Context, r *pb.ListCrewsRequest) ([]orm.QueryOption, error) {
    var opts []orm.QueryOption
    if filters != nil {
        var err error
        if opts, err = filters(ctx, r); err != nil {
            return nil, err
        }
    }
    return append(opts, orm.WhereEq("company_id", owner)), nil
}

// Get — passed INTO the fetch as a query option, not compared after it. A row
// that is not this caller's is never read, and reports NotFound exactly as an
// unknown id does; a distinguishable "exists but is not yours" tells an
// attacker which ids are real.
fetch := op.Fetch
op.Fetch = func(ctx context.Context, id string, opts ...orm.QueryOption) (*db.Crew, error) {
    return fetch(ctx, id, append(opts, orm.WhereEq("company_id", owner))...)
}
```

`Delete` mirrors `Get`. `Create` stamps the column from the caller instead of
filtering — never off the wire, or anyone can file a row under anyone else's
name. `Update` gets both halves: the stamp keeps the written row on the same
owner, and the predicate is checked against the **stored** row, so a caller
cannot take ownership by rewriting `company_id` in the request they send.

Because `Filters` returns an error, a closure that cannot determine the scope can
**fail closed**. Return `svcerr.PermissionDenied(...)` rather than returning no
filter — a missing filter is not "no restriction", it is every row.

### When an RPC really is global

An admin console list, a lookup already keyed by something caller-scoped, an RPC
whose scoping lives in a row-level-security policy — these are correct, and must
not be reported forever. Say so in code, above the handler:

```go
// forge:auth-unscoped-ok: operator console; every caller of this RPC is an
// admin, so there is no narrower scope to apply.
func (s *Service) ListAllCrews(...)
```

This suppresses the **error**, not just the warning. The reason after the colon
is mandatory — a bare directive is the unfalsifiable comment the whole check
exists to replace, and it will not silence anything. It lives next to the code so
it shows up in the diff that introduces it, where a reviewer can disagree.

### When your project resolves the caller through its own package

The audit counts a handler as resolving the caller when it reaches forge's seam,
`middleware.GetUser` (or `ClaimsFromContext`). A project that adopted forge with
its own auth package resolves the caller elsewhere, and every handler would read
as unscoped. Mark the accessors instead of suppressing the findings:

```go
// GetUserIDFromContext returns the signed-in user's id.
//
//forge:resolves-caller
func GetUserIDFromContext(ctx context.Context) (string, bool)
```

A handler that calls a marked function, through the import that binds its
package in that file or through a helper in its own package, resolves the
caller. Mark package-level functions only, because a call site does not name a
method's receiver type. The audit reports a marker on a method as ignored, and
lists what it found under `caller_resolvers`.
