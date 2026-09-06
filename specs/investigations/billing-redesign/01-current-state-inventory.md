# 01 — Current-state inventory: billing + provisioning, end to end

**Status:** reference document. Analysis only; no product code was changed.
**Audience:** the agent designing the target flow, and the implementers after them.
**Method:** read the verified prior investigations
(`onboarding-regression-harness/00-SYNTHESIS.md`, `04-billing-daemon-backend.md`,
`05-onboarding-state-machine.md`, `06-billing-ux.md`), then re-read every file
they cite plus everything reachable from
`control-plane/internal/billing/svcbilling/service.go` and
`reliant/web/src/hooks/useCloudBillingQueries.ts`.

Every claim below carries a file:line. Where I am inferring rather than
reading, the sentence says **INFERRED**. Section 9 lists what surprised me,
what looks like a latent bug, and what the redesign has to *decide* because
nothing in the code decides it today.

Note that `06-billing-ux.md` describes a state of the world that has since been
partly fixed. Section 8 reconciles the two: five of its seven reported bugs are
now closed. Do not carry its bug list forward without reading section 8 first.

---

## 1. The RPC surface

All end-user billing lives on ONE Connect service,
`controlplane.v1.BillingService` (`control-plane/proto/services/billing/v1/billing.proto`),
handled by `control-plane/internal/handlers/billing/` (thin: validate → resolve
caller → call service → map errors) over
`control-plane/internal/billing/svcbilling/service.go` (2,924 lines; its own
comments call it "a god-orchestrator scheduled for rewrite").

Daemon provisioning is a *different* service, `controlplane.v1.DaemonService`
(`proto/services/daemon/v1/daemon.proto` → `internal/handlers/daemon/handlers.go:38`
→ `internal/svcdaemon/service.go:87`). The two are joined only by shared
`compute_grants` / `subscriptions` reads — there is no call from one to the
other.

### 1.1 The master table

"Cost" means: does this spend money, create a Stripe object, or create
infrastructure. "Idempotent" means: what happens if the client fires it twice.

| RPC | Handler | Service impl | Preconditions | Returns | Cost | Idempotent? |
|---|---|---|---|---|---|---|
| `ListPlans` | `handlers.go:28` | `service.go:479` | authenticated | plan rows incl. `limits` JSON | none | yes (pure read) |
| `GetPlan` | `handlers.go:39` | `service.go:488` | authenticated | one plan | none | yes |
| `GetCurrentUserSubscription` | `handlers.go:50` | `service.go:839` | authenticated; user has an org (else `(nil,nil,nil)`) | org-scoped sub + plan | none | yes |
| `GetCurrentUserComputeSubscription` | `handlers.go:201` | `service.go:881` | authenticated | user-scoped **compute** sub + plan | none | yes |
| `GetCurrentUserComputeEligibility` | `handlers.go:237` | `service.go:2294` | authenticated | `{eligible, hasActiveSubscription, reason, planName, grantedMinutesRemaining}` | none | yes |
| `GetCurrentUserWalletOverview` | `handlers.go:94` | `service.go:1024` | authenticated; **user has an org** else `FailedPrecondition` | wallet, ledger page, top-ups | **creates a wallet row if absent** (`service.go:984`) | yes (`CreateWalletIfNotExists`) |
| `GetCurrentUserComputeUsage` | `handlers.go:223` | `service.go:2201` | authenticated | minutes used/included/overage, by-day, by-workspace | none | yes |
| `GetCurrentUserBillingEmail` | `handlers.go:179` | `service.go:2707` | authenticated | `{billingEmail, fallbackEmail}` | none | yes |
| `UpdateBillingEmail` | `handlers.go:190` | `service.go:2684` | authenticated; email passes `validateBillingEmail` (`service.go:2727`) | empty | none | **yes** — last write wins, validated before write |
| `ListCurrentUserInvoices` | `handlers.go:83` | `service.go:1238` | authenticated; user has an org | Stripe invoices | none (Stripe read) | yes |
| `SetCurrentUserComputeOverage` | `handlers.go:212` | `service.go:2169` | authenticated; **active compute sub** | updated sub | none directly — **arms future overage charges** | yes (set-to-value) |
| `RedeemCoupon` | `rpc_redeem_coupon.go:25` | `internal/coupon/service.go:134` | authenticated; **org resolves**; code valid/active/unexpired/uncapped; per-caller rate limit | `{kind, amountCents, newBalanceCents, computeMinutes, newComputeMinutesRemaining}` | **grants value** (wallet credit or compute minutes) | **yes, by construction** — see §3.2 |
| `CreateCurrentUserBillingPortalSession` | `handlers.go:72` | `service.go:1224` → `610` | authenticated; org exists; org has a Stripe customer; `returnUrl` passes `checkRedirectURL` | portal URL | creates a Stripe **portal session** (free) | effectively (new session each time) |
| **`CreateCurrentUserCheckoutSession`** | `handlers.go:61` | `service.go:865` → `CreateComputeCheckoutSession` `service.go:893` | see §2.2 — the longest chain in the system | `checkout_url` | **BILLABLE.** Creates a Stripe Customer and a Checkout Session; **cancels the existing subscription** | **NO** — see §3.1 |
| **`CreateCurrentUserWalletTopupSession`** | `handlers.go:105` | `service.go:1207` → `CreateWalletTopupSession` `service.go:1095` | see §2.3 | `checkout_url` + topup row | **BILLABLE.** Creates a Stripe Customer, a **`wallet_topups` DB row**, and a Checkout Session | **NO** — a new pending `wallet_topups` row per call |
| **`CreateDaemon`** (DaemonService) | `internal/handlers/daemon/handlers.go:38` | `internal/svcdaemon/service.go:87` | see §2.4 | daemon row + NATS create command | **RESOURCE-CREATING.** Provisions a k8s workspace; metered per minute | **yes, by name** — `GetDaemonByName` → `refreshManagedDaemon` (`service.go:119-130`) |
| `CompleteOnboarding` (UserService) | — | `internal/user/contract.go:51` | authenticated | empty | none | yes (idempotent flag write) |
| `StripeWebhookHandler` (plain HTTP `POST /webhooks/stripe`) | mounted at `internal/handlers/billing_gateway/service.go:159` | `internal/billing/svcbilling/stripe_webhook.go:68` | valid `Stripe-Signature` against `STRIPE_WEBHOOK_SECRET` | 200/400/500 | **GRANTS ENTITLEMENT** | **yes** — `RecordStripeEvent` dedup by event ID (`stripe_webhook.go:88-98`) |

