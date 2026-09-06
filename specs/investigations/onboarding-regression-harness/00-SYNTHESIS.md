# Synthesis: catching onboarding / auth / billing regressions before prod

Seven parallel investigations, one ranked plan. Files `01`–`07` hold the
detail and the evidence; this is the decision document. `BRIEFING.md` holds
the shared context and the 90-day regression history the whole wave was
ranked against.

**Nothing here has been implemented.** Everything below is a proposal, costed.
No product code was touched by this investigation.

---

## The finding that reframes the problem

The premise going in was "we need better tests." That is not what the evidence
says. Coverage is extensive — 14 onboarding unit test files, a Playwright
suite, 8 Go e2e user stories, 22 coupon tests — and it was green throughout.

**Every detector we needed already existed somewhere. It was pointed at the
wrong artifact, tolerant of the exact failure, silently skipped, or dark.**
Four independent agents converged on this, and it changes what to build:

| What we have | Why it didn't fire |
|---|---|
| `dist/` bundle assertions in `deploy-reliant-web.yml:226-316` — including the *exact forensic grep* that confirmed the v1.7.5 coupon outage, promoted to a gate | Runs on the **hosted web** path only. `reliant/release.yml`, which builds the desktop artifact users download, runs **none** of them |
| `story_06_billing_test.go` checkout assertion | Accepts 5 error codes as success, **including `InvalidArgument`** — precisely what a missing `ALLOWED_REDIRECT_HOSTS` returns. The outage would have been logged as *expected* |
| `sync-release-config.mjs --check` KCL drift gate | No-ops silently when `kcl` or the sibling checkout is missing. `ci.yml` already documents a past instance: "This job passed only because a cache hit skipped the install" |
| Playwright E2E Frontend job | "Cancelled at 15m in every one of the last 50 runs that reached the test step… never a slow-but-passing job" |
| Sentry (genuinely live, DSN confirmed in shipped prod bytes) | Zero alert rules in version control. A forensic archive, not a detector |
| Statsig onboarding funnel | No key in any KCL file, zero key strings in the prod bundle. **The funnel is discarded by the process that generates it** |
| OTel | Code reads `VITE_OTEL_EXPORTER_OTLP_ENDPOINT`; forge's knob emits `VITE_OTEL_ENDPOINT`. Setting the documented knob does nothing |

The through-line is **a gate that cannot fail is indistinguishable from a gate
that passes**, and this codebase has at least seven of them. That makes "prove
the gate ran, and make it strict" a higher-yield investment than any new
harness — and it is much cheaper.

Second-order consequence, and the reason this was hard to see: several of these
manufacture *belief* in coverage. That is worse than an acknowledged gap,
because it redirects attention away from the hole.

---

## Ranked recommendations

Ranked by (historical bugs caught) ÷ (cost), with ties broken by how early the
detector fires. Tier 1 is roughly three days of work and covers most of the
90-day history.

### Tier 1 — do these first

**1. KCL `assert`s for the redirect/CORS coupling.** ~200 lines, one afternoon.
The single highest-value item, and three agents converged on the invariants
from different directions. `forge ci validate-kcl` **already runs on every PR**
and `assert` is already used in five places, so this needs no new workflow, no
new tool, and no forge change — and it fires at *author time, in the editor,
before a commit exists*. That is the direct answer to "we don't discover until
after we deploy."

The invariants: `APP_URL`'s host ∈ `ALLOWED_REDIRECT_HOSTS`; every origin the
app can present (including `app://bundle`) ∈ `CORS_ORIGINS`; `GITHUB_REDIRECT_URI`
under `APP_URL`; `DEPLOY_ENV` ∈ values `plansconfig.SelectByDeployEnv` accepts;
plan-catalog price mode matches Stripe key mode; duplicated vars across
containers agree.

Catches `6d74bc3d`, `b1146f16`, `d277c59e`, `7f403355`, `bb0e939a`, and the two
live bugs below. Highest-value single assert is **`GITHUB_REDIRECT_URI` must sit
under `APP_URL`** — two lines, one historical outage, one live violation, and a
failure mode that is nearly undiagnosable from symptoms because OAuth just
returns the user to the wrong place with nothing logged server-side.

*Settled disagreement:* the billing and auth agents proposed asserting over
`forge env render` **output**; the config agent argued for siting them in
**KCL source**, and wins on three counts — output-testing fires later (CI only,
never the editor), catches less (it sees rendered values, so it cannot tell that
two literals were *meant to be one fact*), and creates a second place invariants
are written, which is the exact duplication this whole area is about.

