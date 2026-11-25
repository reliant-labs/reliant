---
name: external-provider
description: Running a forge app on an identity provider of its own (Supabase, Auth0, Clerk, Firebase, Cognito, any OIDC issuer) instead of managed identity — the issuer swap in config.k, the Clerk and Firebase validators, naming the provider on the auth middleware's sign-in heartbeat (ChainDeps.AuthProvider, in code), passing Caller when the app's auth keeps the caller in its own context, and the three ways to emit the standard auth events.
---

# Bring your own identity provider

## The issuer

The escape hatch from managed identity: an env that declares no `identity` takes its issuer from `config.k`.

**Most swaps are a CONFIG change, not a code change.** Any standards-compliant issuer — Auth0, Keycloak, Zitadel, Okta, Supabase, a local dev IdP — needs no constructor: point `jwt_issuer` (plus `jwt_jwks_url` if discovery cannot describe it) at the new issuer and the default validator validates against its keys.

Only a NON-STANDARD claim shape needs code — edit `internal/app/auth.go` and wrap a different validator's `.Validate` in `auth.NewAuthenticator`:

```go
// Clerk — JWKS + Clerk's org/session claim shape (sub / org_id / org_role → Claims).
// Email isn't in a Clerk token by default; sync it via webhook.
validator, err := auth.Clerk(auth.ClerkOpts{JWKSURL: cfg.JwtJwksUrl, Issuer: cfg.JwtIssuer})

// Firebase — Google's JWKS + Firebase shape (sub = uid); token must match a project's
// aud AND issuer. Add a `firebase_project_ids` config field for the list.
validator, err := auth.Firebase(auth.FirebaseOpts{ProjectIDs: strings.Split(cfg.FirebaseProjectIds, ",")})
```

A constructor needing a value the config lacks gets a config field, never an `os.Getenv`. Other non-standard shapes ride `UserResolver`. Point the login broker at the same issuer — both halves must name it, or a session minted by one is rejected by the other.

## Name the provider, and let the chain see the caller

The chain's daily sign-in heartbeat (`auth_signed_in {kind: resumed}`, which is what counts active users) labels its events with the provider, which the app names in code and declares nowhere else: set `ChainDeps.AuthProvider` to `supabase` (or `auth0`, `clerk`, `firebase`, …; `observe.Deps.AuthProvider` outside serverkit). Unset, it is `forge` with managed identity and `jwt` otherwise. The heartbeat reads the caller through the Authenticator's `Caller`: **if your auth does not stash forge's `authn` claims (`authn.ContextWithClaims`), pass `Caller`** — build `ChainDeps.Auth` with `serverkit.AuthWithCaller(audience, interceptor, lookup)` (or set `observe.Deps.Caller`), or the heartbeat, and the `user.id` on server spans, see nobody. Return `false` from the lookup for anonymous sessions and machine callers, so they are not counted as signed-in users.

## Emitting the standard auth events

Using another auth provider, an app emits the standard auth events (`auth_signed_up`, `auth_signed_in`, `auth_signed_out`) one of three ways, and each carries the provider it names: the browser adapter for its provider (`@reliantlabs/forge-web-runtime/auth/<provider>`), a webhook handler its provider posts to, or `observe.TrackAuth` called in its own sign-up and sign-in code. Nothing is declared in KCL or config. Load `observability/product-events/auth-events` for each.
