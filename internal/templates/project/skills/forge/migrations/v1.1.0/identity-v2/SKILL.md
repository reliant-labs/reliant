---
name: migrations/v1.1.0/identity-v2
description: Teach a login broker scaffolded before v1.1.0 the identity-v2 sign-in methods — passkeys, magic links, Google/GitHub, invitations, address verification and open or domain sign-up — by reading the delivered idp_login_policy and mounting devidp.SignInRoutes. Without it a forge.ManagedIdentity that declares methods, social or a non-invite signup is converged and silently ignored by the sign-in screen. Use when internal/app/login_broker.go does not call devidp.ParseLoginPolicy, or `forge lint` reports forgeconv-identity-methods.
version: v1.1.0
detection: test -f internal/app/login_broker.go && ! grep -qs 'ParseLoginPolicy' internal/app/login_broker.go && exit 0; exit 1
---

# Identity v2 in an existing login broker

**Use when** `internal/app/login_broker.go` exists and does not call
`devidp.ParseLoginPolicy`, or `forge lint` reports `forgeconv-identity-methods`.

## What changed

`forge.ManagedIdentity` gained `signup` (`invite` | `open` | `domain`),
`methods` (`password`, `passkey`, `magic_link`), `passkey_rp_id`, `social` and
`owners`. Whoever converges the identity delivers the result to the app as one
JSON document, `idp_login_policy` (`IDP_LOGIN_POLICY`). A broker scaffolded
since v1.1.0 parses it and mounts the matching routes; their handlers are
`devidp.SignInRoutes` in forge, so the broker only decides which are served.

`login_broker.go` is yours, so forge does not rewrite it.

**An app that does nothing keeps working:** with no document the broker serves
the legacy policy, today's behaviour. Only a declaration that turns something
on needs the edits, and `forge lint` names each one it would otherwise ignore.

The steps below copy from a scratch project of the same shape
(`forge project new` in a temporary directory), which has the current broker
and pages.

## 1. Declare the config field

In the config proto that declares `idp_broker_token` (usually
`proto/config/v1/config.proto`), add a field with an unused number:

```protobuf
  string idp_login_policy = 49 [(forge.v1.config) = {
    env_var: "IDP_LOGIN_POLICY"
    flag: "idp-login-policy"
    sensitive: true
    description: "The LOGIN_POLICY document the login broker enforces. Delivered by the identity converge; empty keeps the legacy policy."
  }];
```

Add `int32 trusted_proxy_hops = 50` the same way (env `TRUSTED_PROXY_HOPS`,
default `0`): how many reverse proxies in front of the server append to
`X-Forwarded-For`, so per-client rate limits see the client, not the proxy.

Run `forge generate`. The identity fills `idp_login_policy`; no `config.k`
line names it.

## 2. Edit `internal/app/login_broker.go`

1. **Route constants** — after the `const (...)` block of `LoginPath` etc.:

   ```go
   const (
   	PasskeyBeginPath          = devidp.PasskeyBeginPath
   	PasskeyFinishPath         = devidp.PasskeyFinishPath
   	PasskeyRegisterBeginPath  = devidp.PasskeyRegisterBeginPath
   	PasskeyRegisterFinishPath = devidp.PasskeyRegisterFinishPath
   	PasskeysPath              = devidp.PasskeysPath
   	MagicRequestPath          = devidp.MagicRequestPath
   	MagicConsumePath          = devidp.MagicConsumePath
   	SocialStartPath           = devidp.SocialStartPath
   	SocialCallbackPath        = devidp.SocialCallbackPath
   	InviteAcceptPath          = devidp.InviteAcceptPath
   	VerifyPath                = devidp.VerifyPath
   	VerifyResendPath          = devidp.VerifyResendPath
   )
   ```

