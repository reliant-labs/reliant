# Activating the forge shell host (forge.reliantlabs.io)

The forge shell is the same `reliant-web` bundle as `app.reliantlabs.io`,
picked by hostname (`frontends/web/src/lib/shell.ts`, runtime config
`FORGE_APP_HOST`, default `forge.reliantlabs.io`). Shipping the bundle changes
nothing for `app.reliantlabs.io` except one link: **"Open Forge console"** on
`/apps` (web only), which points at `https://<FORGE_APP_HOST>`.

The host itself works only once four things outside this repo are configured.
This page is the order to do them in, how to prove each one live, and how to
back out.

## Prerequisites (external, all additive)

All live in control-plane unless noted; none changes what an existing origin
may do.

1. **Static binding, DNS and TLS.** Bind `forge.reliantlabs.io` as a custom
   domain of prod's `reliant-web` static site (the same site as
   `app.reliantlabs.io`, control-plane `deploy/kcl/prod/main.k`
   `rw.reliant_web`), and point its DNS record where the binding says.
   The certificate is issued by the hosting platform once DNS resolves.
2. **CORS.** Add `https://forge.reliantlabs.io` to `_cors_origins` in
   control-plane `deploy/kcl/prod/main.k` (read by platform-api and
   reliant-api-server; `lib/invariants.k` checks the coupling).
3. **Stripe returns.** Add `forge.reliantlabs.io` to `_allowed_redirect_hosts`
   in the same file, so billing in the forge shell's Settings can return there.
4. **Auth redirects (GoTrue).** Add `https://forge.reliantlabs.io/**` to the
   auth project's redirect URL allow-list (Supabase dashboard,
   Authentication › URL configuration). `getAppURL()` is the forge origin in
   the forge shell, so OAuth and magic-link `redirectTo` point there; a host
   missing from the list falls back to the site URL (`app.reliantlabs.io`) and
   the sign-in lands in the wrong shell.

## Order

1. Configure 2–4 and deploy control-plane prod (CORS and redirect hosts are
   environment variables on the servers). They are inert until a browser
   presents the new origin.
2. Configure 1 (binding, DNS, TLS).
3. Run **every check below** against `https://forge.reliantlabs.io`.
4. Only then let the release carrying this change reach prod's `reliant-web`.
   If it is already live (the link ships with the bundle), steps 1–3 must be
   done before announcing the host; until they pass, apply the rollback below.

## Checks (after each deploy that touches the host)

Run them in order; stop at the first failure and roll back.

1. **TLS and loading.**
   `curl -sSI https://forge.reliantlabs.io/` answers `200` with a valid
   certificate for that name (no `-k`), and the page body is the reliant-web
   `index.html`. In a browser, the page shows the "Forge by Reliant" brand, not
   Reliant's.
2. **Sign-in callback.** Sign in with email and with one OAuth provider from
   `https://forge.reliantlabs.io/`. The callback returns to
   `forge.reliantlabs.io` (not `app.reliantlabs.io`) and lands on the forge
   shell's Apps page signed in. A deep link (`/forge/p/<project>`) survives the
   sign-in.