Admin-only siblings exist (`BillingAdminService.AssignPlan` →
`service.go:2517`) and are how operators and hermetic e2e tests grant a plan
without Stripe. Out of scope for the user-facing redesign, but worth knowing
it is the non-Stripe path into the same subscription rows.

### 1.2 Frontend hook → RPC mapping

All in `reliant/web/src/hooks/useCloudBillingQueries.ts`:

| Hook | RPC | Line |
|---|---|---|
| `useComputeSubscription` | `GetCurrentUserComputeSubscription` | 81 |
| `useWalletOverview` | `GetCurrentUserWalletOverview` | 89 |
| `useComputeUsage(period)` | `GetCurrentUserComputeUsage` | 119 |
| `usePlans` | `ListPlans` | 128 |
| `useCurrentUserInvoices` | `ListCurrentUserInvoices` | 136 |
| `useBillingEmail` | `GetCurrentUserBillingEmail` | 144 |
| `useSetComputeOverage` | `SetCurrentUserComputeOverage` | 154 |
| **`useCreateCheckoutSession`** | `CreateCurrentUserCheckoutSession` | 173 |
| **`useCreateWalletTopupSession`** | `CreateCurrentUserWalletTopupSession` | 194 |
| `useCreateBillingPortalSession` | `CreateCurrentUserBillingPortalSession` | 209 |
| `useUpdateBillingEmail` | `UpdateBillingEmail` | 216 |

`useCloudEligibility` lives elsewhere — `hooks/useOnboardingQueries.ts:101` —
and is the hook that gates every "start a machine" button.

---

## 2. The precondition graph

**This is the deliverable the redesign is built on.** The user's ask is that
provisioning calls be *queued* so they fire only when they can succeed. That
requires knowing, per call, what must already be true.

### 2.1 The global spine

```
sign in (Supabase; may be ANONYMOUS)
  └─ GetCurrentUser  ─────────────────────────────────────────────┐
       auto-provisions users row + personal org                   │
       (internal/user/user.go:140 buildPersonalOrg)               │
       ** NO SUBSCRIPTION IS BUNDLED ** (user.go:266-284)         │
       └─ grantSignupBenefits → signupgrant.Grant                 │
            bumps a per-IP counter ONLY. Grants nothing spendable.│
            (internal/signupgrant/signupgrant.go:61)              │
                                                                  │
  a brand-new user therefore has: an org, a wallet (created lazily),
  ZERO compute entitlement, ZERO wallet credit.                   │
                                                                  │
  ┌───────────────────────────────────────────────────────────────┘
  │
  ├─ path A: RedeemCoupon (compute kind) → compute_grants row
  ├─ path B: CreateCheckoutSession → Stripe → webhook → subscription row
  └─ path C: BillingAdminService.AssignPlan (operators / e2e only)
       │
       └─ GetCurrentUserComputeEligibility now reports eligible=true
            │
            └─ CreateDaemon passes checkDaemonSizeAllowed
                 │
                 └─ daemon PENDING → NATS → workspace-controller → RUNNING
```

The single most important fact in this document, and the one the prior docs
predate: **`buildPersonalOrg` no longer bundles a trial subscription**
(`internal/user/user.go:266-284`). The comment is explicit — the trial was a
handout "with no operator lever", retired alongside the $20 LLM welcome credit.
So every new user is ineligible for compute until they redeem or pay.
`04-billing-daemon-backend.md:91-97` still draws `signupgrant.Grant` as granting
a trial; that is stale. `internal/plansconfig/plans.yaml` still defines
`plan_compute_free` and its comments still describe the auto-grant, so the plan
row exists but nothing creates a subscription to it on signup.

### 2.2 `CreateCheckoutSession` — the full precondition chain, in execution order

From `CreateCurrentUserCheckoutSession` (`service.go:865`) into
`CreateComputeCheckoutSession` (`service.go:893`):

| # | Check | Line | Failure |
|---|---|---|---|
| 0 | **client-side:** caller is not `is_anonymous` | `useCloudBillingQueries.ts:61-66,177` | `CheckoutIdentityRequiredError` (never reaches the server) |
| 1 | plan exists | `865-872` | `NotFound("plan")` |
| 2 | `plan.ProductID == "prod_compute"` | `873-875` | `InvalidArgument("AI access is billed through wallet balance")` |
| 3 | `checkRedirectURL(successURL)` | `894-896` | `InvalidArgument("redirect URL not allowed")` |
| 4 | `checkRedirectURL(cancelURL)` | `897-899` | same |
| 5 | current user resolves | `901` | `Unauthenticated` |
| 6 | plan re-read, still `prod_compute` | `906-915` | `NotFound` / `InvalidArgument` |
| 7 | `stripeClient.Configured()` | `916-918` | `Unimplemented("stripe is not configured")` |
| 8 | `plan.StripePriceID != nil` | `919-921` | `InvalidArgument("compute plan has no associated Stripe price")` |
| 9 | **`resolveBillingEmail(ctx, user)`** | `923-926` | `ErrBillingEmailMissing` → `x-reliant-reason: billing_email_missing` |
| 10 | `getOrCreateComputeStripeCustomer` | `928-931` | **first Stripe write** |
| 11 | **cancel any existing Stripe subscription** | `934-943` | logged, not fatal |
| 12 | `stripeClient.CreateCheckoutSession` | `945-955` | **second Stripe write** |

Steps 3, 4, 7, 8, 9 are all "config or account state" checks that fail *before*
anything is created — good. Steps 10, 11, 12 are the irreversible tail.

Two orderings worth naming for the redesign:

- **`resolveBillingEmail` (9) runs before `CreateCustomer` (10)** — deliberate,
  because the customer is created *with* that email
  (`service.go:972`, `client.go:46-62`). Resolution order is
  `user.BillingEmail` → `user.Email` → JWT email
  (`service.go:2767-2789`), each validated by `validateBillingEmail`
  (`service.go:2727`), which rejects `*.users.noreply.github.com` — so a
  GitHub-only signup with a private email lands on step 9.
