# 03 — Auth / OAuth redirect surface: regression detection

Area: the redirect/callback surface for third-party identity (Google, GitHub,
Apple via Supabase; GitHub *connect*; Claude/Codex provider login), across
browser web, packaged Electron, dev Electron, and the `reliant auth serve` CLI
helper.

Analysis only — no product code touched. Prior art read and not re-derived:
`BRIEFING.md`, `OAUTH_REDIRECT_FINDINGS.md`, `ONBOARDING_FINDINGS.md`.

---

## TL;DR

There are **four structurally different OAuth flows** in this codebase, and they
are routinely discussed as if they were one. They are not: they use different
redirect URIs, register with different parties, and fail for different reasons.
Most of the historical bugs are one specific mistake repeated — *a redirect URI
was derived from the runtime instead of from configuration* — and the reason it
kept shipping is that the value only becomes wrong in a runtime CI never builds.

The single highest-value finding: **there is a fourth registry that no KCL, no
test, and no deploy gate knows about — the Supabase Auth project's own "Redirect
URLs" allow-list, and each provider's registered callback in the Google Cloud
console / GitHub OAuth app settings.** Every other coupling in this area
(`APP_URL` ↔ `ALLOWED_REDIRECT_HOSTS` ↔ `CORS_ORIGINS` ↔ `VITE_APP_URL` ↔
`github_redirect_uri`) is now expressed in one KCL file and is statically
checkable. The provider-side registration is not expressed anywhere in either
repo, and it is the only remaining input that can silently disagree.

My top recommendation is therefore a ~120-line **redirect-coupling invariant
check over the rendered KCL** (catches 4 historical bugs, runs pre-deploy, tiny)
plus a **post-deploy provider-acceptance probe** that resolves the "fourth
registry" problem without a live login. Everything else in my area is already
covered by existing tests or is not worth building.

---

## 1. The four flows (they are not variations of one flow)

Conflating these is the recurring analytical error, so they are named
explicitly. The load-bearing distinction is **who is the intended recipient of
the redirect**: a loopback redirect is *correct* when the local process is the
receiver, and *catastrophic* when it is published to a hosted provider as the
app's public address. `OAUTH_REDIRECT_FINDINGS.md` reached the same conclusion;
I confirmed it holds for all four.

### Flow A — Supabase sign-in / identity-link (Google, GitHub, Apple)

`web/src/lib/oauth-signin.ts` → `supabase.auth.signInWithOAuth({ redirectTo })`
→ provider consent → `/auth/callback` (`web/src/components/OAuthCallback.tsx`)
→ `exchangeCodeForSession`.

One code path, two surfaces, and the *only* difference is
`resolveRedirectTarget()`:

- If `window.electronAPI.startOAuthRedirect` exists (packaged/dev desktop with
  the loopback bridge), the desktop main process starts an RFC 8252 loopback
  listener (`electron/src/oauth-loopback.js`) and returns
  `http://127.0.0.1:<port>/auth/callback`. The listener 302s **back into the
  app's own origin**, so the code re-enters the same renderer route holding the
  PKCE verifier. Nothing is exchanged in the main process.
- Otherwise, `${getAppURL()}/auth/callback`.

`getAppURL()` (`web/src/lib/constants.ts`) is the fix for `72390d35`. Order:
`VITE_APP_URL` → `window.location.origin` *if usable for this runtime* →
hardcoded `DEFAULT_APP_URL = https://app.reliantlabs.io`. "Usable" is
runtime-dependent: in a browser the document origin is always right (including
`localhost:3000` dev); in Electron a loopback / `file:` / `app:` origin is
explicitly rejected, because the system browser that completes consent cannot
reach it.

### Flow B — GitHub *connect* (writes `git_credentials`) — a different flow entirely

Not Supabase. Backend-owned: `control-plane/internal/gitcredential/oauth.go`
serves `GET /auth/github/authorize`, validates a JWT, signs a state nonce, and
redirects to `github.com/login/oauth/authorize` with
`redirect_uri = cfg.GetGithub().GetGithubRedirectUri()` — a **server-side
config value**, not anything the client computes.

