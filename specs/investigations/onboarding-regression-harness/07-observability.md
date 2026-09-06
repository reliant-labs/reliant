# 07 — Post-deploy detection and production observability

**Area: the residual gap.** Five sibling agents own pre-deploy detectors. This
file assumes they all ship and *some bad config still reaches prod*.

Most of what follows is a **fallback** — strictly worse than the equivalent
pre-deploy gate, valuable only because it catches the failure nobody predicted.

**One item is not a fallback.** Section 0 covers a class of breakage that **no
pre-deploy check can cover even in principle**, because the configuration lives
outside both repositories. That is the strongest justification for this area and
it leads.

---

## Part 0 — The fourth registry ★ headline

*(Verified by the auth/OAuth agent; inherited, not re-derived.)*

Every **internal** redirect coupling — `APP_URL` ↔ `ALLOWED_REDIRECT_HOSTS` ↔
`CORS_ORIGINS` ↔ `VITE_APP_URL` ↔ `github_redirect_uri` — now lives in one KCL
file and is statically checkable. A sibling agent is designing exactly that
check, and it should ship.

**But there is a fourth registry, and it is not in any repository:**

| Registry | Lives in | Changed by | Guarded by CI |
|---|---|---|---|
| Backend env | `deploy/kcl/*/main.k` | commit | yes (sibling agent) |
| Frontend bake | `deploy/kcl/lib/env.k` | commit | yes (bundle greps) |
| Packaged app | forge KCL, via `2c859d2d` | commit | partly |
| **Provider allow-lists** | **Supabase Auth / Google / GitHub consoles** | **a human clicking in a web UI** | **NO — impossible** |

A person can change the Supabase Auth redirect allow-list, or a Google/GitHub
OAuth app's authorized callback, **with no commit, no PR, and no CI run**. Every
pre-deploy detector in this investigation stays green while production sign-in
is broken. There is no artifact to inspect and no file to lint, because the
authoritative value is in a third party's database.

This is not a gap better pre-deploy testing could close. It is **structural**,
and it can only be closed by asking the provider, from outside, on a timer.

Note the corollary that determines the design: because the guarded thing changes
**without a deploy**, a deploy-triggered check is insufficient by construction.
**This must run on a timer.** A deploy-time run is a nice-to-have; the timer is
the product.

### The provider-acceptance probe

Issue the **real authorize request** to each provider with the **real redirect
URI**, and assert the provider does not reject the redirect URI. No login
completes, no token is issued, no user is created — you are probing the
provider's *registration*, not the auth *flow*.

The mechanic that makes this work is a deliberate and stable part of the OAuth 2
spec (RFC 6749 §4.1.2.1): a bad `redirect_uri` is the **one** error a provider
must **not** redirect back to the client. Everything else (bad scope, denied
consent) redirects to the callback with `?error=`; an unregistered
`redirect_uri` renders an error **on the provider's own domain**. So the probe
does not need to parse error text or follow a flow — **the shape of the response
is the answer**, which is what makes it robust and low-noise.

**GitHub**

```
GET https://github.com/login/oauth/authorize
      ?client_id=<public>&redirect_uri=<exact>&scope=read:user&state=probe
```
- Registered → `302` to `https://github.com/login?...` (the sign-in wall).
- **Not registered → `200` HTML on `github.com` containing
  `redirect_uri` mismatch text.**
- Assert: **response is a 302 whose `Location` host is `github.com/login`**.
  Do not assert on the error copy, which is not a contract.

**Google**

```
GET https://accounts.google.com/o/oauth2/v2/auth
      ?client_id=<public>&redirect_uri=<exact>&response_type=code&scope=openid&state=probe
```
- Registered → `302` toward the account chooser / consent.
- **Not registered → `400` with the well-known `redirect_uri_mismatch`.**
- Assert: **not a 400**. Google is the crispest of the three.

**Supabase** is different and is the one most likely to bite, because Supabase
proxies to the upstream provider *and* keeps its own separate allow-list:

```
GET https://dash.reliantlabs.io/auth/v1/authorize
      ?provider=github&redirect_to=https://app.reliantlabs.io/...
```
- Allowed → `302` to the upstream provider (`github.com` / `accounts.google.com`).
- **Not allowed → Supabase redirects to its own `SITE_URL` with
  `?error=...redirect_to`, i.e. the `Location` host is NOT the provider.**
- Assert: **the `Location` host is the expected upstream provider host.**

That last assertion is the valuable one. It catches the case where GitHub and
Google are both configured perfectly and **Supabase's own allow-list is not** —
a state no other check in this investigation can observe, and a plausible cause
of "sign-in broke and nothing changed."