- **Step 11 cancels the old subscription BEFORE the new checkout is even
  presented** (`service.go:933-943`). A user who opens checkout to switch plans
  and then abandons it has had their existing subscription set to
  `cancel_at_period_end`. This is a latent defect; see §9.

`checkRedirectURL` (`service.go:439`) policy: non-empty allowlist → validate;
empty + dev env → allow; empty + non-dev → **fail closed**. Host matching is
exact or dot-suffix (`service.go:2799-2803`). All four URL-minting RPCs funnel
through this one helper — `04-billing-daemon-backend.md:63-73` verified there is
no duplication hazard here, and I confirm that still holds.

### 2.3 `CreateWalletTopupSession` — preconditions

`service.go:1095`, in order: Stripe configured (1098) → `checkRedirectURL` ×2
(1101-1106) → `amountCents >= 500` (1107-1114, `walletMinimumTopupAmountCents`
at `service.go:84`) → current user (1115) → **`requireOwner(orgID)`** (1119) →
org exists (1122) → **`resolveBillingEmail`** (1129) → wallet created if absent
(1133) → Stripe customer created if `org.StripeCustomerID == nil`, stored via
`SetStripeCustomerIDIfNull` with a race re-read (1143-1160) → **`wallet_topups`
row inserted, status `pending`** (1162-1176) → Stripe session (1177) → topup row
updated with the checkout URL (1196).

Same `resolveBillingEmail`-before-`CreateCustomer` ordering. Note the extra
precondition checkout does not have: **org ownership**. And the extra artifact:
a pending DB row per call.

### 2.4 `CreateDaemon` — preconditions

`internal/svcdaemon/service.go:87`. Validation (name/image/repo/branch/hostname
lengths, `daemon_type` required) at 93-113. Then:

1. **Idempotency by name** (`119-130`): an existing daemon with this owner+name
   is *refreshed*, not duplicated (`refreshManagedDaemon`, `service.go:335`).
   This is why the onboarding daemon is always called `"onboarding-daemon"`
   (`ComputeStep.tsx:247`).
2. Everything below runs inside a **per-owner advisory lock**
   (`WithOwnerDaemonLock`, `service.go:145`) — the comment at `132-143` explains
   why: daemons are pay-per-use, so an unserialised burst could all clear a
   budget check that should have stopped every one after the first.
3. `checkWorkspaceLimit` — plan `max_workspaces` (`gateAndInsertDaemon:220`)
4. `enforcement.Check(ActionCreateDaemon)` — global free-tier budget (`226-232`)
5. `checkIPComputeLimit` — per-IP free-tier limit (`235-239`)
6. size resolution: explicit, else `resolveDefaultDaemonSize` (`246-255`)
7. **`checkDaemonSizeAllowed`** (`258`, impl `service.go:1294`) — the funding
   gate:
   - non-`user` owners: pass (`1296-1298`)
   - no active compute sub → check `compute_grants` minutes; **>0 minutes
     substitutes for a subscription**, but only within the
     `plan_compute_free` size allowance, i.e. `small` (`1323-1345`; the comment
     is explicit that a coupon buys machine *time*, not a bigger machine)
   - still nothing → `HasExpiredFreeTrialForUser` picks
     `ReasonTrialExpired` vs `ReasonNoComputeSubscription` (`1347-1359`)
   - with a sub: size must be in `allowed_daemon_sizes` (`1363-1367`), then
     included-minutes quota against the period (`1370-1400`)
8. INSERT + `IncrementIPDaemonsCreated` (`315-323`)
9. **outside** the lock: NATS `CreateWorkspaceCommand` publish, with
   **rollback via `HardDeleteDaemon` on publish failure** (`182-193`)

`GetCurrentUserComputeEligibility` (`service.go:2294`) is explicitly documented
as a *prediction* of step 7 (`service.go:2287-2293`) — "an active compute
subscription OR unspent granted minutes" — that deliberately does NOT re-check
per-size allowance or minute exhaustion. So **eligibility can be true while
`CreateDaemon` still refuses**, e.g. a user with an active sub whose included
minutes are spent, or who asks for a size their plan excludes.

### 2.5 Billable / resource-creating vs. safe-to-call-early

This is the table to design the queue against.

**Never fire speculatively:**

| Call | Why |
|---|---|
| `CreateCurrentUserCheckoutSession` | creates a Stripe Customer, **cancels the existing subscription**, mints a session |
| `CreateCurrentUserWalletTopupSession` | creates a Stripe Customer AND a pending `wallet_topups` row |
| `CreateDaemon` | provisions a k8s workspace, metered per minute |
| `RedeemCoupon` | consumes a single-use coupon slot irreversibly |
| `SetCurrentUserComputeOverage(true)` | arms future overage charges |

**Safe to call early / freely:** every `Get*` and `List*` in §1.1, plus
`UpdateBillingEmail` (validated, idempotent) and
`CreateCurrentUserBillingPortalSession` (a free Stripe session against an
existing customer). Note the one wrinkle: `GetCurrentUserWalletOverview` is
*not* a pure read — it creates a wallet row. That is deliberate and harmless,
and the code carries a long comment (`service.go:1032-1037`) about a previous
version that *also* minted a $5 welcome credit on every billing-page open, which
stacked with the signup grant. Do not reintroduce a granting read.

**Idempotency, precisely:**

| Call | Twice-fired behaviour |
|---|---|
| `CreateDaemon` | safe — same name refreshes the existing row |
| `RedeemCoupon` | safe — claim-then-grant, both grants idempotent on `(wallet_id,"coupon",coupon_id)` / `redemption_id` (`coupon/service.go:113-133`) |
| Stripe webhook | safe — event-ID dedup (`stripe_webhook.go:88-98`) |
| `UpdateBillingEmail` | safe |
| **`CreateCheckoutSession`** | **unsafe.** Each call re-runs the cancel-existing-subscription step and mints a fresh session. Two completed sessions = two subscriptions in Stripe; `handleComputeCheckoutCompleted` upserts one row (`stripe_webhook.go:464`), so the DB shows one and Stripe bills two |
| **`CreateWalletTopupSession`** | **unsafe.** One pending `wallet_topups` row per call; abandoned ones are never reaped (no cleanup path found) |

---

## 3. Money and grants: how entitlement actually becomes real

### 3.1 The checkout → entitlement path

