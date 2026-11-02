> Copied on 2026-11-01 from `research/app-ux-review-2026-11/04-onboarding-billing-machines.md` (outside any repository) so it is versioned with the synthesis.

# 04: First-run, onboarding, sign-in, billing, usage, plans, coupons and machines

Area reviewer 04 · 2026-11-01 · review only (no product code changed, no PRs).

- **Preview (my instance, still running):** https://5319-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/
  It is the harness v2, serving a production `vite build` of reliant `origin/main` **0ec4b8119** from my worktree
  `/home/workspace/.reliant/worktrees/reliant-labs/ux-review-onboarding-billing-4ca8c81d/reliant`.
- **My fixtures:** `research/app-ux-review-2026-11/fixtures-onboarding-billing/` (10-catalog, 20-usage, 30-org-obs-hosting,
  40-readiness, 50-machines, 60-coupon, 70-hosting-consent). The catalog and every rate are copied from
  control-plane `origin/main` (406e0e71) `internal/plansconfig/pricing.yaml`. The response shapes follow the control-plane
  handlers I read: `svcbilling/machine_acceptance.go`, `svcbilling/usage_billing.go`, `svcbilling/daemon_usage.go`, and
  the operator's `provisioning_detail.go:122`.
- **Scenario URLs** (all on the preview above):
  - `/onboarding?fx=onboarding` is a new user with nothing paid for.
  - `/onboarding?fx=onboarding,coupon` is a user covered only by a coupon (the server admits `m` only).
  - `/settings/billing?fx=clear` is a subscriber on the M plan, 62 of 160 M-hours used.
  - `&tab=plans` and `&tab=usage` open those tabs.
  - `/settings/machines?fx=capacity,provisioning` adds a machine waiting on capacity and one provisioning.
  - `/settings/machines/d-cloud-big-77a1` is a suspended L machine.
  - `?fx=held` gives the held-pricing answer. `?fx=obs-over` puts observability over its allowance.
  - `/auth?fx=signed-out` is the sign-in screen.
  - Test coupon codes: `BAD` is rejected, `CREDIT25` adds $25 of credit, and anything else adds 100 M-hours.
- **Visual verification:** in this harness `take_screenshot` does not return pixels (bug 3f6a8b8e). Every visual claim
  below is **measured, not seen**: it comes from DOM text, computed styles, contrast math and the code. Two full-page
  captures were saved for the owner in `shots/onboarding-billing/` (`04-onboarding-compute-unfunded.png`,
  `04-change-plan-upgrade-dead-end.png`). I could not view them.
- **On the brief's premise:** reliant **#1844 is merged** (2026-11-01 19:07Z, merge commit 26094149). Since its head
  (9bba3999) there are no diffs on main in `components/Billing`, `components/OnboardingFlow`, `components/Settings/cloud`
  or `RedeemCouponForm.tsx`. So "main" and "the #1844 branch" are the same code for this area, and one review covers both.

---

## 1. Inventory

| Surface | Route / entry | Main files |
|---|---|---|
| Sign-in | `/auth` | `components/AuthScreen.tsx`, `OAuthButton.tsx` |
| Onboarding shell + progress | `/onboarding` | `OnboardingFlow/OnboardingPage.tsx`, `ProgressBar.tsx`, `StepFooter.tsx`, `stepConfig.ts` (`deriveStep` decides the step from the `plan` URL param only) |
| Step 1: Machine (compute/plan) | onboarding | `OnboardingFlow/steps/ComputeStep.tsx`, `Billing/PlanTiles.tsx`, `Billing/machineOptions.ts`, `Billing/machineSpecs.ts`, `Machines/PairMachinePanel.tsx` (local path) |
| Step 2: Model | onboarding | `steps/ModelStep.tsx`, `OAuthHelperPanel.tsx` |
| Step 3: Payment (only when something is owed) | onboarding | `steps/CheckoutStep.tsx`, `Billing/OnboardingCheckout.tsx`, `Billing/MachineSaleConsent.tsx` |
| Steps 4–6: Project / GitHub / Finish, then provisioning | onboarding | `ProjectChoiceStep`, `GitHubConnectStep`, `FinishStep`, `ProvisioningGate.tsx`, `DaemonConnectingGate.tsx`, `commitLaunchPlan.ts`, `useReturningUserHeal.ts` |
| Coupon redemption (one form, five mounts) | compute step, model step, checkout, billing overview, plan checkout | `components/RedeemCouponForm.tsx` |
| Billing: Overview | `/settings/billing` | `Settings/cloud/billing.tsx`, `overview/StatusLine.tsx`, `CreditBand.tsx`, `ComputeBand.tsx`, `ComputeOverageControl.tsx`, `WalletAutoRechargeControl.tsx` |
| Billing: Change plan | `?tab=plans` | `billing.tsx` (PlansTab ~1120–1340), `Billing/MachinePricingReadinessPanel/Card.tsx`, `Billing/ComputeSubscriptionCheckout.tsx` |
| Billing: Usage & invoices | `?tab=usage` | `usage/UsageBillingPanel/Card.tsx` (hosting consent), machine usage (`billing.tsx` ~1495–1545), `Forge/Observability/UsageView.tsx`, `usage/ObservabilityLimits.tsx`, `usage/DeployUsageSection.tsx`, `usage/EnforcementPanel.tsx` |
| Billing: Reliant AI | `?tab=ai` | `Settings/cloud/reliantAI.tsx` |
| Machines list / detail | `/settings/machines`, `/settings/machines/$id` | `Settings/cloud/machines.tsx` (list, detail, ResizeMachineModal), `machineLifecycle.ts`, `lib/machineWake.ts` |
| Add machine (create, local or cloud) | "Add machine" | `Machines/AddMachineSheet.tsx`, `Machines/addMachine.ts` |
| Mobile billing | `/m/settings` → Billing | `Mobile/MobileBillingScreen.tsx` |