Also probe `<supabase_url>/auth/v1/.well-known/jwks.json` for a non-empty key
set (`internal/auth/jwt.go:732` — VERIFIED), covering the `3d79e158`
Supabase-misconfiguration class.

**Credentials: confirmed unnecessary.** All three probes need only the
`client_id`, which is **public by definition** — it is transmitted in the URL of
every user's browser and is already baked into the publicly downloadable prod
bundle. No client secret, no service account, no user credential. Nothing needs
to be held in CI beyond values that are already public. **This is what makes the
proposal cheap enough to actually build**, and it is the reason to prefer it
over any probe that needs an authenticated session.

One caveat to design for: these requests originate from a CI runner, so a
provider may occasionally answer with a bot/rate-limit interstitial. Treat a
response that is neither the expected success shape nor the expected mismatch
shape as **inconclusive → retry**, and only page after N consecutive
inconclusive-or-failed runs. That single rule is what keeps this at zero false
pages.

- **(a) Bugs caught:** `6d74bc3d` (`GITHUB_REDIRECT_URI` at the wrong host) —
  and, far more importantly, **the entire class of provider-side change that has
  no commit to catch**. Detection: **within one timer interval, with no deploy
  required.**
- **(b) Where:** scheduled GitHub Action, every 30 minutes, plus one run inside
  `forge env deploy` (see Part 5). **A GitHub Action, not an in-cluster check** —
  the probe deliberately tests reachability *from outside our infrastructure*,
  which is the vantage a real user has and an in-cluster prober does not.
- **(c) Cost:** ~80 lines of shell/Go, ~3 seconds per run. Zero credentials.
  **Noise: near zero given the inconclusive-retry rule** — assertions are on
  HTTP status and `Location` host, not on scraped copy.
- **(d) Residual gap:** proves the redirect URI is *registered*, not that the
  full flow works (a revoked client secret, a suspended OAuth app, or an expired
  Supabase JWT secret all pass). Cannot see per-user or consent-screen state.

### Does this generalize to Stripe? Partly — and usefully

Not via the same trick: Stripe has no public authorize endpoint, and every
meaningful Stripe call needs a secret key. But the **plan-catalog / price-mode**
class the billing agent flagged is checkable server-side, from inside a live
deployment that already holds the key.

VERIFIED: `internal/plansconfig/plans.prod.yaml` carries real price IDs (e.g.
`price_1Tk6uxPVGOBSO8GiY9B9lHx7`) with an explicit comment that *only*
`stripe_price_id` differs per environment — precisely the "one field varies by
env, silently" shape that produces these outages. A `plans.prod.yaml` shipped
with a test-mode price ID, or a price deleted in the Stripe dashboard (**another
web-console change with no commit** — the fourth-registry problem again, wearing
a different hat), breaks checkout with everything else green.

**Probe:** on startup and on a timer, the deployed billing service iterates
every non-null `stripe_price_id` in its loaded plan config and calls
`price.Get`. Assert each one **exists**, is **active**, and its **livemode flag
matches the deployment's mode**. That last assertion is the whole point: a
valid, active, *test-mode* price in prod is the failure that looks correct
everywhere else. Surface the result via `/configz` (Part 2) so the external
prober can read it without holding a Stripe key.

- Catches the plan-catalog/price-mode class; ~40 lines; no new credentials (the
  service already has the key); no user-visible side effects (`price.Get` is a
  read).
- Residual: does not prove a checkout session can actually be created for that
  price, only that the price is real and in the right mode.

---

## Part 1 — What telemetry is actually live in prod (VERIFIED)

The distinction that matters is **present in code** vs. **enabled in prod** vs.
**someone would be paged**. Almost everything fails at the second or third gate.

| Signal | In code | Enabled in prod | Would page anyone |
|---|---|---|---|
| Sentry (browser) | `reliant/web/src/lib/sentry.ts` | **YES** — VERIFIED | **No alert rules in repo** |
| Sentry (Go services) | `telemetry.NewReporterFromEnv` | **YES** — VERIFIED | **No alert rules in repo** |
| Session Replay | 100% for new users' first 5 sessions | **YES** | n/a (forensic) |
| OTel browser traces | `reliant/web/src/lib/otel.ts` | **NO — dead code** | no |
| Statsig analytics | `reliant/web/src/lib/analytics.ts` | **NO — dead code** | no |
| Onboarding funnel events | `OnboardingFlow/analytics.ts` | **NO — discarded** | no |
| Prometheus metrics | `control-plane/internal/metrics/metrics.go` | served on `/metrics` | **nothing scrapes it** |
| Post-deploy HTTP probes | `deploy-reliant-web.yml` | **YES** — VERIFIED | fails the CI job |
| Cluster health probes | `deploy.yml` | **YES** — VERIFIED | fails the CI job |