1. Client mints return URLs (`lib/stripeCheckout.ts:49`) and calls the RPC.
2. Server returns a hosted `checkout.stripe.com` URL.
3. Client opens it — desktop: an Electron `BrowserWindow`
   (`electron/src/stripe-checkout.js:78`); web: `window.location.href`
   (`stripeCheckout.ts:88`).
4. User pays. Stripe redirects to `successUrl`.
5. **Independently**, Stripe POSTs `checkout.session.completed` to
   `/webhooks/stripe`.
6. `handleCheckoutCompleted` (`stripe_webhook.go:131`) branches:
   - `metadata.billing_flow == "wallet_topup"` → `handleWalletTopupCheckoutCompleted`
     (`245`): marks the topup `succeeded` and applies a
     `topup_credit` wallet ledger entry.
   - org found by `stripe_customer_id` → update/create the org subscription
     (`188-217`), then `syncLLMKeys` (`221`) and a checkout-complete email (`223`).
   - **org NOT found and `metadata.plan_type == "compute"`** →
     `handleComputeCheckoutCompleted` (`464`): `UpsertComputeSubscription(userID, planID, stripeSubID)`.
     This is the normal path for compute plans, because compute customers are
     created per-*user* (`getOrCreateComputeStripeCustomer`, `service.go:964`)
     and never written to `organizations.stripe_customer_id` — so the org lookup
     at `154` legitimately misses. **The compute flow routes through what reads
     like a fallback branch.**
7. `GetCurrentUserComputeSubscription` now returns the sub; eligibility flips.

Other handled events: `customer.subscription.created/updated` (`288`),
`.deleted` (`339`), `invoice.paid` (`381`, recovers `past_due` → `active`),
`invoice.payment_failed` (`414`, sets `past_due` + email).

**`?checkout=success` is presentation only.** `stripeCheckout.ts:22-25` and
`routeSchemas.ts:239-247` both say so explicitly, and `CheckoutReturnBanner`
(`billing.tsx:200`) honours it: it polls `useComputeSubscription` every 2s for
up to 60s and only claims "Your <plan> plan is active" once the *server* reports
the plan (`billing.tsx:214-234`).

### 3.2 Coupons

`RedeemCoupon` → `coupon.Redeem` (`internal/coupon/service.go:134`). Order,
with the reasoning preserved from its own doc comment (`113-133`):
rate-limit → `TryClaimCouponSlot` (atomic; enforces the cap AND records the
redemption) → grant. Claim-before-grant fails toward *under*-granting, which is
the correct direction.

Two kinds, one endpoint (`rpc_redeem_coupon.go:15-24` explains why: a user
should not have to know which product their code belongs to):

- `CouponKindWalletCredit` → `grantWalletCredit` (`187`) → wallet ledger entry
  → funds **AI/LLM** spend.
- `CouponKindComputeMinutes` → `grantComputeMinutes` (`254`) → `compute_grants`
  row carrying the coupon's expiry (copied, not joined, so editing the coupon
  later cannot retroactively expire minutes already held) → funds **machines**.

The org is resolved server-side and never taken from the request
(`rpc_redeem_coupon.go:37-52`), and identity comes from `auth.GetUserID` not raw
JWT claims — the comment records that reading claims yielded the Supabase sub,
which matched no membership row and broke every redemption.

**Only a compute-minutes coupon changes compute eligibility.** The frontend
knows this: `ComputeStep.tsx:628` arms the auto-start only when
`result.kind === RedeemedCouponKind.COMPUTE_MINUTES`.

### 3.3 The wallet

`walletDefaultCurrency = "usd"`, `walletMinimumTopupAmountCents = 500`,
`walletWelcomeCreditAmountCents = 500` (`service.go:80-86`). The welcome credit
survives only as `GrantWelcomeCredit` (`service.go:1046`), which is
owner-gated and called from nowhere in the user flow — **INFERRED** dead-ish
(admin/e2e only); I did not find a caller.

Balances are carried in three parallel precisions — cents, USD micros, USD nanos
— and the frontend reads whichever is present via `nanosFromFields`
(`billingUtils.ts:27`). Top-up presets are `[$10, $25, $50, $100]`
(`billingUtils.ts:23`). Low-balance threshold: 250 cents (`billingUtils.ts:17`).

---

## 4. The plan and sizing data model

### 4.1 Catalog shape

`internal/plansconfig/plans.yaml` (all non-prod, TEST price IDs) and
`plans.prod.yaml` (LIVE price IDs), selected by `SelectByDeployEnv`;
`plansync.Sync` upserts every row at startup. Two products:

- **`prod_code`** — `plan_code_free` / `starter` / `pro` / `enterprise`.
  **`stripe_price_id` is null for every one, in every env**, by design: AI is
  wallet-billed, and checkout rejects any non-`prod_compute` plan
  (`service.go:873`). These plans carry LLM limits (`max_llm_spend_monthly`,
  `max_llm_keys`, `requests_per_min`, `max_seats`, `max_workspaces`).
- **`prod_compute`** — `plan_compute_free` (trial; price null in every env) plus
  `small` / `medium` / `large` / `xl`, which carry real Stripe price IDs.

The compute catalog, from `plans.prod.yaml`:

| Plan | Stripe price (prod) | `allowed_daemon_sizes` | `daemon_compute_included_minutes` | `max_workspaces` |
|---|---|---|---|---|
| `plan_compute_free` | null | `[small]` | 600 | 1 |
| `plan_compute_small` | `price_1Tk6ux…lHx7` | `[small]` | 1000 | 5 |
| `plan_compute_medium` | `price_1Tk6uy…JwLSi` | `[small, medium]` | 2500 | 10 |
| `plan_compute_large` | `price_1Tk6uy…Je38e` | `[small, medium, large]` | 5000 | 25 |
| `plan_compute_xl` | `price_1Tk6uy…tK5x4` | `[small, medium, large, xl]` | -1 (unlimited) | -1 |

`-1` means unlimited throughout.

### 4.2 What the *client* has to invent

`derivePlanDisplay` (`billingUtils.ts:132`) reads `allowedSizes` and
`includedMinutes` from the plan's `limits` JSON — but:

- **`monthlyPriceCents` is a hardcoded client-side table.**
  `COMPUTE_PLAN_PRICE_CENTS` (`billingUtils.ts:102-107`):
  small $20, medium $40, large $80, xl $160. **The price the user sees is not
  the price Stripe charges** — they are two independent declarations that
  nothing reconciles. A plan whose `monthlyPriceCents` is null is *not
  rendered at all* (`billing.tsx:1047`), so an unknown plan ID silently
  disappears from the picker.