The paths walked in the preview, with their status:

- **Onboarding, nothing paid:** Machine → Model → Payment. The Payment step is broken; see B1 and B3.
- **Onboarding, coupon:** Machine (M marked "Covered") → Model → Payment. Payment shows the machine as "already covered" and charges only for AI credit.
- **Onboarding, own computer:** Machine → connect panel.
- **Billing, subscriber:** all four tabs.
- **Plan change:** dead end (B1).
- **Add machine → bigger size:** dead end (B1).
- **Machines:** list, detail, resize modal, capacity row, provisioning row.
- **Coupons:** rejected code and compute-hours code.
- **Sign-in:** rendered.
- **Mobile billing:** rendered.

No JS errors were logged across these. The only console output was a `[warn] settingsSync not initialized` warning, three times.

---

## 2. Pricing correctness

### 2a. Where prices come from (the good news)

The app no longer hard-codes any machine, hosting or observability price. Every figure comes from the server:

- monthly price from `ListPlans.plans[].priceCents`;
- overage from `daemonPricing.sizes[].exactHourlyPriceCents`;
- storage from `storageCentsPerGibMonth`;
- observability from `GetObservabilityUsage.rateCentsPerGb` and `allowanceBytes`;
- hosting from `GetCurrentUserUsageBilling.rates`.

control-plane `pricing.yaml` on main matches the new price list exactly, so every number the app shows is correct when the server is right. The one client-side copy is the hardware table `Billing/machineSpecs.ts:55-63`, and it matches the brief.

### 2b. The table (measured from the rendered preview)

| Size | Brief (vCPU/GB, $/mo, overage/h) | pricing.yaml (CP main) | App renders | Where | Verdict |
|---|---|---|---|---|---|
| S1 | 1/2, $25, $0.20 | 2500, 160 h, "20", 30 GiB | "S1 · 2 GB RAM · 1 CPU · $25.00/mo"; "$0.20/hr past included hours" | ComputeStep, Plans tab, Add machine, machines list | price ✔, overage ✔ |
| S2 | 1/4, $30, $0.25 | 3000, 160, "25", 30 | "4 GB RAM · 1 CPU · $30.00/mo", "$0.25/hr" | same | ✔ |
| M | 2/4, $39, $0.30 | 3900, 160, "30", 50 | "4 GB RAM · 2 CPU · $39.00/mo", "$0.30/hr" | same | ✔ |
| L | 4/8, $69, $0.55 | 6900, 160, "55", 80 | "8 GB RAM · 4 CPU · $69.00/mo"; checkout "l: $69.00/month, 160 own-size hours, 80 GiB disk. Extra running time $0.55/hour" | same + MachineSaleConsent | ✔ (copy wrong, see F6) |
| XL | 8/16, $149, $1.15 | 14900, 160, "115", 120 | "$149.00/mo", "$1.15/hr" | same | ✔ |
| 2XL | 15/32, $249, $1.95 | 24900, 160, "195", 200 | "$249.00/mo", "$1.95/hr" | same | ✔ |
| 3XL | 30/64, $489, $3.80 | 48900, 160, "380", 300 | "$489.00/mo", "$3.80/hr" | same | ✔ |
| Included hours | 160 per plan | 160 | "Includes 160 hours a month" (onboarding, Plans). "Includes **160.0** hours a month" (Resize modal) | `machines.tsx:1234` via `planOwnSizeHours` | value ✔, format ✗ |
| Machine storage | $0.25/GiB-mo | storage "25" | "50 GB included, then $0.25/GB-month" | `ComputeOverageControl.tsx:373` | ✔ |
| Hosting CPU | $0.07/vCPU-hr | 7.0 | "Hosted CPU: **7.0 cents** / vCPU-hour" | `UsageBillingPanel.tsx:49` | value ✔, format ✗ |
| Hosting memory | $0.012/GiB-hr | 1.2 | "Hosted memory: 1.2 cents / GiB-hour" | same | value ✔, format ✗ |
| Hosting storage | **$0.25/GiB-mo** | 0.0342465753424658 ¢/GiB-hr | "Hosted storage: **0.0342465753424658 cents / GiB-hour**" | same | ✗: the unit doesn't match the public price, and the number is unreadable |
| Egress | $0.15/GB **after 10 GB** | 15, included 10 | "Internet egress: 15 cents / GiB" with no free 10 GB stated. Egress is absent from usage attribution entirely | `UsageBillingPanel.tsx:49`; `usage/usageModel.ts:62-70,113` | ✗ |
| Extra static sites | $5/mo | 500 | "Extra static sites: 500 cents / site-month"; first-site-free not stated | `UsageBillingPanel.tsx:49` | ✗ (format) |
| Hosting minimum | **$5 minimum** | minimum_cents 500 | **Not shown anywhere.** The card says "Pay as you go, with **no base fee**" | `usage/UsageBillingCard.tsx:41` | ✗ (omitted; "no base fee" misleads) |
| Observability | 10 GB incl., $0.25/GB | 10, "25" | "6.4 GB billable / 10 GB included", "$0.25 per GB beyond included" | `Forge/Observability/UsageView.tsx` | ✔ |

