---
name: observability/product-events/user-counts
description: Who the console counts as an active, new or returning user — an event that is not activity (observe.TrackState, observe.WithStateEvents, track(…, {state: true}), forge.ProjectAnalytics.not_activity) and a user who is not counted (staff, monitors: exclude_users_with, exclude_user_ids), the project rules every user count (Users dashboard, Overview Users card, forge obs users) compiles in.
---

# Who counts as a user

The user counts (active, new, returning) read one predicate,
`obsquery.UserActivityEvidence`: every product event row with a `UserId`, except a
sign-out, the platform's `auth_accounts` snapshot and an event marked as a
fact about the user rather than something they did. The project narrows it
with its own rules (`obsquery.ActiveEvidenceFor`), and the Users dashboard,
the Overview's Users card and `forge obs users` all compile the same text, so
they agree.

There are two questions, and one answer each:

| Question | The marker / rule |
|---|---|
| Is this **event** a user's activity? | not when it carries `forge.kind = "state"`, or its name is in `not_activity` |
| Is this **user** counted at all? | not when they have an `exclude_users_with` marker event in the environment, or their id is in `exclude_user_ids` |

## Events that are not activity: `forge.kind = "state"`

```go
observe.TrackState(ctx, "billing_subscription_state", slog.String("plan", "pro"))
observe.TrackStateAs(ctx, member.UserID, "internal_user")  // a worker names the user
ctx = observe.WithStateEvents(ctx)                          // everything recorded under ctx
```

A daily snapshot, a staff marker or a record the platform writes on a user's
behalf names a user but is not something they did. Each of these writes the
reserved prop `forge.kind = "state"` (`observe.PropKind`, `observe.KindState`),
first, so the 32-prop bound never drops it:

- `observe.TrackState` is `Track` with the marker; `observe.TrackStateAs` names
  the user itself, for a worker or job with no signed-in caller.
- `observe.WithStateEvents(ctx)` marks every product event recorded under the
  context, including the ones you do not emit yourself: `TrackAuth`, the login
  broker's sign-ins and sign-ups (`pkg/devidp`), and the generated CRUD
  events. Use it for a synthetic monitor's login, or a job that writes
  through the generated handlers on a user's behalf.
- An emitter that writes its own record adds `observe.StateAttrs(attrs)`.
- From the browser: `track(name, props, {state: true})` (`KIND_PROP`,
  `KIND_STATE`), set after the props are bounded.

The user counts skip every row that carries it, and so do Most active users and
Sign-ins by method. Events by name, funnels and counts still see the event.

## The project's rules

```kcl
analytics = forge.ProjectAnalytics {
    not_activity = ["billing_subscription_state", "claimable_account_created"]
    exclude_users_with = ["internal_user"]
    exclude_user_ids = ["8d1c0e5e-identity-canary"]
}
```

- `not_activity`: events that name a user without being activity, written by
  an emitter that does not mark them (history, or code you do not own). Their
  rows never count a user as active or new.
- `exclude_users_with`: marker events. A user with any retained row of one,
  in an environment, is left out of that environment's user counts, history
  included, so a marker emitted today fixes past ranges too. **Staff by email
  or domain** is a marker: a product event never carries an email (the
  emitters refuse `@`), so the app, which knows its users' emails, emits the
  marker for each staff member, e.g. a daily `TrackStateAs(ctx, id,
  "internal_user")` for every account on `@yourco.com`.
- `exclude_user_ids`: users left out by the opaque id their events carry (the
  provider's subject, never an email; KCL refuses `@`), in every environment.
  Use it for an account that emits no marker: a synthetic monitor's login, a
  shared test account. At most 100.
- forge generate compiles all three into every built-in user count (the Users
  dashboard and the Overview's Users card, through
  `obsquery.ActiveEvidenceFor`) and writes them into the project's dashboard
  spec, where `forge obs users` reads them. Every environment's lists are
  merged (their union).

The predicate they compile to, each clause only when its list is non-empty:

```sql
UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state'
  AND EventName NOT IN ('billing_subscription_state', …)                       -- not_activity
  AND (EnvId, UserId) NOT IN (SELECT EnvId, UserId FROM product_events
       WHERE EventName IN ('internal_user') AND UserId != '')                  -- exclude_users_with
  AND UserId NOT IN ('8d1c0e5e-identity-canary')                               -- exclude_user_ids
```
