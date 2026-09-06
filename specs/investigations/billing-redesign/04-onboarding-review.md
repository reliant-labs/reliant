# Onboarding flow — adversarial review

**Reviewer:** independent agent, no authorship of the code under review.
**Scope:** `web/src/components/OnboardingFlow/**`, `web/src/components/Billing/**`,
`web/src/routeSchemas.ts`, `web/src/hooks/useOnboardingQueries.ts`.
**Method:** read the real code; ran the real suites; wrote two throwaway repro
tests to convert hunches into proofs, ran them, and deleted them. No product
code was edited.

**Baseline:** `npx vitest run src/components/OnboardingFlow/ src/components/Billing/`
→ **26 files, 227 tests, all passing.** Both of my top-severity findings are
live behind that green suite. That is the headline: the tests are extensive and
well-written, and they do not cover the two seams where the parallel agents'
slices meet.

---

## Verdict

**REQUEST CHANGES.** Risk: **HIGH**.

The redesign's core idea is sound and, in places, unusually well executed — the
pure `deriveStep`, the single `commitLaunchPlan`, and the single
`leaveOnboarding` exit are genuine improvements over what they replaced, and the
reasoning is documented at the point of use rather than in a doc that rots. The
problems are not in the parts any one agent owned. They are in the two places
where one agent's output becomes another's input, and neither is covered by a
test.

---

## Findings, by severity

### F1 — CRITICAL, PROVEN. Completing onboarding unmounts the step before `ProvisioningGate` can render it.

**Files:** `hooks/useOnboardingQueries.ts:291-301`,
`OnboardingFlow/OnboardingRoute.tsx:37,72-77,115`,
`OnboardingFlow/steps/ProjectChoiceStep.tsx:60-72`,
`steps/ProjectPickerStep.tsx:88-94`, `steps/GitHubConnectStep.tsx:246-254`.

All three terminal steps finish in the same order:

```
await completeOnboardingMutation.mutateAsync(...)   // ← flips the user query
await finalizeOnboardingSideEffects()
await runCommit(finalPlan)                          // ← result drives the gate
```

`useCompleteOnboarding.onSuccess` **optimistically writes**
`onboardingCompleted: true` into `['onboarding','currentUser']`
(`useOnboardingQueries.ts:294-300`). `OnboardingRoute` reads that same query,
derives `isComplete` from it (`:37`), and on the very next render both
navigates away (`:72-77`) **and returns `null`** (`:115`).

So `completeOnboarding` resolving is itself the thing that tears down the
subtree that was about to render the gate. `runCommit` is then awaited by a
component that is being unmounted; `setCommit` lands on a dead component and the
`ProvisioningGate` never renders.

**Proof.** I wrote a repro that renders `OnboardingRoute`, asserts the step
subtree is present, flips `onboardingCompleted` to `true` exactly as the
optimistic write does, and re-renders. It **passes** — the subtree is gone and
`navigate` has been called:

```
✓ REPRO: terminal-step commit vs route unmount
  › unmounts the onboarding subtree the moment completeOnboarding lands
[Onboarding] exit { reason: 'already_complete', steps_viewed_count: 0 }
```

**What the user experiences.** A cloud user pays, answers the project question,
and is dropped on `/` while their machine is still provisioning — with
`onboarding_completed=true` and no ACTIVE daemon. That is, precisely and
literally, the state described in `finalizeOnboarding.cloudGate.test.ts`'s own
header comment as "why a brand-new user reported being dropped onto 'Connect a
daemon'". **The bug that test was written to close is live again**, through a
different door: the fix removed the *navigation* from `finalizeOnboardingSideEffects`,
but the *unmount* now comes from the completion mutation's cache write instead.
The test still passes because it only asserts that one function does not call
`router.navigate`.

This also silently defeats every downstream guarantee that was built on the gate
being seen: partial-failure reporting, the `commitKey` support quote, and the
"the gate's Continue is the ONLY way out" claim asserted in three separate
docstrings.

