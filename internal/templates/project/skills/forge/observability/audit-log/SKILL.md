---
name: observability/audit-log
description: A queryable, DB-persisted audit trail on forge/pkg/audit — the audit_log table you own, the DB-backed audit interceptor in the serverkit chain, and a scoped ListAuditEvents read RPC over the same store. The slog-only audit record every RPC already writes needs none of it.
---

# Audit log

Every RPC already produces a structured audit record: `serverkit.NewChain`
fills its `Audit` slot with `fmw.AuditInterceptor` over the caller's claims by
default (the chain the generated `cmd serve.go` builds). That
is slog-only — the record goes to your logs (queryable in HyperDX) with
`log_type=audit`, message `audit.event`.

The default reads the caller through `ChainDeps.Auth`'s `Caller()`, which
`serverkit.AuthFor` fills for forge's authn interceptor. An authenticator that
does not say how to read its caller (your own interceptor) makes `NewChain`
refuse a defaulted `Audit` rather than audit every call as anonymous: build it
with `serverkit.AuthWithCaller(audience, interceptor, lookup)`, pass your own
`Audit`, or `serverkit.NoAudit` (skill `activity`, "Who the caller is").

For a **queryable, DB-persisted** audit trail (a compliance record you can page
through, plus an admin `ListAuditEvents` RPC), there is **no pack to install**.
The mechanical write-side is the versioned `forge/pkg/audit` library; the
app-specific read-side (the table + the RPC) is code you own.

1. **Own the `audit_log` table** in a plain migration
   (`forge db migration new audit_log`). Its columns are what the library
   store reads/writes (see `audit.Entry`):

   ```sql
   CREATE TABLE audit_log (
       id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
       timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
       user_id VARCHAR(255),
       email VARCHAR(255),
       procedure VARCHAR(512) NOT NULL,
       peer_address VARCHAR(255),
       duration_ms INTEGER,
       status VARCHAR(50) NOT NULL DEFAULT 'success',
       error_code VARCHAR(50),
       error_message TEXT,
       metadata JSONB DEFAULT '{}',
       created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
   );
   CREATE INDEX audit_log_user_id_idx ON audit_log (user_id);
   CREATE INDEX audit_log_procedure_idx ON audit_log (procedure);
   CREATE INDEX audit_log_timestamp_idx ON audit_log (timestamp);
   -- Written and read through forge/pkg/audit, so keep it off the ORM: a
   -- generated AuditLog store would be a second, unaudited write path, and
   -- its entity name collides with any app table named audit_logs.
   COMMENT ON TABLE audit_log IS 'forge:no-entity — written and read through forge/pkg/audit';
   ```

   The ORM projects every applied table unless it says otherwise, so the
   `forge:no-entity` comment is what keeps this one out. Drop it only if you
   deliberately want a generated store over the table as well.

2. **Wire the DB-backed interceptor** from the library — one line. Construct
   the store and set the `Audit` slot of `cmd serve.go`'s
   `serverkit.ChainDeps`, replacing the default:

   ```go
   import "github.com/reliant-labs/forge/pkg/audit"

   store := audit.NewDBAuditStore(db)
   chain, err := serverkit.NewChain(serverkit.ChainDeps{
       // ...Auth, RateLimit, caps as scaffolded...
       Audit: audit.Interceptor(logger, middleware.ClaimsFromContext, store),
   })
   ```

   If your migration puts the table in a schema, say which:
   `audit.NewDBAuditStore(db, audit.WithSchema("app"))` names it
   `"app".audit_log` in every statement. Do the same (with `"public"` if that is
   where it is) whenever the database sits behind a transaction-pooling
   PgBouncer: a pooled backend keeps whatever `search_path` an earlier session
   SET on it, and a bare `audit_log` then fails with SQLSTATE 42P01.

   `audit.Interceptor` logs to slog exactly as the base interceptor does AND
   persists each event to the store off the request path (fire-and-forget, so
   audit writes never add latency). A nil store falls back to slog-only. It is a
   thin convenience over `fmw.AuditInterceptorWithSink(logger, claimsFrom, sink)`
   — reach for that plus `audit.Sink(store, logger)` if you already hold a
   custom sink.

3. **Own the read-side `ListAuditEvents` RPC.** Add a proto service + handler
   the normal way (`proto/audit/v1/…` + a handler package), and back the
   handler with the SAME store so reads see the writes:

   ```go
   entries, err := store.Query(ctx, audit.Filter{
       UserID: req.Msg.GetUserId(),  // filters are AND-combined
       Since:  req.Msg.GetSince().AsTime(),
       Limit:  int(req.Msg.GetLimit()), // <= 0 ⇒ default 100
   })
   ```

   Audit logs enumerate who did what, so scope the read handler (restrict a
   non-admin caller to their own `user_id` via `middleware.ClaimsFromContext`)
   — an unscoped handler is a cross-user enumeration hole.