- **`overageCentsPerMinute` is also a client-side fallback table**
  (`billingUtils.ts:109-114`: 0.2 / 0.4 / 0.8 / 1.6 ¢-per-min), used unless
  `limits.daemon_overage_per_minute_cents` is present. It is present in
  **neither** catalog file, so the fallback is always what renders.
- **`COMPUTE_PLAN_IDS`** (`billingUtils.ts:95`) is a hardcoded allowlist that
  both filters and orders the plan grid (`billing.tsx:960-966`). A new plan ID
  added to the YAML does not appear in the UI until this array is edited.

There is no "billing period other than monthly" anywhere: the UI hardcodes
`/mo` (`billing.tsx:1070`) and included minutes are described as monthly. The
subscription's real period comes from Stripe via the webhook
(`stripe_webhook.go:305-311`); `handleCheckoutCompleted` guesses `now + 1 month`
when creating a row (`stripe_webhook.go:168`).

### 4.3 What a user would need to pick a plan AND a size together

Today these are decided in two different places and neither knows about the
other:

- **Plan** is chosen on `/settings/billing?tab=plans` (`billing.tsx:947`), which
  shows price, included hours, overage rate, and — as prose only —
  "Runs small machines" (`billing.tsx:1089`).
- **Size** is chosen implicitly at `CreateDaemon` time. Onboarding hardcodes
  `DAEMON_SIZE_SMALL` (`ComputeStep.tsx:249`). If no size is given the server
  picks a plan-allowed default (`resolveDefaultDaemonSize`, `svcdaemon/service.go:250`).

To unify them a single surface would need, per plan: monthly price (currently
client-invented), the **set** of sizes it unlocks (server-provided, already in
`limits`), included minutes and what a size *costs* in minutes-per-minute
(**not modelled anywhere** — see §9), and the overage rate (currently
client-invented). Two of those four are not authoritative today.

---

## 5. The two billing surfaces

### 5.1 `Settings/cloud/billing.tsx` (1,401 lines) — compute/machine

Four tabs, now in the URL as `?tab=` (`billing.tsx:113`, schema at
`routeSchemas.ts:231-237`). The doc comment at `billing.tsx:89-96` records that
this was `useState` and that unaddressable tab state was what dropped user
intent across a round trip.

| Tab | Renders | Loads | Actions |
|---|---|---|---|
| **Overview** (`424`) | Credit balance + low-balance warning; coupon redemption card; compute plan card (price, included hours, allowed sizes, overage rate, overage toggle); usage-this-period bar + coupon-minutes + overage; billing email row; Stripe portal link | `useComputeSubscription`, `useWalletOverview`, `useComputeUsage("current")`, `useBillingEmail` | wallet top-up ×4 presets (`493`), overage toggle (`521`), Stripe portal (`532`), redeem coupon, edit billing email (`865`) |
| **Plans** (`947`) | 4-card compute grid: price, included hours, overage, sizes | `usePlans`, `useComputeSubscription` | **Subscribe / Switch** (`969`) |
| **Invoices** (`1132`) | Stripe invoice table with PDF links | `useCurrentUserInvoices` | download |
| **Usage** (`1212`) | current/previous toggle, 4 stat cards, coupon-minutes card, daily bar chart, per-machine table | `useComputeUsage(period)` | none |

Plus `CheckoutReturnBanner` (`200`) above the tabs, and `IdentityRequiredNotice`
(`381`) rendered inline when the checkout mutation throws
`CheckoutIdentityRequiredError`.

### 5.2 `Settings/cloud/reliantAI.tsx` (889 lines) — AI credits

Rendered as the "Reliant AI" tab inside `/settings/general`'s AI section
(`reliantAI.tsx:3`), **not** under `/settings/billing`. Gated on
`reliantAIAvailable` (`145`).

Renders: a no-credit explainer with a "set up billing" link (`269-292`); credit
balance + available models + **a second coupon redemption form** (`317`); AI
spend last-30-days with period spend / remaining / monthly cap (`369`); spend by
model (`418`); recent usage rows (`457`); LLM key management — create, reveal
once, rotate, revoke (`504`, `625`).

Loads: `useReliantOverview`, `useWalletOverview`, `useLLMKeys`, `useLLMSpend`,
`useAvailableModels` (`reliantAI.tsx:173-198`).

### 5.3 Overlap vs. genuinely distinct

**Duplicated:**

- **Credit balance.** Both render the same wallet from the same
  `useWalletOverview`. `billing.tsx:582` formats it via
  `formatCurrencyFromWalletFields`; `reliantAI.tsx:323` via its own local
  `usdFromNanos` (`reliantAI.tsx:78`). Two formatters, one number.
- **Coupon redemption.** Two `RedeemCouponForm` instances
  (`billing.tsx:634` open variant; `reliantAI.tsx:347` small variant). One
  server endpoint, and a code can grant *either* kind — so the same box on two
  pages does the same thing. `reliantAI.tsx:340-345` acknowledges this and
  declares billing canonical.
- **"Set up billing" links.** `reliantAI.tsx:281` and `:356` both navigate to
  `/settings/billing` — and note both use a bare `navigate`, **not**
  `useGoToBilling`, so they land on Overview with no `tab=plans`, no `from`, no
  `returnTo`.

**Genuinely distinct:** LLM keys, per-model spend, available models, and the
monthly spend cap exist only on the AI page. Invoices, plan selection, compute
usage, overage, the Stripe portal and the billing email exist only on billing.

**The structural split** is real and worth preserving in some form: **wallet
credit funds AI tokens; a compute subscription (or coupon minutes) funds
machines.** They are different currencies with different top-up mechanics
(one-off `payment` mode vs. recurring `subscription` mode —
`client.go:101-136`). What is not defensible is that the *balance* and the
*coupon box* appear twice, that the AI surface lives under `/settings/general`
while the money lives under `/settings/billing`, and that a single wallet
number is formatted by two different functions.

### 5.4 `Mobile/MobileBillingScreen.tsx` (241 lines)

Three cards: credit balance, compute plan with usage bar, and one **Upgrade**
button. Reuses the same hooks and `billingUtils` formatters (`:11-17`).

