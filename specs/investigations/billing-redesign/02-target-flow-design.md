# 02 — Target billing + onboarding flow design

**Status:** design only. No product code was written or edited producing this
document. Every file:line reference was read in the tree at the time of writing.

**Inherits from `01-current-state-inventory.md`** — that document's findings are
treated as verified and are **not** re-derived here. Where it and the older
`../onboarding-regression-harness/06-billing-ux.md` disagree, `01` wins; §1 below
records the four inherited facts that changed this design.

---

## 0. The owner's brief, and the one-paragraph answer

The brief: stop redirecting to a third party — on web *and* on mobile; redo the
two internal billing pages into something better and simpler with every payment
method and both plan and compute-size selection; consolidate the two places that
eject a user out of onboarding into **one** billing moment that only fires when
payment is actually needed; and queue the provisioning calls so they happen at
the right time and fire when done.

> Onboarding keeps its five steps and its URL-derived `deriveStep` core. The
> compute and model steps stop *doing* anything — they record **intent** into the
> same `plan` search param they already write. A `checkout` step is derived, not
> from a new flag but from a pure function `requiresPayment(plan, facts)`, so it
> exists only when money is genuinely owed and is skipped silently otherwise.
> When it does appear, Stripe's **embedded** checkout renders inside the
> onboarding card with `redirect_on_completion: "never"` — no navigation, on web,
> in Electron, and on mobile. Payment confirmed **by the server** triggers one
> idempotent `CommitLaunchPlan` that grants AI access and provisions compute in
> that order, with progress shown by a generalised `DaemonConnectingGate`. The
> two settings pages split by verb rather than by product: `/settings/billing`
> becomes the single place anything is bought, mounting the *same* checkout
> component the onboarding step does.
>
> Underneath all of it sits one rule, and it is the rule that fixes both the bug
> the owner named and the worse one he did not: **nothing that creates, cancels,
> or charges may fire from an effect or from session creation. Those calls
> happen only at a commit point — a user action, or a webhook.**

---

## 1. Four inherited facts that changed this design

From `01`, verified, and each one moved something:

**1. A new user gets an org and nothing else.** `buildPersonalOrg` no longer
bundles a trial subscription (`internal/user/user.go:266-284`) and
`signupgrant.Grant` only bumps a per-IP abuse counter
(`internal/signupgrant/signupgrant.go:61`). Compute entitlement arrives from
exactly two places: a compute-minutes coupon writing a `compute_grants` row, or
a completed Stripe checkout granted **by the webhook**. `plan_compute_free`
still exists in both catalog files with comments describing an auto-grant that
no longer happens.

> **Consequence, and it is the largest one in this document: cloud compute now
> essentially always needs payment or a coupon.** My first draft's branch table
> listed "cloud, already on the free trial" as a common no-payment case. It is
> not a case at all for a new user. §5.3 is rewritten around this, and §5.4's
> defence of the checkout step is rewritten because the step is now *common* for
> cloud users rather than exceptional. The free path through onboarding is
> **local compute + bring-your-own key**, and nothing else.

**2. `CreateCheckoutSession` sets no Stripe idempotency key** (`client.go:84-146`,
unlike `CreateCustomer` at `:55`). Two completed sessions produce two Stripe
subscriptions but one DB row (`UpsertComputeSubscription`,
`stripe_webhook.go:472`) — a double charge the control plane cannot see. **And
embedded checkout makes this worse, not better**: a panel that can be closed and
reopened with no page navigation makes minting a second session *easier* than
the redirect did. §6.2 designs the strategy.

**3. `CreateWalletTopupSession` inserts a fresh pending `wallet_topups` row on
every call** (`service.go:1162-1176`), settled only by the webhook, with no
reaper. Same shape, lower stakes. Same fix.

**4. `CreateComputeCheckoutSession` cancels the user's existing subscription at
session-*creation* time** (`service.go:933-943`), not on payment. A user who
clicks "Switch plan" and abandons checkout has silently scheduled their current
plan for cancellation; nothing in the UI reflects it and the cancel failure is
swallowed to a log line (`:940`).

> This is the same defect class as the `CreateDaemon`-from-an-effect the owner
> asked me to fix — a side effect fired at the moment of *intent* rather than at
> the moment of *commitment* — just on the cancel side. **The owner's redesign
> makes plan-switching easier, which makes this bug more reachable.** §6.3 makes
> the prohibition explicit and general rather than fixing this one instance.

A separate agent is fixing the immediate instance of (4) in parallel. This
document's job is to make the class unreachable.

### 1.1 What has already landed since `06-billing-ux.md`

`01` §8 reconciles this in full; the short version, so no implementer
re-proposes finished work: the anonymity check has moved into the checkout
mutation (`useCloudBillingQueries.ts:61`), `useGoToBilling` no longer forks on
anonymity, the billing tab is in the router, `?checkout=success|cancelled` is
deduplicated into `lib/stripeCheckout.ts`, Electron uses a controlled
`BrowserWindow` (`electron/src/stripe-checkout.js`), `leaveOnboarding(reason)`
is the single exit funnel, and `isCloudCompute()` replaced the duplicated
literals.

The hosted-redirect era has been made *survivable*. What remains is removing the
round trip that all of that machinery exists to survive — which `06` itself
named as the right long-term answer and deferred.

---

## 2. Verified constraints that shape the design

### 2.1 Stripe embedded checkout exists in the pinned SDK

`stripe-go/v82 v82.5.1` (`control-plane/go.mod:31`). From `checkout_session.go`
in the module cache:

- `CheckoutSessionUIModeEmbedded = "embedded"` (`:1149`), with `hosted`
  (`:1150`) and `custom` (`:1148`).
- `CheckoutSessionParams.UIMode *string` (`:2703`); `ReturnURL` (`:2678`);
  `RedirectOnCompletion` (`:2674`).
- `CheckoutSession.ClientSecret string` on the response (`:5386`).
- **`SuccessURL` is not allowed when `ui_mode` is `embedded`** — the SDK comment
  at `:2694-2697` states it outright. `CancelURL` likewise has no meaning.

`RedirectOnCompletion` takes `always` / `if_required` / `never` (`:1033-1035`).
**`never` is the linchpin of this design**: Stripe completes the payment inside
the iframe and fires our `onComplete` callback *without navigating the browser
anywhere at all*. `return_url` becomes optional, used only by payment methods
that genuinely must redirect — not cards, not wallets.

### 2.2 Read the installed types, not the README

`@stripe/react-stripe-js`'s master README now leads with `ui_mode: 'elements'`
and `CheckoutElementsProvider` / `useCheckoutElements` — a **different, newer**
product from embedded checkout, giving Elements you lay out yourself. Latest
published: `@stripe/react-stripe-js` **6.9.0**, `@stripe/stripe-js` **9.15.0**.

`EmbeddedCheckoutProvider` / `EmbeddedCheckout` are the `ui_mode: 'embedded'`
components this design specifies. **Implementer's first action: pin a version
and read that version's own `node_modules/@stripe/react-stripe-js/dist/*.d.ts`**
rather than trusting the README or this paragraph. The two APIs are easy to
conflate and the README's prominence of `elements` is a live trap.

