---
name: frontend
description: The browser half of authentication — native sign-in (the browser POSTs credentials to the app's own API and gets an HttpOnly session cookie; the server runs the whole OIDC flow), the scaffolded sign-in screen and route guard, why there is no token in JavaScript, and what React Native does instead.
---

# Frontend auth wiring (native sign-in)

**THE BROWSER NEVER CONTACTS THE IDENTITY PROVIDER.** Not `/authorize`, not the discovery document, not the token endpoint, not a hidden iframe. The browser POSTs an email and a password to **this app's own API** and gets back an HttpOnly session cookie. Your server runs all five OIDC steps against the issuer (`internal/app/login_broker.go`, over `forge/pkg/devidp`).

```
browser  ── email + password ──▶  your server ──▶ identity provider
         ◀── HttpOnly cookie ───┘                (the whole OIDC flow)
```

Seven endpoints, and that is the entire contract the browser knows about:

| call | endpoint | answers with |
| --- | --- | --- |
| sign in | `POST /auth/login` | cookie set, identity returned |
| sign up | `POST /auth/register` | account created **and** signed in, in one call. **Public by default** (see below) |
| who am I | `GET /auth/session` | the identity, or not-authenticated |
| sign out | `POST /auth/logout` | cookie cleared |
| password rules | `GET /auth/password-policy` | `{min_length, has_uppercase, has_lowercase, has_number, has_symbol}`, the identity provider's own policy |
| forgot password | `POST /auth/forgot` | `202`, the same answer for any address; asks the provider to email a reset link |
| reset password | `POST /auth/reset` | `{userID, code, password}` redeemed; cookie set when the follow-up sign-in works |

## What the scaffold ships

- **`src/lib/auth/native-login.ts`** — the only file that talks to those endpoints. It knows nothing about OIDC: no issuer, no client id, no PKCE, no redirect, no token parsing.
- **`src/app/auth/sign-in/page.tsx`** (Next.js) / the `/auth/sign-in` route in `src/routes.tsx` (Vite) — an ordinary form. Deliberately plain starter code; most products restyle sign-in heavily.
- **`src/components/{password-requirements,forgot-password-form,reset-password-form}.tsx`** and the `/auth/forgot` and `/auth/reset` routes (`src/app/auth/{forgot,reset}/page.tsx` in Next.js, `forgotRoute` / `resetRoute` in `src/routes.tsx` in Vite). Static-export safe: `/auth/reset?userID=…&code=…&orgID=…` reads its query in the browser, which is the shape of the link the control plane gives the provider as its reset template.
- **`src/lib/auth/route-guard.tsx`** — what an anonymous visitor sees. Without it a signed-out visitor gets the full app shell and then a wall of 401s, which looks like a broken app rather than one asking you to sign in.
- **`src/lib/auth/context.tsx`** (the provider) and **`src/lib/auth/use-auth.ts`** — `useAuth()`, exposing `{ identity, isAuthenticated, isLoading, refresh, logout }`. In mock mode (`MOCK_API` set to `"true"` or `"hybrid"`) it never calls `GET /auth/session`. The identity is instead the fixture user from `createSessionAuthProvider()` in `src/lib/auth/session-provider.ts`, gated on that file's `isMockMode()`. In hybrid the fixture is for the UI only, and forwarded RPCs carry only the browser's real cookie. See the `frontend` skill's mock-mode table.
- **`src/components/session_nav.tsx`** — the signed-in-user dropdown with a sign-out.

There is no `/auth/callback`. It belonged to the browser redirect flow — the issuer sent the browser back there with a code — and with the exchange moved server-side there is nothing for it to do. The auth routes are sign-in, forgot and reset.

## Password requirements

The provider's complexity policy is **per instance**, so the app asks rather than guessing: the broker reads `GET /v2/settings/password/complexity` (readable with the broker's own token) and serves it, cached for a minute, at the unauthenticated `GET /auth/password-policy`. The sign-up and reset forms render it as a checklist **before** the person types, tick rules off as they type, and on a rejection the server answers `400 {error, code: "password_policy", unmet: [...]}` naming **every** rule broken (the provider itself reports only the first). The client checks are guidance; the provider decides. If the policy cannot be read the forms still work, without the checklist.

## Password reset, and when the email actually arrives

`POST /auth/forgot` asks the provider to **email** a link to `/auth/reset`. A customer instance sends mail **only if the environment declares `smtp`** (`forge.ManagedIdentity.smtp`); without it the request succeeds and nothing is delivered, and the provider does not tell the app. The forgot screen says so. It answers identically for an unknown address (no account oracle) and is limited to 3 requests per address and 10 per client IP per hour, in process. In development (no mail server) the broker mints the code and logs the link; it never returns the code. To deliver by another channel, swap `SendPasswordResetLink` for `devidp.MintPasswordResetCode` in `handleForgot`; the code is the credential, so send it only to the address on file. To turn reset off, delete the two broker routes, the routes/pages and the "Forgot your password?" link.