**It auto-picks `cheapestPlan`** (`:104-110`) — the first entry of
`COMPUTE_PLAN_IDS` present in the catalog, i.e. `plan_compute_small` — and
subscribes to it with one tap (`:112-143`). The comment (`:101-103`) defends
this as "a one-tap Upgrade needs a single target". The user sees no price
before the tap; the price appears first inside Stripe checkout. For an
identity-required failure it tells the user to "Open Settings on desktop"
(`:133`), because the `/upgrade` flow has no mobile route.

---

## 6. Every entry point into billing

The user believed there were 2 in onboarding. Verified: **2 in onboarding, 6
in total**, plus 2 secondary links.

| # | Where | file:line | Trigger | User state | What they need |
|---|---|---|---|---|---|
| 1 | `ComputeStep` "Set up billing" | `ComputeStep.tsx:639` (hook at `:132`, `from="onboarding"`) | click, when `!canStartCloud` | mid-onboarding, compute-ineligible | compute entitlement |
| 2 | `ModelStep` "Set up billing" | `ModelStep.tsx:422` (hook at `:122`, `from="onboarding"`) | click, when `!creditsAvailable` | mid-onboarding, empty wallet | **AI credit** — but lands on `tab=plans`, a compute plan grid |
| 3 | `UpgradeRequiredModal` "Upgrade" | `UpgradeRequiredModal.tsx:44` (hook `:35`, no `from`) | opened by `upgradeInterceptor.ts:68` on `ResourceExhausted` + `x-reliant-reason` | anywhere; a quota just refused them | more quota |
| 4 | `ResumeDaemonPill` "Upgrade" | `ResumeDaemonPill.tsx:96` (hook `:47`) | suspended daemon, resume refused | in a chat | compute entitlement to resume |
| 5 | `ConnectDaemonModal` "Set up billing" | `ConnectDaemonModal.tsx:235` (hook `:65`) | cloud option ineligible | connecting a project | compute entitlement |
| 6 | `BillingEmailRequiredModal` "Manage billing" | `BillingEmailRequiredModal.tsx:70` | opened by `upgradeInterceptor.ts:56` on `InvalidArgument` + `billing_email_missing` | mid-purchase | a billing email |
| 7 | `reliantAI.tsx` "set up billing" (no-credit banner) | `reliantAI.tsx:281` — **bare `navigate`** | zero AI credit | on the AI page | AI credit |
| 8 | `reliantAI.tsx` "Billing" (coupon footnote) | `reliantAI.tsx:356` — **bare `navigate`** | informational | on the AI page | the fuller view |
| 9 | `CombinedGeneralSettings.tsx:779` | bare `navigate` | — | in general settings | — |
| 10 | `IdentityRequiredNotice` → `/upgrade` | `billing.tsx:409` | anonymous user clicked Subscribe | **already on billing** | an identity |

`useGoToBilling(from?)` (`hooks/useGoToBilling.ts:28`) is now a pure navigation:
it always goes to `/settings/billing` with `search: { tab: "plans", from, returnTo }`.
`returnTo` is `pathname + search` captured at click time, and *only* when
`from === "onboarding"` (`:39-42`) — the comment explains that onboarding's
entire state lives in the `plan` search param, so a bare `/onboarding` return
restarts the wizard.

Three inconsistencies fall out of this table:

- Entries 7, 8 and 9 bypass `useGoToBilling` entirely — no `tab=plans`, no
  `returnTo`.
- Entry 2 (`ModelStep`) sends a user who needs **AI credit** to a **compute
  plan grid**. `tab=plans` is hardcoded in the hook with no way to ask for the
  wallet instead.
- Entries 3, 4, 5 pass no `from`, so `returnTo` is undefined and there is no
  route back.

---

## 7. Stripe integration facts (for the embedded-checkout decision)

**SDK: `github.com/stripe/stripe-go/v82 v82.5.1`** (`control-plane/go.mod`).
Confirmed. Global `stripe.Key` set once in `billing.New` with
`SetMaxNetworkRetries(3)` (`client.go:31-38`). `Configured()` is
`apiKey != ""` (`client.go:41`).

**Session creation** — `client.CreateCheckoutSession` (`client.go:84`) builds a
`stripe.CheckoutSessionParams` with `Customer`, `Mode`, `SuccessURL`, `CancelURL`
(`89-94`), then branches:

- `CheckoutModeSubscription` (default): one line item at `params.PriceID`,
  metadata copied onto both the session and `SubscriptionData` (`102-112`).
- `CheckoutModePayment` (wallet top-up): ad-hoc `PriceData` with
  `UnitAmount`/`Currency`/product name `"Reliant wallet top-up"`, metadata on
  the session and `PaymentIntentData` (`113-136`).

Returns `sess.URL` (`146`).

**Idempotency keys:** `CreateCustomer` sets
`customer-<orgID>-<email>` (`client.go:55`) and `ReportMeterEvent` sets one
(`client.go:356`). **`CreateCheckoutSession` sets none** — which is precisely
why double-firing it is unsafe (§2.5).

**Webhook** — `POST /webhooks/stripe`, mounted only when
`STRIPE_WEBHOOK_SECRET` is set (`handlers/billing_gateway/service.go:158-159`;
the nil-interface trap is documented at `:66-68` and mirrored in
`internal/app/compose.go:220-226`). Signature verified via
`webhook.ConstructEventWithOptions` with `IgnoreAPIVersionMismatch: true`
(`internal/billing/webhook.go:34`). Body capped at 64KB
(`stripe_webhook.go:74`). Dedup by event ID before handling; **on handler
failure the event record is deleted and a 500 returned so Stripe retries**
(`stripe_webhook.go:100-107`).

**Payment methods.** No `PaymentMethodTypes`, no
`automatic_payment_methods`, no `PaymentMethodConfiguration` anywhere in the Go
code — I grepped for all of them and found nothing. **Therefore payment-method
availability is entirely Stripe-Dashboard-controlled**, which is Stripe's
default behaviour for hosted Checkout (card plus whatever wallets/APMs the
account has enabled and the session qualifies for). I could not verify what the
Dashboard has enabled from here; **that is an open question for the redesign,
not a code fact.**

**What would change under `UIMode: "embedded"`:**

