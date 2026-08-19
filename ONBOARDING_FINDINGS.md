# Onboarding investigation — new user, mobile, production

Scope: the **client** onboarding flow (screen order, duplication, state). The
server-side billing bypass in `internal/svcdaemon` is owned by another agent and
is deliberately untouched here.

## TL;DR

**Screens 2, 3 and 4 were not onboarding.** Every string the user quoted after
the first screen belongs to `web/src/components/Projects/ProjectPicker.tsx` —
the *post*-onboarding project picker. The user was **ejected out of onboarding**
partway through, and everything they saw afterwards was the picker's own
daemon-connect UI doing exactly what it is designed to do for a user who has no
running machine.

So the answer to "do we have duplicate screens?" is **no, not really** — there
is one onboarding flow, plus a second daemon-connect surface that the user was
dropped into by mistake. The answer to "how did these even pop up?" is a
one-line ordering bug in `finalizeOnboardingSideEffects`.

Two PRs are open. **Neither is merged.**

| PR | What |
|----|------|
| [#169](https://github.com/reliant-labs/reliant/pull/169) | Stop leaving `/onboarding` while the cloud daemon is still provisioning (the ejection). |
| [#172](https://github.com/reliant-labs/reliant/pull/172) | Gate ProjectPicker's "provision a cloud daemon" on compute eligibility (the client half of the money bug). |

---

## 1. Is there more than one onboarding implementation?

**Yes, two trees — but they are two different systems, not two copies of one.**
Both are live. Neither is dead code. **Nothing should be deleted.**

### `web/src/components/OnboardingFlow/` — the SETUP flow (this is the one that routes)

Mounted at `/onboarding` (`routes.tsx:411`, via `OnboardingRoute`). Five steps in
`stepConfig.ts`: `compute → model → project-choice → github-connect →
project-picker`. This is what a new user is sent through.

Notably, the current step is **derived from plan state**, not stored:
`deriveStep(plan)` in `stepConfig.ts`. There is no `step` URL param and no state
machine that can "skip ahead" — the derivation is a pure function of the plan,
and the plan lives in the URL. **This design is sound and is not the bug.**

### `web/src/components/Onboarding/` — the guided TOUR + achievement checklist

`OnboardingWizard`, spotlights, `ContextualTipsLayer`, the checklist store.
Mounted globally on the root route (`routes.tsx:162`). It is the post-setup
walkthrough that spotlights desktop chrome by DOM selector.

### Can they both mount at once?

They can both be *mounted*, but the wizard **suppresses itself** on the setup
route — `OnboardingWizard.tsx:340` computes `isOnboardingRoute` and gates on it
at line 519. It also returns `null` on any non-desktop surface (line ~420). So on
mobile at `/onboarding`, the tour is doubly suppressed. **This is not the source
of the duplicate-feeling screens.**

**Recommendation: do not delete either tree.** The naming is genuinely
confusing, and renaming `Onboarding/` → `Tour/` would make the split obvious —
but that is a separate, purely-cosmetic change and is the user's call, not mine.

---

## 2. What decides the first screen — and why did the user get ejected?

### The ejection (root cause, fixed in #169)

All three terminal steps do this:

```ts
await finalizeOnboardingSideEffects(...)
if (isCloud) setShowDaemonGate(true);
```

…but `finalizeOnboardingSideEffects` (`useOnboardingComplete.ts`) ended with an
**unconditional** `router.navigate({ to: "/" })`.

For a cloud user the router therefore left `/onboarding` **before** the gate
could render. `DaemonConnectingGate` — the screen whose entire job is to say
*"your machine is still starting"* — was never seen.

The user lands on `/` with `onboarding_completed = true` and **no ACTIVE
daemon**. That is precisely the state `ModernApp` answers by rendering
`ProjectPicker` (`!currentProject`), whose `NoActiveDaemonState` renders:

- `"Connect a daemon"` — `ProjectPicker.tsx:315 / 500 / 510`
- `"Cloud daemon requested — It may take a few minutes to provision…"` — `:395–400`
- `"Resume a daemon"` / `"Pick a daemon to wake up…"` — `:526 / :530`

**The clincher:** the user saw `Resume workspace-daemon`. The string
`"workspace-daemon"` appears in **exactly one place in the entire codebase** —
`ProjectPicker.tsx:299`. Onboarding's own cloud path names its daemon
`"onboarding-daemon"` (`ComputeStep.tsx:214`). The daemon name alone proves the
user was in the picker, not in onboarding.

Screens 2→3→4 are therefore one coherent sequence in a **single** component,
which is why they felt like a broken onboarding: they were a *correct* picker
flow shown to someone who should still have been in onboarding.

### The "set your API key" first screen — NOT fully confirmed

I could not reproduce this one from a production trace, so I am flagging it
rather than guessing. What I established:

- The modal is `ApiKeySetupModal`, titled **"Add an API key"**, rendered by
  `ModalLayer`, which is mounted on the **root route** and has **no route
  awareness** — it does not suppress itself on `/onboarding`. `Modal` is
  `z-50`; `OnboardingPage` is `z-40`. **So an API-key modal can render on top of
  the onboarding card.** That is a real ordering hazard.
- It is normally gated by `checklistState.welcomeShown` in `apiKeySetupStore`
  (lines 192, 259), and `welcomeShown` is only set by `markTourCompleted`. For a
  brand-new user it should be `false`, which should defer the modal.
- **The uncertainty:** `loadState()` reads `welcomeShown` from a backend setting
  *unioned with a localStorage mirror*. I could not confirm which value a fresh
  production mobile user gets, and I did not want to assert a mechanism I had
  not proven.

**Recommendation (not in either PR):** make `ModalLayer` suppress
`api-key-setup` while `pathname.startsWith("/onboarding")`. Onboarding's
`ModelStep` already owns provider setup, so the modal is redundant there
regardless of how it was triggered. I did not include this because I could not
build a failing test that proved the trigger, and I would rather hand you a
confirmed diagnosis than a speculative patch.

### On the documented "loading is not the negative of success" anti-pattern

I checked specifically. The onboarding gates are **mostly careful** about this —
`OnboardingRoute` waits on `isUserLoading` *and* `daemonsLoading` before acting,
with a comment explaining why, and `ComputeStep` blocks behind `daemonLoading`
for the same reason.

The one genuine instance I found was in `ProjectPicker`'s cloud button, which
treated *not-yet-known* eligibility as *permitted*. That is fixed in #172, with a
regression test for the still-loading case.

---

## 3. Mobile: is this mobile-only?

**No — this is general.** Mobile shares the setup flow rather than forking it.

- `mobileRedirect.ts:66` lists `/onboarding` as a **preserved** path — phones are
  never redirected away from it.
- `MobileShell.tsx:71` bounces an un-onboarded user to `/onboarding`, the same
  gate `ModernApp` applies.
- Both `MobileShell` and `ModernApp` correctly wait for `isUserLoading` first.

The bug is in `finalizeOnboardingSideEffects`, which is surface-agnostic. A
desktop user on the cloud path hits it identically. Mobile is likely just where
it was noticed — and mobile makes it *worse*, because the picker the user is
dumped into is a dense desktop table.

---

## 4. Is "Resume a daemon" the intended flow?

**No.** It is the symptom.

The intended flow after requesting a cloud daemon is `DaemonConnectingGate`: poll
for up to 60s, then show connected / failed with Retry, View logs, and Skip. That
screen exists, is tested, and is good. The user never reached it.

Instead they were bounced to the picker, which independently discovered "this
account has a daemon that is not ACTIVE" and offered to resume it — showing
`Starting`, because the machine really was still provisioning. **Onboarding lost
its state; the picker was doing its job correctly with the wrong user in front of
it.**

---

## 5. Eligibility: does the UI check before offering a cloud daemon?

**Onboarding does. The picker did not.** This is a real finding and worth having.

- `ComputeStep` gates on `useCloudEligibility()`, keeps a second guard inside the
  click handler, and shows the reason + coupon form + plans link when ineligible.
- `ProjectPicker`'s `ConnectDaemonModal` gated **only** on
  `capabilities.cloudDaemons` — a *deployment* flag, not an *entitlement*.
  `useCloudEligibility` was not referenced anywhere in that file.

So the button the user actually clicked was the **unguarded** one. That is the
client-side half of the P0.

I verified this is not theoretical by reverting the gate in #172: with it
removed, an ineligible user's click reaches `CreateDaemon`. With it in place, it
does not.

**This does not excuse the server.** A client gate is defense-in-depth and a UX
fix; `internal/svcdaemon.CreateDaemon` is the authority and its fix should land
regardless of #172.

---

## Corroborating production-shaped data

From the control-plane dev database (read-only):

- **29 of 43 users** have `onboarding_completed_at IS NULL`.
- **8 distinct users** have `onboarding_completed_at IS NULL` **and** at least
  one daemon — i.e. they paid the provisioning cost but never reached the step
  that records completion. All 8 of those daemon rows are named
  `onboarding-daemon` at `status = 3` (SUSPENDED).

This is the exact population the ejection bug produces, and it is why
`OnboardingRoute` already carries a self-heal path for "has a working daemon but
is flagged incomplete".

---

## A hazard I did not fix (flagging, not touching)

There are **two colliding `DaemonStatus` enums**, and the numbers disagree:

| value | control-plane | reliant registry |
|-------|---------------|------------------|
| 1 | `PENDING` | `ACTIVE` |
| 2 | `ACTIVE` | `IDLE` |
| 3 | `SUSPENDED` | `DISCONNECTED` |

The codebase is *aware* of this — `hasUsableControlPlaneDaemonForOnboarding` and
`cloudDaemonStatusLabel` both exist specifically to avoid it, with good comments.

But note what `OnboardingRoute`'s self-heal treats as proof of completion:
control-plane `ACTIVE` **or `PENDING`**. `PENDING` means *still provisioning*. So
a user whose daemon is merely pending can be marked onboarding-complete and sent
to `/` — where, having no ACTIVE daemon, they meet the project picker. That is a
second, independent route to the same bad screen.

I left this alone: it is a deliberate design decision with a written rationale
(avoiding a worse trap where users get permanently stuck), and changing it needs
a product judgement about which failure is preferable. Worth a conversation.

---

## Verification performed

- New test in #169 **confirmed failing before the fix** (navigate fired with
  `to: "/"` while the caller still had to show the gate) and passing after.
- New tests in #172 **confirmed failing** with the gate reverted (`CreateDaemon`
  fires for an ineligible user) and passing with it.
- Full web suite on both branches: **1903 passed / 255 files**, no regressions.
- `tsc --noEmit` clean on both; `eslint` clean on changed files.

## What I did NOT do

- Did not merge anything.
- Did not touch `internal/svcdaemon` (owned).
- Did not delete either onboarding tree.
- Did not patch the API-key modal ordering, because I could not prove the
  trigger. Recommendation is above.
