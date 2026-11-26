---
name: scopes
description: Access-token scopes in forge/pkg/accesstoken — the closed catalog, each scope's class (personal, member, standard, sensitive), the named bundles (ci, developer, admin) stored as `bundle:<name>` and expanded at check time, the tolerant read of stored grants (ResolveStored), the daemon and exchange flags, why a sensitive scope never joins a bundle, and why adding a scope is one catalog row.
---

# Access-token scopes: classes and bundles

`forge/pkg/accesstoken` owns the scope catalog every store of machine credentials shares: the control plane's tokens, a self-hosted store, a consumer that introspects one. A scope is spelled `<product>:<action>` (`deploy:write`, `obs:read`) and the set is closed: `accesstoken.AllScopes`. Minting goes through `accesstoken.NewSet`, which refuses a scope this binary does not know.

Beside its constant, each scope has ONE catalog row (`pkg/accesstoken/catalog.go`) saying who holds it and how. A consumer derives every per-scope list from that row and keeps none of its own.

## Classes

| Class | Who holds it | Examples |
|---|---|---|
| `personal` | every signed-in person, over their OWN resources; no org grant names it | `reliant:api`, `llm:invoke`, `runs:start`, `inbox:read`, `daemon:manage` |
| `member` | every member of an org, without a grant, and every credential acting as one (their daemon too) | `obs:read`, `obs:triage`, `tests:write` |
| `standard` | org authority held by grant, directly or through a bundle | `deploy:write`, `secret:read`, `domain:write`, `tests:read` |
| `sensitive` | org authority held ONLY by a grant naming the scope | `secret:write`, `db:query`, `storage:read`, `deploy:config:read`, `identity:manage`, `token:read`, `token:write`, `changes:launch` |

A sensitive scope is one whose leak revoking the token does not undo: it reads secrets or users' data, issues credentials or passwords, or shifts production traffic. A future `billing:write` (changing what an org pays, or how) is sensitive from the day it is added; no such scope exists yet.

`accesstoken.ClassOf(scope)` returns a scope's class; `accesstoken.ScopesOfClass(class)` lists a class in `AllScopes` order. Every scope that acts as a person (`accesstoken.RequiresActingUser`) is personal.

## Bundles

A bundle is a reviewed set of scopes for one kind of holder. A grant row or a token stores it as `bundle:<name>` (`accesstoken.BundleRef`) instead of a scope list, and it is **expanded at check time** against the running binary's catalog. So a scope that joins a bundle reaches every holder of it from the release that ships it, with no backfill and no re-mint.

| Bundle | For | Holds |
|---|---|---|
| `ci` | an org's CI, acting as no person | `deploy:read`, `deploy:write`, `vuln:read`, `tests:write` |
| `developer` | a person building and shipping | `ci`'s scopes, their own API, runs, triggers, inbox and questions, `domain:read`, `obs:*`, `changes:read`, `changes:write`, `tests:read`. Not `secret:read`: secrets are the highest-value thing a leaked developer token could reach, so a developer is granted it explicitly |
| `admin` | a person administering the org | every member and standard scope that is not a whole credential of its own, `secret:read` included; no personal scope (a person holds those already) |

**No sensitive scope is in any bundle, `admin` included.** An admin is granted each sensitive scope one by one. `accesstoken.ExpandBundle` skips a sensitive scope even if a catalog row listed one, and forge's own tests fail such a row.

