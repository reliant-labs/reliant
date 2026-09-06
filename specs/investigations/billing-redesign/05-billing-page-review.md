# 05 — Adversarial review of the rebuilt billing page

**Reviewer:** independent agent (did not write any of the code under review)
**Date:** 2026-09-06
**Scope:** `web/src/components/Settings/cloud/billing.tsx`, `overview/{CreditBand,ComputeBand,StatusLine}.tsx`,
`ComputeOverageControl.tsx`, `billingUtils.ts`, `reliantAI.tsx`,
`components/Mobile/MobileBillingScreen.tsx`, `components/Billing/**`, plus the
control-plane RPCs that feed them.
**Tests run:** `npx vitest run src/components/Settings/cloud/__tests__/ src/components/Billing/__tests__/`
→ **17 files, 170 tests, all passing.** Every finding below is against *passing* code.

---

## Verdict up front

**The monotone complaint is genuinely fixed at the structural level, and not yet
fixed at the perceptual level.** The two products are now different *shapes*
built from different components with different primary numbers — that is real,
verifiable, and a large improvement over two identical `<Card>`s. But both bands
are still drawn in the same greys at nearly the same size, so the "hard to see
things" half of the owner's critique is only partly addressed.

**Two findings are severity-1 and neither is a taste call: the page renders two
prominent numbers that the server cannot produce.** The runway estimate is
mathematically unreachable in production, and the compute usage bar is fed by a
hardcoded stub. Both are proven below with code references and, for the runway,
a test I wrote and ran.

The single most valuable change is at the end (§9).

---

## Severity 1 — wrong, user-visible, proven

### F1. The runway estimate can never render in production. It is dead code behind a passing test.

`CreditBand` renders "~N days at recent use" — the headline new feature of the
credit band — from `estimateCreditRunwayDays(...)`, which requires
`spendSampleDays(entries) >= 3` (`billingUtils.ts:326,364`). `spendSampleDays`
counts distinct days by reading `entry.periodStart` (`billingUtils.ts:338-351`).

**The server never sets that field.** The wire type has it
(`shared_pb.ts` `LLMSpendEntry.periodStart`), but the producing code does not:

- `control-plane/internal/llm/service.go:32-38` — `SpendEntry` is
  `{KeyID, KeyName, Model, Spend, Requests}`. **There is no period field on the
  struct at all.**
- `control-plane/internal/handlers/llm_gateway/handlers.go:226-234` —
  `spendEntryToProto` copies exactly those five fields. `PeriodStart` is never
  assigned.
- `control-plane/internal/llm/service.go:718-745` — entries are aggregated per
  `(key, model)` across the **whole** date range, so there is no per-day row for
  a period to be attached to even in principle.

So in production `spendSampleDays` returns `0` for every response, `0 < 3`, and
`estimateCreditRunwayDays` returns `null` unconditionally. `CreditBand.tsx:127`
then renders nothing.

**Proof.** I wrote a throwaway test using the payload shape
`spendEntryToProto` actually emits (run in `web/`, then deleted — not added to
the repo):

```ts
const realWireEntries = [
  { keyId: "k1", keyName: "default", model: "claude-sonnet-4", spend: 40, requests: 900n },
  { keyId: "k1", keyName: "default", model: "gpt-4o",          spend: 20, requests: 300n },
];
expect(spendSampleDays(realWireEntries)).toBe(0);                    // ✓ passes
// $50 balance, $60 spent over 30 days — should read "~25 days":
expect(estimateCreditRunwayDays(50n * 1_000_000_000n, 60, 0)).toBeNull(); // ✓ passes
```

All three assertions passed, confirming the feature is unreachable.

