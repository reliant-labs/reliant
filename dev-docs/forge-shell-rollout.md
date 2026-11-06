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

## The demo (demo.reliantlabs.io) is not this bundle

`demo.reliantlabs.io` is its own build and its own env: the `forge-demo`
frontend of reliant's `demo` env (`deploy/kcl/demo/main.k`), built from
`frontends/web/demo.html` (`npm run build:demo`, or `WEB_BUILD_TARGET=demo`,
which is how forge builds it). That bundle is always the demo shell
(`__WEB_TARGET__`, `lib/shell.ts`), mounts the forge console on sample data and
contains no Reliant chat, onboarding, sign-in, Supabase, daemon or settings
code (`vite-plugin-demo-bundle.ts` fails a build that would). No host of
`prod/reliant-web` mounts the demo shell; `?shell=demo` still does, for a look
in local dev.

Its runtime config is the env's (`DEMO_DATA_SOURCE`, `DEMO_DATA_URL`,
`DEMO_TELEMETRY`, `OTEL_ENDPOINT=/_otel`, `OTEL_REPLAY`); it names no API, no
auth mode and no key. It needs no control-plane CORS entry and no GoTrue
redirect entry: it calls no backend.

`DEMO_APP_HOST` (proto field 24) is deprecated: `prod/reliant-web` still
accepts it, so an existing config stays valid, but nothing reads it.

**Order.** This document's change (no host of `prod/reliant-web` mounts the
demo shell) ships only AFTER the cutover below has passed its checks: until
then, `prod/reliant-web` is the cutover's rollback target, and it must still
serve the demo shell on that host. The full runbook, with every check, is
`dev-docs/forge-demo-cutover.md`.

Ship and bind it from a reliant checkout of `main` (owner, tenant-zero
credential):

```bash
reliant forge env build demo --target forge-demo --release <v>
reliant forge env deploy demo <v>
reliant forge obs check demo --wait --json   # 0 good · 1 regressed · 2 not proven good · 8 not enough data yet
reliant forge domain bind demo.reliantlabs.io --env demo --target forge-demo
reliant forge domain show demo.reliantlabs.io --env demo
node frontends/web/scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
```

**Checks** (all must pass before the change above is released):

```bash
curl -sI https://demo.reliantlabs.io/ | grep -i 'via: 1.1 google'   # served through the edge
curl -s  https://demo.reliantlabs.io/ | grep -o '<title>[^<]*'      # forge demo · Reliant
curl -s  https://demo.reliantlabs.io/config.js                      # only the DEMO_* / OTEL_* keys
node frontends/web/scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
```

**Rollback of the cutover** (if a check fails). The domain's verification and
certificate belong to the domain, not to the binding, so moving it back is
instant and needs no DNS change. It restores exactly what served before: the
build `prod/reliant-web` runs, which still mounts the demo shell on this host as
long as this change has not been released.

```bash
cd ../control-plane
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain bind demo.reliantlabs.io --env prod --target reliant-web
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain show demo.reliantlabs.io --env prod   # serving: reliant-web
```

Then, from this checkout, `node frontends/web/scripts/demo/smoke-demo-site.mjs
https://demo.reliantlabs.io` (it fails a page without `data-shell="demo"`).

To roll back only the demo env's own release, deploy its previous release:
`reliant forge env status demo` lists them, then
`reliant forge env deploy demo <previous version>` (a static site has no
migrations, so that is a complete rollback).

**If this change was already released and the demo env then fails**, binding
the domain back to `prod/reliant-web` serves the Reliant shell there, not the
demo: roll back the demo env's release instead (above), or revert this change
and release before binding back.