**1. Sentry is genuinely live on both halves, and unwatched.** Frontend:
`deploy/kcl/lib/env.k:540-541` sets `VITE_SENTRY_DSN` / `VITE_SENTRY_ENABLED`,
and the DSN is confirmed **in the shipped prod bytes** —
`grep -oE 'ingest\.us\.sentry\.io/[0-9]+'` against
`.firebase-dist/prod/assets/index-DsQHnLmQ.js` returns
`ingest.us.sentry.io/4509933645856778`. Backend: `prod/main.k:219-221`.

What is missing is the other half: **`rg -l 'sentry'` across every `.yml` /
`.yaml` / `.json` in control-plane returns nothing.** No alert rule, no routing,
no integration under version control. **A Sentry that is configured but unwatched
is a forensic archive, not a detector** — excellent once a human knows to look,
useless at telling them to.

**2. Statsig — and the entire onboarding funnel — is dead in prod.**
`analytics.ts` no-ops without `VITE_STATSIG_CLIENT_KEY`;
`grep -rn 'VITE_STATSIG_CLIENT_KEY' deploy/` returns **nothing**, and the prod
bundle contains **zero** Statsig key strings. So the fully-written funnel in
`OnboardingFlow/analytics.ts` is **discarded in the process that generates it**.
The instrumentation is done; the pipe is missing.

**3. OTel browser tracing is doubly dead.** `otel.ts` reads
`VITE_OTEL_EXPORTER_OTLP_ENDPOINT`; forge's `FrontendConfig.otel_endpoint` maps
to `VITE_OTEL_ENDPOINT` (`frontend_config_gen.k:34`). **The two names do not
meet — setting the forge-declared knob today would silently do nothing.** Worth
fixing regardless of whether tracing is wanted: a knob that silently no-ops is
this investigation's entire subject matter.

**4. Prometheus metrics are exported into the void.** `internal/metrics/` is a
substantial collector set on `/metrics`, but no `ServiceMonitor`, Prometheus, or
Alertmanager exists anywhere in `deploy/kcl/`. **Any proposal routed through
Prometheus must first fund a monitoring stack** — which is why nothing below
depends on one.

### Prior art — read before proposing anything

`deploy-reliant-web.yml` already contains four gates of exactly this genre, and
they are good. They should be the template, not duplicated:

- **"not a dev build pointed at its own origin"** — greps `dist/assets` for
  `?window.location.origin:`, the surviving same-origin branch that proves
  `import.meta.env.DEV` was true at build time. Its comment documents the bug:
  RPCs POST to the Hosting origin, the SPA rewrite answers 200 `text/html`,
  Connect cannot parse it, `/onboarding` spins forever with no console error.
- **"carries this environment's real backend config"** — requires the three prod
  endpoints present, fails on `VITE_*_URL` near `localhost` (`a9c4e172`,
  `2c859d2d`).
- **"Verify the deployed site"** — probes `/`, `/onboarding`,
  `/chat/deep/link`, testing the **SPA fallback** not just the root
  (`ed96ce8`), and compares live vs. built chunk hashes.
- **`deploy.yml` health probes** — in-cluster curl against zitadel / nats /
  temporal-ui / litellm, hardened from `|| echo Warning` to a real failure.

**The gap they leave is precise: every one asserts on the FRONTEND ARTIFACT or
INFRA LIVENESS. Not one asserts on the deployed BACKEND's request-time
configuration.** That drives Part 2.

---

## Part 2 — Live config-shape probe against the deployed backend

**One HTTP request per invariant. No auth, no writes, no test account.**

**A1 — CORS preflight admits the real app origin.** Forge's
`pkg/middleware/cors.go` is a clean oracle: exact case-insensitive match → sets
`Access-Control-Allow-Origin`; **no match → emits no CORS header at all and
passes through**. The header's presence *is* the assertion; status is
irrelevant.

```
OPTIONS https://admin.reliantapi.com/<connect-path>
  Origin: https://app.reliantlabs.io
→ MUST return Access-Control-Allow-Origin: https://app.reliantlabs.io
```

Repeat for every origin the env declares — including **`app://bundle`**.

- **Catches `b1146f16`** in **~30 seconds**. Strongest evidence-to-cost ratio
  outside Part 0: the packaged-Electron origin is a runtime value **no
  browser-based test and no bundle grep can observe**, and it is one `curl -I`
  away.

