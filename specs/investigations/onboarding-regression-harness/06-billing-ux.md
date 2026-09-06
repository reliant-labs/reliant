# 06 — Billing flow UX review

**Scope:** the path from the onboarding compute decision to a completed Stripe
purchase and back. Review and design proposal only; no product code was
touched.

**The owner's open question, answered first:** after Stripe, you land back on
`/settings/billing` — but on the **Overview** tab with no success state, no
confirmation, and a full cold boot of the SPA. And you land on the *identical*
URL whether you paid or hit "back" in Stripe, because all three call sites pass
`window.location.href` for BOTH `successUrl` and `cancelUrl`. The app cannot
tell the two apart and does not try.

---

## 1. The trace

Numbered by hop. "Full-page" means the SPA is torn down and cold-booted.

### Entry A — onboarding compute decision (the owner's starting point)

1. **`/onboarding?plan=…` renders `ComputeStep`.** Cloud eligibility comes from
   `useCloudEligibility()`
   (`web/src/components/OnboardingFlow/steps/ComputeStep.tsx:133-138`). When
   ineligible, `canStartCloud` is false
   (`ComputeStep.tsx:157`) and the cloud button is replaced by a
   **"Set up billing"** button (`ComputeStep.tsx:636-646`) whose entire
   `onClick` is `goToBilling` (`ComputeStep.tsx:638`). No interstitial, no
   explanation, no "we'll bring you back."
   *Same affordance, same absence of explanation, at three other doors:*
   `ModelStep.tsx:422` ("Set up billing" under the coupon form),
   `UpgradeRequiredModal.tsx:44` ("Upgrade plan"),
   `Chat/ResumeDaemonPill.tsx:47`, `Projects/ConnectDaemonModal.tsx:65`.

2. **`useGoToBilling` forks on anonymity**
   (`web/src/hooks/useGoToBilling.ts:29-44`). `isAnonymous` is
   `user.is_anonymous === true` (line 33). If anonymous →
   `navigate({ to: "/upgrade", search: { returnTo: "/settings/billing" } })`
   (lines 36-39). Otherwise straight to `/settings/$section` billing (line 43).
   The reasoning in the doc comment (lines 9-19) is sound and the redesign
   below preserves it. **The user is told none of it.**

3. **`/upgrade` renders `UpgradeAccount`** (`web/src/components/UpgradeAccount.tsx:59`).
   This is a full-height `AuthLayout` screen — visually indistinguishable from
   a sign-in page. The component's own doc comment insists "This is NOT a
   sign-in screen, and the distinction is the whole point"
   (`UpgradeAccount.tsx:16-18`) — but that distinction exists only in the
   comment. The rendered screen does not say "you're linking an identity to
   the account you already have," and it does not say where the user is
   headed. **This is precisely the "go back to sign in if you signed in
   anonymously" step the owner flagged as cumbersome; it reads as a demand to
   re-authenticate.**

4. **Identity link — two sub-paths.**
   - **OAuth (GitHub/Google/Apple):** `handleLink` (`UpgradeAccount.tsx:114-135`)
     threads `returnTo` into the OAuth `state` (lines 126-129) and calls
     `linkOAuthIdentity`, which **redirects the whole window out to the
     provider** — "control does not return here on web" (line 125). Full-page
     nav #1 → provider → full-page nav #2 back to `/auth/callback`.
   - **Email + password:** `signUp` upgrades the anon user in place
     (`UpgradeAccount.tsx:157-160`); with confirmation on, it drops into the
     `EmailVerification` OTP screen. No navigation, but an inbox round-trip —
     the user leaves the app to a mail client, which is a context switch the
     app cannot even observe.