### 2c. Stale prices, sizes and copy (file:line, main = #1844)

**Visible to users:**

1. `components/Settings/cloud/overview/ComputeBand.tsx:171`: `Runs {allowedSizesLabel.toLowerCase()}` renders "Runs **s1, s2, m**". It lower-cases the size labels.
2. `components/Settings/cloud/ComputeOverageControl.tsx:135`: `rateLabel` uses the raw wire `s.size`, which renders "s1: $0.20/hr · s2: … · 3xl: $3.80/hr". It lists all seven sizes even on an M plan that can only run s1/s2/m. The same label repeats at `:190` and `:270`.
3. `components/Billing/MachineSaleConsent.tsx:32`: `{terms.size}:` renders "**l**: $69.00/month, 160 **own-size hours**, 80 **GiB** disk". `MachinePricingReadinessCard.tsx:45-47` does the same ("Proposed m: …, own-size running hours … GiB persistent disk", "Version machine-pooled-v1 · route machine-pooled-v1/m").
4. `components/Billing/exactMachineUsage.ts:17`: the unit "equivalent h at plan size" / "weighted h". The overview headline reads "Compute M · ≈ 62.0 equivalent h at plan size of 160.0 equivalent h at plan size included". The Usage tab and mobile show "247.1 **weighted** h included / ≈ 95.7 weighted h". These are S1-weighted hours, so one account shows three different hour counts (see F3).
5. `components/Settings/cloud/billing.tsx:1504, 1531`: "Approximate hours are display-only; weighted balances are not dollar credit." and a "PRICING VERSION · machine-pooled-v1" stat. Both are internal.
6. `components/Settings/cloud/machines.tsx:1234` (via `daemonUsage.ts:planOwnSizeHours`, `formatRational(…,1)`): "Includes **160.0** hours a month."
7. `components/OnboardingFlow/steps/CheckoutStep.tsx:284`: `${selectedPlan.name} — …` renders "**Compute M** — a Reliant-hosted machine…". The compute step called the same thing "M". Mobile (`Mobile/MobileBillingScreen.tsx:327,412`) lists "Compute S1 / S1", "Compute S2 / S1, S2", and so on. The landing page calls the plan "S1".
8. `components/Billing/OnboardingCheckout.tsx:136`: after consent the compute line's detail becomes "**Versioned monthly price shown in the accepted machine terms**".
9. `components/Billing/machineSpecs.ts:77-81`: "`2 GB RAM · 1 CPU`" puts memory first and says "CPU". The landing table (`landing-page src/pages/Pricing.tsx`, columns "Dedicated vCPU / Dedicated memory / Disk / Included / After included") puts vCPU first and calls it **dedicated**. The app never shows disk (30–300 GB) or the per-size overage on any picker.
10. `components/Settings/cloud/billingUtils.ts:522` `formatCentsAsDollars` prints "$25.00/mo", "$489.00/mo" and "$10.00" credit chips. The landing page prints whole dollars ("$25/mo"), and so do the billing Overview credit chips ("$10 $25 $50 $100", `overview/CreditBand.tsx`).
11. `components/Settings/cloud/usage/UsageBillingCard.tsx:41`: "Pay as you go, **with no base fee**". It contradicts the $5 monthly minimum (`pricing.yaml hosting.minimum_cents: 500`), and the minimum appears nowhere in the app.
12. `components/Settings/cloud/usage/usageModel.ts:62-70, 113`: the comment says egress, CDN egress and static-site kinds are "removed from the catalog because nothing measures them". That is no longer true: `pricing.yaml hosting.egress/static_egress/extra_static_site` are billed and metered. So the hosting usage attribution under-reports a bill that now includes egress and extra sites (see B4).
13. `components/OnboardingFlow/steps/ComputeStep.tsx:784-787`: "Your code covers the M machine … **Larger** machines need a monthly plan". S1 and S2 are *smaller* and are also not covered, and they still show $25/$30 beside the free M.

**Code comments only (stale, not visible to users; low priority, but they will mislead the next editor):**

- `Billing/machineOptions.ts:7-9`: the plan nesting "small ⊂ medium ⊂ large ⊂ xl in control-plane's plans.yaml" (there is no plans.yaml any more; the file is `pricing.yaml`).
- `Billing/machineOptions.ts:60`: "costs $79".
- `Billing/PlanTiles.tsx:44, 85-87`: "Medium — 4 GB RAM · 1 CPU"; "the free plan's allowance (small)". The coupon unit is **m**.
- `OnboardingFlow/steps/ComputeStep.tsx:510, 516, 546-547`: "$20.00/mo", "Small / $15.00/mo".
- `ComputeStep.tsx:773-776`: "machine time at the free plan's size (small)".
- `ComputeStep.tsx:926`: "Continue with Large".
- `OnboardingFlow/commitLaunchPlan.ts:103`: "XL — $160.00/mo".
- `Settings/cloud/machines.tsx:240-247`: the old per-minute $0.02–$0.16 table, "a Small that reserves 0.5 CPU".
- `Machines/addMachine.ts:76`, `AddMachineSheet.tsx:15, 94`: "else Small", "Default Small". The code defaults to `"m"`.
- `Billing/daemonUsage.ts:139`: "≈ 10.4 h on your Large (4×)".
- `Billing/OnboardingCheckout.tsx:171-179`: "$45.00 … $46.25".
- Dead code: the "burst" note in `ComputeStep.tsx:757-767` and `billing.tsx:1318-1335`. `MACHINE_BURST` is always `undefined` now that requests equal limits (`machineSpecs.ts:55-63`).