2. **Guard every route, then parse the policy** — rename
   `RegisterLoginBroker`'s `mux *http.ServeMux` parameter to `appMux`, and
   make its first lines:

   ```go
   // Every route refuses a cross-site request (a text/plain POST from
   // another site needs no preflight and would create accounts).
   mux := devidp.GuardMux(appMux, appBaseFrom(cfg.IdpLoginUri))
   policy, policyErr := devidp.ParseLoginPolicy(cfg.IdpLoginPolicy)
   if policyErr != nil {
   	logger.Error("idp_login_policy is unusable; sign-up is OFF and every session needs a verified address until it is fixed", "error", policyErr)
   }
   ```

3. **The struct** — add to `loginBroker`, and rename the existing
   `policy policyCache` field to `passwordRules policyCache` (and its uses in
   `passwordPolicy`):

   ```go
   	legacySelfSignup bool
   	policy           devidp.LoginPolicy
   	methods          *devidp.SignInRoutes
   	proxyHops        int
   ```

   and set `proxyHops: int(cfg.TrustedProxyHops),` in the literal. Replace
   `clientIP(r)` in `handleForgot` with `devidp.ClientIP(r, b.proxyHops)` and
   delete the local `clientIP`.

4. **Construction** — in the `&loginBroker{...}` literal: rename
   `selfSignup:` to `legacySelfSignup:`, add `policy: policy,`, and add
   `RequireVerifiedEmail: policy.RequireVerifiedEmail || policyErr != nil,` to the
   `devidp.FlowConfig`. After the literal, before the first `mux.Handle`:

   ```go
   broker.selfSignup = broker.legacySelfSignup
   if policyErr != nil {
   	broker.selfSignup = false // an unusable policy fails closed
   }
   if policy.Present {
   	broker.selfSignup = policy.RegisterServed()
   }
   broker.mountSignInMethods(mux, cfg)
   ```

5. **`mountSignInMethods`** — copy the method of that name from the scratch
   project's `internal/app/login_broker.go`. It builds
   `devidp.NewSignInRoutes` from the broker's client, flow, policy and
   `appBase`, with `SignedIn` calling `b.setSession`, `SessionToken`
   reading `SessionCookieName` and `TrustedProxyHops: b.proxyHops`; it takes
   `mux *devidp.GuardedMux` and mounts each route inside
   `if b.policy.Offers(...)` / `if len(b.policy.Social) > 0`. Set
   `ProjectID: cfg.JwtAudience` if your config declares `jwt_audience`
   (the project a `signup_role` is granted on).

6. **Options** — `handleOptions` becomes:

   ```go
   opts := devidp.OptionsFor(b.policy, b.selfSignup)
   opts.SelfSignup = b.selfSignup
   writeAuthJSON(w, http.StatusOK, opts)
   ```

   and delete the `authOptions` type.

7. **Sign-up** — first lines of `handleRegister`:

   ```go
   if b.methods != nil {
   	b.methods.HandleRegister(w, r)
   	return
   }
   ```

8. **Unverified address** — in `handleLogin`, before `if err != nil {`:

   ```go
   if errors.Is(err, devidp.ErrEmailUnverified) {
   	writeAuthJSON(w, http.StatusForbidden, map[string]string{
   		"error": "Confirm your email address first: use the link we sent, or ask for a new one.",
   		"code":  "email_unverified",
   	})
   	return
   }
   ```

9. **Password rules** — first lines of `passwordPolicy` (a hosted instance's
   Settings API is not routed; the document carries the rules):

   ```go
   if p, ok := b.policy.PasswordPolicy(); ok {
   	return p, nil
   }
   ```

   If you carry a text-matching workaround for the 404 from before v1.1.0,
   drop it: `errors.Is(err, devidp.ErrPasswordPolicyUnavailable)` is the check.

10. **Passwords follow the policy** — copy `refusePassword` from the scratch
    project's broker, and call it first in `handleLogin`, `handleForgot` and
    `handleReset`:

    ```go
    if b.refusePassword(w) {
    	return
    }
    ```

    A reset sets a password and signs in with it, so guarding only
    `/auth/login` leaves a password sign-in a passkey-only or social-only
    policy excluded.