*Decision:* **`ui_mode: 'embedded'`, not `'elements'`.** Embedded gives us
Stripe's maintained form — wallets, PayPal, Link, 3DS, tax — for one component
and one server param. `elements` hands us layout control we have no design brief
for and makes us responsible for the confirm lifecycle. It is the later upgrade
if we want the form visually inlined into our own plan picker; it is not the
starting point.

### 2.3 Packaged Electron runs at `app://bundle`, and Stripe.js will not load there

The most important constraint in this document; it invalidates the naive
"embedded everywhere" plan.

`electron/src/app-protocol.js` registers a custom `app://` scheme and
`main.js:1504` loads the renderer from `app://bundle/` (the file's header
explains why: `file://` broke root-absolute asset paths). Stripe.js loads from
`https://js.stripe.com` and talks to its iframes by origin-checked
`postMessage`; a non-`https` custom scheme is not an origin Stripe supports. And
**wallets additionally require payment-method domain registration**
(`docs.stripe.com/payments/payment-methods/pmd-registration`), which registers
*hostnames*. `app://bundle` can never be registered.

**Embedded checkout therefore cannot render in the packaged Electron renderer as
it is loaded today.** Any design claiming otherwise is wrong.

| Option | What it means | Verdict |
|---|---|---|
| **A. Keep the existing controlled `BrowserWindow`; point it at our own hosted checkout page** | `electron/src/stripe-checkout.js` already opens a window and watches navigation. Instead of `checkout.stripe.com`, open `https://app.reliantlabs.io/checkout/embed?...` — *our* page on *our* registered domain, rendering the embedded component, closing itself on completion via the existing `classifyCheckoutReturn`. | **Recommended.** Reuses machinery that exists and is tested. The user sees a small branded in-app window, never a system browser, never a `stripe.com` URL bar. Wallets work because the origin is our registered domain. |
| B. Serve the renderer over `https://localhost` in packaged builds | Would make embedded work in-window. | Rejected. Re-litigates `app-protocol.js`'s hard-won design, needs a local TLS cert, and risks the blank-window bug class its header documents. Disproportionate. |
| C. Accept the hosted redirect on Electron only | Status quo for desktop. | Rejected: exactly what the owner asked to stop doing, and A costs little more. |

Option A honours "no third-party redirect" in the sense that matters on desktop:
the user never leaves the app and never sees a browser. The payment form is
still a Stripe iframe, as it must be for PCI scope, but hosted in our page on
our domain.

### 2.4 "Mobile" is mobile web, and it is the *easiest* surface

`MobileBillingScreen.tsx` renders inside the same React app at `/m/settings`;
`MobileShell` is a responsive surface, not a native app. So mobile is a browser
on a registered domain and embedded checkout works there with **no special work
at all** — same component, narrower container.

The current mobile screen's real defect is unrelated to redirects: at
`MobileBillingScreen.tsx:104-113` it picks `cheapestPlan` and the Upgrade button
buys it, with the user seeing **no price before the tap** (`01` §5.4). That is a
purchase decision made for the user. §4.3 removes it.

### 2.5 Wallet availability is narrower than the brief assumes

`01` §7 confirms there is no `PaymentMethodTypes`, no `automatic_payment_methods`
and no `PaymentMethodConfiguration` anywhere in the Go code — availability is
entirely Stripe-Dashboard-controlled today, and what the Dashboard has enabled
is **an open question, not a code fact**.

- **Apple Pay / Google Pay / Link** in embedded checkout require
  payment-method domain registration for every exact domain and subdomain, in
  live **and** each sandbox. Dashboard or API. A named Phase-1 setup task, not
  a code change.
- **Apple Pay** historically renders only in Safari; recent Apple changes allow
  a scan-to-pay handoff in some third-party browsers. Environment-dependent.
  **Never render a static "Apple Pay accepted" badge** — let Stripe decide.
- **PayPal** is Dashboard-enabled with country/currency constraints and is
  **not supported for `mode: subscription`** the way cards are. Compute plans
  are subscriptions. Realistically PayPal is available for **wallet top-ups**
  (`mode: payment`) and not for the compute plan. Do not design UI implying
  otherwise.

*Decision:* **enable automatic payment methods and let Stripe render what
applies.** Our copy says "Card, Apple Pay, Google Pay, Link and more, depending
on your device" — hedged — rather than a fixed row of logos we cannot guarantee.

### 2.6 Plan and machine size are ONE axis; price is not authoritative

From `01` §4:

| Plan | `allowed_daemon_sizes` | included min | `max_workspaces` |
|---|---|---|---|
| `plan_compute_free` | `[small]` | 600 | 1 |
| `plan_compute_small` | `[small]` | 1000 | 5 |
| `plan_compute_medium` | `[small, medium]` | 2500 | 10 |
| `plan_compute_large` | `[small, medium, large]` | 5000 | 25 |
| `plan_compute_xl` | all | -1 | -1 |

Plan *is* the size picker. A second "now pick a size" control could only create
a state where the two disagree.

But three client-side inventions break any unified purchase surface (`01` B4,
B5):

- **`COMPUTE_PLAN_PRICE_CENTS`** (`billingUtils.ts:102-107`) is a hardcoded
  client table — $20/$40/$80/$160. **The price the user sees is not the price
  Stripe charges.** Two independent declarations, nothing reconciling them.
- **`COMPUTE_PLAN_OVERAGE_FALLBACK_CENTS_PER_MIN`** (`:109-114`) is *always*
  what renders, because `daemon_overage_per_minute_cents` appears in neither
  catalog file.
- **`COMPUTE_PLAN_IDS`** (`:95`) is a hardcoded allowlist that filters and
  orders the grid; a plan added to the YAML is invisible until the frontend is
  edited, and `billing.tsx:1047` drops any plan whose derived price is null.

*Decision, and it is a Phase-2 prerequisite rather than a nice-to-have:* **price
and overage move into the plan catalog and are served by `ListPlans`.** Add
`price_cents` and `overage_cents_per_minute` to `plans.yaml` /
`plans.prod.yaml`, surface them through the existing plan `limits` JSON, and
**delete all three client tables**. Ordering comes from a `display_order` field,
not an allowlist. A purchase surface that shows a price it invented is not
something to build on, and building the redesign on top of it would make the
divergence load-bearing.

Related, `01` undecided #2 — *does an `xl` minute draw more than a `small`
minute from included minutes?* **Decision: no.** That is what the code does
today (`checkDaemonSizeAllowed`, `svcdaemon/service.go:1294`, gates on size
membership and then on flat minutes), so the UI must not imply weighting.
Size-weighted metering is a separate metering change and is out of scope here;
flagging it because a plan grid showing "41 hours" invites the question.

### 2.7 Eligibility is a prediction, not a guarantee

`GetCurrentUserComputeEligibility` (`service.go:2294`) deliberately answers "is
there any funding at all" and skips per-size allowance and minute exhaustion
(`:2287-2293`, `01` B6). **So eligibility can be true and `CreateDaemon` still
refuse.** The provisioning queue in §6 must therefore have a refusal path, not
just a wait-for-eligible path. This is the difference between a queue that works
and one that hangs on an edge case.

---

## 3. Embedded checkout: API and component design

### 3.1 Proto

Additive first; deletion in Phase 5. In `billing.proto`:

```proto
message CreateCurrentUserCheckoutSessionRequest {
  string plan_id = 1;
  string success_url = 2 [deprecated = true];  // hosted mode only
  string cancel_url  = 3 [deprecated = true];  // hosted mode only

  // When set, the response carries client_secret instead of checkout_url and
  // no redirect URLs are used or validated. Optional so an un-updated client
  // keeps working during migration; Phase 5 makes embedded the only mode and
  // reserves tags 2 and 3.
  optional CheckoutUiMode ui_mode = 4;

  // For payment methods that genuinely require a redirect (some bank-redirect
  // methods). Cards and wallets never use it. Still validated by
  // checkRedirectURL when present.
  optional string return_url = 5;

  // Stable for one purchase attempt. Two calls with the same key return the
  // SAME Stripe session rather than minting a second. See §6.2 — this is what
  // closes the double-charge hole (01 B2) that embedded mode would otherwise
  // widen.
  string idempotency_key = 6;
}

message CreateCurrentUserCheckoutSessionResponse {
  string checkout_url  = 1;  // hosted mode; empty in embedded
  string client_secret = 2;  // embedded mode; empty in hosted
  CheckoutUiMode ui_mode = 3;  // echoed so the client never infers its shape
  string session_id = 4;       // so the client can ask the SERVER what happened
  bool reused = 5;             // true when an existing open session was returned
}

enum CheckoutUiMode {
  CHECKOUT_UI_MODE_UNSPECIFIED = 0;  // = hosted, for compatibility
  CHECKOUT_UI_MODE_HOSTED      = 1;
  CHECKOUT_UI_MODE_EMBEDDED    = 2;
}
```

Identical treatment for `CreateCurrentUserWalletTopupSessionRequest` / `Response`
(`billing.proto:200-208`) — top-ups are `mode: payment` and need embedded just as
much, since AI credit is the other half of the onboarding purchase, and the
`idempotency_key` there is what stops the `wallet_topups` row leak (`01` B3).

`billing_admin.proto`'s `CreateCheckoutSession` is **left alone**: operator
tooling in a different frontend, converting it buys nothing and widens the blast
radius.

### 3.2 `internal/billing/client.go`

`CreateCheckoutSession` returns `(string, error)` — the URL (`:84`, `sess.URL` at
`:146`). That signature cannot express embedded mode. Return a struct; three
call sites in `svcbilling/service.go`, plus the regenerated `mock_gen.go`.

```go
// CheckoutSession is what Stripe gave us back. Exactly one of URL and
// ClientSecret is populated, decided by CheckoutParams.UIMode.
type CheckoutSession struct {
    ID           string
    URL          string // hosted mode
    ClientSecret string // embedded mode
    UIMode       CheckoutUIMode
}
```

`CheckoutParams` (`billing/contract.go`) gains `UIMode`, `ReturnURL`, and
`IdempotencyKey`. Replacing the unconditional `SuccessURL`/`CancelURL`
assignment at `client.go:92-93`:

```go
switch uiMode {
case CheckoutUIModeEmbedded:
    sp.UIMode = stripe.String(string(stripe.CheckoutSessionUIModeEmbedded))
    // Never navigate the browser. Stripe completes in the iframe and the
    // component's onComplete fires. This is what removes the redirect.
    sp.RedirectOnCompletion = stripe.String(
        string(stripe.CheckoutSessionRedirectOnCompletionNever))
    // SuccessURL/CancelURL are REJECTED by Stripe in embedded mode.
    if params.ReturnURL != "" {
        sp.ReturnURL = stripe.String(params.ReturnURL)
    }
default:
    sp.SuccessURL = stripe.String(params.SuccessURL)
    sp.CancelURL  = stripe.String(params.CancelURL)
}
if params.IdempotencyKey != "" {
    sp.SetIdempotencyKey(params.IdempotencyKey)  // §6.2
}
```

**Caveat to verify:** `redirect_on_completion: never` and `return_url` may be
mutually exclusive in some API versions. If Stripe rejects the combination, drop
`ReturnURL` when `never` is set and accept that redirect-requiring payment
methods are unavailable — cards and wallets, the entire onboarding path, do not
need it.

The `subscription` vs `payment` branch, line items and metadata are untouched.
**Metadata is load-bearing**: `stripe_webhook.go` reads
`session.Metadata["user_id"]`, `["plan_id"]`, `["plan_type"]`,
`["billing_flow"]`, `["topup_id"]` at `:137/:159/:172/:245/:465`. Embedded mode
changes none of it, which is why entitlement stays webhook-driven and correct
for free.

### 3.3 `svcbilling/service.go`

`CreateComputeCheckoutSession` (`:893`) validates both redirect URLs at
`:894-899` before anything else. In embedded mode there are no redirect URLs:
validate `return_url` only when present, skip the other two entirely.

Pleasant side effect worth naming: the `ALLOWED_REDIRECT_HOSTS`
misconfiguration that `00-SYNTHESIS.md` identifies as a repeated outage — and
that `01` §2.2 steps 3–4 shows sitting on the critical path — **stops being on
the checkout path**. It still governs OAuth and the billing portal (which has no
embedded mode). A genuine robustness win, not only a UX one.

Preconditions 1, 2, 5–10 from `01` §2.2 are unchanged. **Step 11 — the cancel —
is deleted from this method entirely**; see §6.3.

**Keep the dev-mode no-Stripe path exactly as it is.** In dev, plans have
`stripe_price_id: null` and the service activates the subscription directly;
`openCheckout` already detects a same-origin URL and no-ops. The embedded client
needs the same escape: a response carrying neither `client_secret` nor a foreign
`checkout_url` means the purchase already completed server-side — treat it as
done and refetch. Losing this breaks every developer's loop.

### 3.4 Frontend: `<CheckoutPanel>`, one component, three mounts

New deps: `@stripe/stripe-js`, `@stripe/react-stripe-js` (pin; read the
installed `.d.ts` per §2.2). New config surface: a `VITE_STRIPE_PUBLISHABLE_KEY`
— `01` §10 confirms none exists today.

```
web/src/components/Billing/
  CheckoutPanel.tsx        the ONLY thing that mounts Stripe's embedded form
  useCheckoutSession.ts    creates/reuses the session, holds client_secret
  stripe.ts                loadStripe singleton, publishable key from env
```

```tsx
export interface CheckoutRequest {
  kind: "compute_plan" | "wallet_topup";
  planId?: string;        // compute_plan
  amountCents?: bigint;   // wallet_topup
  idempotencyKey: string; // §6.2 — required, never generated inside the panel
}
```

`CheckoutPanel` owns: creating the session **via the existing mutations** (so
`assertPurchaseIdentity` still runs — §7), rendering
`<EmbeddedCheckoutProvider stripe={stripePromise} options={{ clientSecret, onComplete }}>`
with `<EmbeddedCheckout />` inside, and surfacing
`CheckoutIdentityRequiredError` as an in-place identity form rather than a
navigation to `/upgrade` (which closes half of `01` B6).

