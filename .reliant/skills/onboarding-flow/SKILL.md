---
name: onboarding-flow
description: How the onboarding step is derived (only from the `plan` URL param) and how a background effect in OnboardingRoute can complete onboarding with no user action. Load before changing or debugging onboarding, or anything that creates a daemon during it.
---

# Onboarding: the step you see is derived, and a background effect can end the flow

`deriveStep(plan)` (`frontends/web/src/components/OnboardingFlow/stepConfig.ts`) is the
ONLY thing that decides which step renders — from the `plan` search param in
the URL, nothing else. `onNext()` is a no-op. So "it skipped steps" is always a
question about what wrote `plan`, or about whether the flow was exited
entirely.

**`frontends/web/src/components/OnboardingFlow/OnboardingRoute.tsx` can complete
onboarding on its own, from a `useEffect`, with no user action.** The
returning-user heal fires when
`hasUsableControlPlaneDaemonForOnboarding(daemonsPostdating(daemons, user.createdAtMs))`
is true — a daemon that postdates the account — and it calls
`CompleteOnboarding` and navigates to `/`. Creating a daemon DURING onboarding
satisfies that condition, so any change that provisions a daemon mid-flow can
trip the heal and end onboarding rather than advance it. Verified in
platform-api's dev log (`admin-server.log`, its file name until the deploy rename ships): `daemon created name=onboarding-daemon` at 17:24:40.809,
`onboarding completed` 39ms later, with no step in between.

When debugging this flow, read `../control-plane/.forge/logs/dev/admin-server.log`
first — the RPC sequence (`RedeemCoupon` → `CreateDaemon` → `CompleteOnboarding`)
tells you what actually happened server-side, independent of any frontend
logging.

## Who gets sent to /onboarding, and where they come back to

Onboarding guards the CHAT routes only (U6): `/`, `/project/$projectId`,
`/m`, `/m/chats…`, `/m/new`, in either form (`/o/<org>/…`). The list is
`routeRequiresOnboarding` in `OnboardingFlow/onboardingGate.ts`, and all three
redirects ask it: ModernApp (`/`, `/project/$projectId`), MobileShell (`/m/*`)
and `OnboardingGate` on the `_authenticated` layout. Apps and every app's
console, Sites, Usage, Workflows, the Inbox and Settings never redirect, so a
person with no machine gets a working app there — and OnboardingRoute (with
its returning-user heal) is never mounted on the way. The gate carries the
page as `?returnTo=` and `leaveOnboarding` lands every exit to the app there
instead of `/`.

The forge host (forge.reliantlabs.io) mounts the same route tree
(`routes.tsx`) as the forge brand (`isForgeShell()`). On that brand a home
(`/`, `/o/<org>`, the phone's chat list) sends a person who has not onboarded
to `/apps` instead of the wizard (`forgeBrandHomeTarget`): `forge cloud login`, claim
links and every console link forge prints land on non-chat pages anyway, and a
forge signup is onboarded only when they open a chat. (#2866 sent new forge
accounts through "Choose your machine" before /apps and was reverted; the
chat-routes scope is what makes the one router safe for them.) "Why wasn't a
forge user onboarded?" starts at `forgeBrandHomeTarget`.