**A2 — the invariant triangle, against the LIVE deployment.** A sibling agent is
asserting `host(APP_URL) ∈ ALLOWED_REDIRECT_HOSTS`, `app://bundle ∈
CORS_ORIGINS`, `host(GITHUB_REDIRECT_URI) == host(APP_URL)` **on the KCL**,
which is strictly better. Doing it *also* against the live deployment is a
**delta check on reality, not a duplicate**: it catches a right-KCL/wrong-runtime
state — stale rollout, failed ExternalSecret, or an `emergency-deploy.yml` run
that shipped an older image.

**Enabling change: a `/configz` endpoint.** Returns the *shape*, never secrets:

```json
{ "app_url": "https://app.reliantlabs.io",
  "cors_origins": ["https://app.reliantlabs.io","https://reliant-prod.web.app","app://bundle"],
  "allowed_redirect_hosts": ["app.reliantlabs.io","reliant-prod.web.app"],
  "github_redirect_uri": "https://app.reliantlabs.io/auth/github/callback",
  "github_client_id": "<public>",
  "stripe_price_check": "ok|mismatch|missing",
  "image_tag": "sha-abc1234" }
```

Every field is already public (baked into a downloadable bundle or sent to a
third party). **Use a hand-curated struct, never a reflective config dump** —
the failure mode to guard in review is this endpoint growing a secret-adjacent
field. It also feeds Part 0 (`github_client_id`, `stripe_price_check`) and
resolves "is what I deployed what is running" (`image_tag`), which the frontend
half already checks and the backend half does not.

- (a) `b1146f16` (~30s), `6d74bc3d` (~30s), rollout/secret staleness.
- (b) A step in `deploy.yml` after `forge env deploy`, **plus** on the timer.
- (c) ~60 lines of shell; `/configz` ~40 lines of Go. **Zero false-page surface**
  — deterministic assertions, no flake beyond reachability, which existing
  probes already retry.
- (d) Proves the config is *shaped* right, not that the flow *works*.

### Extend the read-only canary tier (cheapest item in the file)

`deploy-reliant-web.yml`'s bundle greps run against the artifact CI **just
built** — but the workflow itself admits that `forge env deploy` **rebuilds**
before deploying, so the asserted artifact and the shipped artifact are two
builds. **Re-run those two greps against the downloaded LIVE chunk.** Two extra
seconds upgrades an existing good check into an airtight one.

---

## Part 3 — Making existing loud errors actually reach someone

*(Inherited from the billing agent. This reframes the cheapest win in the file.)*

**A "loud error" that nobody reads is not a detector.** Missing
`ALLOWED_REDIRECT_HOSTS` **already** produces a `logger.Error` and a per-request
fail-closed (`svcbilling/service.go:317-356`). The failure was *already* loud
server-side **and still reached users**. That is a proven miss, and it is the
strongest possible evidence that new instrumentation is not the bottleneck —
**delivery is**.

I accept the billing agent's rejection of boot-time refusal, and I withdraw the
readiness-probe idea I would otherwise have advanced here: failing readiness on
a billing misconfiguration converts a billing outage into a **full control-plane
outage including daemon lifecycle**, which is a strictly worse production
incident than the one being prevented. The right move is not to make the error
more fatal. It is to make it **visible during rollout**.

### The cheapest path from "we log it" to "someone is told"

**Route `slog`/`logr` ERROR to the Sentry that is already live.** Backend Sentry
is confirmed enabled in prod (`SENTRY_DSN`, `SENTRY_ENABLED=true`). A ~30-line
`slog.Handler` that forwards ERROR-level records to `sentry.CaptureMessage`
turns **every existing `logger.Error` in the codebase into a delivered signal**,
with no new instrumentation and no new vendor.

Call sites this immediately covers (VERIFIED counts):
`internal/billing/svcbilling/service.go` (2 — including the
`ALLOWED_REDIRECT_HOSTS` one), `internal/coupon/service.go` (5 — the `3d153cff`
coupon-redemption class), `internal/user/user.go` (3 — the provisioning path
behind `c639f243`).

**Plus a rollout-window gate that costs nothing to build:** after
`forge env deploy`, tail the new pods' logs for 60 seconds and **fail the deploy
on any ERROR containing a config-shaped marker**. `kubectl logs --since=60s`
against the namespace the deploy already targets. This is the "visible during
rollout" the billing agent asked for, and unlike readiness-failure it **does not
take the service down** — it takes the *deploy* down, which is the correct blast
radius.

- (a) The `ALLOWED_REDIRECT_HOSTS` miss the `svcbilling` test was written for;
  `3d153cff`; `c639f243`. **Rollout-window: ~60 seconds, deploy-blocking.**
- (b) Sentry handler: continuous. Log gate: post-deploy step.
- (c) **Smallest build cost of anything here** — ~30 lines plus a `kubectl logs`
  step. Wires proven-live infrastructure to already-written log statements.