5. **`/auth/callback` honors `returnTo`** (`web/src/components/OAuthCallback.tsx:89-92`)
   via `window.location.assign(returnTo)` — **another full-page nav** (#3).
   Note `UpgradeAccount.goToReturnTo` deliberately uses a client-side
   `navigate({ href })` and its comment (lines 47-53) explains that the old
   `window.location.assign` "tore down the SPA and paid a full cold boot, which
   is what made returning from /upgrade look like a page refresh that did
   nothing." **`OAuthCallback.tsx:92` still does exactly that.** The lesson was
   learned in one file and not the other.

6. **`/settings/billing` renders `BillingSection`**
   (`web/src/components/Settings/cloud/billing.tsx:92`), defaulting to the
   **Overview** tab (`billing.tsx:93`). The user asked for a plan; they get a
   wallet/usage dashboard. To buy anything they must notice and click the
   **Plans** tab (`billing.tsx:132`). Tab state is `useState`, explicitly *not*
   in the router (`billing.tsx:83-85`) — so it is unaddressable and cannot be
   deep-linked or restored.

7. **Click "Subscribe" on a plan** → `handleSubscribe`
   (`billing.tsx:711-733`). It sends `successUrl` and `cancelUrl` **both set to
   `window.location.href`** (`billing.tsx:715-720`).

8. **Backend mints the Stripe URL.** `createCurrentUserCheckoutSession` →
   `CreateCurrentUserCheckoutSession`
   (`control-plane/internal/billing/svcbilling/service.go:812`) →
   `CreateComputeCheckoutSession` (`service.go:840`), which validates both URLs
   through `checkRedirectURL` (`service.go:841-846`, impl at `service.go:386-394`,
   host allowlist at `service.go:2733-2747`) and passes them to Stripe at
   `service.go:895-896`.

9. **If no billing email resolves**, `resolveBillingEmail`
   (`service.go:869`) returns `billing_email_missing`, which the global
   interceptor turns into `BillingEmailRequiredModal`
   (`web/src/api/upgradeInterceptor.ts:56-66`). For a user with no account
   email that modal's only action is `handleVerifyIdentity`
   (`BillingEmailRequiredModal.tsx:75-82`) — **which navigates to `/upgrade`
   again.** This is the loop commit `fe56294` ("guide identity-less users
   through the email step before checkout") was aimed at; the loop is now
   guided rather than fatal, but it is still a second trip to the same screen.

10. **`redirectToStripe(res.checkoutUrl)`** (`billing.tsx:178-183`,
    duplicated verbatim at `Mobile/MobileBillingScreen.tsx:45`) sets
    `window.location.href` — **full-page nav out of the app** (#4). In
    packaged Electron this is worse: `shouldOpenExternally`
    (`reliant/electron/src/navigation-policy.js:38-68`) sees an `https://`
    target against an `app://bundle` origin, returns true, and the navigation
    is handed to `shell.openExternal` (`electron/src/main.js:1047-1050`). **The
    user is thrown into their system browser, into a session that may not be
    signed in there at all.**

11. **Stripe hosted checkout.** The user pays, or backs out.

12. **Return.** Stripe sends the browser to `successUrl` — which is whatever
    `window.location.href` was at step 7, i.e. `/settings/billing`. **Answering
    the owner's question: yes, it does come back to billing.** But:
    - It is a cold boot of the SPA, not a client-side return.
    - The **Overview** tab, not Plans — tab state is local (`billing.tsx:93`).
    - **`successUrl === cancelUrl` at every one of the three call sites**
      (`billing.tsx:260-261` top-up, `billing.tsx:715-720` subscribe,
      `MobileBillingScreen.tsx:122` mobile). There is no `?checkout=success`
      marker, so the app renders identically whether money changed hands or
      not.
    - Subscription state arrives whenever the Stripe webhook lands and the
      query refetches. The user may see their old plan for several seconds with
      no indication anything is in flight.
    - **In Electron the user is now in a browser tab, and the app window behind
      it has no idea the purchase happened.** Nothing brings them back.

### The three server call sites (owner asked whether they agree)

They agree on *mechanism* and disagree on nothing structurally, because **all
three take the URL from the client and all three clients pass
`window.location.href`**:

| RPC | Service fn | success/cancel |
|---|---|---|
| `CreateCheckoutSession` (org/admin) | `service.go:471`, Stripe at `540-541` | caller-supplied |
| `CreateComputeCheckoutSession` | `service.go:840`, Stripe at `895-896` | caller-supplied |
| `CreateWalletTopupSession` | `service.go:1042`, Stripe at `1126-1127` | caller-supplied |

So the server is not the problem — it is a pass-through with a host allowlist.
The defect is that **every client passes the same value for success and
cancel**, discarding the one signal Stripe gives you for free.

---

## 2. Cost of the current path

Worst realistic case: anonymous user, OAuth link, buying a compute plan from
onboarding.

- **Full-page navigations: 5** — (1) out to OAuth provider, (2) back to
  `/auth/callback`, (3) `window.location.assign(returnTo)` to billing
  (`OAuthCallback.tsx:92`), (4) out to Stripe (`billing.tsx:181`), (5) back
  from Stripe. Each is a full SPA cold boot.
- **Context switches: 4** — app → provider → app → Stripe → app. In Electron
  it is worse: app → **system browser** → (nothing brings you back).
- **Re-authentications: 1**, presented as though it were a fresh sign-in
  (`UpgradeAccount.tsx` renders `AuthLayout`/`AuthHeader`, step 3). Zero
  actual sign-ins are required — it is an identity *link* — but the user
  cannot tell.
- **In-app clicks before reaching Stripe: minimum 4** — "Set up billing" →
  pick a provider → (return) → **Plans tab** → "Subscribe". The Plans-tab click
  is pure friction: nothing carried the intent "I want to buy a plan" across
  the detour.
- **Explained before it happens: 0 of 5 navigations.** Every one is discovered
  mid-flight. The rationale for the biggest detour lives in a code comment
  (`useGoToBilling.ts:9-19`) that no user will ever read.
- **State at risk:** the anonymous session's work survives by design
  (`signUp`/`linkIdentity` upgrade in place — `UpgradeAccount.tsx:20-25`), and
  that guarantee is real. What does *not* survive is **intent**: the plan the
  user was about to buy, the tab they were on, and the fact that they came from
  onboarding are all dropped. `returnTo` carries a path and nothing else.
- **Onboarding-specific hazard:** leaving `/onboarding` for `/upgrade` exits
  the wizard entirely, and `returnTo` points at `/settings/billing`, **not back
  into onboarding**. There is no route home. Combined with the returning-user
  heal that can call `CompleteOnboarding` from a background effect (BRIEFING,
  "onboarding's control flow"), a user who detours to billing mid-onboarding
  may never see the rest of the wizard.

---

## 3. The "why are we going here" problem, hop by hop

| Hop | What the user sees now | What they need to see |
|---|---|---|
| "Set up billing" click (`ComputeStep.tsx:638`) | Instant navigation away from onboarding | "To start a cloud machine we need a payment method. This takes about a minute and you'll come right back here." |
| `/upgrade` (`UpgradeAccount.tsx:59`) | What looks like a sign-in wall | "You're already signed in — we just need a real identity on this account so your subscription (and your work) isn't tied to this browser. Next: choose a plan." |
| OAuth bounce (`UpgradeAccount.tsx:129`) | Sudden jump to github.com | A one-line "Redirecting you to GitHub…" with the destination named |
| `/auth/callback` (`OAuthCallback.tsx:92`) | Blank flash, page reload | Progress with the *original* goal restated: "Identity linked — taking you to plans" |
| Billing Overview (`billing.tsx:93`) | A usage dashboard | The Plans tab, preselected, because that is what they asked for |
| Stripe hand-off (`billing.tsx:181`) | Instant jump to stripe.com | "Opening secure checkout with Stripe" + what happens on return |
| Post-Stripe return (step 12) | Overview tab, unchanged | "Your Pro plan is active" — or, if the webhook hasn't landed, "Confirming your payment…" |

The through-line: **there is no persistent "you are 2 of 3 steps from a running
machine" affordance anywhere.** Each hop is an isolated screen that has
forgotten why the user started.

---

## 4. Recommended redesign

### Recommendation: keep the identity requirement, delete the navigation.

**Do the identity link inline on the billing page, after plan selection, in a
modal — and make the Stripe round-trip the only navigation that remains.**

Concretely:

1. **`useGoToBilling` stops branching on anonymity.** Everyone goes to
   `/settings/billing`. Delete the `/upgrade` redirect from that hook
   (`useGoToBilling.ts:35-41`). The anonymity check moves to the moment of
   purchase, not the moment of navigation. *The guarantee is preserved because
   the check still gates checkout — it just gates it later and in place.*
2. **Deep-link intent.** Route the CTA to the Plans tab with the originating
   context: `/settings/billing?tab=plans&from=onboarding`. This requires
   lifting the tab out of `useState` (`billing.tsx:93`) into a search param.
   The comment at `billing.tsx:83-85` argues tab state "never touches the
   router" so it composes under the settings shell — that was right when
   nothing needed to link into a tab; it is now the thing preventing intent
   from surviving a redirect.
3. **On "Subscribe", if anonymous or email-less, open an identity modal in
   place** rather than navigating. It reuses `UpgradeAccount`'s existing
   mechanisms (`linkIdentity`, `signUp`, OTP) but as a step in the purchase,
   with copy that names the reason: *"Before we take payment, we need an
   account we can reach — a subscription tied to this browser session would be
   lost with it. Your chats and projects stay exactly as they are."*
   Email+OTP completes with **zero navigation**. OAuth still round-trips, but
   `returnTo` now carries `?tab=plans&plan=<id>` so the user returns *to the
   plan they picked*, mid-purchase, not to a dashboard.
4. **Make the post-Stripe return honest.** `successUrl` gets
   `?checkout=success&plan=<id>`; `cancelUrl` gets `?checkout=cancelled`. Fix
   at all three sites (`billing.tsx:260-261`, `billing.tsx:715-720`,
   `MobileBillingScreen.tsx:122`). Billing reads the param, shows a confirmed
   state or a "confirming your payment…" pending state until the webhook lands,
   and strips the param. `from=onboarding` additionally offers "Back to
   setup" — closing the dead end noted in §2.
5. **Electron: open Stripe in a controlled window, not `shell.openExternal`.**
   A `BrowserWindow` on the Stripe origin whose `will-navigate` is watched for
   the return URL lets the app *know* the purchase completed and close the
   window itself. Today the app never finds out (step 10/12).

Net: **1 navigation** (Stripe, unavoidable) for the email path, **3** for the
OAuth path — down from 5 — and every remaining one is announced before it
happens.

### Alternatives weighed

- **Stripe embedded checkout / Payment Element.** Would remove the last
  navigation. `internal/billing/client.go:92-93` currently builds a hosted
  session from `SuccessURL`/`CancelURL`; embedded mode needs
  `ui_mode: embedded` and a `client_secret` returned to the browser instead of
  a URL — a proto change (`checkoutUrl` → `clientSecret`), a new frontend
  Stripe.js dependency, and it makes the `ALLOWED_REDIRECT_HOSTS` machinery
  moot for checkout. **This is the right long-term answer and it is
  particularly compelling for Electron**, where it eliminates the browser
  escape entirely. I am not recommending it *first* only because it is a
  wire-contract change that lands on top of active work in these files; the
  four steps above are strictly compatible with doing it afterwards, and
  none of them is wasted if it lands.
- **Ask for identity at the compute decision.** Cheap, and it puts the ask
  inside a flow the user has already accepted. But it taxes every user
  including those who pick local compute and never spend a cent, and the whole
  point of anonymous sessions is that you can try Reliant without an account.
  Rejected.
- **Defer identity to after plan selection, still as a navigation.** Better
  than today (the user has committed to something concrete first), but it keeps
  the full-page hop and the sign-in-looking screen. Strictly dominated by the
  modal.

### Devil's advocate, against my own proposal

- **A modal that hosts OAuth still redirects the whole window.** For the
  GitHub/Google path I have moved the screen, not removed the round-trip. The
  honest saving there is 1 nav (the `assign` at `OAuthCallback.tsx:92` becomes
  a client-side return) plus the preserved intent. If most users pick OAuth
  over email, the headline "5 → 1" is really "5 → 3", and I should not oversell
  it.
- **Lifting the tab into the router contradicts a deliberate decision**
  (`billing.tsx:83-85`) and adds search-param surface to a settings shell that
  currently has none. If that shell later owns its own params, this is a
  collision. I still think it is right — unaddressable state is exactly why
  intent dies here — but it is a real cost, not a free win.
- **Removing the `/upgrade` redirect from `useGoToBilling` weakens a
  single-chokepoint guarantee.** That hook exists so no caller can dead-end an
  anonymous user, and its comment says so (lines 21-23). Moving the check to
  the purchase button means *every* purchase button must carry it — five call
  sites today. Mitigation: put the check inside the checkout mutation
  (`useCreateCheckoutSession`, `useCloudBillingQueries.ts:129`) so it is
  structurally impossible to bypass, not inside each button. If that
  mitigation is not implemented, **do not do step 1.**
- **`?checkout=success` is a client-side claim, not proof of payment.** It must
  drive presentation only; entitlement stays webhook-driven. A user can type
  the param. The pending state must be real ("confirming…"), not a fake
  success.

---

## 5. Correctness bugs found (reported, not fixed)

1. **`successUrl === cancelUrl` at all three call sites.**
   `billing.tsx:260-261`, `billing.tsx:715-720`,
   `MobileBillingScreen.tsx:122`. The app cannot distinguish a completed
   purchase from an abandoned one, and shows no success state for either.
2. **`OAuthCallback.tsx:92` uses `window.location.assign(returnTo)`** for an
   in-app path, forcing a full SPA cold boot. `UpgradeAccount.tsx:47-53`
   documents this exact bug as already-fixed — the fix was applied to one file
   only.
3. **Electron sends Stripe checkout to the system browser.**
   `redirectToStripe` (`billing.tsx:181`) sets `window.location.href` to an
   `https://` URL; `shouldOpenExternally`
   (`electron/src/navigation-policy.js:56-67`) sees a cross-origin target
   against `app://bundle` and `main.js:1047-1050` hands it to
   `shell.openExternal`. The purchase then completes in a browser the app has
   no channel to, and the `successUrl` return lands in that browser — possibly
   in a signed-out session. **The app window is never notified.**
4. **`redirectToStripe` is duplicated verbatim** in `billing.tsx:178-183` and
   `MobileBillingScreen.tsx:45`. Any fix to the Electron behaviour above must
   be made twice or it silently isn't.
5. **Onboarding has no return path from billing.** `useGoToBilling.ts:38`
   hard-codes `returnTo: "/settings/billing"`, so a user who detours from
   `ComputeStep` lands on billing with no route back into the wizard.
6. **`BillingEmailRequiredModal` can bounce a user to `/upgrade` a second
   time** (`BillingEmailRequiredModal.tsx:75-82`) after they have already been
   through it via `useGoToBilling` — e.g. a GitHub link that yields no public
   email. Two visits to the same sign-in-looking screen in one purchase.
7. **`checkRedirectURL` fails closed in prod when `ALLOWED_REDIRECT_HOSTS` is
   unset** (`service.go:386-392`) — correct behaviour, but the resulting client
   error is a bare `InvalidArgument("redirect URL not allowed")` surfaced
   through `formatBillingError` as "Failed to start checkout". A config outage
   presents to the user as a generic billing failure with no operator signal in
   the UI. Not a logic bug; a diagnosability gap on a path with a documented
   history of exactly this misconfiguration.
