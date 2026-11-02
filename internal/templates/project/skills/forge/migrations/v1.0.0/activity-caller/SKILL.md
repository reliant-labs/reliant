---
name: migration-v1.0.0-activity-caller
description: Upgrade v1.0.0 chains and activity interceptors to a caller that is never guessed — activity.Deps.Principal (ClaimsPrincipal or the project's own), serverkit.Authenticator.Caller / AuthWithCaller, and NewChain's refusal of a defaulted Audit or Activity it cannot attribute.
relevance: migration
---

# v1.0.0 — the activity and audit caller is never guessed

Use when Go code calls `activity.Interceptor` or names `activity.Deps`'s
`ActorFrom` / `ScopeFrom` or `activity.DefaultActor` / `DefaultScope`, or when
`serverkit.NewChain` fails at boot with `does not say how to read the caller`.

## What changed

Every activity event and audit record names a caller. Forge used to assume
forge/pkg/authn's claims were on the context. An app whose auth keeps the
caller its own way (under a context key it owns) had every event recorded
as `system:anonymous` with an empty scope, and nothing said so.

- **`activity.Deps.Principal` is required.** It is the one seam, a two-method
  interface (`Actor(ctx) activity.Actor`, `Scope(ctx) string`) declared in
  `forge/pkg/activity`. `activity.Interceptor` now returns
  `(connect.Interceptor, error)` and fails without one. `Deps.ActorFrom`,
  `Deps.ScopeFrom`, `DefaultActor` and `DefaultScope` are gone.
- **`serverkit.Authenticator` has `Caller() serverkit.CallerLookup`.** That is
  how the chain's caller-recording slots read who Auth admitted.
  `serverkit.AuthFor` fills it from forge's authn interceptor, which names the
  claims it stashes. A project's own interceptor, or authn's with a
  project-owned `Policy.ContextWithClaims`, leaves it nil.
  `serverkit.AuthWithCaller(audience, interceptor, lookup)` names a project's.
- **`serverkit.NewChain` refuses a defaulted `Audit` or `Activity` when
  `Auth.Caller()` is nil.** Both would record an anonymous caller. The error
  names the slot and the ways out.

The scaffolded chain (`serverkit.AuthFor` over `authn.NewInterceptor` with the
default stash) needs no edit.

## Apply

1. `activity.Interceptor(activity.Deps{ActorFrom: a, ScopeFrom: s, …})`
   becomes:

   ```go
   ic, err := activity.Interceptor(activity.Deps{Principal: p, …})
   if err != nil {
       return err
   }
   ```

   `p` is a type with `Actor` and `Scope` methods wrapping what `a` and `s` did
   (control-plane: `activitystream.Principal`). If neither was set, the old
   default is `activity.ClaimsPrincipal(authn.ClaimsFromContext)`, or your
   `middleware.ClaimsFromContext` if your auth stashes claims under its own
   key.
2. A chain over your own authenticator that `NewChain` now refuses needs one
   of these:
   - `Auth: serverkit.AuthWithCaller(serverkit.AudienceUser, yourAuth, lookup)`,
     where `lookup` returns your caller as `*authn.Claims`. This keeps the
     default `Audit` and `Activity`.
   - `Activity: ic` with your own `Principal` (step 1). This is the right
     choice when callers include machines, and needs `Audit` likewise.
   - `serverkit.NoActivity` / `serverkit.NoAudit` for a chain whose rpcs record
     nothing (a daemon or operator listener).
3. A custom `serverkit.Authenticator` implementation adds
   `Caller() serverkit.CallerLookup`. Return nil if it cannot say.

The `activity` skill ("Who the caller is") documents the seam.