- (d) Only as good as the existing ERROR calls. A silent wrong value that logs
  nothing is invisible — which is what Parts 0 and 2 are for. Watch for
  ERROR-level chattiness; if a noisy site emerges, fix the log level rather than
  filtering at the sink.

---

## Part 4 — The funnel, and the exit-reason event

**The instrumentation exists and its output is thrown away.**
`OnboardingFlow/analytics.ts` already emits, in prod code:
`onboarding_flow_started`, `onboarding_flow_step_viewed`,
`onboarding_flow_step_completed` (with `duration_ms`),
`onboarding_flow_abandoned` (with `last_step`), and `onboarding_completed`
(with `provider`, `compute`, `code_source`, `intent`, `project_source`). The
`completedFlag` bridge already distinguishes "finished" from "bailed".

All discarded, because `VITE_STATSIG_CLIENT_KEY` is set nowhere.

### The production half of `leaveOnboarding(reason)` — the most informative event

*(The onboarding agent proposes funnelling all three exit paths through one
`leaveOnboarding(reason)` and logging the reason. Its framing — turning
"something ended onboarding" from invisible into a grep in
`frontend_reliant-web.log` — describes the **dev** sink. This is the prod half.)*

Context that makes this urgent: there are **three exit paths using three
different mechanisms**, one of them an un-awaited global router singleton
(`useOnboardingComplete.ts:101` — VERIFIED, `void router.navigate(...)`). A user
can be ejected by a background effect with **no action taken**, and today
**nothing records that it happened or why**.

**Emit `onboarding_exited` with a `reason` tag** — `completed`, `heal`,
`redirect`, `error` — plus `last_step_viewed` and `steps_viewed_count`.

**This is the single most informative event in the funnel**, because it is the
only one that distinguishes *"the user completed onboarding"* from *"onboarding
completed itself at the user."* The briefing records a heal firing **39ms** after
daemon creation with no step in between; with this event that is a metric, not
an anecdote recoverable only by reading `admin-server.log` after someone
complains.

**Alert:** `rate(onboarding_exited{reason="heal", steps_viewed_count=0})` above
a small absolute threshold. A config break that ejects users produces a clean
cliff here and **nowhere else in this document**.

### Which pipe: Sentry, not Statsig

Emit these as **Sentry breadcrumbs plus a small number of explicit markers**,
using the pipe **already proven to reach prod in the shipped bytes**. This
avoids standing up a second vendor, and — decisively — means the funnel and the
errors **share a session**, so a funnel cliff arrives with a replay of the user
hitting it. Sentry is a worse analytics product than Statsig and a far better
debugging one; the purpose here is detection-and-diagnosis, not growth
analytics. Sentry's native `release` field also tags every event with the deploy,
making a cliff immediately attributable.

(If Statsig is wanted later, `VITE_STATSIG_CLIENT_KEY` beside `VITE_SENTRY_DSN`
in `lib/env.k` is a one-line change — a Statsig *client* key is publishable like
the Supabase anon key already there. Also confirm the prod default of
`getPrivacySettings().analyticsEnabled` yields usable volume; if it does not,
that is a product decision to surface, not to route around.)

### Alert conditions: absolute-zero, not percentage-change

This is a **pre-launch product with low, bursty volume**. A "conversion dropped
30%" alert on single-digit daily signups fires because Tuesday had two starts
instead of five, gets muted within a week, and **muted is worse than absent**
because it manufactures a belief in coverage.

| Alert | Condition |
|---|---|
| **Total funnel stall** | `started > 0` but `completed == 0` over 6h |
| **Step cliff** | per step S: `viewed{S} > 3` but `completed{S} == 0` over 6h |
| **Boot-to-onboarding gap** | `app_boot > 0` but `started == 0` over 6h (needs Part 5) |
| **System-ejection** | `exited{reason=heal, steps_viewed=0}` above absolute N |
| **Deploy-scoped** | any of the above, 60-min window, for the hour after a deploy |

Every condition is gated on `started > 0`, so genuine zero traffic reads as
silence rather than as a break.

- (a) **Not enumerable by hash, and that is the point.** Would catch `458a830c`,
  `63b18468`, `44a29607`, `3d153cff`, `b33be21a`, `c639f243` — the stranded-user
  and eligibility bugs where the app is *up*, throws *no error*, and the user
  simply cannot proceed, which is **invisible to every other proposal here**.
  Detection ~1–6h; ~1h post-deploy.
- (b) Continuous, plus a tightened post-deploy window.
- (c) **Low — the events are written.** Cost is the pipe and the rules. Ongoing
  cost is ownership, which absolute-zero design keeps near zero.
