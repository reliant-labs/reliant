# 04 — Billing, coupons, compute eligibility, daemon provisioning (control-plane backend)

Analysis only. No product code was edited. Companion to `BRIEFING.md`; facts
marked VERIFIED there are not re-derived here.

---

## The headline: why the e2e user stories stayed green

They stayed green because **they were never run against the environment that
broke, and by construction they cannot fail on a wrong config value.** Three
independent reasons, each sufficient on its own.

### 1. They are opt-in on PRs and never run against prod

`control-plane/e2e/user_stories/*.go` all carry `//go:build e2e`, so `task test`
excludes them entirely (Taskfile.yml:117-125). The only CI job that runs them is
`.github/workflows/e2e.yml`, which is gated:

```yaml
if: ${{ github.event_name != 'pull_request' ||
        contains(github.event.pull_request.labels.*.name, 'run-e2e') }}
```

So on a routine PR they do not execute at all. When they do run, `task e2e:up`
launches admin-server with a **hardcoded literal env block** (Taskfile.yml:186-200
— `ENVIRONMENT=e2e`, `RELIANT_ENV=e2e`, fake HMAC secrets) and `deploy/kcl/e2e/main.k`
sets `DEPLOY_ENV=e2e` and `ALLOWED_REDIRECT_HOSTS=localhost:3000,localhost:3001`.

That is the whole story in one line: **the suite asserts against an env whose
config is written by the test harness, so it can never observe that prod's config
is wrong.** `DEPLOY_ENV=e2e` is deliberately unrecognized (e2e/main.k:248), which
routes to `plans.yaml` with all-null prices — the exact opposite of the prod
configuration whose price IDs are the failure mode.

### 2. The assertions are deliberately written to tolerate the failure

This is the more important reason, and it is visible in the source. `story_06`'s
checkout assertion accepts *five* error codes as success:

```go
case connect.CodeFailedPrecondition, connect.CodeUnavailable,
     connect.CodeInvalidArgument, connect.CodeInternal, connect.CodeUnimplemented:
    t.Logf("... likely STRIPE_SECRET_KEY missing or plan has no stripe_price_id")
```

A missing `ALLOWED_REDIRECT_HOSTS` in a non-dev env makes `checkRedirectURL`
return `svcerr.InvalidArgument("redirect URL not allowed")` (service.go:386-393).
**`InvalidArgument` is on that accept-list.** So the precise prod outage the
briefing names — prod deployed without `ALLOWED_REDIRECT_HOSTS`, every checkout
refusing to mint — would be *logged as expected* by story_06 and the test would
pass. Same for a null/wrong `stripe_price_id`.

`story_06` is not a bad test; it is an honest one that was written to stay
meaningful in a dev env without Stripe creds. But the tolerance it buys for dev
is exactly the tolerance that makes it blind in prod. That tradeoff is the bug.

The same shape recurs in `story_01`/`story_02` (status-transition assertions,
no config content) and in `main_test.go`'s preflight, which `t.Skip`s the whole
test when the JWT is rejected (main_test.go:647) and `t.Skip`s when `AssignPlan`
fails (main_test.go:581-596). A misconfigured server produces skips, not failures.

### 3. What did NOT cause it

Worth stating because it redirects effort. The redirect logic is **not** duplicated
across three call sites, contrary to the initial hypothesis. All four RPCs
(`CreateCheckoutSession`:475, `CreateBillingPortalSession`:561,
`CreateComputeCheckoutSession`:841, `CreateWalletTopupSession`:1043) funnel through
the single `s.checkRedirectURL` helper, which exists precisely to centralize the
empty-allowlist policy (its doc comment says so). The three "call sites at ~540,
~895, ~1126" are where the already-validated URLs are handed to Stripe, not
independent validations. **There is no divergence hazard here and no test is needed
to pin that they agree.**