## Invite-only apps

`POST /auth/register` is mounted by default, so **anyone who reaches the app can create an account**. For an invite-only product delete that route in `login_broker.go` and the "Create an account" toggle in the sign-in screen. There is **no scaffolded invite flow**; see `deploy/managed-identity` for how to build one.

## Why the token is not in JavaScript, and what you get instead

The session cookie is **HttpOnly**, so no script can read it. That is the property the design exists for rather than a limitation: **a token no script can reach is a token an XSS cannot steal.** The cookie rides along automatically on same-origin requests, so nothing has to set an `Authorization` header — and `useAuth()` correctly exposes **no `getToken`**. The only honest implementation would return null, and every caller that trusted it would ship broken.

What you get is an **`Identity`**: `{ authenticated, email?, name?, subject? }`. **Presentation only.** Use it to decide what to SHOW (hide an admin tab, render a name). Never to decide what is ALLOWED — the server validates the real token on every request, and a caller who edits anything in the browser changes the UI and nothing else.

**Where the auth calls go.** `native-login.ts` asks `src/lib/api-base.ts`, the same module the transport asks. Under the dev server that is the page's own origin, `<basePath>/_api/auth/login` and so on, which `next.config.ts` (or `vite.config.ts`) proxies to the API. The API's `Set-Cookie` (`Path=/`, no `Domain`, `SameSite=Lax`, not `Secure` in development) passes through unchanged, so the session cookie is first-party to whatever host the page was opened on: localhost, a Reliant workspace preview URL, or a LAN IP. Every later `/_api` RPC carries it. In a build the calls go to `API_URL` from `config.js`, and an empty value means same-origin. See `frontend/serving`.

`credentials: "include"` on every auth `fetch` is load-bearing and easy to lose. Whenever the API is on another origin (a deployed `API_URL` naming its own host, or the dev loop with `FORGE_DEV_API_PROXY=off`, where the API is on its own port), the default (`"same-origin"`) makes the browser **silently drop the `Set-Cookie`**: login returns 200, the identity comes back, and the user stays signed out with nothing in the console to explain it. The server pairs it with `cors_allow_credentials`. Through the dev proxy it costs nothing.

The same holds for every RPC after sign-in, on both sides:

- **The transport** (`src/lib/connect.ts`) builds every real Connect transport on `fetchWithSession`, a fetch that sends credentials, against the base `src/lib/api-base.ts` chooses. Without it the session cookie never reaches an API on another origin.
- **The server's auth interceptor** (`pkg/middleware/middleware.go`, `credentialFrom`) reads an `Authorization: Bearer` header when one is sent — a service, a CLI, a test — and otherwise the session cookie, `SessionCookieName`. That constant is declared there and `internal/app/login_broker.go` sets the cookie under it, so the name the broker writes and the name the interceptor reads are one value. An interceptor that read only the header answered every RPC after a successful sign-in with 401 "missing Authorization header".

## Why this rather than the browser-side PKCE flow it replaced

Four reasons, each of which was a real limit of the old design:

- **The IdP needs no public origin.** It can sit on a private network, behind a VPN, or bound to loopback. A browser flow requires every user's browser to reach the issuer, which is what forces a self-hosted IdP to be exposed.
- **No tokens in JavaScript.** Access and refresh tokens are created and held server-side. There is nothing in `window` for an XSS to steal.
- **No third-party cookies.** The silent-restore iframe a browser flow depends on is already unreliable and is being switched off by browsers. This design has no equivalent to lose.
- **One origin.** No CORS with the issuer, and no redirect-URI allowlist to keep in step with every deployment.

Staying signed in is likewise a server concern now: the refresh token lives with the broker, so there is no browser-side refresh timer, no single-flight coalescing to get right, and no rotated-token bookkeeping in the bundle.

## Config: the issuer values are SERVER-side now

`oidc_client_id`, `oidc_redirect_uri`, `oidc_scopes` and `jwt_issuer` are read by the **broker**, not by the bundle. `oidc_redirect_uri` is still required even though no browser is redirected — the issuer validates it against the app's registration on the auth request the server makes. Do not add them to the frontend's public env; the browser has no use for them.

The one browser-side config that matters is `API_URL` (empty means same-origin, the common case), read through the generated typed module `src/lib/config_gen.ts`.

**`idp_broker_token` is a real credential and is the reason this file is server-side.** It carries the issuer's authority to create sessions. It must never reach a bundle, a `NEXT_PUBLIC_*`/`VITE_*` name, or a log line.

**Rotation:** where the issuer's operator rotates it (a managed identity), deliver the token as a file and set `idp_broker_token_file` (`IDP_BROKER_TOKEN_FILE`) instead. The broker's `devidp.Client` reads the file on every call, so a rotated token is used on the next sign-in with no restart; `idp_broker_token` stays supported and is the fallback. See `auth/shared-issuer`.