**Why no test catches it.** `ProvisioningGate.test.tsx` renders the gate with a
hand-built `CommitResult` — it never asks whether a terminal step can get one
onto the screen. `OnboardingRoute.returning.test.tsx` mocks `OnboardingPage`
away entirely. Nothing renders a terminal step inside the route. The seam is
exactly where two agents' slices meet.

**Severity note.** I have proved the unmount. I have *inferred* — not
instrumented in a browser — that this reaches the user as a lost gate rather
than being rescued by some render-ordering accident. The inference is strong
(`isComplete` gates a bare `return null`, so there is no frame in which the step
survives), but it is worth 15 minutes with the real stack before designing the
fix.

---

### F2 — HIGH, PROVEN. `plan.paid` is sticky and unqualified: a Back that changes *which* bill is owed skips checkout for a charge never paid.

**File:** `OnboardingFlow/stepConfig.ts:142-149` (`checkoutIsOwed`), with
`BACK_CLEARS` at `:188-199`.

```ts
function checkoutIsOwed(plan, facts) {
  if (!plan.compute || !plan.modelProvider) return false;
  if (plan.paid) return false;              // ← unqualified short-circuit
  return requiresPayment(plan, facts).any;
}
```

`paid` means "every purchase **this plan** owed has been confirmed". But it
records a *verdict*, not *what was bought* — and nothing invalidates it when the
plan changes underneath. `BACK_CLEARS` never lists `paid`, and no other code
path clears it (verified: the only writes are `CheckoutStep.tsx:182` setting it
`true`).

**The reachable sequence, entirely through UI the flow renders:**

1. Local compute + Reliant credits, empty wallet → `checkout`. User pays for AI
   credit. `paid: true` goes into the URL.
2. Back from `project-picker` clears `modelProvider` → model step.
3. Back from model clears `compute` → compute step. `paid: true` survives both.
4. User now picks **cloud** compute + Reliant credits — a monthly subscription
   they have never paid for.
5. `requiresPayment` correctly reports `needsCompute: true`. `checkoutIsOwed`
   returns `false` on the `paid` short-circuit. Derivation goes straight to
   `project-choice`.

**Proof.** Repro asserting the honest answer at step 5:

```
× REPRO: paid is sticky across a Back that changes the bill
  → expected 'project-choice' to be 'checkout'
  Expected: "checkout"   Received: "project-choice"
```

`requiresPayment(nowCloud, NEW_USER).needsCompute === true` in the same test.
The two disagree, and derivation believes the stale flag.

**Why the 1920-state enumeration misses it.** The enumeration is a test over
*states*, not over *transitions*. It treats `paid` as a free axis and asserts
`deriveStep === 'checkout'` iff `compute && modelProvider && !paid &&
requiresPayment().any` — i.e. it asserts `deriveStep` agrees with a formula that
**contains the same `!paid` short-circuit**. It is a tautology with respect to
this bug: the oracle encodes the defect. `deriveStep.enumeration.test.ts:361-370`
is the exact assertion. Invariant 4's BFS has the same shape — it pushes
`{...plan, paid: true}` as an always-available move, so it can only ever prove
reachability, never that payment was actually owed for what the user ended up with.

This is the sharpest instance of the brief's warning: a test that passes against
deliberately broken code, because the test and the code share a wrong premise.

**Consequence:** a user reaches the app with a cloud plan and no compute
subscription. `commitLaunchPlan`'s `provisionDaemon` then polls eligibility for
30s and reports `pending` — "Waiting for payment confirmation" — for a payment
that will never arrive, because nothing ever asked for it.

---

### F3 — MEDIUM, INFERRED. `computeAutoSkipped` is sticky in the same way, and its own docstring says so.

**File:** `types.ts:109-119`, `stepConfig.ts:123-131`.

