# Briefing: onboarding / auth / billing regression detection

**Status: shared context for a parallel investigation. Do not delete.**
Written by the orchestrating agent before fan-out. Every agent in this wave
reads this first. Facts marked VERIFIED were established by direct inspection
and must not be re-derived — re-running those probes wastes the turns this
file exists to save.

## The problem, in the user's words

> We've had a handful of issues and regressions regarding onboarding and
> linking to other features. The annoying part is it's typically env/config
> driven — so prod fails, which is harder to test/validate, or we don't
> discover until after we deploy.

Named problem areas: reliant web **and** Electron; connect/redirect from
GitHub, Google, etc.; connect/redirect from billing; spinning up daemons; the
coupon system.

**The deliverable for this wave is DESIGN AND ANALYSIS, not fixes.** Other
agents are already landing the fixes. Do not edit product code. Write your
findings to your assigned file. Touching a source file risks colliding with a
fix agent working in the same tree.

## The single most important observation

This is **not** a "we need more tests" problem. There is already substantial
coverage:

- `reliant/web/src/components/OnboardingFlow/__tests__/` — 14 unit test files,
  including `launchPlanSchema.drift.test.ts`, `hasUsableDaemonForOnboarding.test.ts`,
  `OnboardingRoute.returning.test.tsx`.
- `reliant/web/e2e/onboarding.spec.ts` — Playwright.
- `control-plane/e2e/user_stories/` — `story_01_onboarding_test.go`,
  `story_06_billing_test.go`, `story_07_full_onboarding_flow_test.go`,
  `story_02_daemon_lifecycle_test.go`.
- `control-plane/internal/billing/svcbilling/service_test.go` — already has a
  test whose comment explicitly describes a prod deploy that dropped
  `ALLOWED_REDIRECT_HOSTS`.

All of that passed while the regressions shipped. **The bugs live in the space
those tests cannot reach: the value of an environment variable in a specific
deploy target, and the shape of a runtime that CI never builds (packaged
Electron, prod Vite build).** Proposals that amount to "add another unit test
in the same layer" are low value. Say so if that is your honest conclusion for
your area.

## VERIFIED: the config surface where these bugs actually live

Per-env KCL is the source of truth for deploy config:
`control-plane/deploy/kcl/{dev,dev-k8s,e2e,prod}/main.k`.

The load-bearing, env-varying, redirect-related variables (VERIFIED by reading
the KCL):

| Var | dev-k8s | prod |
|---|---|---|
| `APP_URL` | `http://localhost:3000` | `https://app.reliantlabs.io` |
| `CORS_ORIGINS` | `http://localhost:3002` (admin) | `https://app.reliantlabs.io,https://reliant-prod.web.app,app://bundle` |
| `ALLOWED_REDIRECT_HOSTS` | — | `app.reliantlabs.io,reliant-prod.web.app` |
| `WORKSPACE_BASE_DOMAIN` | `workspaces.localhost` | `workspaces.reliantapi.com` |

Note the coupling that nothing currently enforces: `APP_URL`'s host must appear
in `ALLOWED_REDIRECT_HOSTS`; the packaged desktop origin `app://bundle` must
appear in `CORS_ORIGINS`; `GITHUB_REDIRECT_URI` must point at the app domain,
not the admin host. Each of those three is a real past outage (see below).