The callback lands on the *app*, not the backend
(`web/src/components/GitHubOAuthCallback.tsx` owns `/auth/github/callback`),
because Firebase Hosting SPA-rewrites cannot proxy to the GKE backend. The SPA
then calls `ExchangeGithubOAuthCode` (`73cad0c9`/`a2a00516`), which is
`auth_required: false` by design — identity rides in the HMAC-signed `state`,
not a bearer token. That is a deliberate and correctly-reasoned choice, not a
gap.

**Config fallback worth flagging** (`internal/app/providers.go:785`): an empty
`github_redirect_uri` silently falls back to
`http://localhost:<port>/auth/github/callback`. In a cloud env that is a
guaranteed `redirect_uri` mismatch that only manifests when a user clicks
Connect. It logs the value at startup but does not refuse to boot. This is the
exact shape of `6d74bc3d`/`d277c59e`.

### Flow C — Claude / Codex provider login (daemon-scoped credentials)

Genuinely loopback-by-design, and correctly so. Contract mirrored in two places:
`reliant/internal/auth/oauthcallback/oauthcallback.go` `InferConfig` (source of
truth) and `reliant/electron/src/oauth-contract.js` (JS mirror). Anthropic
registers `/callback` on an OS-assigned port; OpenAI registers `/auth/callback`
on **fixed port 1455**. These paths differ *because the providers registered
them differently* — unifying them breaks a provider.

This is the **best-defended surface in the whole area**:
`electron/test/oauth-contract.test.js` asserts the JS mirror against the Go
source *by reading the Go file*, so a one-sided edit fails the Electron suite.
That is the pattern the rest of this area lacks. `48969c66` (drain queued
waiters before tearing down the listener) lives here.

### Flow D — `reliant auth serve` (browser users doing Flow C)

`cmd/reliant/commands/auth_serve.go`, fixed port 19284, stateless bridge for the
localhost-callback gap that a browser tab cannot provide. CORS-allowlisted to
the hosted origin plus dynamically-allocated dev frontend ports.
`useOAuthAvailability` deliberately does **not** probe `/health` on mount —
probing loopback from a public origin triggers Chrome's Local Network Access
prompt. Notably, the Flow A loopback redirect does *not* trip that prompt,
because it is a top-level navigation rather than a background fetch; the
distinction is documented in `oauth-loopback.js` and is correct.

---

## 2. The runtime × provider matrix

`AppURL` = `getAppURL()` result. "Registered with" = the party that must have
the URI on file or the flow fails with `redirect_uri_mismatch`.

### Flow A — Supabase sign-in / link

| Runtime | Redirect URI sent | Source of that value | Must be registered with | Status |
|---|---|---|---|---|
| Browser, prod (`app.reliantlabs.io`) | `https://app.reliantlabs.io/auth/callback` | `VITE_APP_URL`, baked at build from KCL `reliant_endpoints("prod").app_url` | Supabase Auth redirect allow-list (prod project `dash.reliantlabs.io`) | **UNVERIFIED** — allow-list not in either repo |
| Browser, prod alt origin (`reliant-prod.web.app`) | `https://app.reliantlabs.io/auth/callback` | `VITE_APP_URL` wins over document origin | same | Works, but user lands on the *other* origin post-callback; session is per-origin. **UNVERIFIED cell.** |
| Browser, dev (`localhost:<alloc>`) | `http://localhost:<port>/auth/callback` | `VITE_APP_URL` from `dev/main.k` (`_reliant_web_port`) | Supabase nonprod project | Port is **dynamically allocated per worktree** — allow-list cannot enumerate it. See §3. |
| Electron packaged | `http://127.0.0.1:<ephemeral>/auth/callback` | `oauth-loopback.js` bridge (runtime) | Supabase (loopback is generally exempt from exact-port matching; **assumption, unverified**) | Fallback if bridge absent: `VITE_APP_URL` from `release.config.json` |
| Electron packaged, **pre-bridge build** | `https://app.reliantlabs.io/auth/callback` | `getAppURL()` refuses `app:`/loopback → hosted | Supabase prod | This is the `72390d35`/`3fcd9f79` fix path |
| Electron dev | `http://127.0.0.1:<ephemeral>/auth/callback` | bridge | Supabase nonprod | Pre-`VITE_APP_URL` this fell through to **prod's** callback from a local stack — documented in `lib/env.k:528-532` |
| CLI `auth serve` | n/a | — | — | Flow A does not use it |