11. **A host-only session cookie.** In `pkg/middleware/middleware.go` add,
    beside `SessionCookieName`:

    ```go
    const SecureSessionCookieName = "__Host-" + SessionCookieName
    ```

    Outside local development read ONLY that name: a plain-named session
    cookie there can only have been planted by a sibling site. Decide it from
    config, never from a request header (a proxy may not send
    `X-Forwarded-Proto`). Add to `AuthDeps`:

    ```go
    // The plain-named session cookie is read only when this is true
    // (local development, where the broker serves plain http).
    DevSessionCookie bool
    ```

    make `credentialFrom` read `SecureSessionCookieName` instead of
    `SessionCookieName`, add a `devCredentialFrom` that falls back to the
    plain name (copy it from the scratch project), and in
    `NewAuthInterceptor` pick the reader:

    ```go
    extract := credentialFrom
    if deps.DevSessionCookie {
    	extract = devCredentialFrom
    }
    // … ExtractToken: extract,
    ```

    `forge generate` then sets `authDeps.DevSessionCookie = d.Config.Environment == "development"`
    in the generated providers (graph projects). In an owned
    `cmd/<bin>/cmd/serve.go`, add after `authDeps.Validate = …`:
    `authDeps.DevSessionCookie = cfg.Environment == serverkit.EnvDevelopment`.

    In the broker add `const SecureSessionCookieName = middleware.SecureSessionCookieName`
    and a `sessionCookie()` method returning it when `b.secureCookie`, and use
    `b.sessionCookie()` wherever the session cookie is set, read or cleared.
    Signed-in users sign in once more after the deploy.

