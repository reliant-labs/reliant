---
name: social
description: Add GitHub and Google sign-in to a forge.ManagedIdentity, step by step — declare forge.IdentityGitHub / forge.IdentityGoogle with client id and secret as managed-secret names, get each env's callback (forge env up in dev, forge cloud identity status hosted), register one OAuth app per env, store the credentials, the control plane's social_enabled, and prove it with `forge cloud identity status --check-social`.
---

# Add GitHub / Google sign-in

The issuer (not your app) does the OAuth code exchange, so each provider's OAuth app registers the ISSUER's callback, `<issuer>/idps/callback`, and its client secret lives only in the issuer. An OAuth app has one callback, so **every env (each its own issuer) needs its own OAuth app**.

1. **Declare** both providers once, in the identity every env imports. Put the client id in the store too, so one declaration serves every env and each env's store names its own app:

   ```python
   social = [
       forge.IdentityGitHub {
           client_id = forge.ManagedSecret {name = "GITHUB_CLIENT_ID"}         # or a literal: it is public
           client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"} # always a NAME, never the secret
           # scopes default to ["read:user", "user:email"]
       },
       forge.IdentityGoogle {
           client_id = forge.ManagedSecret {name = "GOOGLE_CLIENT_ID"}
           client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}
           # scopes default to ["openid", "profile", "email"]
       },
   ]
   ```

   A literal `client_secret` does not render: it must be a `forge.ManagedSecret`.
2. **Get the env's callback.** Dev: `reliant forge env up dev` prints `identity <name>: social sign-in callback (…): http://localhost:<idp port>/idps/callback`, even before the secrets exist, so you can register the app first. Hosted: `reliant forge cloud identity status <env>` prints `Social callback: https://<slug>.reliantidentity.com/idps/callback`.
3. **Register the OAuth app** with that callback, exactly:
   - GitHub: Settings → Developer settings → **OAuth Apps** → New OAuth App; *Authorization callback URL* = the callback. GitHub matches the callback's host AND port, so a dev app serves one checkout's IdP port.
   - Google: Google Cloud console → APIs & Services → Credentials → Create credentials → **OAuth client ID** → *Web application*; add the callback under *Authorized redirect URIs* (Google accepts `http://localhost` for dev). Configure the consent screen first.
4. **Store the credentials** in that env's store: `reliant forge secret set --env <env> GITHUB_CLIENT_ID` (and `GITHUB_CLIENT_SECRET`, `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`). Dev applies them on the next `forge env up`; a missing one turns that provider off with a warning naming the command. Hosted applies them on the next reconcile pass, and `status` shows each provider `applied`, `pending` or `secret_missing`.
5. **Hosted needs the control plane's `identity.social_enabled`**: it serves the instance's public `/idps/callback` route. Without it a social declaration is refused (`status` prints `Refused:`).
6. **Check it**: `reliant forge cloud identity status <env> --check-social` starts a sign-in with each applied provider and fails unless the authorize URL is the provider's (github.com / accounts.google.com) with a client id, scopes and `redirect_uri` equal to the printed callback. `--app <url>` starts them through your deployed login broker (`POST <url>/auth/social/start`) instead of the control plane, which also proves the release in that env offers both buttons.

The sign-in screen shows a button per provider (`GET /auth/options` lists them). A person who cancels at the provider lands on `/auth/sign-in?social=failed` (the issuer replaces `error` with the provider's own). An account is linked to a provider identity only when both assert the same verified address; otherwise sign-up follows `signup`. Changing `scopes` is applied on the next pass, and an existing user grants the wider scopes at their next sign-in, through the provider's consent screen.

## What each piece does

| Piece | Where |
|---|---|
| declaration | `ManagedIdentity.social`, rendered into `shape.identity.social` with secret NAMES only |
| issuer side | the reconciler creates `forge:github` / `forge:google` with your client id, secret and scopes, Zitadel's own creation and linking off, linked into the login policy; a removed provider is unlinked, never deleted (users keep their links) |
| broker side | `LOGIN_POLICY` lists each provider with its issuer id; the broker mounts `POST /auth/social/start` and `POST /auth/social/callback` (`devidp.SignInRoutes`) and binds each sign-in to the browser that started it |
| frontend | the sign-in screen's buttons and the `/auth/social/callback` page |

Proven against the pinned Zitadel by `TestE2ESocialSignInGitHubAndGoogleAgainstZitadel` (Forge's e2e corpus), up to the provider itself. The following real-provider check is separate from that automated test.

## Verify real providers before promoting

1. In an approved hosted canary environment, use its own GitHub and Google OAuth apps and dedicated test accounts. Verify an existing non-social sign-in works before enabling social sign-in; leave it enabled for recovery.
2. After deploying and reconciling the canary, run `reliant forge cloud identity status <env> --check-social --app <app-url>`. Both providers must be `applied` and report the exact canary issuer callback. This checks authorization URL construction, not the code exchange.
3. In a fresh browser session for each provider, complete consent at the real provider. Confirm the browser returns to the canary app, opens an authenticated page, and is signed in as the intended canary user. With signup enabled, the first sign-in should create one account and a second sign-in should return to that same account. To check linking, use a separate existing canary account whose verified email matches the provider's verified email; confirm the sign-in keeps the existing account ID and does not create a duplicate. Record only the provider and outcome, never OAuth codes, cookies, or credentials.
4. If either provider fails, stop promotion. Remove the failing provider from `ManagedIdentity.social` in the affected environment's declaration and reconcile it, or revert the canary's social declaration if both fail. The issuer unlinks the provider from the login policy without deleting user links. Confirm its button is no longer offered and verify the pre-existing non-social sign-in still works. Correct the issuer callback or credential setup before reenabling and repeating the check.