3. **Authenticated API and CORS.** A CORS preflight from the new origin is
   allowed by both servers:
   `curl -sSI -X OPTIONS -H 'Origin: https://forge.reliantlabs.io' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: authorization,content-type' <admin-server origin>/controlplane.v1.AccessTokenService/ListMyTokens`
   returns `Access-Control-Allow-Origin: https://forge.reliantlabs.io`; the
   same against reliant-api-server. In the browser, Apps, Sites, Usage & spend
   and Settings › API tokens load with no CORS error in the console (and no
   `[browser:error]` lines in the session's telemetry for `forge-web`).
4. **Claim.** Make a claimable deploy (`forge env deploy <env> --claimable`
   from a signed-out forge CLI), open its claim link on the forge host, claim
   it, and confirm it lands in the claimed app's forge console.
5. **Billing return.** From Settings › Billing on the forge host, open the
   Stripe portal and return: the browser comes back to `forge.reliantlabs.io`.
6. **Reliant unchanged.** On `app.reliantlabs.io`, sign-in and `/apps` work as
   before, and "Open Forge console" opens the forge host signed in after one
   sign-in.

## Rollback

Each step undoes independently; do them in this order.

1. **Hide the host from Reliant users first.** Re-promote prod's previous
   `reliant-web` release (releases are promoted by digest, so the previous one
   is still in the registry) or revert this change on main and let the next
   release ship it. That removes the "Open Forge console" link; nothing else
   in Reliant depends on the forge host.
2. **Unbind the host.** Remove the `forge.reliantlabs.io` custom-domain binding
   (and its DNS record) from prod's `reliant-web` site, so the name stops
   serving a half-configured shell.
3. **Revert the external configuration** only if it is itself wrong: remove
   the origin from `_cors_origins` and `_allowed_redirect_hosts` and redeploy
   control-plane prod, and remove the redirect URL from the auth allow-list.
   These entries grant nothing to any other origin, so leaving them in place
   while the host is unbound is harmless.

After a rollback, re-run check 6 to confirm `app.reliantlabs.io` is unaffected.

## One session across app.* and forge.* (AUTH_COOKIE_DOMAIN)

Unset, each host keeps its own supabase-js session in localStorage and a user
signs in once per host. With `AUTH_COOKIE_DOMAIN = ".reliantlabs.io"` in prod's
`reliant-web` runtime config (control-plane `deploy/kcl/lib/reliant_web.k`, not
`lib/env.k`, whose document is also the desktop release config), the session
moves to chunked cookies `supabase-auth.0..n` on that domain (`Secure;
SameSite=Lax`, 400-day rolling lifetime) and is shared: a sign-in, refresh or
sign-out on one host applies to the other (lib/auth/sharedSession.ts).

- Only https pages inside the domain use it. Electron, dev servers, localhost,
  preview hosts and the demo host (and the demo shell) keep their own storage.
- The first load after it is set moves an existing localStorage session into
  the cookie, so nobody is signed out.
- The PKCE code verifier stays per origin; the OAuth round trip returns to the
  host that started it.
- **Every host under the domain receives and can read the session.** Before
  setting it, make sure no subdomain serves third-party or user content
  (research/2026-11-05-console-health/shared-auth-cookie.md lists them).
- Rollback: unset the key. Pages fall back to localStorage, which the
  migration emptied, so users sign in once more; the shared cookies expire
  unused (or clear them by signing out before the rollback ships).

## Demo host (demo.reliantlabs.io)

The same bundle mounts a third shell, `demo`, on DEMO_APP_HOST (default
`demo.reliantlabs.io`): one route, `/forge/demo`, on sample data, with every
other path redirecting there and the forge tour started on every page load
(`src/demoShellRoutes.tsx`, `lib/shell.ts`). It has no sign-in route, so it
needs **no** control-plane CORS entry, **no** GoTrue redirect entry and no new
static site: it is a third custom domain on `prod/reliant-web`.

Runtime config (all defaults are the owner's, 2026-11-05):

| Key | Default | What it does |
|---|---|---|
| `DEMO_APP_HOST` | `demo.reliantlabs.io` | the host that mounts the demo shell |
| `DEMO_TELEMETRY` | `replay` | `replay` / `analytics` / `off`, service `forge-demo`, `surface=demo`; never for a browser sending DNT or GPC |
| `DEMO_TOUR` | `short` | `short` (≤ 10 steps) or `full` |
| `DEMO_DATA_SOURCE` | `fixtures` | `fixtures`, or `remote` = the demo-data gateway at `DEMO_DATA_URL`, falling back to fixtures |
| `DEMO_DATA_URL` | unset | the gateway's base URL (phase 2) |

Owner actions, from `control-plane/` with the tenant-zero credential, after
the release that carries the demo shell is live:

```bash
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain add  demo.reliantlabs.io --env prod
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain bind demo.reliantlabs.io --env prod --target reliant-web
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain show demo.reliantlabs.io --env prod   # the exact records
```

Then, in Cloudflare (zone `reliantlabs.io`), exactly what `show` prints: the
`_reliant-challenge.demo` TXT (never delete it), the
`_acme-challenge_<id>.demo` CNAME, and `demo` as a DNS-only (grey cloud)
CNAME. Check with `reliant forge domain verify demo.reliantlabs.io --env prod`
until LIVE.

Checks, signed out: `/` lands on `/forge/demo?tour=forge:fg-console` with
`data-shell="demo"` on the root; the tour makes no request to any other origin
(with `DEMO_DATA_SOURCE=fixtures`); Resolve, assign and comment are disabled
with "Read-only demo"; a `POST /_otel/…` answers 2xx and ClickStack shows
`ServiceName='forge-demo'` page views.

Rollback: `reliant forge domain unbind demo.reliantlabs.io --env prod` (or
delete the `demo` CNAME). Reverting the shell code instead makes the host fall
back to the Reliant shell.