- `client.CreateCheckoutSession` sets `UIMode: stripe.String("embedded")` and
  returns `sess.ClientSecret` instead of `sess.URL`.
- `CancelURL` is **not permitted** in embedded mode; `SuccessURL` becomes
  `ReturnURL` (with `{CHECKOUT_SESSION_ID}`), or is omitted entirely for the
  fully-embedded `redirect_on_completion: "never"` variant.
- Proto change: `CreateCurrentUserCheckoutSessionResponse.checkout_url` →
  `client_secret` (`billing.proto`, and the same for the top-up response).
- The whole `checkRedirectURL` / `ALLOWED_REDIRECT_HOSTS` machinery becomes moot
  for checkout — which deletes the failure mode that
  `04-billing-daemon-backend.md` identifies as a real prod outage. It stays
  live for the billing portal, which has no embedded mode.
- Frontend adds `@stripe/stripe-js` + `@stripe/react-stripe-js` and needs a
  publishable key delivered to the client (there is no `VITE_STRIPE_*` today —
  **INFERRED** from its absence in the web env; a new config surface).
- `electron/src/stripe-checkout.js` and the `openCheckout` bridge in
  `lib/stripeCheckout.ts:82-86` become unnecessary for checkout — the round trip
  no longer leaves the app, which is exactly the Electron problem they were
  written to solve.
- `?checkout=success|cancelled` and `CheckoutReturnBanner` survive in reduced
  form: the webhook is still the source of entitlement truth, so the
  "confirming your payment…" poll (`billing.tsx:226-234`) is still needed.

Embedded mode does **not** change: the webhook contract, the metadata keys
(`plan_type`, `user_id`, `plan_id`, `billing_flow`, `topup_id`), the
`handleComputeCheckoutCompleted` routing, or any precondition in §2.2 except
steps 3 and 4.

---

## 8. Reconciling `06-billing-ux.md` — what has since been fixed

Read `06`'s §5 bug list only alongside this section.

| `06` bug | Status |
|---|---|
| 1. `successUrl === cancelUrl` at all three sites | **FIXED.** `buildCheckoutReturnUrls` (`stripeCheckout.ts:49`) appends `?checkout=success|cancelled`; used at `billing.tsx:495`, `:534`, `:980`, `MobileBillingScreen.tsx:118` |
| 2. `OAuthCallback.tsx:92` full-page `assign` | **NOT VERIFIED** — out of the path I traced. Assume still open. |
| 3. Electron sends checkout to the system browser | **FIXED.** `openCheckout` (`stripeCheckout.ts:75`) uses the main-process `BrowserWindow` (`electron/src/stripe-checkout.js:78`) with `classifyCheckoutReturn` (`:47`) watching `will-navigate`/`will-redirect`, and reports `success`/`cancelled`/`dismissed` |
| 4. `redirectToStripe` duplicated | **FIXED.** Both surfaces call the shared `lib/stripeCheckout` |
| 5. Onboarding has no return path | **FIXED.** `useGoToBilling("onboarding")` captures `returnTo` (`useGoToBilling.ts:39-42`), it rides through Stripe (`billing.tsx:980-988`), and `CheckoutReturnBanner` offers "Back to setup" with a same-origin guard (`billing.tsx:247-270`) |
| 6. `BillingEmailRequiredModal` can bounce to `/upgrade` twice | **PARTLY.** The first bounce is gone (`useGoToBilling` no longer forks on anonymity), but `BillingEmailRequiredModal.tsx:75-82` still navigates to `/upgrade`, and so does `IdentityRequiredNotice` (`billing.tsx:409`) — so a user can still meet `/upgrade` from two directions |
| 7. `checkRedirectURL` diagnosability gap | **OPEN.** Unchanged (`service.go:439`) |

Also fixed since `06`: the Overview-tab landing (now `?tab=plans` via
`useGoToBilling.ts:47`, schema `routeSchemas.ts:231`), the missing success state
(`CheckoutReturnBanner`, `billing.tsx:200`), and the anonymity chokepoint moving
from navigation to the mutation (`useCloudBillingQueries.ts:41-66`) — which is
exactly the mitigation `06`'s own devil's-advocate section demanded as a
precondition for that move.

`providers.go:412-429` now constructs the Stripe client **unconditionally**; the
comment records that the previous `if stripeKey != ""` left a nil
`billing.Service` *interface*, and a nil interface has no receiver, so
`s.stripeClient.Configured()` panicked.

---

## 9. Surprises, latent bugs, and undecided questions

### Latent bugs

**B1 — checkout cancels your existing subscription before you have paid.**
`service.go:933-943` calls `CancelSubscription` (which sets
`cancel_at_period_end`) on the *existing* compute subscription every time a
checkout session is created. A user who clicks "Switch" and then closes the
Stripe window has silently scheduled their current plan for cancellation, with
no UI anywhere reflecting it. The failure is also swallowed — logged, then
"proceeding with new checkout" (`940`). This is the single most consequential
thing I found, and any redesign that makes plan-switching easier makes it worse.

**B2 — `CreateCheckoutSession` has no Stripe idempotency key.**
`client.go:84-146` sets none, unlike `CreateCustomer` (`:55`). Two completed
sessions produce two Stripe subscriptions but one DB row
(`UpsertComputeSubscription`, `stripe_webhook.go:472`) — a double-charge the
control plane cannot see.

**B3 — abandoned `wallet_topups` rows are never reaped.** Every
`CreateWalletTopupSession` call inserts a `pending` row (`service.go:1162-1176`)
that only the webhook ever settles (`stripe_webhook.go:263`). I found no cleanup
job. Cosmetic today; it makes the top-up history list misleading.

**B4 — the displayed price is not the charged price.**
`COMPUTE_PLAN_PRICE_CENTS` (`billingUtils.ts:102`) is a hardcoded client table;
Stripe charges whatever the `stripe_price_id` says. Nothing reconciles them, and
`plans.prod.yaml` carries no price field at all. A price change in Stripe leaves
the UI lying. Same for `COMPUTE_PLAN_OVERAGE_FALLBACK_CENTS_PER_MIN`
(`billingUtils.ts:109`), which is *always* what renders because
`daemon_overage_per_minute_cents` appears in neither catalog file.

**B5 — a plan not in `COMPUTE_PLAN_IDS` is invisible.**
`billing.tsx:960-966` filters by that hardcoded array, and `:1047` drops any
plan whose derived price is null. Adding a tier to the YAML ships a plan nobody
can buy until the frontend is edited.