`stepConfig.ts` explicitly documents that "`computeAutoSkipped` outlives the
field it describes — Back from `model` clears `compute` but leaves the flag
set". `visibleStepsForPlan` was patched to tolerate this (never hide the current
step), which is a correct local fix. But the root cause — a plan field that
describes a *past event* and is never invalidated when that event is undone —
is the identical shape as F2. Two instances of one defect class suggests
`BACK_CLEARS` should be clearing derived/verdict fields alongside the answers
that produced them, rather than each symptom being patched where it surfaces.

Not independently reachable as a user-visible bug today (the `visibleStepsForPlan`
guard holds), so MEDIUM rather than HIGH. Flagged because F2 is the same bug
with money attached.

---

### F4 — MEDIUM, INFERRED. Hand-edited / stale `plan` URLs are validated for *shape* but never for *coherence*.

**Files:** `routeSchemas.ts:25-77`, `useOnboardingPlan.ts:20`.

`launchPlanSchema` is `.strict()` and rejects unknown keys — good, and the
type-level drift guard at `:94-131` is genuinely excellent work. But every field
is independently `.optional()`, so any *combination* is accepted. Two concrete
consequences:

- **`compute: "undecided"`** is in the schema (`:32`) and in `ComputeChoice`, but
  no step ever writes it and `isCloudCompute` returns false for it — so a URL
  carrying it routes to `project-picker` as though the user had chosen local. It
  is a value the type system blesses and the flow silently mistranslates. Worth
  deleting from both if nothing sets it.
- **A yesterday-URL** carrying `paid: true` and a `commitKey` restores both. The
  `commitKey` restoration is deliberate and correct (resume, don't re-provision).
  The `paid: true` restoration is F2's precondition arriving without any Back
  needed — the user just reopens a bookmark.

The brief asked what happens with "a `plan` param someone hand-edited to
nonsense": *structural* nonsense is caught cleanly (`.strict()` throws, and the
docstring at `:72-75` shows they learned this the hard way with
`computeAutoSkipped`). *Semantic* nonsense is not caught at all.

---

### F5 — LOW/MEDIUM, INFERRED. The identity modal's "Skip" removal is right; its remount path is not obviously safe on Electron.

**Files:** `CheckoutPanelWithIdentity.tsx:56-84`, `LinkIdentityModal.tsx`,
`LinkIdentityForm.tsx:79-95`.

The design here is good and the reasoning is correct: the email path completes
in-modal, `linkAttempt` bumps the panel key to force a fresh session, dismissal
leaves a re-entry affordance rather than a dead form, and there is deliberately
no "Skip for now" because it would call `signInAnonymously` and recreate the
blocking session. `useCheckoutSession` also resets `startedKey` on
`identity_required` (`:119`) so the retry can actually re-fire. That chain
checks out.

The gap is the **OAuth** path. `handleLink` (`LinkIdentityForm.tsx:86-89`)
performs a full-window redirect. In the packaged Electron renderer the origin is
`app://bundle`, and `currentOnboardingUrl()` (`CheckoutStep.tsx:100-103`) builds
`returnTo` from `window.location.pathname + search`, guarded by `isSafeReturnTo`.
That is a *path*, so it passes the guard — but whether the provider round-trip
returns to `app://bundle` + that path, versus dropping the user at a cold app
root with the plan lost, depends on the loopback/`app://` handling in
`electron/src/oauth-loopback.js` and `app-protocol.js`. I read enough to see the
machinery exists and is thought about; I did **not** verify the round trip end to
end. The email path is unaffected and is the prominent one.

**Recommend:** exercise anonymous → *OAuth* link → return, in a packaged build
specifically. If it loses the plan, the user is back at step one having already
answered everything — the exact failure `currentOnboardingUrl` was written to
prevent.

The brief also asked whether an existing-account holder can *sign in* rather than
link, stranding their anonymous chats. **They cannot, and that is correct** —
`LinkIdentityForm` deliberately does not build on `AuthScreen` and offers only
link mechanics (`signUp` in place, `linkIdentity`). The docstring at `:10-25`
states this reasoning explicitly. Good.