**`onComplete` is a presentation signal, never proof of payment.**
`stripeCheckout.ts`'s header already states this rule for `?checkout=success` and
it survives verbatim: entitlement is webhook-driven. `onComplete` licenses
showing "Confirming your payment…" and polling
`GetCurrentUserComputeSubscription` / `GetCurrentUserWalletOverview`. It never
asserts a subscription the server has not reported. `CheckoutReturnBanner`
(`billing.tsx:200`) already implements exactly this poll — 2s interval, 60s cap,
only claims the plan once the server reports it — and that logic moves into the
panel rather than being rewritten.

Three mounts, one component:

1. `/settings/billing` — the purchase surface (§4).
2. The onboarding `checkout` step (§5).
3. `/checkout/embed` — a **minimal standalone route** whose only job is to host
   `CheckoutPanel` for the Electron window (§2.3 option A). No app chrome; on
   completion it navigates to `?checkout=success`, which the *existing*
   `classifyCheckoutReturn` already recognises and closes on. **Zero Electron
   main-process changes in Phase 1.**

That third mount is the whole desktop story, and it is why this is cheap: the
round-trip machinery already exists and works; we only change what URL it opens.

---

## 4. The unified billing page

### 4.1 The decision: unify the money, not the pages

The brief says "2 pages… either unify, or just simply make it better." Having
read both, and with `01` §5.3 in hand, the honest finding is that **they are not
two halves of one thing.**

- `billing.tsx` (1,401 lines, four tabs) is **money**: wallet balance, compute
  subscription, top-ups, invoices, usage, billing email, overage.
- `reliantAI.tsx` (889 lines) is mostly **AI access administration**: LLM keys
  (create / reveal / rotate / revoke), allowed models, spend cap, per-model
  spend. It also lives under `/settings/general`, not under billing.

`01` §5.3 pins the genuine duplication precisely: the **credit balance** appears
on both (formatted by two different functions — `formatCurrencyFromWalletFields`
vs a local `usdFromNanos` at `reliantAI.tsx:78`), the **coupon box** appears on
both, and `reliantAI.tsx` has two "set up billing" links that **bypass
`useGoToBilling`** (`:281`, `:356` — `01` B8).

Merging keys-and-models into billing produces one very long page with two
unrelated jobs. The defect is duplication in one direction, not separation.

**Recommendation — split by verb, not by product:**

| Page | Owns | Change |
|---|---|---|
| `/settings/billing` | **Everything that costs money.** Wallet balance + top-up, compute plan selection, invoices, usage, billing email, overage, coupon redemption. | Gains a first-class **purchase surface**; keeps its four tabs. |
| `/settings/ai-access` (renamed from Reliant AI) | **Everything that configures AI.** LLM keys, allowed models, spend cap, per-model spend. | Loses its top-up affordance and its coupon box; keeps a read-only balance chip linking to billing. Moves out of `/settings/general`. |

Net: **one place to spend money**, which is what "make this simple" requires,
without a 2,000-line page. Delete the second `usdFromNanos`.

*Devil's advocate against my own recommendation.* The owner said "2 pages" and
will still see 2 pages. If his model is "AI costs money, compute costs money, so
one page", this looks like I did not do it. What makes me confident anyway:
`/settings/billing` genuinely becomes the single place you *buy* anything — AI
credit **and** compute, both, in one purchase surface — and the other page stops
offering a competing door. The rename is load-bearing, not cosmetic: it is what
makes the split read as intentional rather than as a leftover.

### 4.2 Information hierarchy

The current Overview is a dashboard: it reports state and makes you navigate to
Plans to change it. Right for a returning user, wrong for someone who arrived to
buy — which is why `useGoToBilling` had to hardcode `?tab=plans`, and why a user
who needs **AI credit** lands on a **compute plan grid** (`01` B9).

Target Overview, top to bottom:

1. **One status line in plain language.** *"Compute Medium · 12 of 41 hours used
   this month · $18.40 AI credit remaining."* Not three cards to assemble — the
   sentence the user came to read.
2. **Anything wrong, immediately under it**, each with the one action that fixes
   it: empty/low wallet (`getWalletBalanceState`, `billingUtils.ts:62`), payment
   failed, no billing email. Nothing at all when everything is fine — no
   permanent yellow furniture.
3. **The two purchase actions, always present, always in the same place:** "Add
   AI credit" and "Change compute plan". Both open the purchase surface in-page;
   neither navigates.
4. **Detail below**, in the existing tabs. Usage, invoices, billing email,
   overage. Unchanged; they are fine.

The **purchase surface** is one component used by both actions *and* by
onboarding:

```
<PurchaseSheet>                  modal on desktop, full-screen on mobile
  ├── what you're buying         plan cards (compute) / amount presets (credit)
  ├── <CheckoutPanel>            Stripe embedded — mounts once a choice is made
  └── confirming / done          polls the server; never claims success early
```

Plan cards **are** the size picker (§2.6): each names the sizes it unlocks and
the included hours, both from `ListPlans` — **and the price, which by Phase 2 is
server-authoritative, not the client table.**

`?tab=` gains a `credit` target so a user who needs AI credit can be sent to the
top-up affordance directly. That closes `01` B9, and it is what the onboarding
model step's deleted link would have needed.

### 4.3 Mobile

Delete the `cheapestPlan` selection at `MobileBillingScreen.tsx:104-113` and
mount the same `<PurchaseSheet>` full-screen. The existing comment's defence —
"Stripe Checkout is mobile-native, so this needs no mobile-specific UI" — was
true of the hosted page and stops being true the moment we host the form.
Embedded checkout is responsive; the sheet needs a `sm:` breakpoint and nothing
more. The identity-required message that currently says "Open Settings on
desktop" (`:133`) is replaced by the in-place identity form.

Everything else on that screen is right and stays, including the deliberate
read-only posture for invoices and the usage chart.

---

## 5. The consolidated onboarding + billing flow

### 5.1 Principle: steps record intent; one commit point acts

Today `ComputeStep` and `ModelStep` each *do* things — `CreateDaemon`, a
coupon-armed auto-start, a wallet-funding gate — and each has an escape hatch to
billing when the doing is blocked. **An exit is what a step needs when its side
effect cannot proceed.** Remove the side effects and both exits disappear on
their own.

The plan object already is the intent record: it lives in the URL, survives
full-page navigation, and `deriveStep` is a pure function of it. This design adds
fields and one derived step. It does not add a state machine, a reducer, a
context, or a store — per `05`'s conclusion, which is correct and which I am not
relitigating.

### 5.2 Plan shape

```ts
export interface LaunchPlan {
  // ... existing fields unchanged

  /** Compute plan selected, when the compute choice needs one.
   *  plan_compute_small | _medium | _large | _xl. */
  computePlanId?: string;

  /** AI credit chosen to buy, in cents. Only for reliant_credits with an
   *  unfunded wallet. */
  aiCreditCents?: number;

  /** Stable for one onboarding run. Used as the Stripe idempotency key AND as
   *  the CommitLaunchPlan key, so a reload, a retry or a double-click cannot
   *  produce a second session or a second daemon. Generated once, on first
   *  arrival at a paid/terminal step. */
  commitKey?: string;

  /** Set only when the SERVER has confirmed the purchase (subscription and/or
   *  wallet balance observed) — never from Stripe's onComplete alone. */
  paid?: boolean;

  // REMOVED: daemonProvisioning. Provisioning is no longer something a step
  // starts and records in the URL; it is what the commit point does, and its
  // progress is server state the gate reads.
}
```