- (d) Lagging by construction: needs users to fail first. Tells you *where* the
  funnel broke, never *why* — it pages you, then hands off to Sentry replay.

### The billing funnel's final step is unmeasurable client-side — use the webhook

*(From the billing-UX agent.)* All Stripe checkout call sites pass the same URL
as **both** `successUrl` and `cancelUrl` — VERIFIED at
`web/src/components/Settings/cloud/billing.tsx:260-261` and `719-720`. **The app
cannot distinguish a completed purchase from an abandoned one on return, and
does not try.** In packaged Electron it is worse: the hand-off is
`shell.openExternal`, checkout completes in the system browser, and the app
window is never notified at all.

The proposed `?checkout=success|cancelled` param is a prerequisite for
*client-side* instrumentation — **note that dependency** — but the robust answer
does not need it. **The Stripe webhook is the authoritative completion signal
and it is server-side, so it is correct even in the Electron case where the
client never learns the outcome.**

VERIFIED that it exists and is wired: `StripeWebhookHandler.ServeHTTP` serves
`/webhooks/stripe` (`internal/billing/svcbilling/stripe_webhook.go:67`),
`handleEvent` dispatches `checkout.session.completed`,
`customer.subscription.created/updated/deleted`, `invoice.paid`,
`invoice.payment_failed`, and `metrics.WebhookProcessed` **already counts
processed webhooks labeled by event type** (`metrics.go:300`).

**So build the billing funnel as a server-side ratio: checkout sessions created
vs. `checkout.session.completed` webhooks received.** Both numbers already exist
or are one counter away (`StripeAPILatency`/`StripeAPIErrors` cover the mint
side; add a `CheckoutSessionsCreated` counter). A cliff in that ratio is a
**strong, low-noise signal for exactly the prod-only config breakage the owner
is worried about** — a wrong price ID, a redirect rejection, a mode mismatch —
and it is immune to the client-side blindness above.

Caveat that shapes the alert: webhook delivery is asynchronous and retried, so
compare over a window with lag tolerance (created in window *T*, completed in
*T + 15min*), and alert on **created > 0 with zero completions**, not on a
percentage.

**This does require something to read the counters** — see the Prometheus gap in
Part 1. Cheapest path meanwhile: a periodic job that computes the ratio and
emits a Sentry message when it trips, reusing the live pipe rather than funding
a monitoring stack for one alert.

---

## Part 5 — Making silence loud

The recurring shape (`3d79e158` white-screen, `487fb19a` packaged renderer, the
same-origin hang) is **a failure that produces no error report**. The dev stack
forwards the whole browser console to `frontend_reliant-web.log`
(`browser-log-boot.ts` + `vite-plugin-browser-logs.ts`). **Prod has no
equivalent.**

**5a. A boot beacon.** `main.tsx` already has both moments: it renders
`STARTUP_SPINNER_MARKUP` while awaiting `waitForConfig(15000)`, then mounts
`<Root>`. Emit `app_boot_started` at the top (it is the first module after
`browser-log-boot`) and `app_boot_succeeded` after first paint. **The alert is
the gap**: `started > 0 && succeeded == 0` is a white screen expressed as a
metric, and this is **the only proposal that detects `3d79e158` and `487fb19a`
at all**. `waitForConfig`'s 15s timeout is itself a boot failure that currently
just proceeds — beacon it explicitly.

**5b. A positive "the app is usable" assertion — not an error listener.**
*(Confirmed live instance from the onboarding agent.)* `OnboardingPage.tsx`
returns `null` when `!StepComponent` — VERIFIED at the line preceding the
returned dialog — **before** rendering the card that holds the escape hatch. A
failed lazy chunk therefore yields a blank screen with no Back, no Settings, no
Sign out.

**React error boundaries and `window.onerror` both miss this, because returning
`null` is not an error.** That is exactly why the detector must be a *positive*
assertion rather than an error listener:

- **Chunk-load-failure beacon** — `import()` rejections are the common cause and
  *are* catchable; wrap the lazy boundaries and emit `chunk_load_failed` with
  the chunk name. Cheap, and directly catches the CDN-rollover case that
  `deploy-reliant-web.yml`'s hash comparison only warns about.
- **"Rendered but not interactive"** — 5 seconds after mount, assert the root
  contains at least one focusable element. If not, emit `app_blank_render` with
  the route. This is the general detector for the whole `return null` class, of
  which the `OnboardingPage` instance is one confirmed member.

**5c. Early-error capture.** `@sentry/react` wires `unhandledrejection`/`error`
on init — **but `initSentry()` is `async` and returns early when
`crashReportingEnabled` is false**, so failures before it resolves are
unreported. That window is precisely when config-driven boot failures happen. A
~10-line handler in `browser-log-boot.ts` that buffers early errors and flushes
once Sentry is up closes it — the prod analogue of the dev console forwarding,
in the same file, for the same reason.

