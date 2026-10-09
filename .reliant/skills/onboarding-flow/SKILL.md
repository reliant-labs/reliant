---
name: onboarding-flow
description: How the onboarding step is derived (only from the `plan` URL param) and how a background effect in OnboardingRoute can complete onboarding with no user action. Load before changing or debugging onboarding, or anything that creates a daemon during it.
---

# Onboarding: the step you see is derived, and a background effect can end the flow

`deriveStep(plan)` (`web/src/components/OnboardingFlow/stepConfig.ts`) is the
ONLY thing that decides which step renders — from the `plan` search param in
the URL, nothing else. `onNext()` is a no-op. So "it skipped steps" is always a
question about what wrote `plan`, or about whether the flow was exited
entirely.

**`web/src/components/OnboardingFlow/OnboardingRoute.tsx` can complete
onboarding on its own, from a `useEffect`, with no user action.** The
returning-user heal fires when
`hasUsableControlPlaneDaemonForOnboarding(daemonsPostdating(daemons, user.createdAtMs))`
is true — a daemon that postdates the account — and it calls
`CompleteOnboarding` and navigates to `/`. Creating a daemon DURING onboarding
satisfies that condition, so any change that provisions a daemon mid-flow can
trip the heal and end onboarding rather than advance it. Verified in
`admin-server.log`: `daemon created name=onboarding-daemon` at 17:24:40.809,
`onboarding completed` 39ms later, with no step in between.

When debugging this flow, read `../control-plane/.forge/logs/dev/admin-server.log`
first — the RPC sequence (`RedeemCoupon` → `CreateDaemon` → `CompleteOnboarding`)
tells you what actually happened server-side, independent of any frontend
logging.