`routeSchemas.ts`'s `launchPlanSchema` must gain all four, and
`launchPlanSchema.drift.test.ts` — which `05` correctly calls the best test in
the directory — will fail until it does. That is the drift guard working.

### 5.3 `requiresPayment(plan, facts)` — the pure function that decides everything

```ts
/** Does this plan, as chosen, need money before it can run?
 *
 *  The single definition, in the shape of isCloudCompute: cloud-ness and
 *  payment-ness are both properties of the plan computed in exactly one place,
 *  so no step can disagree with another about them.
 *
 *  Deliberately NOT a function of eligibility internals — those are server
 *  facts the CALLER supplies. Pure over (plan, facts), so it is exhaustively
 *  testable in the 240-state style of 05 §1.
 */
export function requiresPayment(
  plan: Partial<LaunchPlan>,
  facts: { computeEligible: boolean; walletFunded: boolean },
): { needsCompute: boolean; needsCredit: boolean; any: boolean }
```

The branch table, **corrected for the no-trial reality of inherited fact 1**:

| compute | model | Payment? | What happens |
|---|---|---|---|
| local | own key | **No** | No checkout step, ever. **This is the free path through onboarding, and it is the only one.** |
| local | `reliant_credits` | **Credit only** | Checkout buys AI credit (`mode: payment`). No subscription. |
| cloud + compute coupon redeemed | own key | **No** | `compute_grants` minutes make `computeEligible` true. Step skipped. |
| cloud + existing subscription | own key | **No** | Returning user. Step skipped. |
| **cloud, new user** | own key | **Compute** | `mode: subscription`. **The common cloud case now.** |
| **cloud, new user** | `reliant_credits` | **Both** | See below. |

> **Say this plainly, because it changes the product's shape:** with the trial
> retired, **every new cloud user pays at onboarding**, and the only free way
> through is local compute with your own API key. The checkout step is therefore
> a *normal* part of the cloud flow, not an exception. That is a product fact
> the owner should see stated, not a design choice I am making — but it is worth
> confirming it is intended, because it is the strongest argument for keeping
> the model and compute steps exactly where they are (the user is choosing what
> to pay for before being asked to pay).

**"Both" is two Stripe sessions, not one**, and this is the design's most
consequential trade-off. A compute plan is `mode: subscription` against a Stripe
price; AI credit is `mode: payment` for a variable amount landing in the wallet
via `handleWalletTopupCheckoutCompleted` (`stripe_webhook.go:245`). Stripe
cannot combine a subscription and a variable one-off in one Checkout Session
while keeping both webhook paths intact.

- **(i) Two sequential embedded checkouts in one step**, "1 of 2". Two card
  entries — mitigated because Stripe offers the saved method for the second, and
  wallets are one tap.
- **(ii) Credit as a subscription line item.** Elegant; requires modelling AI
  credit as a Stripe product and rewriting `handleWalletTopupCheckoutCompleted`
  to grant from an invoice. A billing-model change on the critical path of an
  evening's work.
- **(iii) Grant a small free allowance and prompt later.** Best UX; gives away
  AI credit, which the constraints state is not affordable.

*Decision: (i), and make it rare.* When a user is buying compute and picks
`reliant_credits`, present credit as an **optional add-on with a preselected
amount inside the same step**, so most people take the default and see one
payment flow with two confirmations rather than two separate journeys. A user
who declines and later needs credit hits the second session from settings, not
during onboarding. Flag (ii) as the right follow-up once the billing model can
absorb it.

### 5.4 Where the billing moment sits

**A derived `checkout` step, after `model`, before the project steps.**

```
compute → model → [checkout] → project-choice → github-connect
                              ↘ project-picker
```

One clause in `deriveStep`, in one place:

```ts
export function deriveStep(
  plan: Partial<LaunchPlan>,
  facts: OnboardingFacts,   // { computeEligible, walletFunded }
): OnboardingStepId {
  if (!plan.compute) return 'compute';
  if (!plan.modelProvider) return 'model';

  // Both choices are made; if either costs money and it is not yet paid, this
  // is the ONE place we ask. Absent entirely when nothing is owed.
  if (!plan.paid && requiresPayment(plan, facts).any) return 'checkout';

  if (!isCloudCompute(plan.compute)) return 'project-picker';
  if (!plan.intent) return 'project-choice';
  if (plan.intent === 'existing_codebase') return 'github-connect';
  return 'project-choice';
}
```

**This widens `deriveStep` from a pure function of the plan to a pure function of
`(plan, facts)`, and that is a real cost** — `05` §1's 240-state enumeration
becomes 240 × 4, and every caller must supply facts. It stays pure, which is what
makes it acceptable. The alternative — writing eligibility into the URL plan — is
worse: it puts a server-owned, time-varying fact into user-editable state, which
is precisely the `cloud_paid`-shaped hazard of `05` Defect A.

*Why a step and not a modal.* `06` recommended a modal, and that was right **when
checkout meant leaving the app** — a modal was the smallest container for an
unavoidable round trip. With `redirect_on_completion: never` the argument
inverts, and inherited fact 1 inverts it further:

- A step is **addressable**: it is `deriveStep`'s output, so it appears in the
  progress bar, Back works through `BACK_CLEARS`, and a reload lands back on it.
  A modal is component state a reload destroys — while the user is mid-payment.
- A step is **honest about the flow's length.** "Payment, 3 of 5" is a smaller
  surprise than a dialog over step 2.
- A step **cannot be dismissed into an inconsistent state.** A modal has an X;
  what does closing it mean when a paid plan is required to continue?
- **Because payment is now the normal cloud path, not an exception, it deserves
  to be a step.** Modals are for exceptions. My first draft defended the step by
  saying most users would never see it; with the trial retired that defence is
  wrong, and the honest one is stronger: a routine part of the flow should look
  like a routine part of the flow.
- It costs no duplicated UI: the step and the settings `PurchaseSheet` mount the
  same `CheckoutPanel`.