### Flow B — GitHub connect

| Runtime | Redirect URI sent | Source | Registered with | Status |
|---|---|---|---|---|
| Browser prod | `https://app.reliantlabs.io/auth/github/callback` | `prod/config.k:18` → `GITHUB_REDIRECT_URI` (server-side) | GitHub OAuth app `Iv23liNim45KYgLY6DN9` | **UNVERIFIED** against GitHub app settings |
| Browser dev / dev-k8s | `http://localhost:3000/auth/github/callback` | `dev/config.k:21`, `dev-k8s/config.k:18` | GitHub OAuth app `Iv23li0hfjiDEzlJIPkA` | **Hardcoded :3000** while dev's frontend port is `fp.allocate_port`-allocated — see §3, live latent bug |
| Electron (any) | same as the browser value for its env | server config; client never computes it | same | Callback lands in the system browser at the app origin, not in the desktop window |
| e2e | falls back to `http://localhost:<cfg.Port>/auth/github/callback` | `providers.go:786` fallback; `e2e/main.k` sets client_id `""` so the handler is nil | — | Flow disabled, not exercised |
| CLI | n/a | — | — | — |

### Flow C — Claude / Codex provider login

| Runtime | Claude redirect | Codex redirect | Source | Status |
|---|---|---|---|---|
| Electron (packaged + dev) | `http://localhost:<OS port>/callback` | `http://localhost:1455/auth/callback` | `oauth-contract.js`, mirror-tested against Go | **VERIFIED** by `oauth-contract.test.js` |
| Browser + `auth serve` | same | same | `oauthcallback.InferConfig` | **VERIFIED** (same source of truth) |
| Browser without `auth serve` | — | — | `useOAuthAvailability` gates the UI | Correct; unavailable rather than broken |

### Where each value is decided

| Value | Decided at | By |
|---|---|---|
| `VITE_APP_URL` (Flow A web/Electron fallback) | **build time**, baked into the bundle | `control-plane/deploy/kcl/lib/env.k` `reliant_endpoints(env).app_url` |
| Loopback URI (Flow A desktop) | **runtime**, per sign-in | `electron/src/oauth-loopback.js`, OS-assigned port |
| `GITHUB_REDIRECT_URI` (Flow B) | **deploy time**, server env | `deploy/kcl/<env>/config.k` |
| `ALLOWED_REDIRECT_HOSTS` (Stripe return URLs) | **deploy time**, server env | `deploy/kcl/<env>/main.k` |
| `CORS_ORIGINS` | **deploy time**, server env | `deploy/kcl/<env>/main.k` |
| Claude/Codex paths (Flow C) | **compile time**, two mirrored constants | Go `InferConfig` + JS mirror |
| Packaged desktop's whole VITE set | **release time**, committed generated file | `electron/release.config.json` ← KCL, drift-gated in `pr-ci.yml` |

---

## 3. Cells that are currently unverified or actively suspect

Ranked by my confidence that they are wrong *now*.

**(a) `github_redirect_uri` hardcodes `localhost:3000` in dev while the dev
frontend port is dynamically allocated.** `dev/config.k:21` says
`http://localhost:3000/auth/github/callback`; `dev/main.k` allocates the
frontend as `_reliant_web_port` via `fp.allocate_port` and composes `APP_URL`,
`CORS_ORIGINS`, `ALLOWED_REDIRECT_HOSTS` and `VITE_APP_URL` from it. On any
worktree that does not draw block 0, the four agree with each other and the
GitHub redirect disagrees with all of them. Flow B in dev then either fails at
GitHub (URI not registered) or lands on a *different worktree's* frontend. I
have not run a stack to confirm which — but the derivation is inconsistent in
the source, which is the point. This is `6d74bc3d`'s bug with the hosts swapped.

