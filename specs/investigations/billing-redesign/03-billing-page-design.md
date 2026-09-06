# 03 — Billing page design: two products, two shapes

**Status:** design only. No product code was written or edited producing this
document. Read-only git; no database was mutated.

**Inherits** `01-current-state-inventory.md` (verified current state) and
`02-target-flow-design.md` (embedded checkout, the intent→commit rule, the
purchase surface). Neither is re-derived. Where this document disagrees with
`02` it says so explicitly — there is one such place, §4.1.

**Scope:** the owner's critique of the billing *page*. Three asks —
distinguish AI from compute, allow recurring billing for AI, allow and cap
compute overage — plus the empty/degraded states that made a working
environment look broken.

---

## 0. The answer in one paragraph

> The page is monotone because it renders a **wallet** and a **subscription**
> in the same card, at the same weight, with the same border — and those are
> not the same kind of thing. A wallet is a *quantity you deplete*; a
> subscription is a *capacity you rent*. Everything else in the critique
> follows from that one confusion: "add credits" is the only affordance
> because a wallet is all the page really knows how to model, and overage has
> no ceiling control because a rented capacity with no ceiling was never
> designed as a capacity at all. The fix is to give the two products different
> *shapes*, not different colors: **Credit is a meter that drains and refills**
> (balance, burn rate, refill rule); **Compute is a capacity with a ceiling**
> (what you rent, how much of it you've used, how far past it you'll allow
> yourself to go, and what happens at the edge). Both live under one Overview,
> in two visually distinct bands, with the tab strip reduced from four tabs to
> three. The recurring ask is **auto-recharge on the credit meter** — the
> smaller of the two recurring options and the one that matches how the
> product actually bills. The overage ask is a **spend cap**, and the good news
> is that the enforcement for it already exists in the backend and is
> unreachable from any RPC.

---

## 1. Three findings that were not in `01` or `02`

I re-read the backend for the two features the owner asked for. Two of the
three findings change the design; the third changes the cost estimate.

### 1.1 The overage spend cap ALREADY EXISTS server-side and has no setter

This is the most useful thing in this document. `subscriptions.budget_cents`
is a real, migrated column (`control-plane/db/migrations/00036_add_subscription_budget_cents.up.sql`),
carried on the model (`internal/db/models.go:442`), read and written by the
repository (`internal/db/postgres.go:2434,3051`), seeded in fixtures
(`db/seeds/0006_subscriptions.sql`) — **and enforced**, in the funding gate:

```go
// internal/svcdaemon/service.go:1425-1433
if sub.BudgetCents != nil && *sub.BudgetCents > 0 {
    overageMinutes  := usedMinutes - includedMinutes
    overageCostCents := int64(float64(overageMinutes) * limits.DaemonOveragePerMinuteCents)
    if overageCostCents >= *sub.BudgetCents {
        metrics.PlanLimitRejections.WithLabelValues("compute_budget_exceeded").Inc()
        return daemonDenied(connect.CodeResourceExhausted, ReasonComputeBudgetExhausted,
            fmt.Errorf("compute overage budget cap reached — …"))
    }
}
```

**What is missing is only the setter and the readout.** I grepped
`internal/billing/`, `internal/handlers/` and `proto/` for `budget_cents` and
`SetSubscriptionBudget`: **zero hits.** There is no RPC that writes it, no
field on the `Subscription` proto message that reads it back
(`proto/controlplane/v1/shared.proto:318-330` stops at `overage_enabled = 11`),
and therefore no way for a user to ever set a value other than NULL. The
enforcement is live code guarding a column nothing can populate.

So the owner's "we might want to set limits for those overages" is **not** a
feature to design from scratch. It is a proto field, a setter RPC, a
repository method next to the one that already exists for
`SetSubscriptionOverageEnabled` (`internal/billing/svcbilling/repo.go:35`),
and the UI in §5. That is a materially smaller piece of work than I expected
to be writing up, and it is the piece with the highest ratio of user-visible
safety to engineering cost in this whole document.

### 1.2 The cap gates STARTING a machine — it does not stop a RUNNING one

This is the finding that decides the hardest question in §5, "what actually
happens to a running machine at the cap?", and the answer today is: **nothing.**

`checkDaemonSizeAllowed` — where the budget check lives — is called from
exactly two places: `CreateDaemon` (`internal/svcdaemon/service.go:258`) and
`ResumeDaemon` (`:727`). Both are user-initiated transitions into a running
pod. Neither runs while a machine is already up.

The one thing that *does* sweep running machines is
`BillingEnforcementService` (`internal/svcdaemon/billing_enforcement.go`),
which ticks every minute (`enforcementInterval = 1 * time.Minute`), groups
active managed daemons by owner, and suspends them when
`checker.Check(…, ActionTickSweep).SuspendNow` is true. But read what that
checker actually decides on (`internal/enforcement/service.go:207-260`): it is
the **global free-tier spend cap** (`FreeTierGlobalCapCents`), an
operator-owned pool, gated further by `SuspendOnOverage`. It never reads
`sub.BudgetCents`. A paid user's own overage cap is invisible to the only
component that can stop a running machine.

**Consequence, stated plainly, because it is a real hole and not a design
preference:** a user who sets a $20 overage cap, starts a machine at $19.90 of
overage, and leaves it running over a weekend is billed for the whole weekend.
The cap stopped them from starting a *new* machine and did nothing else. A
"limit" that behaves this way is worse than no limit, because the user
believes they are protected. §5.4 designs the honest version and names the
backend work; §5.5 is explicit that shipping the control without that work is
the one thing not to do.