**Harness note (not product):** the shared `harness/fixtures/00-base.js` still uses `size: 'medium'`, `allowedDaemonSizes: ['small','medium']`, and an unknown field `grantedMinutesRemaining`. That field makes protobuf-es reject the eligibility response without any visible error. I told the harness owner and override both in my own fixtures.

---

## 3. What's broken

### B1 (H): An existing subscriber cannot change plan, and "Add machine → bigger size" is a dead end

- **Repro:** `/settings/billing?fx=clear&tab=plans`, pick **L**. Or Machines → Add machine → pick L → "Continue to payment".
- **Result:** both land on "PAY WITH CARD — *This organization has an existing machine period. Proposed changes require the separate next-period terms acceptance in Billing; this is not a new-subscription checkout.*", followed by "Have a coupon? … covers this in full". There is no button and no path forward.
- **Cause:** `Billing/MachineSaleConsent.tsx:23` returns that paragraph whenever the readiness answer carries `effectivePeriodStart`. control-plane sets it for **every** org with a live subscription (`svcbilling/machine_acceptance.go:102-103`).
  - The only other affordance is `MachinePricingReadinessCard`'s "Accept next-period terms". It re-accepts the **current** plan's terms ("Proposed m: $39.00/month"); it does not switch to L.
  - The client has no plan-change RPC: `CreateCurrentUserComputeSubscriptionIntent` is the only purchase path (`billing.proto:426`).
- **Effect:** the Resize modal's "Larger sizes need a bigger compute plan. See plans" and Add machine's "upgrades your plan" both point into this dead end.
- **Status:** measured in the preview, using fixtures that mirror the server's logic. It needs a backend/product decision (an upgrade RPC with proration, or cancel-and-resubscribe), so the owner has to decide.

### B2 (H): Checkout shows internal, legal and engineering copy to customers, and hides the card form behind an unstyled checkbox

The surfaces involved are `MachineSaleConsent.tsx:30-37`, `MachinePricingReadinessCard.tsx:42-57`, `billing.tsx:1230` and `usage/UsageBillingCard.tsx:53`.

- **Onboarding Payment step, nothing paid:** the order summary *and* the pay form only render after the user ticks a 13 px native checkbox. Measured: `appearance:auto`, `accent-color:auto`, no Tailwind classes. The checkbox reads: "I accept version **machine-pooled-v1** at the displayed monthly and hourly prices for this new subscription. **This is not next-period acceptance for an existing subscription.**"
- **Change plan tab, every subscriber:** a "Proposed machine pricing" card says:
  - "Version machine-pooled-v1 · route machine-pooled-v1/m";
  - "Other sizes use the versioned equivalent-hours exchange, not a dollar credit";
  - "**These are proposed terms, not an approved live offer.** Commercial approval, commissioning, published prices and period reconciliation must all be verified before activation. **A billing hold must not destroy machine data or interrupt conversation recovery.**"

  These prices *are* the approved live offer (`pricing.yaml machine_profiles.machine-pooled-v1: stage active, commercial_status approved`), so the card is also false.
- **Held state (`?fx=held`):** the server's hold reasons are listed verbatim, e.g. "machine route machine-pooled-v1/l has not been commissioned" and "proposed route economics do not clear the complete cost guard".
- **Empty or partial readiness:** the sentence renders with zero values and a leading colon: ": $0.00/month, 0 own-size hours, 0 GiB disk. Extra running time /hour." (`MachineSaleConsent.tsx:32`, reached with `{}`).
- **Hosting consent:** the same pattern. "I authorize organization-wide pay-as-you-go app hosting at the displayed rates and budget under policy &lt;version>." uses a native checkbox (`UsageBillingCard.tsx:53`).

### B3 (H): The unfunded onboarding checkout depends entirely on `GetMachinePricingReadiness`

When that call errors, the user sees "Machine pricing is unavailable on this server. No new paid checkout is authorized." (`MachineSaleConsent.tsx:18`). When it is held, they see the internal hold reasons above.

Either way the only screen in onboarding that can take money has no way forward and no fallback. In particular it never offers "use your own computer instead" or "continue without a machine". The org-unverified copy (`:13`) does mention the free option; these two branches do not. The checkout's Back button is still there.

### B4 (H): Hosting price disclosure and usage are wrong against the new hosting pricing

- **Consent rates** (`UsageBillingPanel.tsx:49`): `${displayName}: ${priceCents} cents / ${unit}` renders "Hosted storage: 0.0342465753424658 cents / GiB-hour" and "Extra static sites: 500 cents / site-month". It never says "after 10 GB free" for egress, never says the first static site is free, and never mentions the $5 minimum. The card above it says "no base fee" (`UsageBillingCard.tsx:41`).
- **Usage attribution** only knows cpu/memory/storage (`usageModel.ts:62-70`), so egress, static-site egress and extra sites never appear in "Where it is going". The header disclaims this ("Estimated retail cost of the attribution rows shown below, not a complete monthly charge", `DeployUsageSection.tsx:197`), but under the new pricing those are real line items.
- **Enforcement with no state** (`EnforcementPanel.tsx:69-71`): "Enforcement state unavailable — The platform reported a state this app does not recognise. **Treat your services as potentially restricted**". This is an alarm shown to an org that may have no hosting at all.