---

### F6 — LOW, VERIFIED. Copy honesty is mostly strong; one gap.

I checked the specific thing the brief pointed at and the surrounding copy. This
is one of the better-executed parts of the change:

- `ComputeStep`'s cloud card branches its copy on eligibility rather than its
  *availability* (`:355-361`) — both branches are true statements about the same
  button, which is the right pattern.
- `ModelStep` says "we'll ask for credit on the next step" on an empty wallet
  (`:396-401`) — an accurate forward reference, not a promise.
- `CheckoutStep`'s two-leg header is deliberately derived from the **plan**, not
  from what is currently outstanding (`:252-260`), so it cannot renumber "Step 1
  of 2" under a user mid-payment. That is a subtle failure someone actually
  thought about.
- The settle-timeout copy (`:317-320`) is honest: "your payment went through, but
  we haven't been able to confirm it yet".
- `vocabulary.test.tsx` enforces "machine" over "daemon"/"environment" across the
  whole onboarding source. This is a genuinely good test — it guards source files
  it does not import.

**The gap:** `ProvisioningGate`'s `provision_daemon` **`pending`** state renders
with a spinner and the detail "Waiting for payment confirmation."
(`commitLaunchPlan.ts:296-301`, `ProvisioningGate.tsx:41-48`). After
`awaitComputeEligibility` exhausts 15 × 2s, that is no longer a wait — it is a
30-second-old failure still animating a spinner. The `settleTimedOut` branch in
`CheckoutStep` gets this right and says so; the gate's equivalent does not.
`commit.status` is `partial`/`failed` at that point so Continue and Try again do
render, but the row above them still implies progress.

---

## What is genuinely good

Not filler — these are things I tried to break and could not:

1. **`deriveStep` as a pure function over `(plan, facts)`** is the right call,
   and keeping the facts *out* of the URL is the right trade. The docstring at
   `stepConfig.ts:52-66` correctly identifies that putting a server-owned,
   time-varying fact into user-editable state is the hazard. F2 is a bug in a
   *plan* field, which is the argument *for* this boundary, not against it.
2. **`requiresPayment` and `isCloudCompute` as single definitions.** The
   `cloud_paid`-treated-as-local class of bug is genuinely eliminated at the
   root, not patched per call site. `types.ts:14-41` explains exactly why.
3. **`commitLaunchPlan`'s `CommitDeps` interface** (`:116-135`) — "the list of
   side effects this module is allowed to have, written down" — is a real
   architectural constraint, not test scaffolding. A billable call that is not in
   that interface cannot be made from the commit point.
4. **No automatic retry of a billable call.** A settled failure stays settled;
   only the explicit Retry button clears it (`:211-213`). Correct and rare.
5. **`hasUsableControlPlaneDaemonForOnboarding` getting its own predicate**
   because the two `DaemonStatus` enums collide numerically
   (`ComputeStep.tsx:50-73`). Casting would have made PENDING read as ACTIVE.
   Someone caught a real trap here.
6. **`useReturningUserHeal`'s `unknown` storage state** (`:76-99`). Treating a
   failed `sessionStorage` read as "unknown, decline to heal" rather than
   "not-seen" is exactly right, and the asymmetry argument for it is correct:
   ending a flow wrongly is unrecoverable; declining costs one extra pass.
7. **The `routeSchemas.ts` type-level drift guard** (`:94-131`), including the
   note that assignability alone is insufficient because TypeScript is
   structural, and that it lives in a source file because tsconfig excludes
   tests. That was verified by experiment before being written, and it shows.
8. **The `ModelStep` guard inversion the brief asked me to check: it happened,
   and it is coherent.** `ModelStep.tsx:201-207` has no `!hasFunds` refusal, the
   button is disabled only while saving (`:419-422`), and `deriveStep` routes the
   unfunded `reliant_credits` plan to `checkout`. The guarantee moved from a
   disabled button to step derivation rather than being dropped. The handoff
   between those two agents worked.