- (a) `3d79e158`, `487fb19a` (5a — otherwise undetectable, **minutes**); the
  live `OnboardingPage` blank-screen (5b); the same-origin hang (5b).
- (b) Continuous; same Sentry pipe as Part 4.
- (c) Small — tens of lines in files that already do this shape of work.
- (d) Blind to failures so early no JS runs (500 on `index.html`, CSP block,
  broken CDN). **Part 2's HTTP probes cover exactly that**, which is why the two
  tiers are complements.

---

## Part 6 — Where this runs, the forge change, and the meta-check

### The forge defect, named

**`forge env deploy` has no post-deploy verification hook.** VERIFIED: no
`post_deploy` / `verify` / `smoke` field anywhere in `forge/kcl/schema.k`, and
no verification in `internal/cluster/cluster.go` beyond the rollout wait.

Per the house rule this is a forge gap to fix in forge — and the argument stands
on merit. The information these checks need is **already in the KCL**:
`CORS_ORIGINS`, `ALLOWED_REDIRECT_HOSTS`, `APP_URL`, `github_redirect_uri`, the
Hosting site. The current shell steps work by **re-hardcoding those same values
in YAML**, a second source of truth that can drift from the first — the exact
failure mode this investigation exists to eliminate.

**Proposal: `forge.PostDeployCheck`**, attachable to a `Service` or `Frontend`
deploy target:

```python
schema HTTPCheck:
    url: str                        # may interpolate declared config
    method: str = "GET"
    headers?: {str: str}
    expect_status?: [int] = [200]
    expect_header?: {str: str}      # Access-Control-Allow-Origin
    expect_redirect_host?: str      # the Supabase/provider assertion
    expect_body_contains?: [str]
    expect_body_absent?: [str]      # the "?window.location.origin:" gate
    retries: int = 5
    inconclusive_status?: [int]     # retry rather than fail (bot walls)
    timeout_seconds: int = 30
```

`forge env deploy` runs these after the rollout wait and fails the deploy on
failure. `expect_redirect_host` is what lets **Part 0's provider probe** be
declared rather than hand-rolled. That gives: one implementation across every
env and target; no drift, because the check reads the same KCL value the
deployment does; coverage of every path **including `emergency-deploy.yml`**;
and **`forge env verify <env>`** as a separate entry point — which is what the
timer calls, detecting drift *between* deploys.

It generalizes cleanly beyond this project, which is the test of whether it
belongs in forge. It does: "assert the thing I just deployed responds correctly"
is universal, and every forge user currently hand-rolls it or skips it. In v1 a
failed check should surface forge's usual runbook-style error and should **not**
auto-rollback — rollback policy is a separate decision, and conflating them
makes the feature harder to adopt.

### A meta-check: did our detectors actually run?

*(Orchestrator finding — unusual, but well-evidenced in this codebase.)*
`sync-release-config.mjs --check` silently no-ops when `kcl` or the sibling
checkout is unavailable, and `control-plane/.github/workflows/ci.yml` (~line 245)
documents a past instance of **"this job passed only because a cache hit skipped
the install."**

**A gate that can pass by skipping is not a gate**, and it fails in the most
dangerous direction: green. Two cheap countermeasures:

1. **Every check emits a positive receipt.** Each assertion prints a machine-
   readable `CHECK <name> PASS`, and a final step asserts the expected *set* of
   names is present. A skipped check then fails as a **missing receipt** rather
   than passing as an absent failure. This is the same "positive assertion, not
   absence of error" principle as 5b, applied to CI.
2. **No silent no-op on a missing tool.** A check that cannot run must **fail**,
   not return zero. Pre-launch, there is no compatibility argument for the
   lenient path — delete it.

`forge.PostDeployCheck` should adopt receipt #1 natively: forge knows the
declared set of checks, so it can assert every one produced a result. That is a
guarantee a bolted-on shell step cannot make about itself, and it is a further
argument for the forge feature over more YAML.

### Placement

| Tier | Where | Cadence |
|---|---|---|
| **0 — provider-acceptance probe** | scheduled GH Action (external vantage) | **every 30 min** |
| 2 — config probe + canary | `forge.PostDeployCheck` via `forge env deploy` | every deploy |
| 2 again | `forge env verify prod` on a schedule | every 15 min |
| 3 — rollout log gate | post-deploy `kubectl logs --since=60s` | every deploy |
| 3 — slog→Sentry | in-process | always |
| 4, 5 — funnel, exits, boot | live Sentry pipe | always |