### B5 (M): Observability billing ships engineering notes

- `usage/ObservabilityLimits.tsx:112`: "Usage threshold and cap-reached notices are wired to email organization owners and admins via **Resend**. **Delivery has not been live-tested.**"
- `:91` and `usage/observabilityBillingModel.ts:12`: "A Stripe subscription is required for overage". Since #1844, hosting and observability are independent of machine subscriptions, so it is unclear which subscription is meant.
- `Forge/Observability/UsageView.tsx:86,88`: raw ISO timestamps ("Measured through 2026-11-01 20:07:16.210Z", "Period: 2026-11-01 00:00:00 UTC to 2026-12-01 00:00:00 UTC (exclusive)"). "Unavailable to Unavailable (exclusive)" appears before the first reading. "GB = 2^30 bytes of telemetry as stored, before compression, after sampling."
- A link "Usage & limits in Billing & usage" points at the page you are already on.

---

## 4. Findings by surface

### Onboarding: Machine step (`steps/ComputeStep.tsx`)

- **F1 (M) No overage or disk at the point of decision.** The rows show only "$NN.00/mo", and the footnote says "Extra hours and storage are billed separately" (`daemonUsage.ts:247`). The landing page shows "After included $0.20/h" and "Disk 30 GB" per size. A user comparing XL ($1.15/h) with M ($0.30/h) learns the overage only after buying, on the Overview tab. `PlanTileRow` has `detail` and `includedHours` slots; overage and disk fit a second detail line.
- **F2 (M) The coupon-covered path doesn't say what the coupon actually is.** Redeeming shows "Machine-time coupon redeemed. Your machine usage balance shows the hours it adds." (`RedeemCouponForm.tsx:116`). That balance is not on this screen. `res.computeMinutes` is available and unused (`services/controlPlane/reliantAI.ts:170`).
  - The coverage note "Your code covers the M machine — no charge today" doesn't give the hours (100) or say they are one-time. The plan footnote beside it still says "Includes 160 hours a month", which a coupon user does not get.
  - "Larger machines need a monthly plan" is wrong for S1/S2 (2c-13).
  - The Payment step then shows "Cloud machine — already covered — ~~$39.00~~", with no "/mo" and no word about what happens when the coupon hours run out.