**(b) The provider-side registrations are in nobody's repo.** Supabase Auth
redirect allow-list (two projects), the Google OAuth client's authorized
redirect URIs, and both GitHub OAuth apps' callback URLs. Every `UNVERIFIED`
above is this one gap. A correct `VITE_APP_URL` that the Supabase project has
not allow-listed fails at the provider with no code change in either repo — so
no CI signal exists, by construction.

**(c) `preprod` is declared but not deployable.** `lib/env.k`
`reliant_endpoints` has a full `preprod` branch (`reliant-preprod.web.app`,
`preprod.reliantapi.com`) and comments reference `preprod/main.k` and
`preprod/config.k`. `deploy/kcl/` contains only `dev`, `dev-k8s`, `e2e`, `prod`.
Either preprod was removed and `env.k` is stale, or it lives elsewhere. Any
detector that iterates envs must not silently skip it. **Unresolved — flag to
the user rather than guess.**

**(d) The `reliant-prod.web.app` second origin.** It is in `CORS_ORIGINS` and
`ALLOWED_REDIRECT_HOSTS` but `VITE_APP_URL` is unconditionally
`app.reliantlabs.io`, so a user who reaches the Firebase default domain starts
sign-in there and is returned to the custom domain. Supabase sessions are
origin-scoped (localStorage / the Electron file store), so they arrive
authenticated on an origin they did not start from. Probably harmless — it is
the same app — but nothing tests it and it is a real matrix cell.

**(e) Loopback-port registration assumption.** Flow A desktop sends an ephemeral
loopback port to Supabase/Google. RFC 8252 says providers should ignore the port
for loopback URIs, and Google does; whether the Supabase Auth allow-list does is
an assumption I could not verify statically. If it does not, the packaged
desktop app's Google sign-in is broken in a way no test here would see.

---

## 4. The invariants, and which are statically checkable

| # | Invariant | Statically checkable? |
|---|---|---|
| I1 | `APP_URL`'s host ∈ `ALLOWED_REDIRECT_HOSTS` | **Yes** — both in the rendered KCL |
| I2 | `VITE_APP_URL` == `APP_URL`, per env | **Yes** — same KCL, one is a projection of the other |
| I3 | `github_redirect_uri` == `${APP_URL}/auth/github/callback` | **Yes** — this is (a) above |
| I4 | `APP_URL` ∈ `CORS_ORIGINS`, and `app://bundle` ∈ prod's `CORS_ORIGINS` | **Yes** (`b1146f16`) |
| I5 | No runtime where `window.location.origin` can be loopback/`app:` may source a hosted redirect | **Yes** — `getAppURL()` is the single chokepoint; assert no other call site builds a redirect from `location.origin` |
| I6 | JS `oauth-contract.js` ≡ Go `InferConfig` | **Yes — already enforced**, `oauth-contract.test.js` |
| I7 | `release.config.json` ≡ rendered KCL | **Yes — already enforced**, `pr-ci.yml` + `sync-release-config.mjs` |
| I8 | `GITHUB_CLIENT_ID` set ⟹ `GITHUB_REDIRECT_URI` non-empty (no localhost fallback in a cloud env) | **Yes** — config-level |
| I9 | The redirect URI is registered **with the provider** | **No.** Requires talking to the provider. See D2. |
| I10 | The provider actually returns a code and the exchange succeeds | **No** — needs a live round trip with a real human or a mock IdP |

I1–I5 and I8 are the *entire* config half of this area, and all six are
checkable from `forge env render <env>` output plus a grep of `web/src`. That is
the shape of D1.

---

## 5. Anonymous → linked identity (intersects billing)

`useGoToBilling` sends `is_anonymous === true` users to `/upgrade` with
`returnTo: "/settings/billing"`. `UpgradeAccount` threads
`{ source: 'link', returnTo }` onto the OAuth redirect as query params
(`withState` in `oauth-signin.ts`), and `OAuthCallback` honors `returnTo` only
when it is a same-origin relative path — the open-redirect guard is present and
correct in all three places I checked (`OAuthCallback.tsx:89`,
`GitHubOAuthCallback.tsx` `landBackHome`, `UpgradeAccount.tsx:39`).