**2. Run the existing bundle gates on the desktop artifact.** Half a day,
sub-second wall-clock. Consolidate `deploy-reliant-web.yml`'s three gates into
one script, derive expected hostnames from `release.config.json` instead of
hardcoding them (closing the 15-vs-22 variable gap *by construction*), add a
base-path check, and run it in `release.yml`'s `prepare` job before the
three-platform matrix. Covers 6–7 historical commits including `a9c4e172`,
`2c859d2d`, `7f403355`. This is pure reuse — the hard part is written and
proven.

**3. Forward `logger.Error` to Sentry.** ~30 lines. The cheapest item with a
*proven* miss behind it: missing `ALLOWED_REDIRECT_HOSTS` **already logged an
error and still reached users**. Delivery is the bottleneck, not instrumentation.
Pair with a 60-second `kubectl logs --since=60s` rollout gate that blocks the
*deploy* rather than the *service*.

**4. Make the gates strict and prove they ran.** ~half a day total. Fail
`sync-release-config.mjs --check` loudly when it cannot render rather than
skipping; make `story_06` require checkout success where Stripe is wired;
alert when a CI gate has been red or cancelled for N consecutive runs.

**5. Cold deep-route loads in Playwright.** ~20 lines, 60–90s of CI. Catches
`ed96ce8` — and the analysis of *why* the existing preview-mode suite missed it
is the most transferable insight in the investigation (see below).

**6. One `leaveOnboarding(reason)` exit point.** Onboarding currently has three
exits using three different mechanisms — two via the route's `useNavigate`, one
via an un-awaited global router singleton. This **deletes** the `navigate: false`
flag that must currently be threaded correctly through four call sites rather
than adding machinery, and surfaced as `onboarding_exited{reason}` it becomes
the only signal that distinguishes "user completed onboarding" from "onboarding
completed itself at the user" — currently recorded nowhere.

**7. Exhaustive `deriveStep` enumeration.** <1 hour, 240 states, 4 invariants,
in the shape of the existing `launchPlanSchema.drift.test.ts`. **Fails today**
on both live defects below.

### Tier 2 — worth doing, after Tier 1 lands

**8. Provider-acceptance probe, on a timer.** The only proposal in the wave that
is *not* a fallback. The Supabase / Google / GitHub redirect allow-lists live
outside both repos; a human changes one in a web console with no commit, and
every pre-deploy gate stays green while sign-in breaks. No static check can
reach this **even in principle**. It works because RFC 6749 makes an
unregistered `redirect_uri` the one error a provider must not redirect back on,
so the response *shape* is the assertion; confirmed to need only public
`client_id` values, no secrets. Must run on a **timer**, not on deploy, because
the thing it guards against changes without a deploy.

**9. Packaged-Electron boot smoke test.** CI already packages an unsigned
`--dir` Linux app and discards it, so launching it is cheap. Asserts renderer
boot over `app://`, config presence, and non-blank window. **Consciously accept**
the backend-dependent half: that job ships an empty `resources/server`, so it
cannot cover daemon spawn or purchase completion.

**10. Absolute-zero funnel alerts via Sentry**, plus the billing funnel routed
server-side through the **Stripe webhook** — which stays correct in packaged
Electron where checkout completes in the system browser and the client never
learns the outcome. Alert on absolute zero, not percentage change: at pre-launch
volume a "conversion dropped 30%" alert fires because Tuesday had two signups
instead of five, gets muted within a week, and a muted alert is worse than none.

### Explicitly rejected, with reasons

- **Boot-time refusal on incoherent billing config.** Billing already fails
  closed per-request, so it degrades alone; refusing to boot converts a billing
  misconfiguration into a full control-plane outage *including daemon lifecycle*.
  Two agents independently reached this, and one withdrew its own earlier
  fail-fast proposal. Line drawn: fail fast only where absence breaks *every*
  request path; warn-and-fail-closed per-request for feature-scoped vars.
- **Mock-IdP round trip.** `forgetest/jwks.go` mints tokens for reliant's own
  validation; it is not an authorization server. None of the ~20 historical
  regressions were protocol bugs.
- **Full XState refactor of onboarding.** The pure URL-derived state machine is
  the part that was never broken, and it survives OAuth full-page navigation
  *for free* precisely because it lives in the URL.
- **MSW.** Not the bug class.
- **A Prometheus scraping stack** for one alert. A periodic job emitting to
  Sentry is proportionate.

---

## Live defects found while investigating

Not history — present in the tree, found incidentally, **not fixed** (this wave
was analysis-only, and fix agents are working in the same tree).