- **F3 (H, belongs to billing but starts here) One account, three hour units.**
  - Onboarding and Plans: "Includes 160 hours a month".
  - Overview: "≈ 62.0 equivalent h at plan size … of 160.0 equivalent h at plan size included", plus "≈ 98.0 h left at the current running-machine rate".
  - Usage tab and mobile: "247.1 weighted h included / ≈ 95.7 weighted h".

  All three describe the same 62-of-160 M-hours. Users bought "160 hours". Show M-hours (the plan's own size) everywhere, and keep the S1 weighting server-side.
- **F4 (M) Brand and contrast.**
  - The footer CTA `FOOTER_PRIMARY` is a hard-coded `bg-sky-600` (`ComputeStep.tsx:960`; measured `oklch(0.588 0.158 241.966)` ≈ #0084d1). White 14 px/600 on it is **≈ 4.0:1, which fails WCAG AA** (needs 4.5:1 below 18.66 px bold). The brand fill #0a70e0 measures ≈ 4.8:1. Elsewhere in the app the primary button is near-white (`forge-ui` Button, pure-black scheme), so onboarding's CTA is a third colour.
  - `ProgressBar.tsx:28` uses a `from-sky-500 to-violet-500` gradient on the current step, and `:49` an `emerald-400 → cyan-400 → sky-500` connector. That is two gradients on a brand that uses blue and violet sparingly and keeps green for success.
  - "Free" is `text-emerald-500` (`ComputeStep.tsx:645`). That is green used for a price, not a success.
  - Flagged for the brand-token agent, area-specific only.
- **F5 (L) Copy.**
  - "The paid machines are ours: nothing to install…" (`ComputeStep.tsx:745`) is awkward.
  - "Paid machines run in Reliant Cloud, and allow access from any device, including mobile." is fine.
  - The heading "Where should Reliant run your code?" is good. The brand line "You set the direction. Your agents do the work." is absent from first-run entirely.
  - Measured: the onboarding window was 1905×2053 in the harness, and the footer CTA sat at y = 1511. That is fine here, and the old below-the-fold concern is solved by the sticky footer.
- **F6 (L) Specs wording:** "2 GB RAM · 1 CPU" versus the landing's "1 vCPU · 2 GB, dedicated". Lead with vCPU, say "dedicated", and add disk (2c-9).

### Onboarding: Model step (other reviewer's core area; noting only where it touches money)

- "Have a coupon? Redeem it here — it covers this in full, and no card is needed." (`ModelStep.tsx:543`) is false for a $10 coupon against $25 of credit. The same promise appears in `WalletTopupCheckout.tsx:201` and `ComputeSubscriptionCheckout.tsx:223`. `OnboardingCheckout.tsx:294`'s "covers what it applies to in full" is closer to true but still vague.
- The default provider, Claude subscription, shows "The local OAuth helper is not running. **We need this to intercept your credentials:**" (`OAuthHelperPanel.tsx:230`). "Intercept your credentials" is the worst possible phrase on a first-run screen.
- Credit chips here read "$10.00 / $25.00"; on Overview they read "$10 / $25".

### Onboarding: Payment step (`CheckoutStep.tsx`, `OnboardingCheckout.tsx`)

- B2 and B3 above.
- The compute line reads "Compute M" (2c-7) and then becomes "Versioned monthly price shown in the accepted machine terms" (2c-8).
- "Due today $94.00" (L + $25 credit) was correct in the preview, but the card fee only appears via `GetCurrentUserWalletTopupQuote`, which I left unfaked. The code handles it (`OnboardingCheckout.tsx:171-233`), and I did not verify it.

### Sign-in (`AuthScreen.tsx`)

- Measured: the heading is just "Reliant" / "Sign in", with no product line. "Build it. Ship it. Run it." would give a first-time visitor a reason to sign in.
- Provider buttons hard-code two unrelated fills: GitHub `bg-[#24292e]` (`OAuthButton.tsx:29`) and Google `dark:bg-gray-800` (`:23`, measured `oklch(0.278 0.033 256.848)`, a blue-tinted gray). The primary "Continue" is near-white. Hand this to the brand agent; noting it because the colours are specific to this screen.
- "Skip for now" and "Use a password instead" are 23 px tall text buttons. That is fine for pointers, but they are small touch targets on mobile.

### Billing: Overview (`billing.tsx`, `overview/*`)

- F3 (units), 2c-1 and 2c-2.
- "as of 20:08" is formatted `en-GB` (`daemonUsage.ts:180`); every other date on the page is `en-US` ("Nov 19, 2026", "10/20/2026, 8:22:30 PM").
- The Coupon box sits in the "AI CREDIT" band but redeems machine hours too. The helper text admits this ("One box, either kind of code…"). Move it to the page header, or give it its own row.
- The redundant top line "$0.00 credit remaining" sits above the AI CREDIT band that says the same thing.

### Billing: Change plan

- B1 and B2.
- A row's only facts are "S1 · 2 GB RAM · 1 CPU · $25.00/mo"; see F1.
- "Payment opens on this page — you won't leave Reliant." Fine.

### Billing: Usage & invoices

- B4, B5, F3, 2c-4 and 2c-5.
- "Organization usage (select an organization first) is unavailable." showed even with one org fixtured, and is jargon.
- Section order: hosting consent → invoices → machine usage → observability → deployments. A machine-only user scrolls past a hosting agreement card before reaching their own usage.

### Billing: Reliant AI

- "AI spend (last 30 days) $0.00" sits beside "Spent this period: $18.40". That is two windows with no explanation (partly the fixture, but the layout invites it).

### Coupon form (`RedeemCouponForm.tsx`)

- The error message is passed through from the server, lower-case ("coupon not found or no longer valid"). The input gets no `aria-invalid` and no `aria-describedby`, and the error `<p>` has no `role="alert"` (measured), so a screen reader isn't told the code failed.
- Success for a compute coupon doesn't say how many hours were added (F2).
- In one test, the devtools `fill()` on the input left React state empty ("Enter a coupon code." after filling "bad"). A native input event worked, so I am not counting this as a product bug, but it is worth a check with real typing on Electron.

### Machines list / detail / resize / create (`machines.tsx`, `AddMachineSheet.tsx`)

- **F7 (M) Size names never appear in the list.** Resources shows only "4 GB RAM · 2 CPU", and users bought "M". The detail page does show "Size L". Add the tier name to the Resources cell.
- **F8 (M) The capacity-unavailable state is styled like routine progress.**
  - The operator's "Waiting for capacity: machines of this size are temporarily unavailable. Your **workspace** starts by itself as soon as capacity frees up." renders `text-muted-foreground` under a blue "Pending" (`machines.tsx:711`). That is identical to "Starting your machine".
  - The row offers "Make default" and "Suspend". It offers no "try a smaller size" or "cancel", and nothing tells the user whether they are being billed while waiting.
  - "Workspace" contradicts "machine" everywhere else; the fix is in control-plane `operators/workspace/provisioning_detail.go:122`.
- **F9 (M) Status colours.**
  - Suspended is amber `text-warning-ink` (measured rgb(246,168,35)) via `statusDotVariant.suspended: "paused"` (`machines.tsx:161-168`), but a deliberate suspend is not a warning.
  - A laptop that is simply offline is red: `disconnected: "error"`, measured rgb(248,129,129).
  - With the brand's "green is success only", the list should be neutral except for real failures.
- **F10 (M) Resize modal.**
  - It shows sizes with no prices at all.
  - It says "Includes 160.0 hours a month" (2c-6).
  - "Larger sizes need a bigger compute plan. See plans" leads to B1.
  - Measured: a suspended L on an M plan listed S1–L only, with L "Current".
- **F11 (L) Add machine.**
  - "ON YOUR PLAN" against "$69.00/mo upgrades your plan" is ambiguous: is that the new total or the delta? Say "Switch plan to L — $69/mo (now $39)".
  - The primary button is "Create cloud-m": it uses the auto-generated name `cloud-${size}` (`AddMachineSheet.tsx:258,316`, `addMachine.ts:91`). "Create M machine" reads better.
  - The payment step re-shows the whole size picker. S1/S2 flip from "ON YOUR PLAN" to "$25.00/mo".
- **F12 (L)** "Disk size unavailable" shows on every row when the daemon reports no disk. The catalog's own `daemonPricing.sizes[].storageGib` (30–300) could stand in as "50 GB disk".
- **Local vs cloud choice:** the order (free first) and the single list are good. The connect instructions under "Run Reliant on your own machine" stay expanded at the bottom of the Machines page even when machines exist ("You already have a machine connected. Open this if you are setting up another one."). Collapse them behind "Add machine".

### Mobile billing (`MobileBillingScreen.tsx`)

- Plans are listed by plan name with an allowed-size list ("Compute L / S1, S2, M, L"), and with no specs. "Switch plan" goes through the same `ComputeSubscriptionCheckout → MachineSaleConsent`, so it is assumed to hit B1. I did not click it through.
- The hours are in "weighted h" (F3).

---

## 5. Ranked recommendations

| # | Impact | Effort | Change | Files |
|---|---|---|---|---|
| 1 | H | M (frontend) + owner decision (backend) | **Unblock plan changes (B1).** Decide the upgrade model (proration-now upgrade RPC vs. next-period switch). Until then, replace the dead-end paragraph with an honest path: "Plan changes take effect at your next renewal on Nov 19" plus a button that records it, or "Contact us". Hide "upgrades your plan" / "See plans" links that lead nowhere. | `Billing/MachineSaleConsent.tsx:23`; `Billing/ComputeSubscriptionCheckout.tsx:181`; `Machines/AddMachineSheet.tsx:572`; `Settings/cloud/machines.tsx` (ResizeMachineModal "See plans"); control-plane `svcbilling/machine_acceptance.go` + new RPC in `proto/services/billing/v1/billing.proto` |
| 2 | H | M | **Rewrite the consent surfaces as product UI (B2).** One summary line: "M machine · $39/month · 160 hours included · 50 GB disk · $0.30/hour after that". A styled forge-ui Checkbox: "I agree to the [machine pricing terms]". The pricing version and terms hash go in a details link or `title`, never in the sentence. Delete the "These are proposed terms, not an approved live offer…" paragraph and the "route …" line. Map hold reasons to one customer sentence ("New cloud machines are briefly unavailable — use your own computer, or try again later") and log the raw reasons. Render a loading or error state for partial data, never the zero-value sentence. | `Billing/MachineSaleConsent.tsx:13-37`; `Billing/MachinePricingReadinessCard.tsx:42-57`; `Billing/OnboardingCheckout.tsx:136`; `Settings/cloud/billing.tsx:1230` (show the readiness card only when terms actually change next period); `usage/UsageBillingCard.tsx:53` |
| 3 | H | S | **Fix hosting price disclosure (B4).** Format rates as dollars in the public units: "$0.07 per vCPU-hour", "$0.012 per GiB-hour", "$0.25 per GiB-month" (multiply the GiB-hour rate by 730 or have the server send a month rate), "$0.15 per GB after 10 GB free each month", "$5 per extra static site per month (first site free)". Add "Hosting invoices with any usage have a $5 monthly minimum". Replace "no base fee" with "No base fee; $5 minimum in months you use it". Ideally the server sends `included`/`display` fields, so wording lives with pricing.yaml as it does for the landing's `pricing.public.json`. | `usage/UsageBillingPanel.tsx:49`; `usage/UsageBillingCard.tsx:41,50`; control-plane `svcbilling/usage_billing.go:51-60` |
| 4 | H | M | **Bring egress and static sites into hosting usage.** Add `egress_gib`, `static_egress_kib` and `static_site` to the attribution model and render them. Delete the stale "nothing measures them" rationale. | `usage/usageModel.ts:62-70,113`; `usage/DeployUsageSection.tsx`; `usage/useDeployUsage.ts` |
| 5 | H | S–M | **One hour unit everywhere (F3).** Show the plan's own-size hours: "62 of 160 M-hours used · ≈ 98 h left". Drop "weighted h", "equivalent h at plan size", the "PRICING VERSION" stat and the "display-only" disclaimer. If another size is running, show "≈ N h at L" as a secondary line. | `Billing/exactMachineUsage.ts:17-21`; `Settings/cloud/billing.tsx:1495-1545`; `overview/StatusLine.tsx`; `Mobile/MobileBillingScreen.tsx` |
| 6 | H | S | **Give the payment step a way out (B3).** On readiness error or hold, add "Use your own computer instead (free)", which writes `compute: local_daemon` and clears `computePlanId` through the existing Back semantics. | `Billing/MachineSaleConsent.tsx:18,33`; `OnboardingFlow/steps/CheckoutStep.tsx` |
| 7 | M | S | **Show overage and disk on every machine picker (F1).** Add a second detail line, "160 h included · then $0.30/h · 50 GB disk", from `daemonPricing.sizes[]`. Lead the spec with vCPU and say "dedicated": "2 vCPU · 4 GB · dedicated". Prices in whole dollars when integral ("$39/mo"), matching the landing page. | `Billing/PlanTiles.tsx:150-200`; `Billing/machineOptions.ts:77-90`; `Billing/machineSpecs.ts:77-81`; `Settings/cloud/billingUtils.ts:522` (add `formatPrice` without the trailing `.00`) |
| 8 | M | S | **Coupon clarity (F2).** Success: "Added 100 hours on an M machine. One-time, used after any plan hours." (from `computeMinutes`). Coverage note: "Your code covers 100 hours on the M machine. Other sizes need a plan." Don't show "Includes 160 hours a month" to a coupon-only user. Fix "covers this in full" in all four places. Add `aria-invalid`, `aria-describedby` and `role="alert"` to the error. | `RedeemCouponForm.tsx:105-120` and the error `<p>`; `OnboardingFlow/steps/ComputeStep.tsx:748,784-787`; `ModelStep.tsx:543`; `WalletTopupCheckout.tsx:201`; `ComputeSubscriptionCheckout.tsx:223`; `OnboardingCheckout.tsx:294` |
| 9 | M | S | **Size labels, not raw wire names.** `Runs S1, S2, M`, and overage as "S1 $0.20 · S2 $0.25 · M $0.30 per hour", listing only sizes the plan can run. Consent "L:" via `formatSizeLabel`. Plan names "M" rather than "Compute M" in checkout and mobile. "160 hours", not "160.0". | `overview/ComputeBand.tsx:171`; `ComputeOverageControl.tsx:135`; `MachineSaleConsent.tsx:32`; `CheckoutStep.tsx:284`; `Mobile/MobileBillingScreen.tsx:327,412`; `machines.tsx:1234` / `daemonUsage.ts:planOwnSizeHours` |
| 10 | M | S | **Capacity state as a first-class state (F8).** A distinct "Waiting for capacity" status (amber dot, info callout) explaining whether time is billed. Actions: "Choose a smaller size" (resize) and "Cancel". Change the operator string to "machine". | `Settings/cloud/machines.tsx:700-723`; `lib/machineWake.ts`; control-plane `internal/operators/workspace/provisioning_detail.go:122` |
| 11 | M | S | **Neutral machine status colours (F9).** Suspended uses a neutral or muted dot, disconnected a neutral dot with "Offline since …". Keep red for `failed`. Add the size name to Resources ("M · 4 GB · 2 vCPU") (F7). | `Settings/cloud/machines.tsx:161-168`, Resources column ~725-760 |
| 12 | M | S | **Remove engineering notes from observability billing (B5).** Delete the "Resend / not live-tested" sentence. Reword "A Stripe subscription is required for overage" to "Add a payment method to allow overage". Human dates ("Nov 1, 8:07 PM", "Nov 1 – Nov 30"). Drop the self-link. | `usage/ObservabilityLimits.tsx:91,112`; `usage/observabilityBillingModel.ts:7,12`; `Forge/Observability/UsageView.tsx:86,88` |
| 13 | M | S | **Onboarding CTA contrast and brand (F4).** Replace `bg-sky-600` with the brand button fill #0a70e0 via a token (≈ 4.8:1 with white), or the app's `Button` primary. Drop the progress gradients for a flat brand-blue current step and a neutral connector. "Free" in `text-foreground`. Coordinate with the brand agent. | `OnboardingFlow/steps/ComputeStep.tsx:645,959-962`; `OnboardingFlow/ProgressBar.tsx:28,49` |
| 14 | M | S | **Copy fixes.** Replace "We need this to intercept your credentials" with "Reliant's sign-in helper needs to be running to finish connecting your Claude account". "Larger machines need a monthly plan" becomes "Other sizes need a monthly plan". Replace "The paid machines are ours…" with "We host it — nothing to install. It starts at the end of setup." "Enforcement state unavailable… treat your services as potentially restricted" becomes neutral copy, or hide it for orgs with no hosting. | `OAuthHelperPanel.tsx:230`; `ComputeStep.tsx:745,785`; `usage/EnforcementPanel.tsx:69-71` |
| 15 | L | S | **Resize modal prices (F10).** Show "$NN/mo · $X/h" per size, and the plan delta when the size needs a plan change. | `Settings/cloud/machines.tsx` ResizeMachineModal (~1181-1280) |
| 16 | L | S | **Add machine wording (F11).** "Create M machine"; "Switch to L plan — $69/mo (you pay $39 now)". | `Machines/AddMachineSheet.tsx:258,316,564,572` |
| 17 | L | S | **Sign-in brand line.** Add "Build it. Ship it. Run it." under the wordmark, and normalise the provider button fills to tokens. Brand-agent territory, specific to this screen. | `AuthScreen.tsx`; `OAuthButton.tsx:23,29,31` |
| 18 | L | S | **Clean up stale comments and dead burst code** (2c, comments list). | the files listed in 2c |

---

## 6. What's working (keep)

- No client price table. Every price on screen came from the server, and with pricing.yaml correct, every machine, storage and observability figure rendered correctly (2b).
- Onboarding's single machine list, with "Use your own computer · Free" as a peer row, and the sticky footer CTA. The coupon "Covered" marking only the size the server admits (M). Prices shown on the decision step rather than the payment step.
- `deriveStep` routing that only asks for money when something is owed. The coupon-covered user skipped the machine charge and paid only for the AI credit they chose.
- The machine overage-cap control (stop at included hours / capped / uncapped) is clear, and its "Not recommended" warning is the right tone.
- Per-machine "$0.30/hr past included hours" in the Machines list is exactly the overage disclosure the pickers are missing.