### 1.3 There is genuinely no saved-payment-method model — confirmed, with one nuance

Confirmed as briefed. `rg -i 'setupintent|payment_method|off_session'` across
control-plane returns **only** `internal/billing/client.go:154` and its test —
and those are a deliberate *negative*: a comment and a
`TestCreateCheckoutSession_LeavesPaymentMethodsDynamic` that asserts we never
send `payment_method_types`, so Stripe's dynamic selection stays on. That test
is a constraint on §4 and I treat it as one: any design that pins a method
list breaks a test written on purpose.

The nuance that lowers the cost of §4: **a compute subscriber already has a
saved card at Stripe.** `mode: subscription` necessarily stores a payment
method on the Customer for the recurring charge. So "there is no saved card"
is true of the *wallet* path and true of our *data model*, but not of Stripe's
records for anyone on a compute plan. That is why §4 recommends what it does.

---

## 2. Why the page reads as monotone — the diagnosis before the fix

The screenshot shows BALANCE (credit + "Add credits: $10 $25 $50 $100" +
Redeem-a-coupon) then PLAN (Compute Small), every card the same dark grey,
same weight, same border, same corner radius. Four observations, in the order
they matter:

**1. Identical containers assert identical kinds.** A card is a visual claim
that its contents are a peer of the other cards. Credit balance, coupon
redemption, and compute plan are rendered as three peers; they are a
*quantity*, an *action*, and a *contract*. The reader has to do the sorting
that the layout should have done.

**2. The two products have the same information shape, so nothing
distinguishes them.** Both render as `<big number> + <label> + <row of
controls>`. Look at the code and they are literally parallel: `walletUi` and
`planUi` are both `useMemo` blocks producing a display string and some
sub-values (`billing.tsx:481-513`), rendered into the same `<Card><CardHeader>
<CardTitle><Icon/>` scaffold. The visual sameness is a faithful rendering of a
structural sameness in the component that should not exist.

**3. "Add credits: $10 $25 $50 $100" is the page's only real verb**, so credit
reads as the primary product and compute reads as a status readout — the
inverse of the actual money. A compute plan is $20–$160/month recurring; a
top-up is a $10 one-off.

**4. The `— / — / 0 h` triple is rendered at full weight.** `INCLUDED HOURS
0 h / mo`, `ALLOWED SIZES —`, `OVERAGE RATE —` occupy the same prominent
`<dl>` grid as real values would, styled `font-medium text-foreground`
(`billing.tsx:718-740`). Missing data is presented with the confidence of
present data. §6 is entirely about this.

**The single sentence:** *the page renders the wallet's shape twice.* Credit
genuinely is `balance + top-up`. Compute is not — it is `capacity + how much
of it you've used + what happens past the edge` — but it was drawn with the
wallet's template, so it lost every part of itself that does not fit.

---

## 3. Recommended information architecture

### 3.1 Tabs: four become three

| Today | Recommended | Why |
|---|---|---|
| Overview | **Overview** | Rebuilt around two bands (§3.2). Absorbs nothing. |
| Plans | **Plans** | Keep. It is already good — size-first, server-priced, in-page checkout (`billing.tsx:994-1210`). Rename the tab label to **"Change plan"**: it is a verb, and the tab is entered to *do* something. |
| Invoices | *(merged)* | **Fold into Usage.** |
| Usage | **Usage & invoices** | An invoice is the settled record of a period's usage. They answer one question — "what did I spend and when" — and splitting them makes the user check two tabs to reconcile one number. |

Three tabs, and the strip stops being a filing cabinet. `BILLING_TAB_IDS` and
`routeSchemas.ts`'s tab enum both change; keep `invoices` as an accepted
inbound value that resolves to `usage`, because `01` §6 shows external links
carry `?tab=`.

*Devil's advocate.* Merging Invoices into Usage puts a dense table under a
dense chart, and someone who wants a receipt now scrolls past a bar chart to
get it. Two mitigations make me keep the recommendation: invoices go **above**
the daily chart, not below (receipts are looked up by intent, usage is
browsed), and the merged tab gets a period selector shared by both halves,
which is a genuine improvement — today the invoice list has no period control
at all (`billing.tsx:1277`) while the usage panel does.