12. **Sign-up proves the address before anything is set (forge#2121).**
    Since this release no credential is put on an unverified account, and a
    proof never verifies an account that holds one. `devidp.SignInRoutes`
    enforces that on its own (it works on a copy of your client with the
    rule on), so a broker that mounts them gets it with the forge bump.
    Three things to re-wire by hand:

    - **Turn the rule on for the broker's own calls.** In
      `RegisterLoginBroker`, add to the `&devidp.Client{…}` literal:

      ```go
      // No call this broker makes may add a credential to an account
      // whose address is unverified (forge#2121).
      ProvenCredentialsOnly: true,
      ```

      With it, every client call that adds a credential (`SetPassword`,
      the passkey calls, `AddIDPLink`, `EnsureOTPEmail`, `CreateUser` with a
      password or links) refuses an unverified account with
      `devidp.ErrUnverifiedAccount`, and refuses an account it could not
      read (fail closed; an unknown account still answers like a wrong reset
      code). It is OFF by default, so code of your own that relies on the old
      behaviour is unchanged until you opt in. A contact-only account's own
      password reset still works, and so does a reset code the person
      presents for an unverified account holding NO credential (a mailed
      reset link, or a console invitation's first-password code): the code
      proves the address, so `ResetPassword` sets the password and marks the
      address verified. One holding a credential nobody proved is refused.
    - **Adopt before you invite.** Code of your own that hands an EXISTING
      account its first sign-in path — an invitation or console flow that
      reuses a user found by email and then issues a password-reset or
      return code — must call `devidp.Client.AdoptPendingAccount(ctx,
      userID)` first. It marks an unverified, credential-less account's
      address verified (the administrator asserts it, as when creating the
      account verified) and refuses, unchanged, one that holds credentials
      (`devidp.ErrAccountHoldsCredentials`: a squatter; move it aside). A
      first-password reset code still redeems without it (above), but a
      passkey code or a social link does not, and adopting first is what
      surfaces a squatter to the administrator instead of to the invitee.
      Known caller: control-plane's `identityapi` Invite. forge's own
      identity converge already does this for declared members.
    - **Drop the proof lock** if you applied it from an earlier version of
      this step: delete `proofLockerFor` (with its `pgx` and `dbtrace`
      imports) and the `ProofLocker:` line in `mountSignInMethods`.
      `SignInRoutes` still takes a supplied locker where it always did (and
      still refuses when it cannot be taken), but nothing needs it any more.
      `devidp.NewPostgresProofLocker` and `UnavailableProofLocker` are
      deprecated and keep working exactly as before.
    - **Serve sign-up through the LOGIN_POLICY, with mail.** The legacy
      sign-up (`auth_self_signup` outside development) and a policy whose
      `email` is `delivery = "none"` create the account WITH its password,
      because no mail can prove the address. Its email is then a contact
      field: no later proof claims that account (`forge.contact_only`), and
      once mail is on, the address's owner gets a fresh account instead.
      `forge lint` names each such environment (`forgeconv-unproven-signup`).
      Declare `signup = "open"` (or `"domain"`) on `forge.ManagedIdentity`
      with mail delivered (the default), and leave `auth_self_signup` unset.
    - **Show the two new refusals.** `POST /auth/verify` answers
      `new_link_required` (409) for a link that names an account holding
      credentials nobody proved — a fresh link is mailed in the same request
      — and `POST /auth/invite/accept` answers `account_needs_admin` (409)
      for an invitation naming such an account. Both carry a `message` the
      scaffolded forms already display; a hand-written form must show it.

    **Roll it out in one step.** A broker from before this release takes an
    unverified account's credentials back and verifies it; this one leaves
    such an account untouched and proves its owner into a fresh account. If
    replicas of both versions serve the same users at once, an old replica
    can verify an account a new one has already superseded, and the two
    then both claim the address (the new broker leaves that pair alone, for
    an administrator). Deploy every replica of the broker in one step, or
    drain the old replicas' sign-in traffic before the new ones serve it.
    `SignInRoutesConfig.ProofLocker` is deprecated but still taken when set,
    so a broker that keeps its lock during the rollout keeps its behaviour.

    No database, table or setting is involved. `forge lint` cannot see the
    client option; check the literal by hand.

## 3. Add the frontend pages

From the scratch project's frontend (same kind), copy:

- `src/lib/auth/native-login.ts` (or merge: `AuthOptions` grew `signup`,
  `methods`, `social`, `mail`; `AuthError` grew `code`; `signUp` may answer
  `VerifySent`; the passkey, magic-link, social, invite and verify calls are
  appended) and `src/lib/auth/use-auth-options.ts`;
- `src/components/{sign-in-methods,magic-link-form,verify-email-form,accept-invite-form,social-callback,passkeys-panel}.tsx`;
- the shared components those import that an older scaffold lacks or has
  in an older shape: `src/components/auth-ui.tsx` (new: `AuthCard`,
  `AuthInput`, `PasswordInput`, `AuthAlert`, `AuthDivider`, `AuthLink`,
  imported by every component above but `passkeys-panel`) and
  `src/components/password-requirements.tsx` (`PasswordRequirements` grew
  the `attempted` prop `accept-invite-form` passes). Both need only
  `lucide-react` and `@/components/ui/input`, which every scaffold has.
  After copying, the frontend's typecheck (`npx tsc --noEmit`) must pass:
  an unresolved `@/components/…` import names a file still to copy;
- Next.js: `src/app/auth/{magic,invite,verify,social/callback}/page.tsx`.
  Vite: the `magicRoute`, `inviteRoute`, `verifyRoute` and
  `socialCallbackRoute` blocks of `src/routes.tsx`, and their entries in
  `addChildren`;
- `src/lib/auth/route-guard.tsx`'s `EMAIL_LINK_PATHS` (the pages are public);
- `src/lib/auth/return-to.ts` (every navigation to a returned path goes
  through `safeReturnTo`);
- in the sign-in screen: render `<SignInMethods options={options} …/>`, hide
  the password field when `options.methods` lacks `"password"`, show
  `created.message` when `isVerifySent(created)`, and `<UnverifiedNotice>`
  on `err.code === "email_unverified"`.

## 4. Check

`forge lint` reports no `forgeconv-identity-methods`. `forge env up dev`, then
`GET /auth/options` lists the declared methods.

## 5. After deploying to a hosted environment

Local dev runs the dev issuer; a hosted environment runs real Zitadel behind
the gateway, with the broker's own service account. Check each method the
declaration turns on, in that environment, right after the deploy:

1. **Options.** `GET <app>/auth/options` lists exactly the declared
   `methods`, `social` and `signup`. A policy the broker could not parse logs
   `idp_login_policy is unusable` at start-up and serves sign-up OFF.
2. **Broker permissions.** Search the server's logs for `PERMISSION_DENIED`
   or a 403 from the issuer while you run the flows below. The broker's
   account must create users, verify email and invite codes, set passwords,
   read, set and delete user metadata, change a user's username and email (a
   fresh account taking an address over), list identity-provider links and
   passkeys, and search and delete sessions. Any refusal here is a missing grant on that account, not a code
   defect.
3. **Each method, with a throwaway address:** a passkey enrols and signs in
   again in a fresh browser; a magic link signs in and is refused on a second
   use; each social provider round-trips and lands signed in; with `open` or
   `domain` sign-up, register, follow the verification mail, and sign in; an
   invitation accepted with each offered method signs in.
4. **Sign-up role.** With `signup_role` set, the account that public sign-up
   created holds that role on the project once its address is verified
   (Zitadel console → the user → Authorizations); an invited account does not.
5. **Planted credentials never authenticate.** In the Zitadel console create
   an UNVERIFIED user for a throwaway address and give it a password, as an
   attacker's pre-registration (or an older broker) would. Then, from the
   address's inbox, sign up or ask for a magic link:
   - the mail links to a NEW user (username `forge-fresh-…`) with no
     credential, and the planted user is untouched until that link is used;
   - after it is used, the new user has the address as its username and is
     verified; the planted user is renamed (`forge-superseded-<id>`, email
     `superseded-<id>@forge.invalid`, metadata `forge.superseded_by` and
     `forge.superseded_email`), keeps every credential, and is still
     unverified, so it cannot sign in;
   - the planted password does not sign in by the address.
6. **Counting what is left.** `devidp.Client.UnprovenAccounts` lists every
   unverified account holding a credential (read-only): those an older
   broker left, and contact-only ones. Nothing deletes them; decide case by
   case.

### Rolling back

- **Turn the new methods off without a release:** remove `methods`,
  `social` and the non-invite `signup` from the `forge.ManagedIdentity`
  declaration and converge. The delivered `idp_login_policy` then offers
  password sign-in by invitation only, and the broker stops serving every
  other route. To fall back to the legacy behaviour entirely, deliver no
  document (an empty `IDP_LOGIN_POLICY`).
- **Revert the broker:** redeploy the previous release. The new code wrote
  only account metadata (`forge.self_signup`, `forge.signup_role_granted`,
  `forge.contact_only`, `forge.supersedes`, `forge.first_password.*`, `forge.first_password_disqualified`, `forge.superseded_by`,
  `forge.superseded_email`), which the old broker ignores, and renamed the
  users a fresh account superseded. Signed-in users sign in once more,
  because the session cookie name changed (step 2.11).
- **Accounts an older broker left locked.** A broker before forge#2121's fix
  locked an account whose credentials it could not take back
  (`alert=identity.revoke_failed`). This one never locks or revokes. Such an
  account is unverified and holds credentials: its owner proves into a fresh
  account as above, and the locked one is moved aside. Its `forge.proof_lock`
  and `forge.proof_window.*` metadata are inert; delete them at will.