`BACK_CLEARS` gains `'checkout': ['modelProvider']` — Back from payment returns
to the decision that created the cost. Verify against `05` §1 invariant 3 ("Back
is a true inverse") in the enumeration test; it is exactly the class of thing
enumeration catches and hand-written tests do not.

### 5.5 What the steps stop doing

**`ComputeStep`:**
- **Deletes `handleCloud`'s `CreateDaemon` call** and everything supporting it:
  `pendingCloudStart`, `cloudStartAttempt`, `isStartingCloud`, the
  eligibility-flip effect at `:388-407`, and the coupon auto-start. Selecting
  cloud writes `compute` and advances. That is all it does. This is `01` B7 and
  the deletion the brief names.
- **Keeps** the local auto-skip effect at `:433-449` — a genuine "the question is
  already answered" shortcut, not a side effect, and `computeAutoSkipped`
  already handles its progress-bar consequences.
- **Keeps** `RedeemCouponForm`; a redemption now only refetches eligibility. If
  the coupon covers compute, `requiresPayment` returns false and the checkout
  step never appears. **The ~120 lines of effect-ordering race commentary at
  `:369-450` — which `01` §9 correctly reads as the signal that
  provisioning-from-effects is the wrong shape — are deleted with the race.**
- The "Set up billing" button at `:639` is **deleted**. Users who want prices
  first get an inline "See plans" disclosure — no navigation.

**`ModelStep`:**
- The `!hasFunds` guard at `:207` inverts. Choosing `reliant_credits` with an
  empty wallet no longer blocks; it records the choice and lets `deriveStep`
  route to `checkout`. "Start with Reliant" stops being `disabled` on an
  unfunded wallet, removing the dead end its `title` attribute apologises for.
- The "Set up billing" link at `:422` is **deleted** — which incidentally
  retires `01` B9 rather than fixing it.

Both onboarding exits are gone. `useGoToBilling(from: "onboarding")` and its
`returnTo` round-tripping become dead for onboarding; delete in Phase 5. The
hook still serves `ResumeDaemonPill`, `ConnectDaemonModal` and
`UpgradeRequiredModal`, which are in-app. While there, point `01` B8's three
bare-`navigate` call sites at the hook.

### 5.6 The checkout step's UI

```
┌─ Set up billing ─────────────────────────────────┐
│  You chose: Reliant Cloud + Reliant's models     │
│                                                   │
│  Compute plan                                     │
│    ○ Small   small machines · 16 h/mo   $20/mo   │
│    ● Medium  up to medium  · 41 h/mo   $40/mo   │
│    ○ Large   …                                    │
│                                                   │
│  ☑ Add $20 of AI credit    (preselected, §5.3)   │
│                                                   │
│  ┌──────────────────────────────────────────┐    │
│  │  <CheckoutPanel> — Stripe embedded       │    │
│  │  card · Apple Pay · Google Pay · Link    │    │
│  └──────────────────────────────────────────┘    │
│                                                   │
│  ← Back                    Have a code? [Redeem] │
└───────────────────────────────────────────────────┘
```

- Prices shown here are the **server's** prices (§2.6). Shipping this step on
  top of the client price table would put an invented number next to a real
  card form.
- Selecting a plan writes `computePlanId` to the URL, so the choice survives a
  reload mid-payment; the credit add-on writes `aiCreditCents`.
- `CheckoutPanel` mounts once a plan is chosen. **Changing the plan expires the
  old session** (`checkout.sessions.expire`) and creates a new one under a new
  idempotency key — see §6.2; this is safe *because* the cancel-on-create is
  gone (§6.3).
- Coupon redemption stays. Redeeming enough flips `requiresPayment` to false and
  derivation advances the user with no button to press — the URL-derived design
  paying off.
- On `onComplete`: show "Confirming your payment…", poll subscription + wallet,
  and only when the **server** reports entitlement `updatePlan({ paid: true })`.
  If the webhook is slow the user waits on an honest pending state with a
  support affordance after ~30s — never a false success, never a silent hang.

---

## 6. Provisioning and payment: intent → commit

### 6.1 The commit RPC

**Onboarding never provisions. It records intent and, at exactly one point, asks
the server to fulfil it.**

New RPC — recommend `reliant.v1.OnboardingService/CommitLaunchPlan`, in
`reliant`, since it orchestrates reliant-side project creation as well as
control-plane provisioning:

```proto
message CommitLaunchPlanRequest {
  string commit_key = 1;       // the plan's commitKey; see §5.2
  ComputeIntent compute = 2;   // cloud + plan, or local (no-op)
  ModelIntent   model   = 3;
}
message CommitLaunchPlanResponse {
  string commit_id = 1;
  CommitStatus status = 2;     // PENDING | RUNNING | COMPLETE | PARTIAL | FAILED
  repeated CommitTask tasks = 3;
}
message CommitTask {
  string name = 1;             // "grant_ai_access" | "provision_daemon"
  TaskStatus status = 2;
  string detail = 3;           // human-readable; safe to show
  string daemon_id = 4;
}
```

Plus `GetCommitStatus(commit_id)`. **Poll, do not stream**, for Phase 1: the
existing `DaemonConnectingGate` already polls and its `derivePhase(daemon,
elapsedMs)` is a pure function `05` §4 confirms is well-tested with fake timers.
A stream is a transport to get wrong; polling reuses a harness that works.

The server records `(user_id, commit_key)`; a second call with the same key
returns the existing commit. A user who reloads mid-provision, double-clicks, or
whose webhook arrives twice gets one daemon. `CreateDaemon` is *already*
idempotent by name (`01` §2.4), so this is belt and braces on the one call that
had it — and the only protection on the ones that did not.

### 6.2 Idempotency for money — inherited facts 2 and 3

**The problem embedded checkout makes worse.** With a redirect, minting a second
session required navigating back and clicking again. With a panel, closing and
reopening it is one click and looks like nothing happened. `01` B2: two
completed sessions produce two Stripe subscriptions and one DB row — a double
charge the control plane cannot see. `01` B3: every top-up call leaks a pending
`wallet_topups` row.

**Strategy, two layers, both required:**

**Layer 1 — a Stripe idempotency key on every session-creating call.** Derived
server-side from facts the server holds, not trusted from the client:

```
checkout-<userID>-<planID>-<clientIdempotencyKey>
topup-<orgID>-<amountCents>-<clientIdempotencyKey>
```

`client.go` already does exactly this for `CreateCustomer`
(`customer-<orgID>-<email>`, `:55`) — the pattern exists and is proven in this
file; it was simply never applied to checkout. Stripe returns the **same
session** for a repeated key within 24h, so a reopened panel gets its original
session back rather than a second one.

Note the constraint: Stripe **errors** if one key is reused with different
parameters. That is desirable here — it makes "same key, different plan" a loud
failure instead of a silent second subscription. Changing plan legitimately
means a new key, which is why the client key is part of the composite and why
the UI expires the old session (§5.6).

**Layer 2 — reuse before minting.** Before calling Stripe, look for an open
session for this `(user, purpose, key)`. If one exists and is not expired,
return its `client_secret` with `reused = true`. This is what makes the
`wallet_topups` row leak stop: the row is created **once per key**, not once per
call. The row already exists as the natural place to record this for top-ups;
compute checkout needs the equivalent, which is either a small
`checkout_sessions` table or a `stripe_checkout_session_id` on the existing
attempt record.

**And a reaper**, since abandoned attempts still accumulate: a periodic job that
expires Stripe sessions older than 24h and marks their rows `abandoned`. Without
it the top-up history stays misleading (`01` B3). Small, and it belongs with
this work rather than after it.

### 6.3 The class prohibition — inherited fact 4

`01` B1: `CreateComputeCheckoutSession` cancels the existing subscription at
**session creation** (`service.go:933-943`), swallowing the failure to a log
line. A user who opens "Switch plan" and closes the panel has silently scheduled
their current plan for cancellation, with nothing in the UI reflecting it.

This is the `CreateDaemon`-from-an-effect bug wearing different clothes: **a
side effect fired at the moment of intent rather than the moment of
commitment.** And the owner's redesign makes plan-switching *easier* — a
purchase surface always visible on Overview, a panel one click away, no redirect
to discourage a second look — so it makes this bug **more reachable**, not less.
A redesign that fixed the provisioning half and left the cancel half would have
made the system worse on net.

**The rule, stated generally so it forbids the class rather than the instance:**

> A call that **creates**, **cancels**, or **charges** may be issued only at a
> commit point: a direct user action, or a webhook confirming money moved.
> Never from a `useEffect` observing a state change, and never as a side effect
> of preparing an option the user has not yet taken.

Applied:

| Instance | Fires today at | Must fire at |
|---|---|---|
| `CreateDaemon` on eligibility flip (`ComputeStep.tsx:388-407`, `01` B7) | an effect watching server state | `CommitLaunchPlan`, after payment confirmed (§6.4) |
| `CancelSubscription` on plan switch (`service.go:933-943`, `01` B1) | checkout **session creation** | the `checkout.session.completed` webhook for the *new* subscription |
| `wallet_topups` row insert (`service.go:1162-1176`, `01` B3) | every session-creation call | once per idempotency key (§6.2) |
| Second Stripe subscription (`01` B2) | any repeat call | prevented by the idempotency key (§6.2) |

**Concretely for the cancel:** delete step 11 from
`CreateComputeCheckoutSession`. Move it into `handleComputeCheckoutCompleted`
(`stripe_webhook.go:464`), which already runs at the moment the new subscription
becomes real and already knows the `user_id` — cancel any *other* active compute
subscription for that user there, before the upsert. This is strictly safer: it
cannot fire for an abandoned checkout, because an abandoned checkout produces no
`completed` event. And unlike today it should **not** swallow its failure — a
user paying for two plans simultaneously is a billing incident, so log at error
and surface it, per `00-SYNTHESIS.md` Tier-1 item 3 (forward `logger.Error` to
Sentry).

*Sequencing note:* a separate agent is fixing B1 in parallel. If their fix moves
the cancel to the webhook, this design is already satisfied and Phase 1 inherits
it. If they fix it another way — e.g. cancelling on panel *close* — that
re-creates the class at a different moment; the rule above is what to check
their fix against.

### 6.4 Ordering and dependencies

```
payment confirmed (server-observed, not onComplete)
        │
        ▼
  CommitLaunchPlan(commit_key)
        │
        ├── grant_ai_access     ← no dependency; fast; often a no-op
        │
        └── provision_daemon    ← DEPENDS on the compute subscription being
                                  active, which is webhook-driven
```

**AI credit before compute**, for a concrete reason. The top-up webhook
(`handleWalletTopupCheckoutCompleted`) is a simple balance credit and lands
fast. The compute webhook (`handleComputeCheckoutCompleted`,
`stripe_webhook.go:464`) must create a subscription row before
`GetCurrentUserComputeEligibility` reports eligible. Calling `CreateDaemon`
before that lands returns a `ResourceExhausted` denial, which the existing
`isEntitlementDenial` / `upgradeInterceptor` machinery turns into an
`UpgradeRequiredModal` — **so a user who has just paid gets told to upgrade.**

`provision_daemon` therefore waits for entitlement rather than assuming it —
**and, per §2.7, must also handle eligibility being right and `CreateDaemon`
still refusing:**

```
provision_daemon:
  poll GetCurrentUserComputeEligibility (server-side) up to N seconds
    not yet eligible after N → status PENDING, detail
        "Waiting for payment confirmation from Stripe"     (NOT failed)
  eligible → CreateDaemon(size = largest allowed by the purchased plan)
    ResourceExhausted / entitlement denial → status FAILED with the server's
        own reason (size not allowed, minutes exhausted), NOT a generic error.
        This is 01 B6: eligibility is a prediction, so the refusal path is
        reachable even on the happy timeline.
```

**Do not move provisioning into the webhook.** It is tempting — the webhook is
the moment entitlement becomes true — but a webhook that provisions
infrastructure provisions it for *every* purchase, including a settings-page
plan change by a user who already has a daemon. Provisioning stays in the
explicit commit path, which knows whether it was asked for.

### 6.5 Failure modes

| Failure | Behaviour | Rationale |
|---|---|---|
| Payment succeeds, webhook late | "Confirming your payment…", polling; after ~30s adds "taking longer than usual" + support link. Never advances on `onComplete`. | The client cannot know; the server can. The rule already written in `stripeCheckout.ts`'s header. |
| Webhook duplicate / replay | Event-ID dedup already exists (`stripe_webhook.go:88-98`); `commit_key` covers the commit side. | Duplicates are normal, not exceptional. |
| Panel closed and reopened | Same idempotency key → same session, `reused = true`. No second charge, no second row. | §6.2 — the hole embedded mode would otherwise widen. |
| AI granted, compute failed | `PARTIAL`. User continues with a working model and a banner: "Your machine couldn't start — retry, or use your own computer." **Onboarding completes.** | They paid and got something. Blocking them in the wizard to punish a provisioning failure is the worst response. |
| Compute up, AI grant failed | `PARTIAL`, same shape. | Same. |
| Everything failed after payment | `FAILED`, explicit "we've charged you and something went wrong", support path, `commit_id` to quote. | Never silent. A charge with nothing to show must reach a human. |
| Abandoned mid-payment | Nothing committed, **and nothing cancelled** (§6.3). Plan is in the URL; returning re-derives to `checkout`. | The point of intent-first. |
| Reload mid-provision | `commit_key` from the URL → `GetCommitStatus` → the gate resumes. | Idempotency working. |

### 6.6 What the user sees while it runs

`DaemonConnectingGate` already exists, polls, has a tested `derivePhase`, and is
the cloud path's only exit (`ProjectChoiceStep.tsx:36-38`). **Generalise it into
`ProvisioningGate`** rendering `CommitTask[]` as a short checklist:

```
Setting up your workspace
  ✓ AI access ready
  ⟳ Starting your machine…      (medium)

                                    [ Continue ]  ← enabled on COMPLETE or PARTIAL
```

Keep its timeout and failure phases — they are the product of a real bug fix
(`458a830c`) and are correct. The change is that it reports two tasks instead of
inferring one from `ListDaemons`.

---

## 7. The invariant that must survive every phase

**`assertPurchaseIdentity()` runs on every path that spends money.** It lives in
the mutation (`useCloudBillingQueries.ts:61`, called at `:177` and `:198`), and
`useGoToBilling`'s comment records why that placement is load-bearing: one
chokepoint replacing five navigation call sites, any of which could have been
added without it. `01` B8 shows three call sites that already bypass the
navigation helper — proof the failure mode is live.

Therefore: **`CheckoutPanel` must create its session through
`useCreateCheckoutSession` / `useCreateWalletTopupSession`, never by calling the
billing client directly.** A panel that bypasses the mutation to "simplify"
silently removes the anti-anonymous-purchase guarantee and nothing would fail.
Pin it with a test that mounts `CheckoutPanel` as an anonymous user and asserts
the identity form appears and **no session is created**.

Server-side, `resolveBillingEmail` (`service.go:923-926`) remains the backstop,
unaffected by `ui_mode`.

---

## 8. Migration path

Five phases, each independently shippable and testable.

### Phase 0 — Make price authoritative *(small, and it gates Phase 2)*

Add `price_cents` and `overage_cents_per_minute` to `plans.yaml` /
`plans.prod.yaml`, serve through `ListPlans`, delete
`COMPUTE_PLAN_PRICE_CENTS`, `COMPUTE_PLAN_OVERAGE_FALLBACK_CENTS_PER_MIN` and
the `COMPUTE_PLAN_IDS` allowlist (`billingUtils.ts:95-114`), replacing ordering
with a catalog `display_order`. Closes `01` B4 and B5.

Ships value alone: the price the UI shows becomes the price Stripe charges. Do
it first because every later phase renders prices next to a payment form.

### Phase 1 — Embedded checkout behind a flag *(highest value, highest risk)*

Backend: proto (§3.1), `client.go` returning `CheckoutSession` with the
idempotency key (§3.2, §6.2), `service.go` conditional redirect validation
(§3.3), **the cancel moved to the webhook (§6.3)**, session reuse + reaper
(§6.2). Frontend: Stripe deps, `CheckoutPanel`, `/checkout/embed`. Wire into
`/settings/billing` only, behind `VITE_BILLING_EMBEDDED_CHECKOUT`; the hosted
path stays default.

Setup tasks that gate the whole thing: **register payment-method domains** for
every environment hostname in live *and* each test mode; enable automatic
payment methods; plumb `VITE_STRIPE_PUBLISHABLE_KEY` to every env — and per
`00-SYNTHESIS.md`'s finding that env plumbing to the bundle is a repeated
failure class, **add the key to the bundle assertions in
`deploy-reliant-web.yml`**.

**Verification is this phase's real deliverable:** a test-mode purchase
completing with no navigation on (a) desktop web, (b) mobile web, (c) the
Electron window via `/checkout/embed`. Plus a deliberate double-submit proving
one Stripe subscription and one DB row. If (c) fails, §2.3's premise is wrong
and Phase 3's desktop story needs rework — better to learn it now.

### Phase 2 — The unified billing page

`PurchaseSheet`, the Overview hierarchy (§4.2), the `credit` tab target, the AI
page renamed and stripped of its money doors, mobile losing `cheapestPlan`,
`01` B8's three bare navigates pointed at the hook. Pure frontend; depends on
Phase 0 for prices, not on Phase 1 (it mounts whichever checkout is live), so it
can be built in parallel by a second person.

### Phase 3 — Intent-only steps

`ComputeStep` stops calling `CreateDaemon`; `ModelStep` stops gating on funds;
both "Set up billing" buttons deleted; `requiresPayment`, the `checkout` step,
`deriveStep(plan, facts)`. Provisioning still happens where it does today
(`finalizeOnboardingSideEffects`), just later and unconditionally rather than
speculatively. Flag: `VITE_ONBOARDING_UNIFIED_BILLING` — both paths coexist
because `deriveStep` is one function and the flag decides whether the clause is
evaluated.

Tests before code: extend `05` §1's enumeration over `(plan, facts)` with all
four invariants including the new step, and extend
`launchPlanSchema.drift.test.ts` for the four new fields.

**This is the phase that delivers the owner's headline ask.**

### Phase 4 — The commit point

`CommitLaunchPlan` / `GetCommitStatus`, `ProvisioningGate`, the ordering and
refusal paths of §6.4, `PARTIAL` handling. Phase 3 makes the *decision* single;
Phase 4 makes the *execution* correct.

### Phase 5 — Delete the hosted path

Remove `success_url`/`cancel_url` (reserve tags 2 and 3), the
`?checkout=success|cancelled` handling, `buildCheckoutReturnUrls`,
`openCheckout`, the Stripe-specific Electron window if `/checkout/embed` fully
replaces it, `useGoToBilling`'s onboarding branch, and both flags. Pre-launch,
no compatibility owed; this deletion is the point.

**Do not skip it.** Two live checkout paths is exactly the shape that produced
"a fix applied to one file only" (`06` §5.2, §5.4).

---

## 9. Decisions made on the owner's behalf

Implementation starts tonight; none of these blocks it, and each is reversible.

1. **`ui_mode: "embedded"`, not `"elements"`** (§2.2).
2. **`redirect_on_completion: "never"`** (§3.2) — what actually deletes the
   redirect; everything else follows.
3. **Electron gets `/checkout/embed` in the existing controlled window**, not
   embedded-in-renderer (§2.3). Forced by `app://bundle`.
4. **Price and overage become server-authoritative before the new purchase
   surface ships** (§2.6, Phase 0). A payment form next to an invented price is
   not shippable.
5. **Included minutes are not size-weighted** (§2.6) — matches current metering;
   weighting is a separate change.
6. **Split by verb: `/settings/billing` owns money, `/settings/ai-access` owns
   AI configuration** (§4.1), with the rename load-bearing. Differs from a
   literal "merge the two pages"; delivers the "one place to spend money" the
   brief actually asks for.
7. **Plan cards are the compute-size picker** (§2.6, §4.2).
8. **Billing is a derived `checkout` step, not a modal** (§5.4) — reversing
   `06`'s modal recommendation, because the redirect is gone *and* because
   payment is now the normal cloud path rather than an exception.
9. **`deriveStep` takes `(plan, facts)`** (§5.4). Eligibility is server-owned and
   must not live in the URL.
10. **Two sequential sessions when both AI and compute are owed, with the credit
    add-on preselected** (§5.3). Option (ii) is the right follow-up, not
    tonight's work.
11. **Server-derived Stripe idempotency keys + reuse-before-mint + a 24h
    reaper** (§6.2). Closes `01` B2 and B3, and closes the hole embedded mode
    would otherwise widen.
12. **Subscription cancellation moves to the webhook**, and stops swallowing its
    failure (§6.3). Closes `01` B1 and forbids the class.
13. **AI grant before compute provisioning, with compute waiting on entitlement
    and handling refusal** (§6.4). Prevents "you just paid — please upgrade",
    and covers `01` B6.
14. **Partial failure completes onboarding with a banner** (§6.5).
15. **Polling, not streaming, for commit status** (§6.1).
16. **`ComputeStep` keeps local auto-skip and coupon redemption; loses
    `CreateDaemon` and the auto-start effect** (§5.5).

## 10. Open risks and one thing the owner should confirm

- **For the owner:** with the trial retired (inherited fact 1), **every new
  cloud user now pays during onboarding**, and the only free path is local
  compute with your own API key. This design is built on that being intended.
  If it is not — if some free cloud allowance is meant to exist — §5.3's branch
  table and §5.4's justification both change, and it is worth saying before
  Phase 3.
- **Stripe.js in an Electron `BrowserWindow` on an https origin is assumed to
  work, not verified.** Phase 1 verification (c) is the check, deliberately in
  the first phase.
- **`redirect_on_completion: never` + `return_url` may be mutually exclusive.**
  If so drop `return_url`; cards and wallets do not need it.
- **PayPal will probably not appear for compute subscriptions** (§2.5), and the
  brief names PayPal explicitly. Copy must not promise it.
- **What the Stripe Dashboard has enabled is unverified** (`01` §7) — a setup
  task, not a code fact, and Phase 1 cannot be called done without checking it.
- **`deriveStep`'s widened signature touches every caller** — mechanical, but in
  the file `05` warns is under concurrent edit; sequence Phase 3 after other
  agents' fixes land.
- **`svcbilling` is already flagged as "a god-orchestrator scheduled for
  rewrite"** in its own `//nolint` comments (`service.go:889`, and `01` §1 quotes
  it). This design adds to it. That is right for now — a rewrite on the critical
  path of a billing change loses a week — but it is debt taken knowingly.
