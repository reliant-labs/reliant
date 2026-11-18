---
name: observability/product-events/purge
description: Deleting one user's or visitor's product events (account deletion, a data-subject request) with observe.RequestPurge — a purge request sent as a product event through the app's own telemetry pipeline, which the Reliant control plane verifies, executes against the environment's product_events and records once per request id.
---

# Deleting a user's events: `observe.RequestPurge`

```go
id, err := observe.RequestPurge(ctx, "account-"+accountID, observe.PurgeSubject{UserID: userID})
```

The app asks; the platform that stores the events deletes them. There is no
new endpoint or credential: the request is one product event,
`forge_purge_requested`, written through the context's logger like every
`observe.Track` event, with the subject in its props (`purge.user_id`,
`purge.visitor_id`) and a request id (`purge.request_id`).

- **Who.** `UserID` is the id your events carry as `user.id`; `VisitorID` the
  one they carry as `visitor.id` (a browser at the `full` tier, a CLI's install
  id). Set either or both: a row matching either is deleted.
- **Idempotent.** The platform records each request once by (org, environment,
  request id). Pass an id derived from the deletion (the account id) and a
  retried call is the same request; pass `""` and RequestPurge mints a random
  one and returns it.
- **Never refused by consent or the PII guard**, unlike `Track`: a purge is
  how withdrawn consent is honoured, and the ids are the point. It fails only
  on a request that cannot be honoured: no subject, an id over 256 characters
  (the pipeline would truncate it and miss the rows) or a malformed request id
  (`observe.ErrInvalidPurge`).
- **No actor.** The record carries no `user.id`, `org.id` or `visitor.id`, so
  it is nobody's activity.

## What the Reliant control plane does with it

- **Authenticated as your environment.** It acts only on a request that
  arrived through the environment's own server-side ingest (a workload's OTLP
  token or the cluster's log agent). A request sent with a publishable browser
  or CLI key is recorded as rejected and never executed, so a page cannot
  purge another user's data.
- **Scoped by the gateway.** The org and environment are the ones the ingest
  gateway stamped, not anything in the record, and the delete runs in that
  org's own `product_events` table, restricted to that environment. A request
  cannot reach another org's or another environment's rows.
- **Recorded.** One audit row per request: the environment, the request id,
  SHA-256 hashes of the subject ids (never the ids), the outcome and the rows
  deleted. The request's own row is deleted after it runs, so the ids do not
  outlive the purge.
- **What it covers.** The long-retention `product_events` table, as stored
  when the purge runs (within minutes). Raw logs and traces keep their own
  short retention. Stop emitting events for the subject first: an event
  stored after the purge ran is not purged.

A local `forge env up` has no purge executor: the request is an ordinary log
line there.