Whole credentials join no bundle either: `sourcemaps:write` (registry-only), `mcp:oauth` and `mcp:forge` (an MCP client's token), and the scopes that must be bound to one resource or are a daemon's own (`proxy:port`, `mcp:connector`, `daemon:resume`, `daemon:connect`, `daemon:manage`). Each bundle's expansion validates as a single grant of its holder's shape (`accesstoken.Grant`), unbound.

The API: `accesstoken.AllBundles`, `accesstoken.ParseBundle("ci")`, `accesstoken.BundleRef(b)` / `accesstoken.ParseBundleRef("bundle:ci")`, `accesstoken.ExpandBundle(b)` (a fresh `Set`), `accesstoken.BundlesOf(scope)`.

## Reading stored grants: tolerant

```go
held, skipped := accesstoken.ResolveStored(row.Grants) // []string{"bundle:developer", "secret:read", ...}
if len(skipped) > 0 {
    slog.WarnContext(ctx, "grants this binary does not know", "token_id", row.ID, "skipped", skipped)
}
if len(held) == 0 {
    return nil, errNoAuthority // refuse: a token with no authority must not authenticate
}
```

`ResolveStored` expands `bundle:<name>` entries and keeps known scopes. It **skips** an unknown scope or an unknown bundle, returns each skipped entry in input order, and never fails: a row written by a newer binary must stay readable after a rollback, and dropping what this binary cannot evaluate only ever narrows the result. Log and count every skipped entry with the token's id: a skip nobody sees is a token quietly narrower than its holder believes. forge does not refuse an empty result; the consumer refuses a token whose resolved set is empty.

Use it for STORED grants only. A list a caller is about to mint still goes through `NewSet`, which refuses an unknown scope and a `bundle:` entry alike: a typo must not mint a token that authenticates and then refuses everything.

## Daemon and exchange flags

- `accesstoken.NeverOnDaemon(scope)`: `token:read`, `token:write` and `approvals:decide` never ride a daemon's credential, even when its person holds them. Remove them after resolving a daemon's wildcard (`Grant.Admissible`, which already leaves out person-only and whole-credential scopes).
- `accesstoken.Elevatable(scope)`: whether an on-demand exchange (a session trading up for an hour when a refusal names the scope) may produce it. Org authority minus `deploy:write` (it authorizes the exchange), `token:read`, `token:write`, `sourcemaps:write` and `changes:launch`. Elevation is a separate, explicit path: a sensitive scope may be elevatable (`identity:manage`, `db:query`) and is still never reachable through a bundle, and an exchange yields a scope only while the person holds it by grant.

## Adding a scope is one catalog row

1. The constant, in `pkg/accesstoken/scope.go`, with a doc comment saying what it authorizes and what it does NOT imply.
2. Append it to `AllScopes`: last, never in the middle, because a store's CHECK may list the set in order.
3. One catalog row: its class, its bundles, its flags.

forge's tests fail a scope with no row, a sensitive scope in a bundle, and a standard scope `admin` misses, so a consumer's pin bump needs no code: its lists derive from `ClassOf`, its bundles expand at check time.

## The column's CHECK: a format check

A store's scope column can declare `forge:closed-set accesstoken.AllScopes` on its CHECK, and `forge lint` keeps the two equal (`db/closed-sets`). That works and keeps working, but every new scope then costs each store a widening migration and a validate migration before a row can name it, and a `bundle:<name>` entry cannot be stored at all.

The alternative is a FORMAT check, since the write path (`NewSet`, or `ParseScope` plus `ParseBundleRef`) already refuses an unknown entry. It replaces the closed-set CHECK in one ALTER, `NOT VALID`, with `VALIDATE CONSTRAINT` in the next migration:

```sql
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND string_to_array(array_to_string(scopes, ',', ''), ',') = scopes
        AND array_to_string(scopes, ',') ~ '^[a-z]+(:[a-z]+)*(,[a-z]+(:[a-z]+)*)*$'
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'format check: every element matches ^[a-z]+(:[a-z]+)*$; the vocabulary is the write path''s';
```

A CHECK cannot unnest an array, so it judges the comma-joined string, and each part closes a gap the regex alone leaves:

- `cardinality(scopes) > 0`: a token with no scope authenticates and can do nothing.
- The round trip: the joined string splits back to the same array only when no element contains a comma and none is NULL (the third argument joins NULL as `''`, which splits back as `''`, not NULL; the two-argument form drops it silently). So the pieces the regex sees ARE the elements. Without it, `{"deploy:read,obs:read"}` and `{deploy:read,NULL}` both pass.
- The regex: every piece is lower-case words joined by colons. `deploy:config:read` and `bundle:admin` pass; `''`, `Deploy:read`, `a::b` and `not:a-scope` do not.

The COMMENT is required. postgres drops the `forge:closed-set` declaration with the old constraint, and with no new comment `forge lint --closed-sets` reports the declaration lost. A comment without the marker tells it the closed set was retired on purpose.

Then the read path must be tolerant (`ResolveStored`), because the database no longer stops a newer binary's scope from reaching an older one.
