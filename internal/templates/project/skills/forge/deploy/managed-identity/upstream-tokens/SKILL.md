---
name: deploy/managed-identity/upstream-tokens
description: Calling GitHub or Google as the signed-in user — `pass_tokens = True` on forge.IdentityGitHub / forge.IdentityGoogle hands the provider's own access (and refresh, where issued) token from each social sign-in to the app's login broker hook, server-side only, dev-only (IdentityOnHost) until the hosted control plane delivers it; WithUpstreamTokens / RequireUpstreamTokens, devidp.UpstreamGrant, storing it sealed, what is never handed, and refresh.
---

# Upstream provider tokens from a social sign-in

An app that calls the provider's API **as the user** (their repositories, git
over HTTPS, their calendar) needs the provider's own token, and the user
should sign in once. The identity provider does the OAuth code exchange, so it
holds that token; `pass_tokens` asks the login broker to hand it to **your
server code**. It never reaches the browser: no response body, cookie or
redirect carries it, it prints as `[redacted]` in logs, and
`devidp.UpstreamTokens` refuses `json.Marshal`.

## Dev-only for now

`pass_tokens` works on a `forge.IdentityOnHost` identity (the dev loop). A
hosted identity refuses it at render time: the hosted control plane does not
yet carry it into the login policy it delivers, so the hook would never run.
The refusal lifts when the control plane does.

## Declare it

```kcl
social = [forge.IdentityGitHub {
    client_id = "Iv1.abc"
    client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"}
    scopes = ["read:user", "user:email", "repo"]   # what the token may do
    pass_tokens = True                              # default False
}]
```

The token carries exactly the declared `scopes`; changing them re-converges
the provider, and users get the new scopes at their next sign-in. The login
policy entry the broker reads gains `pass_tokens` and the scopes; neither is
in `/auth/options`.

## Take it in the broker

`RegisterLoginBroker` takes options (`internal/app`, your file):

```go
app.RegisterLoginBroker(mux, cfg, logger,
    app.WithUpstreamTokens(func(ctx context.Context, g devidp.UpstreamGrant) error {
        return store.PutProviderToken(ctx, g.Subject, g.Provider, seal(g.Tokens.AccessToken), seal(g.Tokens.RefreshToken))
    }),
    // app.RequireUpstreamTokens(), // refuse the sign-in when the store fails
)
```

- `g.Subject` is the issuer user id the new session is for (never the
  provider's id); key the row by `(Subject, Provider)` and replace it on each
  sign-in. `g.Event` is `sign_in`, `sign_up` or `invite_accept`.
- Encrypt at rest (your key, your KMS). Treat it like a password.
- The hook runs after the session is issued and before the response. An error
  is logged and the sign-in still succeeds; with `RequireUpstreamTokens()` it
  is refused (`social_failed`, 401) and the issuer session is deleted. Strict
  mode also refuses when the issuer returned no access token.

## When nothing is handed

- a refused sign-in (no account, a sign-up the policy does not allow, an
  unverified link);
- a sign-up whose address still waits for its emailed code: no session exists
  yet, so the token is dropped and the next sign-in hands it;
- a provider without `pass_tokens`, or a broker without `WithUpstreamTokens`.

## Expiry and refresh

The issuer reports no expiry (`ExpiresAt` is zero) and not the scopes the
user granted (`Scopes` are the declared ones). A GitHub OAuth app's token does
not expire and has no refresh token; a GitHub App's user token does. Google
gives no refresh token (the issuer does not ask for offline access). forge
never refreshes: refreshing needs the OAuth client secret, which only the
identity reconciler reads. On a 401 from the provider, ask the user to sign in
again.

The issuer keeps its own encrypted copy of the tokens with the sign-in intent.