**Not an external uptime service** for the config assertions — it would know
nothing about the declared config and add a vendor. (A free-tier ping on `/` for
pure off-network reachability is a fine 5-minute add; just don't put the
assertions there.)

---

## Part 7 — Not recommended: a synthetic user journey against prod

The brief asks the hard question; the honest answer is that it has no good
answer, so route around it.

**There is no test-tenant concept in control-plane.** VERIFIED: `newTestUser`
(`e2e/user_stories/main_test.go:491`) **self-signs a JWT** via
`forgetest.NewTokenMinter`, which works because the e2e env trusts a test JWKS.
Prod trusts Supabase. So the harness is not reusable against prod without either
(i) prod trusting a test issuer — **an authentication bypass in production**, an
unacceptable trade for a smoke test, or (ii) real credentials in CI.

And a real identity creates real rows: `internal/signupgrant` grants a compute
trial subscription on first login, `buildPersonalOrg` creates an org, and
metering carries `ChatID`/`WorkspaceID`/`ThreadID`. Synthetic users manufacture
orgs, trials, and metering rows **indistinguishable from real ones** — which
silently corrupts the very funnel metrics Part 4 depends on. An `is_synthetic`
column is a real schema change every billing query must then remember to filter,
and the first one that forgets produces a subtly wrong revenue number.

**Instead:** make the `e2e` env mirror prod's *config shape* (same structure,
different hosts) so the existing `story_07_full_onboarding_flow_test.go` covers
the redirect invariants **pre-deploy** — that belongs to a sibling agent — and
let Part 4's funnel cover the rest, since real users already execute the journey
continuously, more faithfully, and pollute nothing.

Even if built, it would follow one scripted happy path through a flow whose step
selection is `deriveStep(plan)` over a URL param with a background effect able
to end it — it would likely have missed `458a830c`, `63b18468`, and `44a29607`
entirely.

---

## Ranked summary

| # | Proposal | Bugs caught | Speed | Build | Noise |
|---|---|---|---|---|---|
| **1** | **Provider-acceptance probe (Part 0)** | `6d74bc3d` + **the whole no-commit provider class** | 30 min, **no deploy needed** | S | ~zero |
| **2** | **slog→Sentry + rollout log gate (Part 3)** | the `ALLOWED_REDIRECT_HOSTS` miss, `3d153cff`, `c639f243` | **60s, deploy-blocking** | **XS** | low |
| **3** | **Funnel pipe + `onboarding_exited{reason}` (Part 4)** | `458a830c`, `63b18468`, `44a29607`, `b33be21a`, **+ unpredicted** | 1–6h | S (events exist) | low by design |
| **4** | **Live CORS/config probe + `/configz` (Part 2)** | `b1146f16`, `6d74bc3d`, rollout staleness | ~30s | S | zero |
| **5** | **Boot beacon + "usable" assertion (Part 5)** | `3d79e158`, `487fb19a`, live `OnboardingPage` blank | minutes | S | zero |
| **6** | **Stripe created-vs-webhook ratio (Part 4)** | price-mode / checkout-config class | ~15 min | S | low |
| **7** | **Live-bytes bundle greps** | closes the rebuild gap on `a9c4e172`, `2c859d2d` | ~1 min | XS | zero |
| **8** | **`forge.PostDeployCheck` + receipts (Part 6)** | makes 1, 4, 7 durable; closes the skipped-gate hole | n/a | M | zero |
| — | Synthetic prod user journey | most, in principle | ~5 min | **L, unbounded** | data pollution |

**#2 is the cheapest thing here and has a proven miss behind it. #1 is the only
thing here that is not a fallback.** Build both.

## Residual gap after all of this

- **The packaged Electron runtime**, beyond its origin being admitted. Nothing
  here builds or launches a packaged app, and `a9c4e172`, `3fcd9f79`,
  `487fb19a`, `2c859d2d` all live there. The funnel sees the *cliff* only if
  desktop users are tagged distinctly — **so tag them**; one line, and the only
  leverage this area has on desktop.
- **Provider state beyond redirect registration** — a revoked secret, a
  suspended OAuth app, an expired Supabase JWT secret. Part 0 asks one question
  and gets one answer.
- **Correct-but-wrong values.** `APP_URL` set to a real, allowed, reachable host
  that is simply the wrong one passes every check here.
- **First-user-of-the-day latency.** At pre-launch volume a 6h window means a
  2am break is found at 8am. The post-deploy 60-min window mitigates only breaks
  a deploy caused.

The framing worth repeating: **Part 0 is structural and Part 3 is proven;
everything else is a fallback.** Eighteen of the twenty regressions in the
briefing were knowable before deploy, and the sibling agents' pre-deploy gates
are where they should be caught.