1. **`GITHUB_REDIRECT_URI` breaks on every parallel worktree past the first.**
   `dev/config.k:21` hardcodes `http://localhost:3000/auth/github/callback`,
   while `dev/main.k:326` derives `APP_URL`, `CORS_ORIGINS`,
   `ALLOWED_REDIRECT_HOSTS` and `VITE_APP_URL` from `fp.allocate_port(3000, _key)`
   (feeding `:633` and `:710`). Outside port block 0 those four move together and
   the GitHub redirect stays pinned. Same shape as `6d74bc3d`. *Confirmed at
   source level by three parties independently; runtime symptom not confirmed —
   nobody ran a non-block-0 worktree.*

2. **`ALLOWED_REDIRECT_HOSTS` duplicated verbatim** at `prod/main.k:275` and
   `:452` (admin vs controller env), with nothing keeping them in sync.

3. **`compute: "cloud_paid"` takes the local branch.** It is a valid
   `ComputeChoice` the schema accepts, but every cloud test in the flow is
   `=== 'cloud_free_trial'`, so it never renders `DaemonConnectingGate` —
   bug `458a830c`, latent on the paid path.

4. **Blank screen with no escape hatch.** `OnboardingPage.tsx:64` returns `null`
   before rendering the card that holds the escape hatch. Trigger is step
   *registration* never running (the steps are statically imported and
   registered by a module-scope side effect, not lazy-loaded). Bug `63b18468`
   reintroduced through a different door.

5. **Post-Stripe return cannot distinguish success from cancel.** All three
   client call sites pass `window.location.href` for **both** `successUrl` and
   `cancelUrl`.

6. **OTel knob name mismatch** — code reads `VITE_OTEL_EXPORTER_OTLP_ENDPOINT`,
   forge emits `VITE_OTEL_ENDPOINT`. A forge-side defect worth reporting
   upstream per the forge bug rule.

7. **`lib/env.k` has a full `preprod` branch but `deploy/kcl/` has no `preprod`
   directory.** Either dead config to delete or a missing env.

Plus seven more in the billing path, listed in `06-billing-ux.md`.

---

## The billing flow (the second ask)

**What actually happens after Stripe** — the open question: it *does* return to
`/settings/billing`, but on the **Overview** tab rather than Plans, with no
success confirmation, as a full SPA cold boot. And because both redirect URLs
are `window.location.href`, the app lands on a byte-identical URL whether you
paid or clicked back. It cannot tell, and does not try.

In packaged Electron it is worse: `redirectToStripe` sets `window.location.href`
to an https URL, `navigation-policy.js:56-67` correctly classifies that as
external relative to `app://bundle`, and `main.js:1047-1050` hands it to
`shell.openExternal`. **The purchase completes in the system browser and the app
window is never notified.** Note this is *not* a navigation-policy bug — the
classification is right, and a test asserting `false` for Stripe would encode
the wrong rule. The defect is a missing return path for a round trip.

**Current cost:** 5 full-page navigations, 4 context switches, 1 re-authentication
that is really an identity *link* but renders as a sign-in screen, ≥4 clicks
before Stripe. **Zero of the five navigations are explained before they happen** —
the rationale for the biggest one lives in a code comment no user reads.

**Recommended: keep the identity guarantee, delete the navigation.** Send
everyone straight to billing, deep-linked to Plans with the originating context.
Move the anonymity check *into the checkout mutation* so it cannot be bypassed,
and when it fires open an identity-link modal **in place**, with copy that names
the reason. Email+OTP then completes with zero navigation. Add
`?checkout=success|cancelled`. Open Stripe in a controlled Electron
`BrowserWindow` rather than the system browser.

Result: **1 navigation** on the email path, **3** on OAuth (honest number — the
OAuth path still redirects the whole window, so this is 5→3, not 5→1).

**Sequencing constraint, load-bearing:** removing the redirect from
`useGoToBilling` deletes a single-chokepoint guarantee across five call sites.
**Do not do that step unless the check has already moved into the mutation.**

---

## Why the existing Playwright suite missed the Vite base bug

Worth stating separately, because it generalizes. Two independent reasons,
either sufficient:

1. **The gate was dark** — cancelled at 15m in every one of the last 50 runs
   that reached the test step.
2. **Even fully green it could not have caught it.** Every test navigates to a
   depth-0 or depth-1 route, and the bug only manifests at depth 2+. At `/`,
   `./assets/x.js` and `/assets/x.js` resolve identically. You need a *cold* load
   at something like `/auth/github/callback` for the relative path to resolve
   into the route's directory, 404, and return `index.html` as `text/html`.
   Client-side navigation cannot catch it either — once booted, `pushState`
   fetches nothing.

**And this is why the whole bug class presents as an OAuth/billing problem:
those are exactly the URLs users arrive at cold, from an external redirect.**
The onboarding-adjacent features are not unusually fragile; they are simply
where cold deep-link entry happens.
