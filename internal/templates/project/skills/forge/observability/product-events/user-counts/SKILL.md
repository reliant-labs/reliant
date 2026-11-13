---
name: observability/product-events/user-counts
description: Who the console counts as an active, new or returning user — observe.TrackState for user-attributed events that are not activity (snapshots, markers), and forge.ProjectAnalytics.not_activity / exclude_users_with, the project's own rules every user count (Users dashboard, Overview Users card, forge obs users) compiles in.
---

# Who counts as a user

The user counts (active, new, returning) read one predicate,
`obsquery.UserActivityEvidence`: every product event row with a `UserId`, except a
sign-out, the platform's `auth_accounts` snapshot and an event marked as a
fact about the user rather than something they did. The project narrows it
with its own rules (`obsquery.ActiveEvidenceFor`), and the Users dashboard,
the Overview's Users card and `forge obs users` all compile the same text, so
they agree.

## Facts about a user that are not activity: `observe.TrackState`

```go
observe.TrackState(ctx, "billing_subscription_state", slog.String("plan", "pro"))
```

A daily snapshot, a staff marker or a record the platform writes on a user's
behalf names a user but is not something they did. `observe.TrackState` is
`Track` with the reserved prop `forge.kind = "state"` (`observe.PropKind`,
`observe.KindState`); the user counts (`obsquery.UserActivityEvidence`: the Users
dashboard, the Overview's Users card, `forge obs users`) skip every row that
carries it, while Events by name, funnels and counts still see the event. An
emitter that writes its own record (one that names the user itself) adds
`observe.StateAttrs(attrs)`. From the browser, `track(name, {"forge.kind":
"state"})` marks the same.

## The project's rules: `not_activity` and `exclude_users_with`

```kcl
analytics = forge.ProjectAnalytics {
    not_activity = ["billing_subscription_state", "claimable_account_created"]
    exclude_users_with = ["internal_user"]
}
```

- `not_activity`: events that name a user without being activity, written by
  an emitter that does not mark them with `observe.TrackState` (history, or
  code you do not own). Their rows never count a user as active or new.
- `exclude_users_with`: marker events. A user with any retained row of one,
  in an environment, is left out of that environment's user counts, history
  included, so a marker emitted today fixes past ranges too.
- forge generate compiles both into every built-in user count (the Users
  dashboard and the Overview's Users card, through
  `obsquery.ActiveEvidenceFor`) and writes them into the project's dashboard
  spec, where `forge obs users` reads them. Every environment's lists are
  merged (their union).