Likewise `service_test.go:94-151` already covers the policy thoroughly:
fail-closed in prod-ish envs, fail-open in dev, allowlist enforced regardless of
env once set. `plansconfig_test.go` has `TestSelectByDeployEnv_PreprodNeverLive`
asserting no LIVE price ID can leak to preprod. `internal/coupon/service_test.go`
has 22 tests including concurrency-under-cap and per-caller throttling;
`internal/svcdaemon` has `resume_funding_test.go` and
`workspace_state_reconciler_funding_test.go` pinning exactly the `b33be21a` fixes.

**The unit layer is genuinely good. Adding to it is low value.** Every remaining
gap is about *which values a deploy actually carries*, which no unit test can see.

---

## The state machine and where config enters it

```
signup ─→ GetCurrentUser (auto-provisions user + personal org)
       ─→ signupgrant.Grant (trial counter, IP-keyed)
       ─→ [optional] coupon.Redeem → wallet credit OR compute-minutes grant
       ─→ GetCurrentUserComputeEligibility ──┐
       ─→ CreateDaemon → checkDaemonSizeAllowed (per-owner advisory lock)
       ─→ daemon PENDING → workspace-controller → RUNNING
       ─→ CreateCurrentUserCheckoutSession → checkRedirectURL → Stripe
```

Config touches this path at four points, and they fail very differently:

| Config | Where it lands | Failure mode | Loud? |
|---|---|---|---|
| `ALLOWED_REDIRECT_HOSTS` | `checkRedirectURL` | **Loud-ish.** `logger.Error` at `NewService` (service.go:355) + every checkout returns InvalidArgument. But the server still boots and serves. | Partial |
| `DEPLOY_ENV` | `plansconfig.SelectByDeployEnv` | **Silent.** Unknown/empty → `plans.yaml`, all-null prices. Checkout then fails per-request with no startup signal. Safe-by-design (never leaks live prices) but silently un-checkout-able. | **No** |
| `STRIPE_SECRET_KEY` | `stripeClient` left nil | **Silent at boot**, per-RPC failure later. | **No** |
| test-vs-live key/price mismatch | Stripe API | **Silent at boot**, Stripe rejects at checkout with a confusing error. | **No** |

The asymmetry is the finding: **the one variable with a loud startup signal is
the one that already has good unit coverage. The three silent ones have none.**
And `ALLOWED_REDIRECT_HOSTS`'s "loud" is a log line in a Kubernetes pod that
nobody reads during a deploy — it does not fail the rollout.

One structural coupling worth naming: `APP_URL`'s host must appear in
`ALLOWED_REDIRECT_HOSTS`, or the frontend's own success URL is rejected by its own
backend. In `deploy/kcl/prod/main.k` these are two adjacent literal strings
(lines 273, 275) with nothing enforcing the relationship. `ALLOWED_REDIRECT_HOSTS`
is also duplicated verbatim on `_controller_env` (line 452) — a second literal
that can drift from the first.

---

## Proposals, ranked by evidence

### P1 — Pre-deploy config invariant check over `forge env render` output

**What.** A test/CI step that runs `forge env render <env>`, extracts the env
vars of each container, and asserts a set of cross-variable invariants that are
currently only enforced by human attention:

1. `URL(APP_URL).host ∈ ALLOWED_REDIRECT_HOSTS` — for every service that has both.
2. `ALLOWED_REDIRECT_HOSTS` non-empty whenever `RELIANT_ENV ∉ {local, dev, development}`
   (i.e. whenever `isDevReliantEnv` is false and the service would fail closed).
3. `DEPLOY_ENV` is set and is one of the four values `SelectByDeployEnv` actually
   recognizes — catching the "unknown label silently falls back to null prices" path.
4. `DEPLOY_ENV`-implied catalog matches `STRIPE_SECRET_KEY` mode: prod catalog
   (live price IDs) requires a live key, staging/preprod catalog requires a test key.
5. Where a var is duplicated across containers (`ALLOWED_REDIRECT_HOSTS` on both
   `_admin_env` and `_controller_env`), all copies agree.
6. The packaged-desktop origin `app://bundle` ∈ `CORS_ORIGINS` in any env that
   ships a packaged app.