## The one thing not to change carelessly

The issuer treats "which credentials were checked" as the **caller's** decision. A session created with a user check and NO password check is valid, completes an auth request, and yields real tokens — for someone who proved nothing. Such a token carries no `amr` claim at all, so a downstream validator cannot even detect it.

`devidp.CreateSession` refuses a credential set with no verifying factor before it makes any network call, and the broker builds its request from **typed fields it reads itself**. Do not "simplify" either by forwarding a request body straight through: that single change turns a login form into an impersonation endpoint, and nothing in the response would look wrong.

## Public frontends (`--auth-mode none`)

`forge scaffold frontend --auth-mode none` scaffolds a frontend with **no
sign-in gate**: no route guard around the app and no `/auth/sign-in` screen. It
is what the command picks by default when `deploy/kcl/dev/main.k` declares no
identity provider — a gated frontend there could only redirect every page to a
form nothing can complete. `--auth-mode native|none` overrides the default
either way.

The choice is **not recorded in `forge.yaml`** (there is no `auth_mode` key). It
is visible in the files: a public frontend simply has no `RouteGuard` in
`providers.tsx`.

It changes nothing on the server: an RPC that requires a caller still answers
401. To gate a public frontend later, wrap the app in `RouteGuard` from
`src/lib/auth/route-guard.tsx` (still emitted, unused) and add a sign-in screen —
`forge scaffold frontend <tmp> --auth-mode native` in a scratch checkout shows
the exact files.

## React Native is different, and ships a mock

RN has neither half of what makes native sign-in work: no automatic cookie jar on `fetch`, and no same-origin. Shipping the web flow there produces code that **typechecks** (RN's tsconfig includes the DOM lib) and silently never authenticates — every call returns 200 and the next one is anonymous. So `native-login.ts` and `route-guard.tsx` are deliberately not emitted into an RN tree.

What RN gets instead is the credential seam: `AuthProvider` in `src/lib/auth/provider.ts` — `getToken` / `getUser` / `logout` — with `createSessionAuthProvider()` in `src/lib/auth/session-provider.ts` as the scaffolded implementation. It holds no real credential: in mock mode the session is a **fixture** so scaffolded UI has a user to render; against a real backend it is signed out and warns on first use.

To give an RN app real sign-in, implement `AuthProvider` over your IdP's **native SDK** (or a token you obtain and store yourself), use it in `src/lib/auth/context.tsx`, and feed the same object's `getToken` to the transport in `app/_layout.tsx` via `setAuthTokenGetter`. **One credential, one source** — the UI and the requests must read the same object, or they can disagree about who is signed in. The scaffold wires no token bridge by default, on purpose: attaching the fixture token would send an `Authorization` header every real backend rejects, making a 401 look like a server fault rather than an unwired app.

`useAuth()`'s value is identical on every platform, so a component written against it moves between the mobile and browser trees unchanged.

## Using a hosted IdP's own sign-in UI instead

Native sign-in is the scaffolded path, not the only one. If you would rather use a vendor's hosted pages or SDK widget (Clerk, Auth0, Firebase), that flow owns the browser session itself: drop the broker routes and the sign-in screen, mount the vendor's components, and have `context.tsx` read its identity from the vendor's SDK. Swap the backend validator to match (`auth.Clerk(...)` / `auth.Firebase(...)` in `SetupAuth`) and point both halves at the same issuer.

The trade you are making: hosted pages give you MFA, social sign-in and password reset for free, and cost you the branding. Native sign-in keeps the user inside your product and keeps the IdP off the public internet, and you own the screens.

## Rules

- **The browser never contacts the identity provider.** If a frontend file grows an issuer URL, a client id, or a redirect, the flow has been half-reverted — and a tree with two ways to become authenticated only protects the token on one of them.
- **No token in the bundle.** `useAuth()` exposes none, `native-login.ts` stores none, and nothing may put one in `localStorage`, `sessionStorage` or `document.cookie` — anything written there is script-readable, which is the exact exposure the HttpOnly cookie removes.
- **`credentials: "include"`, always**, on every auth fetch. The default silently drops the session cookie cross-origin.
- Identity is for RENDERING. Authorization is the server's, on every request.
- Never put a credential in a bundle's public env (`NEXT_PUBLIC_*` / `VITE_*` / `EXPO_PUBLIC_*`) — those are inlined at build time and readable by anyone who loads the page.
- `returnTo` on the sign-in screen is attacker-supplied: accept absolute same-origin paths only. `//evil.com` is protocol-relative, so the second character matters as much as the first.
- The access token your backend validates must be a **JWT**. Some IdPs issue an OPAQUE access token unless the client requests a registered API resource/audience — an opaque string will never validate against a JWKS, however correct the rest of the wiring is.