Frontend config crosses the same boundary a second way, through Vite:
`VITE_APP_URL`, `VITE_CONTROL_PLANE_API_URL`, `VITE_GRPC_URL`, `VITE_API_URL`,
`VITE_SUPABASE_*`, `VITE_CLI_DEFAULTS_BAKED`, `VITE_DISABLE_TLS`. These are
**build-time baked**, injected from KCL
(`control-plane/deploy/kcl/frontend_config_gen.k`, and the electron/web env
blocks in each env's `main.k`). A wrong value is compiled into an artifact; no
runtime test of a dev server can observe it.

Relevant reading, already located so you don't have to search:
- `reliant/web/src/api/grpc-client.ts` and `grpc-unauth.ts` — the URL
  resolution ladder (`RELIANT_CONFIG` → `window.location.origin` → `VITE_*`).
  The ladder's ORDER has itself been a bug.
- `reliant/web/src/lib/cli-commands.ts` — carries a long comment about
  `VITE_CLI_DEFAULTS_BAKED` being the only input, added after a bug where
  `import.meta.env.DEV` was checked first.
- `reliant/web/src/store/authStore.ts` — comment: "`window.location.origin` is
  an ephemeral loopback port — sending that to a[n OAuth provider]…"
- `control-plane/internal/billing/svcbilling/service.go:317-356` — fail-loud
  handling for missing `ALLOWED_REDIRECT_HOSTS`.

## VERIFIED: the regression history (mined from git log, last 90 days)

This is the evidence base. Note how many are config/packaging shape, not logic:

**reliant repo**
- `a9c4e172` fix(release): inject hosted endpoints into packaged builds
- `3fcd9f79` fix(web): recognize the packaged `app://` origin, and fail PKCE loudly
- `72390d35` fix(auth): never send a loopback origin as the OAuth redirect
- `487fb19a` fix(electron): serve the packaged renderer over `app://` so it actually loads
- `2c859d2d` fix(release): source the packaged app's config from forge KCL, delete the `.env`
- `3d79e158` fix(web): a missing Supabase key must not white-screen the app
- `458a830c` fix(web): don't leave /onboarding while the cloud daemon is still provisioning
- `63b18468` fix(web): give stranded users a way to settings, and keep modals off /onboarding
- `44a29607` fix(web): gate ProjectPicker's cloud-daemon offer on compute eligibility
- `48969c66` fix(oauth): hand off to queued flows before tearing down the callback listener
- `5b17a5d9` docs: investigation findings for the OAuth and onboarding bugs
- `1dccce7f` fix(web): rewrite onboarding.spec.ts against the current onboarding model
- `e35d0b13` …the prod daemon/clone/onboarding fixes
- `ed96ce8` fix(web): use absolute Vite base so deep SPA routes load assets ← current branch

**control-plane repo**
- `6d74bc3d` deploy: `GITHUB_REDIRECT_URI` targets the app domain, not admin host
- `b1146f16` fix(cors): allow the packaged desktop app's `app://bundle` origin
- `7f403355` fix(kcl): tell prod's web UI that the CLI defaults are baked
- `d277c59e` config: move GitHub OAuth client-id/redirect to config source of truth
- `c5e0f0d6` / `989e2194` / `8f04e708` dev+electron env wiring that "survives OSS .env strip"
- `3d153cff` coupon redemption fixes
- `b33be21a` fix(daemon): gate compute funding on resume and reconciler republish
- `c639f243` compute eligibility, coupons, and Zitadel recovery
- `cd4bffa3` docs: billing-bypass investigation findings
- `bb0e939a` fix(prod): stop composing an empty-interpolation port-block key

There are also two existing findings docs at the reliant repo root:
`reliant/ONBOARDING_FINDINGS.md` and `reliant/OAUTH_REDIRECT_FINDINGS.md`.
Read them — they are prior art on these exact bugs.

## VERIFIED: onboarding's control flow

`deriveStep(plan)` in
`reliant/web/src/components/OnboardingFlow/stepConfig.ts` is the ONLY thing
that decides which step renders, derived purely from the `plan` search param.
Steps: `compute → model → project-choice → github-connect → project-picker`,
with a cloud/local branch. `onNext()` is a no-op. Backward motion works by
clearing plan fields (`BACK_CLEARS`).

Critically (from repo memory, VERIFIED against `OnboardingRoute.tsx`'s
existence and `useReturningUserHeal.ts`): **a background `useEffect` can end
onboarding with no user action.** The returning-user heal calls
`CompleteOnboarding` and navigates to `/` when it sees a daemon postdating the
account — and creating a daemon *during* onboarding satisfies that condition.
A previously observed instance: `daemon created name=onboarding-daemon` at
17:24:40.809, `onboarding completed` 39ms later, no step in between.

## VERIFIED: the billing detour

`reliant/web/src/hooks/useGoToBilling.ts` routes anonymous users to `/upgrade`
with `returnTo: "/settings/billing"` before letting them reach billing, because
a subscription bought against an anonymous browser-session identity belongs to
nobody. Stripe redirect URLs are minted server-side in
`control-plane/internal/billing/svcbilling/service.go` (`successURL` /
`cancelURL`, three separate call sites at ~540, ~895, ~1126) and validated
against `ALLOWED_REDIRECT_HOSTS`.

## Where the logs are (do not hunt for these)

Every process in the `forge env up` dev stack — Go services, Vite, Electron,
**and the browser console** — writes to ONE directory:

```
control-plane/.forge/logs/dev/
```

`frontend_reliant-web.log` carries browser/renderer `console.*` prefixed
`[browser:<level>]`, plus uncaught errors and unhandled rejections, from both
Chrome and the Electron renderer. `admin-server.log` has the billing/coupon/
daemon/CompleteOnboarding RPC sequence. Do not add a second log location and
do not ask a human to paste console output.

## House rules that constrain your proposals

- **Never work around forge.** Build/deploy/render all go through forge
  (`forge build`, `forge env up`, `forge env deploy`, `forge env render`). If
  forge cannot do a thing your proposal needs, that is a forge defect to name
  explicitly — propose the forge change, don't propose a bypass.
- **Never run `forge env down`, `pkill forge`, or any pattern-matched kill.**
  You are running inside a forge-hosted session; that terminates your own work
  and every parallel agent's.
- **Do not run git commands that mutate state** (stash/checkout/reset/commit).
  Multiple agents share this checkout. Read-only git (`log`, `show`, `diff`) is
  fine.
- Pre-launch: no backwards-compatibility burden. Removing an old code path is
  a legitimate part of a proposal.
- Postgres is the only supported driver in reliant.

## What a good deliverable looks like

Rank your proposals by **evidence**, not by elegance. For each one state:

1. **Which specific past bug(s) from the list above it would have caught**, by
   commit hash. A proposal that catches zero historical bugs needs an argument
   for why the next bug differs from the last twenty.
2. **Where in the pipeline it fires** — pre-commit, CI on PR, pre-deploy gate,
   post-deploy smoke, or continuous production monitor. Note the user's
   specific pain: "we don't discover until after we deploy," so detectors that
   fire *before* a prod deploy are worth more than ones that fire after — but a
   fast post-deploy detector still beats a user bug report.
3. **Cost to build and to maintain**, honestly. A hermetic full-stack harness
   that nobody can keep green is worth less than a 40-line invariant check that
   runs in 200ms.
4. **The failure mode it CANNOT catch.** Be explicit about the residual gap.

Prefer a small number of high-conviction recommendations over an exhaustive
menu. If your honest read is that one cheap detector covers 70% of your area
and the rest is not worth building, say exactly that.