**(a) Historical bugs it would have caught.** This is the highest-evidence
proposal in my area:
- The prod deploy that dropped `ALLOWED_REDIRECT_HOSTS` (the outage
  `service_test.go:97-101` documents in prose but cannot detect) — invariant 2.
- `6d74bc3d` `GITHUB_REDIRECT_URI` targeting the admin host rather than the app
  domain — same family; extend invariant 1 to that var.
- `b1146f16` missing `app://bundle` in `CORS_ORIGINS` — invariant 6.
- `bb0e939a` empty-interpolation port-block key in prod — a render-shape assertion
  catches empty-string interpolation directly.
- The `ENVIRONMENT`-vs-`DEPLOY_ENV` staging/preprod mis-selection that motivated
  `SelectByDeployEnv` — invariant 3/4 catches the recurrence.
- `7f403355` (`VITE_CLI_DEFAULTS_BAKED` missing in prod) is the frontend twin;
  the same harness covers it if the Vite env block is included.

Six-plus historical bugs, several of them prod-only, from one detector.

**(b) Where it fires.** CI on **every** PR (render needs no cluster — verified:
`forge env render` is explicitly read-only, resolves no kubectl context, creates
no cluster, builds no image), **and** as a pre-deploy gate in `deploy.yml`. This
is the one that answers the user's actual complaint — "we don't discover until
after we deploy" — because it inspects prod's config without deploying prod.

**(c) Cost.** Low. One Go test file, ~200 lines including the invariant table,
plus a YAML walk of the render stream. Runtime: seconds per env. Maintenance is
genuinely low because invariants are *relationships*, not values — adding a new
host to `ALLOWED_REDIRECT_HOSTS` does not touch the test. The one real cost is
that each new invariant is a deliberate decision someone must make.

**(d) Residual gap.** It validates the *rendered manifest*, not the *running pod*.
It cannot catch: a secret whose Kubernetes reference resolves to an empty or wrong
value (invariant 2 sees the var is declared, not that the Secret has content); a
value mutated post-deploy by hand; anything Stripe-side (a price ID that is
syntactically fine and correctly mode-matched but points at the wrong product or
a deleted price). P3 covers that last one.

**Forge note.** No forge defect blocks this — `forge env render` is exactly the
right primitive and already exists. If anything, the natural home for invariants
1/2/6 is *forge itself* as a `forge lint`-style env-coherence check, since
"APP_URL's host must be in the redirect allowlist" is a pattern any forge project
with Stripe would want. Proposing it as a project-local test first and promoting
it to forge once the invariant set stabilizes is the lower-risk order.

---

### P2 — Make story_06's checkout assertion strict, keyed off the server's own config

**What.** The change is small and surgical: story_06 currently accepts five error
codes unconditionally. Instead, have the test read the target server's declared
env (it already knows which env it is pointed at via `E2E_ENV` /
`E2E_ADMIN_SERVER_URL`) and **require success** when the env is one that claims
to have Stripe wired, permitting the tolerant path only for the null-price dev
catalog. Concretely: if `ALLOWED_REDIRECT_HOSTS` is configured and the picked plan
has a non-null `stripe_price_id`, `InvalidArgument` from checkout is a **failure**,
not a log line.

Second, narrower change: add an assertion that a success/cancel URL built from the
server's own `APP_URL` is *accepted*. Today story_06 hardcodes
`http://localhost:3000/billing?status=success`, which passes trivially in e2e and
tells you nothing about prod's allowlist.

**(a) Historical bugs.** The `ALLOWED_REDIRECT_HOSTS` prod outage — but only if
the suite is ever pointed at a prod-like env, which today it is not. Zero bugs
caught in its current wiring. That is the honest answer and it is why this is P2,
not P1.

**(b) Where it fires.** Wherever the suite is pointed. Its real value is as the
**post-deploy smoke test** — run the strict story_06 against staging/preprod
immediately after a deploy. A fast post-deploy detector still beats a user bug
report.

**(c) Cost.** Very low to write (a conditional around an existing switch).
Moderate to *keep green*, and this is the honest risk: the reason those five
codes are accepted is that the suite runs against whatever env a developer has
up. Making it strict will produce failures on under-configured local envs unless
the conditional is exactly right. Mitigate by keying strictness off observable
server config rather than a hardcoded env list.