**Does the user's work survive linking?** Yes on the designed paths, and the
code is unusually careful about it. `linkOAuthIdentity` uses Supabase
`linkIdentity` rather than `signInWithOAuth` specifically so the anonymous
account (and its chats/worktrees) is preserved. The `identity_already_exists`
branch in `OAuthCallback.tsx:37-45` explicitly refuses to sign the user in as
the pre-existing account, because that would silently discard the anonymous
session's work, and says so to the user. That is the right call and it is
deliberate.

**Where it can break, in detection terms:**

1. **The `returnTo` query param must survive the redirect.** In the desktop
   loopback path the URI is built by `withState(await resolveRedirectTarget())`
   — state is appended to the *loopback* URI, and `oauth-loopback.js` preserves
   the query string when it 302s back into the app origin. So it survives, but
   only because of that preservation; it is one line, in a runtime CI never
   builds, and nothing asserts it. **Cheap contract test, worth having.**
2. **PKCE verifier loss.** `supabase.ts` keeps the verifier in a `Map` plus
   `sessionStorage`. In Electron the round trip re-enters the *same* renderer,
   so it holds. If a future change makes the callback land in a new window, the
   verifier is gone and linking fails with a confusing error. `3fcd9f79` ("fail
   PKCE loudly") is the scar.
3. **Anonymous session lost mid-flow ⇒ the link silently becomes a sign-up.**
   If the anon session is gone by the time the code comes back, the user gets a
   fresh account and their work is orphaned rather than erroring. I did not find
   an assertion anywhere that the post-link `user.id` equals the pre-link
   `user.id`. **That is the single most valuable assertion in this section**,
   and it is a unit test, not an integration test.

UX observations deferred to the agent who owns that redesign, noted only: the
`/upgrade` detour is invisible to a user who clicked "Billing", and the
`identity_already_exists` path is a dead end with no "sign in as that account
and merge" affordance.

---

## 6. Proposed detectors

### D1 — Redirect-coupling invariant check over rendered KCL ⭐ top pick

Assert I1–I4 and I8 against `forge env render <env>` output, for every env, as a
Go test in control-plane. ~120 lines, no network, no cluster.

- **(a) Historical bugs caught:** `6d74bc3d` (GITHUB_REDIRECT_URI on the wrong
  host — I3), `d277c59e` (redirect config not in the source of truth — I3
  forces it there), `b1146f16` (`app://bundle` missing from CORS — I4),
  `7f403355` (`VITE_CLI_DEFAULTS_BAKED`, same class: a KCL projection nobody
  cross-checked). Partially `a9c4e172` and `2c859d2d`, whose durable fix
  (`release.config.json` ← KCL + drift gate) already landed as I7. It also
  catches **live bug (a)** above, today, with no new information.
- **(b) Fires:** CI on PR **and** as a pre-deploy gate. This is the "before we
  deploy" half of the user's complaint, which is the half they said hurts.
- **(c) Cost:** low to build (one render call + string assertions), very low to
  maintain — it fails only when a redirect-shaped value genuinely changes, and
  when it does, that is exactly the review you want. Runs in well under a
  second.
- **(d) Residual gap:** cannot see I9. Every value can be mutually consistent
  and still unregistered with Google/GitHub/Supabase. It also cannot see
  anything about the packaged Electron runtime beyond the baked VITE values.

### D2 — Post-deploy provider-acceptance probe ⭐ second pick

For each `(env, provider)`: issue the authorize request with the **real**
`redirect_uri` and `prompt=none`-style parameters, follow **no** redirects, and
assert the provider responds with a consent/login page rather than
`redirect_uri_mismatch` / `invalid_request`. No login completes; no credentials
needed beyond the public client id. For Supabase, `GET /auth/v1/authorize?...`
returns a 302 to the provider on an allow-listed `redirect_to` and an error on a
non-allow-listed one — which is precisely the I9 signal.

- **(a) Historical bugs caught:** this is the *only* proposal that catches
  I9-class failures, and I9 is the residual gap of literally everything else. It
  would also have caught `6d74bc3d` and the dev half of bug (a) — from the
  provider's own mouth, which is stronger evidence than internal consistency.
- **(b) Fires:** post-deploy smoke, and ideally as a scheduled monitor —
  provider-side registration can be changed in a console by a human with no
  commit, so this is the one check that needs to run on a timer rather than on a
  diff.
- **(c) Cost:** moderate. Needs the client ids (public) and network egress to
  the providers. The main maintenance risk is provider HTML/error-format drift;
  keep the assertion coarse — "not an error response" — rather than matching
  copy.
- **(d) Residual gap:** does not prove a code exchange works (no consent), and
  cannot probe the ephemeral loopback port for the desktop flow, which by
  definition does not exist until a user clicks.

### D3 — `getAppURL()` chokepoint assertion (static, ~20 lines)

A lint-style test asserting that no file under `web/src` outside
`lib/constants.ts` composes a redirect/callback URL from
`window.location.origin`. The `72390d35` bug was exactly this, in exactly one
file, and the current fix is one well-documented function that a future edit can
trivially bypass.

- **(a) Caught:** `72390d35` directly; `3fcd9f79` adjacently.
- **(b) Fires:** pre-commit / CI on PR. Milliseconds.
- **(c) Cost:** trivial to build. Maintenance risk is false positives on
  legitimate same-origin uses (`landBackHome` in `GitHubOAuthCallback.tsx` uses
  `location.origin` correctly, to resolve a *relative* path) — so the rule must
  match "origin used as a redirect base handed outward", not all uses. Allowlist
  the two known-good sites explicitly with a comment.
- **(d) Residual gap:** a new redirect built in `electron/` or in a hook that
  spells it differently. It is a tripwire, not a proof.

### D4 — Anonymous-link identity-preservation test

Unit test: mock `linkIdentity`, run the `/upgrade` → callback → `returnTo`
sequence, assert (i) `user.id` is unchanged across the link and (ii) `returnTo`
survives `withState` → loopback → app-origin round trip for **both** the
loopback and hosted redirect targets.

- **(a) Caught:** none of the listed commits directly — this is prophylactic,
  which by the briefing's own standard makes it weaker. Its argument is that
  §5.3 has no assertion anywhere and its failure mode is *silent data loss*
  rather than an error, which is the worst detectability profile in my area.
- **(b) Fires:** CI on PR.
- **(c) Cost:** low; the existing `authStore.redirect-origin.test.ts` and
  `oauth-desktop-routing.test.ts` are the template.
- **(d) Residual gap:** mocks Supabase, so it cannot catch a real
  `linkIdentity` behavior change.

### D5 — Mock-IdP full round trip — **I recommend against building this now**

The `forgetest/jwks.go` infrastructure is real and good, but it mints tokens for
*reliant's own* JWT validation; it is not an OAuth authorization server. Getting
to a full mock-IdP round trip means implementing authorize + token + PKCE
verification and pointing Supabase at it — and the bugs in my list are not
protocol bugs. Not one of the twenty historical regressions would have been
caught by a correct-protocol round trip against a fake provider, because the
protocol was never what broke. **This is the exhaustive-looking proposal that
buys the least, and I would rather say so than pad the list.**

---

## 7. Honest summary of coverage

D1 + D3 cover the config and code-shape half — which is where roughly four of
the six commits in my area's history came from — for maybe a day of work
combined, and they fire before a deploy. D2 covers the provider-registration
half, which nothing currently covers at all and which no amount of internal
testing can reach. D4 is cheap insurance on the one silent-data-loss path.

The residual gap after all four is narrow and worth stating plainly: **nothing
proposed here builds a packaged Electron app and completes a real sign-in.**
That runtime is where `72390d35`, `3fcd9f79`, `487fb19a` and `a9c4e172` all
lived, and the reason they were found by users rather than by CI is that CI has
never produced that artifact and exercised it. If there is appetite for one
expensive thing in this area, it is not a mock IdP — it is a CI job that builds
the packaged app and asserts, in the real renderer, that
`resolveRedirectTarget()` returns a non-loopback, non-`app:` URL for the hosted
fallback path. That is a smoke test of the artifact, not of the protocol, and it
targets the actual gap.