---

## Answers to the brief's specific questions

- **Back at each step:** correct except that it does not clear verdict fields
  (F2, F3). Invariant 3 proves Back always moves strictly earlier *in the step
  list*; it does not prove the plan is left coherent.
- **Refresh mid-flow:** fine. Plan is in the URL; `commitKey` makes provisioning
  resume rather than duplicate.
- **Stale URL from yesterday:** F4. Shape-valid, coherence-unchecked.
- **Facts changing under a mounted step:** handled deliberately and well —
  pessimistic-while-loading is the correct asymmetry and is argued for at
  `useOnboardingFacts.ts:8-20`. The step *can* change under the user when a
  webhook lands, but only ever forward, out of `checkout`, which is benign.
- **Commit fired not-awaited, user navigates away:** recovers. The module-level
  `commits` map dedupes within a session; across a reload the `commitKey` is in
  the URL and `CreateDaemon` is idempotent by name. Two surfaces firing it is
  safe for the same reason. **This one holds up.**
- **Commit result rendering:** does **not** hold up — see F1. That is the failure.
- **Web vs Electron vs mobile:** mobile redirects to the same `/onboarding`
  (`MobileShell.tsx:61-71`) and is fine. Electron's Stripe handling is
  thought-through (`electron/src/stripe-checkout.js` watches for the return
  rather than one-waying into the system browser, and explicitly declines to
  "fix" this by lying in the navigation policy). The `@stripe/stripe-js`
  **5.10.0 exact pin** is correct and its rationale (`Billing/stripe.ts:9-31`) is
  right — v9's `dahlia` train rejects `ui_mode=embedded` sessions, which is what
  the brief flagged. Verified both `package.json` pins are exact, not caret.
  Residual Electron risk is the OAuth-link return path only (F5).

---

## The single thing I would fix first

**F1.** It is critical, it is proven, it silently re-opens a bug this codebase
has already fixed once and written a test about, and every other guarantee built
on `ProvisioningGate` — partial-failure visibility, the support quote, "the
gate's Continue is the only way out" — is downstream of it.

The fix is not to remove the optimistic cache write (it exists for a real
reason: stopping `ModernApp`/`MobileShell` bouncing the user back to
`/onboarding`). It is that **"the server has recorded completion" and "this flow
is finished with the user" are two different facts, and the route currently
conflates them.** The terminal step needs to own its own exit — which
`leaveOnboarding` already models correctly — and `OnboardingRoute`'s
already-complete redirect needs to not fire while a step is mid-commit.

I would design that fix rather than reach for the smallest patch, because the
smallest patch here (suppress the redirect with a flag) is how the *previous*
version of this bug was fixed, and `leaveOnboarding.ts:9-19` documents why that
flag-threading approach failed: it left the bug one forgotten argument away on
any new path.

**F2 second**, and when fixing it, fix the *class* (F3) rather than the instance:
verdict fields on the plan need to be invalidated by the Back that invalidates
their premise. And the enumeration test's oracle must be rewritten to not share
the short-circuit it is supposed to be checking — as written it cannot fail on
this bug.

---

## Confidence

| Finding | Status | Evidence |
|---|---|---|
| F1 | **Proven** (unmount); user impact inferred | Repro test passed; `OnboardingRoute.tsx:115` |
| F2 | **Proven** | Repro test failed with exact expected/received |
| F3 | Inferred | Code's own docstring states the staleness |
| F4 | Inferred | Schema read; no repro attempted |
| F5 | Suspected, unverified | Read Electron OAuth machinery, did not run it |
| F6 | Verified by reading | Copy read against behaviour at each site |

Everything in "What is genuinely good" was checked against the code, not taken
from the design docs — which, as the brief warned, are wrong in places.