**(d) Residual gap.** Only runs where it is pointed, and only post-deploy. Cannot
catch anything in an env nobody smoke-tests. Does not catch build-time-baked
frontend config at all (out of my area; see the frontend agent's file).

---

### P3 — Periodic assertion that the plan catalog matches Stripe reality

**What.** A scheduled job (daily) that, for each billing env, loads the catalog
`SelectByDeployEnv` would select and calls Stripe's API to confirm every non-null
`stripe_price_id` (a) exists, (b) is active, (c) is in the expected mode
(test vs live), and (d) belongs to the expected product. Read-only against Stripe.

**(a) Historical bugs.** None directly from the listed hashes — this is the one
proposal I cannot back with a specific commit. Its argument is prospective: the
`1a46421a` "single-source Stripe" work and the preprod-live-price near-miss both
indicate this surface has been actively churned, and a price ID that is deleted
or archived *in the Stripe dashboard* is a failure mode with **no code change at
all** to trigger CI. Nothing else proposed here can see it.

**(b) Where it fires.** Continuous production monitor (scheduled workflow).

**(c) Cost.** Moderate — needs read-only Stripe API keys for both modes in CI,
which is a real secrets-management decision. Low maintenance once running.

**(d) Residual gap.** Says nothing about whether a *user* can complete checkout;
only that the catalog references real prices. Cannot catch webhook
misconfiguration, which is the other half of the Stripe integration.

---

### P4 — Startup refusal on incoherent billing config (considered, recommended AGAINST as designed)

The tempting version is: refuse to boot when `ALLOWED_REDIRECT_HOSTS` is empty in
a non-dev env. I do not recommend it. The current design is better — it already
fails **closed** per-request (`checkRedirectURL` returns InvalidArgument) with a
loud `logger.Error` at construction, so billing is safely disabled while the rest
of the control plane keeps serving. Refusing to boot would convert a
billing-only outage into a total control-plane outage, including daemon
lifecycle and the LLM gateway, which is strictly worse. `internal/plansconfig`'s
fallback has the same shape and the same justification.

The part of the idea worth keeping: make the existing `logger.Error` **visible
during rollout** — surface it as a failing readiness detail or a metric that the
deploy watches — so the signal that already exists stops being a log line nobody
reads. That is a small change and it composes with P1 rather than competing.

I also considered a **stripe-mock contract test** for redirect minting and
rejected it: `checkRedirectURL` is already unit-tested against every branch, and
a mock cannot know what `ALLOWED_REDIRECT_HOSTS` prod carries — which is the
entire bug. It would add a harness and catch nothing new.

---

## Summary table

| # | Proposal | Historical bugs | Fires | Cost | Recommend |
|---|---|---|---|---|---|
| P1 | Config invariants over `forge env render` | 6+ (incl. the redirect-hosts outage, `6d74bc3d`, `b1146f16`, `bb0e939a`) | PR CI + pre-deploy gate | Low | **Yes — build first** |
| P2 | Strict story_06 keyed off server config | 0 as wired; the redirect outage if pointed at prod-like | post-deploy smoke | Low build, moderate upkeep | Yes, second |
| P3 | Stripe catalog reality check | 0 (prospective) | daily monitor | Moderate (secrets) | Yes, if P1/P2 land |
| P4 | Boot-time refusal | — | startup | Low | **No** — keep fail-closed-per-request; surface the existing error instead |

## What I explicitly do not recommend

- More unit tests in `svcbilling`, `coupon`, `svcdaemon`, or `plansconfig`. That
  layer is well covered and every listed regression in my area either already has
  a pinning test (`b33be21a` → `resume_funding_test.go` +
  `workspace_state_reconciler_funding_test.go`; `3d153cff` → the 22 coupon tests)
  or is invisible to unit tests by nature.
- A test pinning that the three Stripe redirect call sites agree — they already
  share one helper. The premise does not hold.