**B6 — eligibility over-promises.** `GetCurrentUserComputeEligibility`
deliberately answers "is there any funding at all" and skips per-size and
minute-exhaustion checks (`service.go:2287-2293`). So `canStartCloud` can be
true and `CreateDaemon` still refuse. The onboarding code handles the refusal
(`ComputeStep.tsx:291-319` catches, does not advance, refetches eligibility),
but the button was still offered.

**B7 — `ComputeStep.tsx:389-411` fires a billable `CreateDaemon` from an
effect.** The user flagged this and it is real. Redeeming a compute coupon arms
`pendingCloudStart.current` (`:628-633`); the effect fires `handleCloud()` — and
therefore `CreateDaemon` — on the render where eligibility flips, with no second
click. The code's own comments defend it as "a second confirmation of a decision
already made", which is a defensible product call; what makes it a hazard is
that provisioning is triggered by a *server state change* rather than by user
intent, and `05-onboarding-state-machine.md:112` independently flagged the same
line. It is mitigated by `CreateDaemon`'s name-idempotency (§2.4), so the worst
case is one machine, not many.

**B8 — three billing links bypass `useGoToBilling`** (`reliantAI.tsx:281`,
`:356`, `CombinedGeneralSettings.tsx:779`). Exactly the "a sixth call site added
without the guard" failure mode that motivated moving the anonymity check into
the mutation — the mutation guard still holds, but intent and `returnTo` are
silently dropped.

**B9 — `ModelStep` sends an AI-credit need to a compute-plan grid.**
`ModelStep.tsx:422` calls `goToBilling`, which hardcodes `tab: "plans"`
(`useGoToBilling.ts:47`). The user wants wallet credit; they get a machine-plan
comparison. There is no `tab` for "add credit" — top-ups live on Overview.

### Surprises

- **The compute checkout completes through what reads like an error fallback.**
  `handleCheckoutCompleted` looks up the org by `stripe_customer_id`, and only
  when that lookup *fails* does it check `plan_type == "compute"`
  (`stripe_webhook.go:154-163`). Since compute customers are per-user and never
  written to `organizations.stripe_customer_id`, the miss is the normal case.
  It works, but the control flow reads backwards and a future "always create the
  org customer" change would silently reroute every compute purchase.
- **New users get nothing.** `buildPersonalOrg` bundles no subscription
  (`internal/user/user.go:266-284`), and `signupgrant` grants nothing spendable
  (`signupgrant.go:1-25`). `plan_compute_free` still exists in both catalogs
  with comments describing an auto-grant that no longer happens.
- **`GetCurrentUserWalletOverview` writes** (creates a wallet row,
  `service.go:984`) — and used to write *money*, which stacked to $25 for
  healthy users (`service.go:1032-1037`).
- **`getPrimaryOrgForUser`** (`service.go:708`) disambiguates via managed-LLM
  access and the Reliant entitlement. `GetCurrentUserComputeEligibility`
  deliberately does *not* use it (`service.go:2325-2328`) because those are
  LLM-shaped inputs. Two different notions of "the user's org" coexist, and
  orgs are 1:1 with users anyway (`service.go:2317-2322`).
- **`ComputeStep` has ~120 lines of comments explaining effect-ordering races**
  between a redemption-armed cloud start and a local-daemon auto-skip
  (`:375-450`). The reasoning is careful and correct; the fact that it is needed
  at all is the signal that provisioning-from-effects is the wrong shape.

### Undecided — the redesign must choose

1. **Where does the price live?** Client table, plan `limits` JSON, a new proto
   field, or fetched live from Stripe. Today it is the client table and it can
   silently disagree with what Stripe charges (B4).
2. **What does a *size* cost?** `allowed_daemon_sizes` says which sizes a plan
   permits; nothing says whether an `xl` minute draws more than a `small` minute
   from `daemon_compute_included_minutes`. If plan-and-size are to be picked on
   one screen, this has to be answered.
3. **Which payment methods?** Not in code at all (§7). Dashboard-controlled
   today; embedded checkout makes this an explicit decision.
4. **Hosted vs. embedded checkout.** `06-billing-ux.md:252-264` recommends
   embedded as the right long-term answer and defers it as a wire-contract
   change. Everything since has landed on the hosted path (return URLs, the
   Electron window, the return banner) — none of it wasted, but the cost of the
   switch has not gone down.
5. **Should the two surfaces merge?** Wallet-funds-AI / subscription-funds-
   machines is a real split (§5.3), but the balance and the coupon box are
   duplicated and the AI page lives under a different settings section.
6. **Does `?tab=` need a wallet/credit target?** Required to fix B9.
7. **What replaces the effect-driven `CreateDaemon`?** An explicit queue is what
   the user asked for. The precondition it must wait on is
   `GetCurrentUserComputeEligibility().eligible` — but see B6: that is a
   *prediction*, not a guarantee, so the queue still needs a refusal path.
8. **Is B1 (cancel-before-pay) intended?** It reads as double-billing avoidance
   on a plan switch, but it fires on session *creation*, not on payment.

---

## 10. Verified vs. inferred

**Verified by reading the code:** every file:line citation; the RPC list and
handler/service mapping; every precondition in §2; the idempotency claims in
§2.5 (from `GetDaemonByName`/`refreshManagedDaemon`, `TryClaimCouponSlot`'s doc
comment, and `RecordStripeEvent`); the plan catalog in §4.1 (read from
`plans.prod.yaml`); the client-side price/overage tables in §4.2; the surface
inventories in §5; the entry-point table in §6; the stripe-go version, session
params, metadata keys and webhook routing in §7; and every fix status in §8.

**Inferred, and flagged as such:** that `GrantWelcomeCredit` is admin/e2e-only
(I found no product caller, but did not exhaustively search the admin service);
that no `VITE_STRIPE_*` publishable key exists today; the exact embedded-mode
Stripe parameter names in §7, which come from Stripe's API rather than from this
repo.

**Not verified:** `OAuthCallback.tsx:92` (`06` bug 2) — outside the path I
traced; whether abandoned `wallet_topups` rows have a reaper somewhere I did not
look; what payment methods the Stripe Dashboard has enabled; and the runtime
behaviour of any of this — nothing in this document was executed.