*The alternative I rejected:* a per-product tab split (`AI` / `Compute` /
`Account`). It looks like the cleanest answer to "you don't distinguish the
two products" and it is a trap. It doubles the navigation for every
cross-product question the user actually has ("am I about to be charged for
anything?"), and it hides each product's state behind a click *from* the other
— which is the same class of error as the AI page living under
`/settings/general`. Distinguish them **within** one view, where the contrast
is visible; do not separate them into views where neither can be compared.

### 3.2 Overview: two bands with different shapes

```
┌─ You're set up ──────────────────────────────────────────────────────┐
│  Compute Medium · 12 of 41 hours used · $18.40 credit remaining      │  ← one line, plain language
└──────────────────────────────────────────────────────────────────────┘

  [ only when something is wrong — nothing here when nothing is wrong ]

╭─ AI CREDIT ──────────────────────────────────────── a meter ─────────╮
│                                                                      │
│   $18.40                                     ▓▓▓▓▓▓▓▓░░░░░░░░░░      │  ← a DRAIN
│   credit remaining                           ~9 days at recent use    │
│                                                                      │
│   Auto-recharge   ● On — add $25 when it drops below $10   [Change]  │  ← §4
│                                                                      │
│   [ Add credit ]   [ Redeem a code ]                                 │
╰──────────────────────────────────────────────────────────────────────╯

┌─ COMPUTE ────────────────────────────────────── a capacity ──────────┐
│                                                                      │
│  Compute Medium              Small, Medium machines      [Change plan]│
│  $40/mo · renews 14 Mar                                              │
│                                                                      │
│  ├──────────── included 41 h ────────────┤── overage ──┤             │  ← a CEILING
│  ███████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░              ┊            │
│  12 h used                                              ┊ cap $20    │
│                                                                      │
│  Beyond included hours:  ● Allow, up to $20/mo   $0.40/min  [Change] │  ← §5
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘

  ─────────────────────────────────────────────────────────────────────
  Billing account — receipts to sean@… · manage payment method in Stripe   ← quiet, last
```

The differentiation is **shape, not color**, which is what the repo's styling
contract requires (semantic tokens, `cn()`, no invented brand colors) and also
what actually works:

| | AI credit | Compute |
|---|---|---|
| **Metaphor** | a meter that drains | a capacity with a ceiling |
| **Primary visual** | horizontal depletion bar + runway ("~9 days") | segmented usage bar with a marked boundary between included and overage |
| **Primary number** | dollars remaining | hours used *of* hours included |
| **Container** | rounded, filled `bg-muted/40`, no border — a *reservoir* | bordered `border-border`, square-ish, sectioned — a *contract* |
| **Time axis** | backward-looking (burn rate) | forward-looking (renews on a date) |
| **The verb** | Add / auto-recharge | Change plan / set a ceiling |
| **Icon** | `Wallet` | `Cpu` (both already imported) |

Two things about the "runway" number, because it is the one new piece of
information and it carries a risk. `~9 days at recent use` is computed from
`useLLMSpend`'s 30-day window, which the AI page already loads
(`reliantAI.tsx:198`). It is the single most useful thing you can tell someone
holding a depleting balance, and it is the thing the current page most
conspicuously does not say. But it is an **estimate about money**, so: hedge
it in the copy (`~`, "at recent use"), suppress it entirely below a minimum
sample (fewer than ~3 days of spend gives a number that swings wildly and
would be quoted back at us), and never let it gate a control. If the estimate
is unavailable the row simply is not rendered — the balance above it still
answers the primary question.

### 3.3 What moves off `/settings/general`

`02` §4.1 recommends splitting by verb: `/settings/billing` owns money,
`/settings/ai-access` (renamed from Reliant AI) owns AI configuration. **I
inherit that and do not relitigate it** — it is the right call and the rename
is what makes it read as intentional.

One consequence this design adds: with a **credit band** now on Overview
carrying balance, runway and auto-recharge, the AI page's credit card
(`reliantAI.tsx:320-360`) becomes a **read-only chip** — balance plus a link —
and the second `RedeemCouponForm` there is deleted rather than kept as a
secondary. Its own comment concedes billing is canonical
(`reliantAI.tsx:~340`); once billing's credit band leads with a redeem action
at equal prominence to "Add credit", the second box is pure duplication and a
second answer to "where do I put my code". The local `usdFromNanos` alias
(`reliantAI.tsx:83`) goes with it.

### 3.4 One thing the current page gets right, and must keep

The Plans tab's **size-first** structure (`billing.tsx:994-1210`) is correct
and should not be touched by this redesign. Size is the question the user has
("I need a Medium machine"); the plan is the answer. It filters the grid,
disables plans that cannot run the size, and — importantly — *says why*
("Doesn't run medium machines") rather than leaving a dimmed card to be
interpreted. That, plus server-authoritative price and `display_order`, is
already the design this document would otherwise have had to argue for.

---

## 4. Recurring billing for AI credit

### 4.1 The options

**A. Auto-recharge.** "When my balance drops below $10, add $25." Needs a
saved card and an off-session charge. Consumption stays consumption; the
refill becomes automatic.

**B. Monthly credit subscription.** "$25 of credit on the 1st of every
month." A Stripe subscription whose `invoice.paid` webhook credits the wallet.

**C. Keep manual top-ups as one choice among several.** Status quo plus
better prompting.

### 4.2 Recommendation: **A, auto-recharge**

Three reasons, in order of weight.

**1. It matches how the product actually bills.** AI spend is metered per
token against a wallet (`canonical_metering.go`) — genuinely variable, with no
natural monthly quantum. A monthly subscription forces the user to predict a
number they cannot predict, and then punishes both errors: too low and they
run dry mid-month anyway (so we have added a subscription *and* kept the
top-up flow), too high and credit piles up in a wallet, which is us holding
their money for a service they did not use. Auto-recharge asks for a threshold
and an amount, which are questions about *comfort*, not forecasting.

**2. The failure mode is survivable.** If an auto-recharge declines, the user
has whatever balance remains and we have days of warning before zero. If a
monthly credit subscription declines, Stripe's dunning starts, the
subscription can lapse, and the recovery path is the one `01` §3.1 shows is
already fragile (`invoice.payment_failed` → `past_due`, `invoice.paid` →
`active`). Adding a *second* subscription per user doubles the surface of a
mechanism we already have bugs in.

**3. It composes with what exists rather than replacing it.** Top-ups stay
exactly as they are — `mode: payment`, one-off, embedded panel. Auto-recharge
is the *same charge*, issued by a trigger instead of a click. Option B is a new
Stripe product, a new price object, a new webhook branch, and a new place
`plansconfig` has to be right.

*Devil's advocate against my own recommendation, seriously.* Auto-recharge is
the option that charges a card with **no human present**, which makes it the
option that generates the "why did you take $25 from me" support ticket. B
never surprises anyone: it is the same amount on the same day, and it is what
a customer already understands from every other subscription they have. And B
has a real technical advantage I should not bury — Stripe's own dunning,
retries and card-update emails come free with a subscription, whereas
auto-recharge means we own the retry ladder ourselves.

What decides it for me is the shape of the surprise. B's failure is *silent
over-purchase*: money leaves monthly whether or not the product is used, and
the user discovers it on a statement. A's failure is a *visible, attributable*
charge that always corresponds to consumption that already happened. And A is
reversible in a way B is not — a user who dislikes auto-recharge turns it off
and is immediately back to today's behaviour with no residual state. I would
rather ship the reversible one. **If the owner disagrees, B is a clean
addition on top of A rather than a replacement, and the UI band in §3.2 has
room for it as a second option in the same `[Change]` dialog.**

*And the option I am not recommending but would accept:* C, shipped first, as
the Phase-0 of A. Everything in §3.2's credit band except the auto-recharge
row is valuable on its own — the runway estimate in particular. If backend
capacity is the constraint, ship the band with a "coming soon"-free version
that simply omits the row, rather than shipping a row that does not work.

### 4.3 What auto-recharge costs, honestly

This is the largest backend item in this document. Nothing below exists today.

**a. Store a payment method.** A `SetupIntent` collected via Stripe's embedded
form, giving a `PaymentMethod` attached to the Customer and set as
`invoice_settings.default_payment_method`. New columns on the wallet or org:
`default_payment_method_id`, plus display fields (`brand`, `last4`, `exp`) so
the UI can say "Visa ···4242" without a live Stripe call on page load.
**Never a hardcoded logo row** — brand comes from Stripe's response, and the
collection UI is Stripe's, which is what keeps
`TestCreateCheckoutSession_LeavesPaymentMethodsDynamic`'s spirit intact.

Note the nuance from §1.3: a compute subscriber **already has** a default
payment method at Stripe. For those users this step is a read, not a
collection — a real simplification, and the reason auto-recharge should be
offered most prominently to exactly that cohort.

**b. Store the rule.** `auto_recharge_enabled`, `auto_recharge_threshold_cents`,
`auto_recharge_amount_cents`, and — non-negotiable — a **monthly ceiling**:
`auto_recharge_max_per_month_cents`. A runaway agent loop that burns credit
faster than a human can react must hit a wall we put there. Same principle as
§5, applied to the other product.

**c. The trigger.** Evaluate on the ledger write that lowers the balance —
i.e. in the metering path, not on a timer — so the recharge fires when the
balance actually crosses the threshold rather than up to a minute later. It
must be **debounced and idempotent**: one in-flight recharge per wallet at a
time, an idempotency key of `autorecharge-<walletID>-<periodBucket>-<n>`, and a
hard refusal when the monthly ceiling is reached. Concurrent metering writes on
a busy wallet are the norm, not the exception; without the lock this fires
three times.

**d. The charge.** An off-session `PaymentIntent` (`off_session: true`,
`confirm: true`) against the stored method. Credit is applied by the **webhook**
on `payment_intent.succeeded`, never optimistically at charge time — the same
rule `02` §7 pins for checkout, for the same reason.

**e. The failure ladder, which is the part that is easy to under-scope.**
Off-session charges decline more often than on-session ones (no 3DS challenge
available). When one declines:

1. Wallet balance is untouched. The user still has what they had.
2. `auto_recharge_last_error` is recorded and the credit band shows a
   **degraded state**, not a silent off: *"Auto-recharge couldn't complete —
   your card was declined. Add credit manually, or update your card."* with
   both actions inline.
3. Retry **once** after ~24h, then stop and require user action. Do not build a
   retry ladder; Stripe's `payment_intent.requires_action` for a card needing
   authentication is a case where the honest answer is "we need you here" —
   surface it as a one-click on-session confirm.
4. Auto-recharge stays **enabled** through this. Disabling it on a decline
   means a user who updates their card silently no longer has the feature they
   configured.

**f. Cancellation/teardown.** Turning it off must detach nothing (the card may
be funding a compute subscription) and must clear any pending retry.

**Sizing, plainly:** (a) through (d) is a solid piece of backend work — a
migration, a SetupIntent flow, a webhook branch, a trigger with a lock, and a
new RPC pair (`GetWalletAutoRecharge` / `SetWalletAutoRecharge`). (e) is where
this kind of feature usually goes wrong and deserves its own tests. This is
**not** an evening. It is the largest single item here and it should be
sequenced after §5, which is a fraction of the cost and closes an actual
safety hole.

---

## 5. Compute overage: allow more time, and cap the spend

### 5.1 What the user is actually deciding

Three questions, and today's UI asks only the first:

1. **May machines run past my included hours at all?** → `overage_enabled`,
   the existing boolean toggle.
2. **How much extra am I willing to pay in a month?** → `budget_cents`, the
   column that exists and cannot be set (§1.1).
3. **What happens when I get there?** → today: you cannot start or resume a
   machine; running ones continue. (§1.2)

The current single toggle collapses (1) and hides (2) and (3), so the honest
reading of "Per-machine overage · [toggle]" is "allow unbounded additional
charges, rate unstated." That the rate *is* stated beside it does not fix it —
a rate without a ceiling is not a bound.

### 5.2 The control

One control, replacing the toggle, in the compute band:

```
Beyond your included hours
  ○ Stop at my included hours
      Machines won't start once the 41 hours are used. Nothing extra is charged.

  ● Allow extra time, up to a monthly limit
      Limit  [ $20 ]/mo        ≈ 50 extra minutes at $0.40/min
      ────────────────────────────────────────────────
      $0.00 of $20 used this month

  ○ Allow extra time with no limit
      Not recommended. Machine time past 41 hours is charged at $0.40/min
      with no ceiling.
```

Design decisions embedded there, each with a reason:

- **Three radio options, not a toggle plus a field.** The middle option is the
  recommended default and is selected when the user first enables overage, with
  a **pre-filled suggestion of 50% of the plan's monthly price**
  (Medium $40 → $20). A blank number input next to "allow charges" is a
  scavenger hunt, and per the project's own philosophy a scaffolded blank is
  not neutral — it is a broken state plus homework.
- **"No limit" is reachable but is the third option and says "not
  recommended".** Not disabled: some users genuinely want it, and a wall where
  a warning belongs is the pattern the 20% rule forbids. But it must be chosen,
  not defaulted into — which is exactly what today's `budget_cents IS NULL`
  does silently.
- **The dollar limit converts to minutes live.** `$20` at `$0.40/min` is
  "≈50 extra minutes". Users reason in machine time and are billed in dollars;
  showing both is the difference between a number they set and a number they
  understand. The rate is `daemon_overage_per_minute_cents`, server-supplied
  (`plans.yaml:145,161,177,193`) — no client price constant.
- **The consumption bar sits under the limit**, so the ceiling and the distance
  to it are one glance. This is the "make the ceiling legible" requirement.
- **The limit is per billing period**, aligned to the subscription period
  (`current_period_start/end`), and the copy says `/mo` because everything else
  on the page does. If we ever sell a non-monthly period, this label and
  `billing.tsx`'s other `/mo` strings change together.

### 5.3 The states, and how each is surfaced before it bites

| State | Threshold | Surface | Copy |
|---|---|---|---|
| Included hours running low | ≥80% of included | Compute band bar turns `warning`; no banner | "33 of 41 hours used" |
| Included hours spent, overage **off** | used ≥ included | Band shows a blocked state + the one action | "You've used your 41 included hours. New machines won't start until your plan renews on 14 Mar — or allow extra time." |
| Overage on, approaching cap | ≥75% of `budget_cents` | Band alert + **one** notification | "You've used $15 of your $20 extra-time limit. At $0.40/min that's about 12 minutes left." |
| At the cap | overage cost ≥ cap | Band blocked state, prominent, with three actions | "Extra-time limit reached. Machines won't start or resume until 14 Mar. Raise the limit, upgrade your plan, or wait for renewal." |
| At the cap, machine **running** | — | **See §5.4 — this is the unsolved one** | — |
| Renewal | period rolls | State clears; usage resets | — |

Two deliberate restraints. The approaching-cap notification fires **once per
period**, not per session — a cap you chose is not an emergency, and a warning
that repeats is a warning that gets dismissed reflexively. And the at-the-cap
state offers "raise the limit" as a **first-class action**, because the user's
own ceiling is not a punishment and making them hunt for the setting they set
is hostile.

### 5.4 What happens to a RUNNING machine at the cap — the open piece

Per §1.2, today: **nothing.** The cap gates `CreateDaemon` and `ResumeDaemon`;
the only sweeper that stops running machines reads the global free-tier pool,
not `budget_cents`. Three options:

| | Behaviour | Verdict |
|---|---|---|
| **A. Gate-only (today)** | Cap blocks start/resume. Running machines run to their idle timeout. | **Ship this first, and SAY it.** Honest, already-built, and no new failure mode. |
| **B. Suspend at the cap** | Extend `BillingEnforcementService` to read per-subscription `budget_cents` and suspend when exceeded. | **The right end state.** It is the sweeper's existing job; the per-owner grouping and the suspend path already exist (`billing_enforcement.go:113-140`). What it needs is a per-owner budget query alongside the global check. |
| **C. Warn, grace, then suspend** | Suspend at cap + a grace window and an in-app warning first. | The correct *final* form, and strictly B plus a timer. Not a separate option so much as B done properly. |

**Recommendation: ship A with copy that states it, then B+C.** And the copy is
the load-bearing part, because A silently sold as a "limit" is the failure
mode. Under the limit field:

> This limit stops new machines from starting. A machine that is already
> running keeps running until it goes idle — so your final bill can exceed the
> limit by the cost of finishing what's in flight.

That sentence is unglamorous and it is the difference between a control the
user can reason about and one that misleads them. **If the owner would rather
not ship a limit with that caveat, the alternative is to do B first — it is not
a large piece of work, and it is confined to one worker.**

For B, the design: in `BillingEnforcementService.enforce`, alongside the
existing per-owner `checker.Check(…, ActionTickSweep)`, resolve the owner's
compute subscription and, when `overage_enabled && budget_cents > 0 &&
overage_cost >= budget_cents`, mark `SuspendNow` with a distinct reason —
`compute_overage_cap_reached`, not `free_tier_global_budget`. Distinct because
the user-facing message is completely different: one says "you hit the limit
you set," the other says "a system-wide cap was hit," and the existing
`upgradeInterceptor` machinery keys off exactly this reason string. For C, the
grace window belongs in the sweeper (suspend on the *second* consecutive tick
over the cap, ~1 minute apart), not in the UI, so it holds for a user who is
not looking at the page.

**The reconciler's fail-open note matters here.** `ResumeDaemon`'s comment
(`service.go:717-722`) records that the funding check fails *closed* on the
user-initiated path while the reconciler's `ownerComputeFunded` fails *open*.
A suspend sweeper must fail **open** on a lookup error — suspending a paying
customer's running machine because a query timed out is far worse than a
minute of unbilled overage.

### 5.5 Backend work for §5

| Item | Size | Notes |
|---|---|---|
| `budget_cents` on the `Subscription` proto message | trivial | `shared.proto:318-330`, next to `overage_enabled = 11`. Optional/nullable — NULL means "no limit" and must not collapse to 0, which means something else. |
| `SetCurrentUserComputeOverage` gains an optional limit | small | Extend `SetCurrentUserComputeOverageRequest` (`billing.proto:384`) with `optional int64 budget_cents`. One RPC keeps the enable-and-cap decision atomic — two RPCs make "enabled with no cap" a reachable intermediate state, which is the exact state we are trying to stop happening by accident. |
| Repo `SetSubscriptionBudgetCents` | trivial | Beside `SetSubscriptionOverageEnabled` (`svcbilling/repo.go:35`). |
| Overage-cost-to-date in `GetCurrentUserComputeUsage` | small | The response already carries `overage_minutes` and `estimated_overage_cost_cents` (`billing.proto:334-335`). The band needs cost against cap; the numerator already exists, so this may be a pure UI change. **Verify before scoping.** |
| Sweeper reads per-subscription budget (§5.4 B) | medium | One worker. The suspend path exists. |
| Grace window (§5.4 C) | small, after B | Second-consecutive-tick. |

Everything above the sweeper row is **hours, not days**, and it converts a
dormant enforcement branch into a shipped feature.

---

## 6. Empty, loading, and degraded states

### 6.1 The rule

**The UI must distinguish "no data", "not loaded yet", and "loaded but
unusable", and must never render an unusable value at full confidence.**

The `0 h / mo` in the screenshot is the whole problem in three characters. The
data *arrived*; it was *unusable*; it rendered as `0`, which is a legitimate
value meaning "this plan includes no hours". Zero and unknown were collapsed,
and the collapse resolved toward the alarming reading — next to a purchase
button.

The Plans tab already implements the good version of this and it is worth
naming as the pattern to copy, because someone did the thinking already. It
counts plans it had to drop for having no price, and branches:

```tsx
// billing.tsx:1046-1071
unpricedPlanCount > 0
  ? <EmptyState title="Plan pricing unavailable"
      description={`The server returned ${n} compute plan(s) with no price …
        This usually means the control plane has not restarted since the plan
        catalog changed … Restart the control plane, or contact support.`} />
  : <EmptyState title="No plans available"
      description="Compute plans are not configured for this environment yet." />
```

`billingUtils.ts` backs it with `COMPUTE_PLAN_UNPRICED = null` and a comment
that states the principle exactly: *"A withheld price is recoverable; a
confidently wrong one next to a pay button is not."* **That principle is
correct and this section generalises it to every field on the page.**

### 6.2 State ladder, per surface

**Credit band**

| Condition | Render |
|---|---|
| Loading | Skeleton at the balance's dimensions. Not a spinner — the layout should not jump when a number this prominent arrives. |
| Wallet query failed | `$—` with "Couldn't load your balance" + Retry. **Top-up and auto-recharge controls disabled**: never offer to spend against a number we could not read. |
| Loaded, zero balance | `$0.00` — a real value, rendered plainly — plus the empty-wallet warning `getWalletBalanceState` already returns (`billingUtils.ts:61`). |
| Loaded, no spend history | Balance and controls render; **the runway line is omitted entirely.** No "~0 days", no "—". |
| Auto-recharge configured but last attempt failed | Degraded row per §4.3(e). |

**Compute band**

| Condition | Render |
|---|---|
| Loading | Skeleton for plan name and bar. |
| No subscription (the true empty) | Not a card of dashes. A **purposeful empty state**: "No compute plan. Machines run on a plan — pick one to get started." + `Change plan`. This is the single biggest improvement available on Overview, because a new user's Overview today is a wall of `—`. |
| Subscription present, `plan.limits` unusable (the screenshot) | **The degraded state, and it must exist.** Plan name and price render (they are known). The `included hours / sizes / overage` triple is **replaced by one line**, not three dashes: *"Plan details are unavailable — the control plane may not have restarted since the plan catalog changed."* The overage control is **disabled** with "Set a limit once plan details load", because a limit whose rate is unknown cannot be converted to minutes and cannot be reasoned about. |
| Usage query failed, subscription fine | Plan renders; the usage bar is replaced by "Usage unavailable — retry". Partial failure degrades partially. |

**How the degraded case is detected.** Exactly as the Plans tab does it: a
subscription exists but `derivePlanDisplay` yields `includedMinutes === 0 &&
allowedSizes.length === 0 && overageCentsPerMinute === 0` — all three absent
together, which is the stale-row signature, not a legitimate plan. A plan with
genuinely zero included hours would still carry sizes and a rate. Worth an
explicit predicate in `billingUtils.ts` (`isPlanDetailUnavailable(plan)`) with
a test, so the rule is stated once rather than re-derived at each render site.

### 6.3 The three situations the copy must separate

Named in the brief and worth restating as the acceptance criterion:

1. **"This environment has no plans."** The catalog is genuinely empty →
   *"Compute plans are not configured for this environment yet."*
2. **"The server hasn't synced."** Plans arrived without prices/limits →
   *"…the control plane has not restarted since the plan catalog changed."*
   Actionable and true; `plansync` upserts on boot.
3. **"We couldn't load it."** The query failed → *"Couldn't load…"* + Retry.

Today all three render as `—` or "not configured". The Plans tab separates 1
and 2; **Overview separates none of them**, and Overview is what the owner
screenshotted.

### 6.4 One non-obvious rule

**A degraded state disables the controls that depend on the degraded data, and
only those.** Not the whole card. On the screenshot's plan card: `Change plan`
stays live (it needs the catalog, not this subscription's stale limits) while
the overage control is disabled (its rate is exactly what is missing).
Blanket-disabling a card on partial failure removes the user's escape route,
which is usually the one thing that would have fixed their problem.

---

## 7. Component boundaries

New and changed components. Everything mounts inside the existing
`BillingSection` tab shell (`billing.tsx:108`).

```
web/src/components/Settings/cloud/
  billing.tsx                    tab shell; OverviewTab becomes ~120 lines,
                                 composing the two bands instead of building
                                 both products' UI inline
  overview/
    CreditBand.tsx               balance, runway, auto-recharge row, actions
    ComputeBand.tsx              plan, capacity bar, overage control, states
    StatusLine.tsx               the one-sentence summary
    AutoRechargeDialog.tsx       threshold / amount / monthly ceiling / card   [§4]
    OverageLimitDialog.tsx       the three-option radio + limit field          [§5]
  billingUtils.ts                + isPlanDetailUnavailable(), formatRunway(),
                                 overageMinutesForCap(cents, ratePerMin)
```

Boundaries, stated as rules rather than as a diagram:

- **A band owns one product's presentation and none of its business rules.**
  Both take fully-resolved props (`balance`, `runwayDays | null`,
  `autoRecharge | null`) plus callbacks. Neither calls a mutation directly.
- **Mutations stay in `useCloudBillingQueries.ts`**, which is where
  `assertPurchaseIdentity` lives (`:61`). `02` §7 pins this as the invariant
  that must survive every phase, and the anti-anonymous-purchase guarantee is a
  non-negotiable constraint of this brief. `AutoRechargeDialog` saves a payment
  method — that is spending-adjacent and goes through the same chokepoint. Pin
  it with the same test shape `02` §7 specifies: mount as an anonymous user,
  assert the identity form and **no** SetupIntent.
- **Every price, rate and limit is a prop from the server.** No module in
  `overview/` may contain a `Record<string, number>` keyed by plan id.
  `billingUtils.ts` already carries a comment naming that exact shape as the
  defect a previous change removed, and
  `__tests__/billingUtils.price.test.ts` reads the source and fails on it.
  Extend that test's glob to `overview/`.
- **Checkout stays `EmbeddedCheckoutPanel`.** Both bands mount the existing
  panel; neither reimplements session creation.

### 7.1 What each RPC must provide

| Surface | RPC | Needs |
|---|---|---|
| Credit balance | `GetCurrentUserWalletOverview` | ✅ today |
| Runway | `GetLLMSpend` (already loaded by the AI page) | ✅ today — the band needs the hook moved/shared, not new server work |
| Auto-recharge state | **new** `GetWalletAutoRecharge` | rule + card display fields + `last_error` |
| Auto-recharge write | **new** `SetWalletAutoRecharge` | enabled, threshold, amount, monthly ceiling |
| Save a card | **new** `CreateSetupIntent` | client secret for Stripe's embedded form |
| Plan + overage state | `GetCurrentUserComputeSubscription` | **needs `budget_cents` added** to the `Subscription` proto (§5.5) |
| Usage vs cap | `GetCurrentUserComputeUsage` | has `overage_minutes` + `estimated_overage_cost_cents`; verify cost-to-date is period-scoped |
| Set overage + limit | `SetCurrentUserComputeOverage` | **needs `optional int64 budget_cents`** (§5.5) |
| Plans | `ListPlans` | ✅ today — priced, ordered, `structuredLimits` |

**Frontend-only** (ship immediately, no backend): §3.1 tab merge, §3.2 band
shapes, §6 empty/loading/degraded states, the runway line, the status line.
That is most of the owner's "it's monotone" complaint and it does not wait on
anything.

---

## 8. Sequencing

| Phase | Contents | Backend | Value |
|---|---|---|---|
| **1** | Two bands, tab merge, all empty/loading/degraded states, runway, status line | **none** | Fixes "monotone" and "0 h / mo looks broken". Ships now. |
| **2** | Overage limit: proto field, RPC param, repo method, the three-option control | small | Closes a real safety gap; enforcement already exists (§1.1) |
| **3** | Sweeper reads per-subscription budget + grace window | medium, one worker | Makes the limit in Phase 2 mean what users think it means (§1.2) |
| **4** | Auto-recharge: SetupIntent, stored method, trigger, off-session charge, failure ladder | large | The owner's recurring-billing ask (§4) |

Phase 1 before Phase 2 is deliberate: the degraded state in §6.2 is a
*precondition* for the overage control. A limit whose per-minute rate is
unreadable cannot be set safely, and the current page cannot tell that
situation from a plan that genuinely has no overage — it renders `—` for both.

Phase 3 before Phase 4 is a judgment call I will defend: Phase 3 stops a bill
the user did not agree to, Phase 4 creates a charge they did. Safety before
convenience.

---

## 9. Decisions made on the owner's behalf

Each is reversible; each has a stated default so work is not blocked.

1. **Four tabs → three**, Invoices folded into "Usage & invoices"; Plans
   relabelled "Change plan". (§3.1)
2. **Differentiate by shape, not color** — meter vs. capacity — within one
   Overview, not by splitting the products into separate tabs. (§3.2)
3. **A credit runway estimate** ("~9 days at recent use"), hedged and
   suppressed on thin data. (§3.2)
4. **Auto-recharge, not a monthly credit subscription**, for recurring AI.
   (§4.2)
5. **Auto-recharge carries a mandatory monthly ceiling.** (§4.3b)
6. **The overage control is three radio options**, not a toggle plus a field,
   with the limit pre-filled at 50% of plan price and "no limit" reachable but
   marked not recommended. (§5.2)
7. **`budget_cents` extends the existing `SetCurrentUserComputeOverage` RPC**
   rather than getting its own, keeping enable-and-cap atomic. (§5.5)
8. **Ship the gate-only cap (A) with copy that states its limitation**, then
   the sweeper. (§5.4)
9. **The AI page loses its coupon form and its writable credit card**, keeping
   a read-only chip. (§3.3)
10. **A degraded state disables only the controls that depend on the missing
    data.** (§6.4)

## 10. What needs the owner's decision

1. **Auto-recharge vs. a monthly credit subscription** (§4.2). I recommend
   auto-recharge and the argument against it is real — B never surprises
   anyone and inherits Stripe's dunning for free. This is a product-feel call.
   **Default if he does not answer: auto-recharge.**

2. **Ship the overage cap gate-only, or wait for the sweeper?** (§5.4) Today a
   cap does not stop a running machine and cannot be made to without work in
   `BillingEnforcementService`. Gate-only plus honest copy is available
   immediately; the sweeper is not large but is not free.
   **Default: ship gate-only with the caveat copy, sweeper next.**

3. **Is a credit runway estimate acceptable?** (§3.2) It is the most useful
   thing on the credit band and it is a prediction about money that a user may
   quote back at us. **Default: ship it, hedged and suppressed on thin data.**

4. **Does folding Invoices into Usage lose something he wants?** (§3.1) He may
   consider invoices a first-class destination. **Default: fold, keep
   `?tab=invoices` resolving to the merged tab.**

5. **Not a decision, a flag:** the empty-plans symptom he screenshotted is
   stale dev data and a control-plane restart fixes it. The design does not
   depend on that restart — §6 makes the UI honest either way — but the
   environment stays wrong until someone restarts it, and I did not, because
   restarting a shared dev stack is his call, not mine.

---

## 11. Verified vs. inferred

**Verified by reading code in this tree:** the `budget_cents` column,
migration, model field, repository read/write, and its enforcement at
`internal/svcdaemon/service.go:1425-1433`; the absence of any setter or proto
field for it (`rg budget_cents` over `internal/billing/`, `internal/handlers/`,
`proto/` → zero hits); that `checkDaemonSizeAllowed` is called only from
`CreateDaemon:258` and `ResumeDaemon:727`; that `BillingEnforcementService`
enforces `FreeTierGlobalCapCents` via `ActionTickSweep` and never reads
`budget_cents`; the absence of any `SetupIntent` / `payment_method` /
`off_session` usage outside the deliberate negative test in
`internal/billing/embedded_checkout_test.go`; the current `plans.yaml` prices,
overage rates and `display_order`; that `billingUtils.ts` no longer carries
client price tables and that `derivePlanDisplay` reads `structuredLimits`; the
Plans tab's two-way empty state; and the Overview tab's structure, including
the full-weight `— / — / 0 h` triple at `billing.tsx:713-740`.

**Inherited from `01`/`02` and not re-verified:** the RPC precondition graph,
the webhook entitlement path, the checkout idempotency findings, the Electron
`app://bundle` constraint, and the `/settings/ai-access` split.

**Inferred, flagged:** that a compute subscriber necessarily has a default
payment method at Stripe (true of `mode: subscription` in general; I did not
read our Customer records); that the all-three-zero signature reliably
identifies a stale plan row rather than a legitimate one (it matches what I
saw, and §6.2 asks for a tested predicate rather than trusting the inference);
and the sizing estimates in §4.3 and §5.5, which are judgment, not measurement.

**Not verified:** runtime behaviour of anything here — nothing in this
document was executed, no database was queried, and no code was changed.