**Why the existing test does not catch this.** `billing.bands.test.tsx:276-292`
builds its fixture with `spendOver(6, 12)`, which synthesizes a `periodStart`
**per entry, one per day** — a shape the server never produces. This is the same
class of defect the sibling agent already found once today ("my test passed while
both products were drawn identically"): the test pins the component's contract
with its props, and nothing pins the props to the wire format.

**Impact.** Not a crash — the band degrades to balance-only, which is the honest
fallback. But the most-advertised new feature of the redesign does not exist for
any user, and the spec (`03-...md:667`) lists it as delivered. Either the server
must emit per-day entries, or the runway must be computed from a source that has
day granularity, or the feature should be removed rather than shipped as
invisible code with tests that imply it works.

---

### F2. The compute band's usage bar, hours, and overage figures are all fed by a hardcoded stub — the page presents "0.0 h used" as measured fact.

`control-plane/internal/billing/svcbilling/service.go:2545-2553`,
`GetCurrentUserComputeUsage`, returns:

```go
UsedMinutes:               0,
OverageMinutes:            0,
EstimatedOverageCostCents: 0,
ByWorkspace:               []*billingv1.ComputeUsageByWorkspace{},
ByDay:                     []*billingv1.ComputeUsageByDay{},
```

with the comment at `:2480-2481`: *"TODO: aggregate real daemon usage once the
daemon usage table is available. This implementation currently returns zero
usage with an empty breakdown."* The log line at `:2544` literally reads
`"compute usage requested (stub)"`.

`includedMinutes` **is** real (read from plan limits at `:2506-2512`), and
`grantedMinutesRemaining` **is** real (`:2537-2542`). Everything else is zero.

This is the exact defect the redesign was built to eliminate, one field over.
The team correctly identified that `0 h / mo` for *included* hours was
"an unknown rendered as a known" and built `isPlanDetailUnavailable` to guard it
(`billingUtils.ts:302`). But `usedMinutes` gets no such guard — a stubbed zero
flows straight through `deriveComputeCapacity` into a rendered bar:

- `billing.tsx:530` — `usedMinutes = usage?.usedMinutes ?? 0`
- `billing.tsx:547` — `usedHoursLabel: "0.0 h"`
- `ComputeBand.tsx:250-252` — renders **"0.0 h used"** in `font-medium
  text-foreground`, the band's most emphatic treatment
- `StatusLine` (`billing.tsx:585-588`) hoists it to the top of the page:
  **"Compute Small · 0.0 h of 33 h included · $18.40 credit remaining"**

A user who has been running machines all month sees a confident, prominent
"0.0 h used" and a bar pinned at empty. There is no "usage unavailable" state,
because the query *succeeds* — `usageUnavailable` is only true on
`usageQ.error` (`billing.tsx:527`), and a stub returns 200.

**This is worse than the original complaint.** `0 h / mo` was alarming;
"0.0 h used" is *reassuring* and wrong in the direction that costs money — it
tells a user they have consumed nothing when the overage gate in
`svcdaemon/service.go:1425-1432` is meanwhile enforcing against real metered
usage. The UI and the enforcement path disagree.

**Note the asymmetry that makes this dangerous:** the *enforcement* logic
(`svcdaemon/service.go:1396-1434`) is fully implemented and will genuinely
refuse to start machines when included minutes are spent. So a user can be
blocked by a limit the billing page insists they are nowhere near.

---

## Severity 2 — misleading or unreachable in a way that will cost trust

### F3. "Previous" period is a control that does nothing.

`UsagePanel` offers a Current/Previous toggle (`billing.tsx:1292-1307`) and the
hook keys a separate query on it (`useCloudBillingQueries.ts:122-129`). The
server ignores it: `service.go:2482-2483` is
`func (s *Service) GetCurrentUserComputeUsage(ctx, period string)` whose first
statement is `_ = period`, with the doc comment *"The period argument is
reserved for future use and currently ignored (we always return the current
month)."*

So clicking "Previous" swaps to a different cache key, refetches, and renders
identical numbers. A user comparing months concludes their usage was identical,
or that the page is broken. Given F2 both are zero anyway, but this control will
still be wrong after F2 is fixed.

### F4. The overage cap caveat is honest, but the *place* it appears means the people who most need it never read it.

Credit where due: `ComputeOverageControl.tsx:251-255` is genuinely good and rare:

> "This limit stops new machines from starting. A machine that's already running
> keeps running until it goes idle, so your final bill can pass the limit by the
> cost of finishing what's in flight."

That matches the server exactly (`svcdaemon/service.go:1425-1432` checks at
start, nothing stops a running machine). **This is the best copy on the page.**

The problem is placement. It is rendered *inside* the "capped" radio option
(`:243-256`) as `text-xs text-muted-foreground` — so it is only visible once the
user has already selected "Allow extra time, up to a monthly limit". The
**uncapped** option (`:258-265`) says only *"Not recommended… charged at
$X/min with no ceiling"* — no mention that nothing will stop a running machine.
And the caveat is at the *smallest* type size on a control that authorizes
unbounded spend.

A user who picks "no limit" — the genuinely dangerous option — never sees the
sentence explaining how overruns actually behave.

### F5. The identity-modal / anonymous-purchase claim holds, but mobile has no way to add credit at all.

Verified good: every purchase routes through `CheckoutPanelWithIdentity`, and
`assertPurchaseIdentity` sits in the mutation, not the band
(`CheckoutPanelWithIdentity.tsx:19-21`). Bands take resolved props and call no
mutations. That claim is true and well-constructed.

But `MobileBillingScreen.tsx` contains **no add-credit path whatsoever** — I
grepped for `Add credit`, `RedeemCoupon`, `topup`, `wallet_topup`: zero hits. The
phone shows the AI credit balance and the "Your wallet is empty. New AI requests
can fail until you add credits" warning (`:249-257`) — and then offers no way to
add credits, and no coupon box. The footer says *"For invoices, usage charts, and
plan comparisons, use desktop"* (`:364-366`), which does not mention credit.

So mobile tells a user their AI will start failing, and gives them nothing to do
about it, without even naming the workaround. The dead-end that was fixed for
plan purchase still exists for the wallet.

---

## Severity 3 — real but lower impact

### F6. `role="tab"` without `tabpanel`, `aria-controls`, or arrow-key navigation.

`billing.tsx:196-217` declares `role="tablist"` and `role="tab"` with
`aria-selected`, but:

- no element carries `role="tabpanel"` (grep: zero hits in the file)
- no `aria-controls` linking tab → panel
- no `tabIndex`/roving-focus or arrow-key handler

Declaring the ARIA tab pattern and omitting its keyboard contract is worse than
using plain buttons: a screen-reader user is told "tab 1 of 3" and that arrow
keys will work, and they do not. Either complete the pattern or drop to nav
buttons.

### F7. Compute capacity state is conveyed by colour alone.

`ComputeBand.tsx:269` switches `bg-primary` → `bg-warning` at 80% of included
hours (`NEAR_CEILING_FRACTION`, `billingUtils.ts:397`), and the overage segment
is `bg-destructive` (`:280`). Nothing in text, `aria-valuetext`, or iconography
reflects the `near`/`spent`/`overage` distinction — the `progressbar` at `:262`
exposes only `aria-valuenow`. The static labels beneath read "included" /
"overage" regardless of state (`:286-292`).

So "you are about to exhaust your hours" is communicated purely as a hue change,
invisible to a screen reader and to a red-green colourblind user. The credit band
has the same issue at `CreditBand.tsx:119` (`warning ? "bg-warning" :
"bg-primary"`), though there the warning *text* box at `:136-141` carries it
properly — which is the pattern compute should copy.

### F8. Vocabulary: eleven terms for two products.

Counted across surfaces, for what is fundamentally two things:

| For AI money | Where |
|---|---|
| "AI credit" | `CreditBand.tsx:83`, `MobileBillingScreen.tsx:239` |
| "credit remaining" | `CreditBand.tsx:95` |
| "Add credit" | `CreditBand.tsx:154` |
| "Credit balance" | `reliantAI.tsx:322` |
| "wallet" / "Your wallet is empty" | `billingUtils.ts:75` |
| "top-up" | code + `EmbeddedCheckoutPanel` |

The user-facing drift that matters: **`getWalletWarning` is the only place the
word "wallet" is shown to users** (`billingUtils.ts:75,80`) — "Your wallet is
empty", "Your remaining credits are running low" — and it appears inside a band
titled "AI credit", on a page that otherwise never says wallet. Two names for
one thing, in adjacent lines. `reliantAI.tsx:322` adds a third framing
("Credit balance").

| For compute time | Where |
|---|---|
| "included hours" | `ComputeBand.tsx:255` |
| "machine minutes" | `CreditBand.tsx:183` |
| "Coupon minutes" | `ComputeBand.tsx:171` |
| "extra time" | `ComputeOverageControl.tsx:194,294` |
| "overage" | bar labels, plan cards |
| "limit" / "cap" / "budget" | control, code, server |

The `ComputeOverageControl` deserves specific credit for choosing "extra time"
over "overage" in the *options* (`:202,210,264`) — that is the right instinct for
a novice. But it then labels its own save button "Save extra-time settings"
(`:294`) while the section header two lines up says "Beyond your included hours"
(`:183`) and the bar beneath says "overage" (`:291`), so a user sees three names
for one concept within a single card.

Worst single instance: `CreditBand.tsx:181-186`. The coupon helper text, inside
the **AI credit** band, explains machine-minute behaviour:

> "a coupon can add account credit or machine minutes… Machine minutes show up
> under Compute, and are spent after your plan's included hours."

That is correct and well-intentioned, but it introduces "account credit",
"machine minutes", and "included hours" in one sentence inside the band that owns
none of those concepts.

### F9. `renderOverageControl`'s `disabled`/`reason` API has no caller that uses it.

`ComputeBand.tsx:49` types the slot as
`(args: { disabled: boolean; reason?: string })`, and `billing.tsx:713-722`
threads both into `ComputeOverageControl`. But the only call site is
`:213`, `renderOverageControl({ disabled: false })` — hardcoded, `reason`
never passed. Both degraded branches *replace* the control instead (`:193-211`),
which is the better design. The disabled path is unreachable, so
`ComputeOverageControl`'s `disabled`/`disabledReason` props and the
`disabled:opacity-60` fieldset styling are dead. Not a bug; it is speculative API
surface that a future reader will assume is exercised.

---

## Answers to the specific questions asked

### 1. Is the monotone problem actually fixed?

**Structurally yes; perceptually about half.**

The differentiation is real and is *not* just a `data-band-shape` attribute. The
attribute is declarative documentation, but the components genuinely diverge:

| | Credit | Compute |
|---|---|---|
| container | `rounded-2xl bg-muted/40`, **no border** (`CreditBand.tsx:75`) | `rounded-lg border border-border bg-card` (`ComputeBand.tsx:74`) |
| structure | one flow | header row + `border-b` + sectioned body |
| primary number | `text-4xl` dollars (`:89`) | `text-2xl` plan name (`ComputeBand.tsx:117`) |
| bar | one depleting meter | two segments + drawn boundary (`:258-284`) |
| time | backward (burn rate) | forward (renews on date) |

A sibling agent's earlier failure mode — both products drawn identically while
the test passed — is genuinely not present here. I checked.

**What is still monotone.** Everything on the page is `bg-muted/40`,
`bg-card`, `border-border`, `text-muted-foreground`. The two bands differ in
*shape* but sit at nearly identical visual weight in nearly identical greys, at
`gap-8` in a plain vertical stack (`billing.tsx:652`). At a squint — which is how
the owner looked at it — you still see two grey rectangles. The `text-4xl` vs
`text-2xl` difference is the main working signal, and it is doing all the load.

Additionally, three of the four section headings are the same treatment —
`text-xs font-semibold uppercase tracking-wide text-muted-foreground` appears in
`CreditBand.tsx:81`, `ComputeBand.tsx:81`, and `SectionHeading`
(`billing.tsx:405`) — so "AI credit", "Compute", and "Billing account" are
typographically indistinguishable despite the last being explicitly intended as
*quieter* (`billing.tsx:739-741`).

**Is there a clear primary action?** No — and this is the sharper remaining
problem. The Overview presents, at comparable weight: four top-up preset buttons
(`CreditBand.tsx:155-170`), an always-open coupon form (`:180`), "Change plan"
(`ComputeBand.tsx:89-96`), three overage radios plus a save button
(`ComputeOverageControl.tsx:196-296`), "See usage and invoices"
(`billing.tsx:729-735`), "Change"/"Set" billing email (`:812`), and "Manage
payment method in Stripe" (`:749-759`). That is **eight** competing actions with
no visual hierarchy among them. The only `variant="primary"` filled button in
either band is a top-up preset, and only while a checkout is in flight
(`CreditBand.tsx:159`) — so in the resting state *nothing* on the page is styled
as the primary action.

The owner said "I don't just want to have adding credit be the way to go." The
page has answered by giving compute real verbs — correct — but has not
established what the *page's* primary action is in any given state.

### 2. Is the vocabulary coherent?

No — see F8. Eleven distinct terms; the sharpest issue is "wallet" appearing only
in the warning strings inside a band called "AI credit", and the coupon helper in
the credit band explaining machine-minute semantics.

Cross-surface, the *numbers* are genuinely consistent (mobile and desktop share
`billingUtils` formatters, `MobileBillingScreen.tsx:37-51`) — that claim holds
and is good. It is the *nouns* that drift.

### 3. Is anything misleading or scary?

- **F2 is the big one** — "0.0 h used" from a stub, presented as measurement, in
  the page's most prominent line.
- **F1** — a runway that silently never appears.
- **F4** — the honest cap caveat is hidden from the users who pick the risky option.
- The runway copy itself, *if it worked*, is well hedged: "~9 days at recent use"
  with a `~`, a named basis, a 90-day cap (`billingUtils.ts:375`), and suppression
  below a 3-day sample. That reasoning is sound.
- `EstimatedOverageCostCents` is labelled "Estimated overage"
  (`billing.tsx:1330`) and "so far" (`ComputeBand.tsx:290`) — appropriately
  hedged, though currently always `$0.00` per F2.

### 4. Degraded and empty states

**Genuinely improved and reachable.** The `unpricedPlanCount` split
(`billing.tsx:938-950`) correctly distinguishes "catalog serves unpriced plans"
from "no plans" — this directly addresses the owner's "plans is empty in dev"
and names the real cause (control plane not restarted since the catalog changed).
`isPlanDetailUnavailable` requires all three facts absent together
(`billingUtils.ts:302-310`), which correctly keeps a genuine zero-hour plan out
of the degraded branch. Partial failure degrades partially — "Change plan" stays
live when usage fails (`ComputeBand.tsx:86-96`). These are good.

**States that still render questionably:**

- **The free trial plan is unbuyable and uncounted.** `plan_compute_free`
  (`plans.yaml:88-103`) has `stripe_price_id: null` and no `price_cents`, so
  `isPurchasableComputePlan` (`priceCents > 0n`) filters it out of the grid — but
  it *is* `productId: prod_compute`, so `isComputePlan` counts it in
  `unpricedPlanCount` (`billing.tsx:886-888`). In an environment with only the
  free plan seeded, the user gets "Plan pricing unavailable — the server returned
  1 compute plan with no price… Restart the control plane", when nothing is
  wrong: that plan is *intentionally* unpriced. The alarming message the redesign
  set out to eliminate can still fire on a correct catalog.
- **`includedMinutes === 0` with no overage** returns `state: "spent"` with
  `usedPct: 0` (`billingUtils.ts:420-425`) — a bar drawn empty while labelled as
  fully spent.
- **Unlimited plans** are handled (`:417-419` guards the `-1` sentinel;
  `ComputeBand.tsx:238-244` renders prose) — good.

### 5. Mobile

**Much better than "auto-picked the cheapest plan", but not finished.** It now
lists every purchasable plan with price, sizes and hours on each row
(`MobileBillingScreen.tsx:314-349`), uses 44px touch targets, mounts checkout in
place, and carries the same two-shape distinction (`:98-103`). The anonymous
dead-end is fixed via `CheckoutPanelWithIdentity` (`:217-225`). It is not a
squeezed desktop layout — it is a deliberately reduced surface.

**But:** no add-credit path at all (F5), while still showing the empty-wallet
warning; no coupon redemption; no overage control (arguably fine); and the
"unlimited" label reads lowercase mid-sentence (`:289`) where desktop capitalises.

### 6. The unanswered half — recurring AI billing

**There is a coherent place for it, and the seam is real.** The spec chose
auto-recharge over a credit subscription (`03-...md:308`, §4.2) with sound
reasoning, and `CreditBand`'s action group (`:143-171`) is exactly where a "or
recharge automatically when I drop below $X" row belongs — same band, same
`border-t` group, directly under the presets. The band takes resolved props and
owns no business logic, so adding it requires no restructuring.

**The risk is not structural, it is the eighth-action problem.** Dropping
auto-recharge into a band that already holds four presets, a checkout slot, and
an always-open coupon form will make the credit band the most crowded thing on
the page — it will *read* as bolted on even though the seam is clean. The
always-open coupon form (`:180`, `variant="open"`) is the weakest claim on that
space: it is deliberately open so it can be found, but it makes redemption
visually equal to purchasing, and it is what auto-recharge should displace.

### 7. Accessibility

Good: both bands are `<section aria-labelledby>` with real headings; `role="meter"`
is the correct choice over `progressbar` for a level (`CreditBand.tsx:105-113`);
the top-up presets are a labelled `role="group"` so "$25" is not announced bare
(`:147-151`); `SizePicker` is a real `radiogroup` (`billing.tsx:1115-1119`); the
overage control is a `fieldset` with an `sr-only` legend, a real `<label
htmlFor>`, `aria-invalid`, and `aria-describedby` pointing at the caveat
(`ComputeOverageControl.tsx:190-255`). That is above-average work.

Gaps: F6 (incomplete tab pattern), F7 (colour-only state), and the segmented
capacity bar exposes only the *included* segment to AT — the overage segment
(`ComputeBand.tsx:278-283`) has no role or label at all, so a screen-reader user
is told "Included hours used, 100" and never learns an overage segment exists.

---

## What is genuinely good

Not padding — these are things I tried to break and could not:

1. **The band split is real**, not an attribute. Different components, containers,
   primary numbers, bar semantics, and time axes.
2. **`COMPUTE_PLAN_UNPRICED = null` vs `0`** (`billingUtils.ts:106`) and the
   refusal to render a price for an unpriced plan. A withheld price is
   recoverable; a wrong one next to a pay button is not.
3. **Deleting the hardcoded price/overage/allowlist tables**, with
   `billingUtils.price.test.ts` reading the source to fail on their return. Pinning
   the *shape of the defect* rather than the values is the right test.
4. **The overage caveat sentence** (`ComputeOverageControl.tsx:251-255`) — it
   correctly describes server behaviour and refuses to promise a hard ceiling.
   Rare honesty; my only complaint is placement.
5. **One submit for permission + ceiling** (`:146-157`), and the refusal to
   submit from an effect (`:114-121`). Rejecting a `0` cap in the form rather
   than sending it, because the server would read it back as uncapped
   (`:136-139`), is a genuinely subtle catch.
6. **`unpricedPlanCount`** turning "no plans configured" into a diagnosis — the
   owner's specific complaint, addressed at the cause.
7. **Coupon minutes shown outside the plan branch** (`ComputeBand.tsx:164-181`),
   so someone who redeemed before subscribing can still see them.
8. **`?tab=` in the URL with `invoices` still accepted inbound**
   (`billing.tsx:146-148`) — preserves existing links.
9. **The checkout banner refuses to claim success until the server confirms**
   (`:236-241`), with a bounded 60s poll.

---

## §9 — The single change that would most improve clarity

**Stop rendering `usedMinutes` as a measured value while the server returns a
stub (F2) — and make the "we don't know" state visible rather than resolving it
to a confident zero.**

Concretely: the page needs the same treatment for *usage* that
`isPlanDetailUnavailable` already gives *plan detail*. Right now
`usageUnavailable` is only true when the query **errors** (`billing.tsx:527`),
and a stub returns 200, so the unknown resolves to `0.0 h` and is promoted into
the page's most prominent line via `StatusLine`.

This is the highest-value change for three reasons:

1. It is the same defect class the redesign was built to fix (`0 h / mo`), so
   fixing it completes the work rather than adding to it.
2. It is the only finding where the UI actively contradicts the enforcement path
   — a user can be refused a machine by `svcdaemon/service.go:1425` while the
   billing page shows them at 0% of their allowance. That is the "surprised by a
   bill" outcome, arriving as "surprised by a refusal".
3. It removes a false number from the top of the page, which *also* helps the
   monotone problem: `StatusLine` currently spends the most prominent position on
   the page saying something untrue.

The right fix is server-side (aggregate real daemon usage — the TODO at
`service.go:2480`). If that cannot land soon, the interim must be an explicit
unknown — a `usage_available` signal on the response, or suppression of the bar
and the used-hours label — **not** a zero.

Immediately after that, in order: **F1** (make the runway reachable or delete it —
do not ship an invisible feature with tests implying it works), then **F4** (move
the cap caveat out of the capped-only branch so it is visible on the uncapped
option too).

For the residual monotone complaint specifically: give the Overview **one**
primary action at any moment — the filled `variant="primary"` button, currently
unused in the resting state — chosen by state (empty balance → *Add credit*; no
plan → *Choose a plan*; near ceiling → *Set a limit*). Eight equal-weight
controls is what "hard to see things" feels like once the shapes are already
different.
