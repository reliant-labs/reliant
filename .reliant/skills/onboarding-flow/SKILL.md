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

Three places redirect a signed-in user whose `onboardingCompleted` is false:
ModernApp (`/`, `/project/$projectId`), MobileShell (`/m/*`), and
`OnboardingGate` on the `_authenticated` layout (every other app route —
`/forge/*`, `/inbox`, `/workflows`, … — except the exemptions listed in
`OnboardingFlow/onboardingGate.ts`: `/onboarding`, `/settings`, `/m`,
`/forge/demo`, `/apps`). The gate carries the page as `?returnTo=` and
`leaveOnboarding` lands every exit to the app there instead of `/`.

The forge site (forge.reliantlabs.io, `forgeShellRoutes.tsx`) runs the SAME
wizard at the same route id (`/_authenticated/onboarding`), behind the same
gate with its own exemptions (`FORGE_SHELL_ONBOARDING_EXEMPT_PREFIXES`:
`/onboarding`, `/settings`; `/apps` and the console are gated). Two marks
excuse a user there: a claim landing (`markClaimLanding`: only the claimed
app's `/forge/p/<app>/…` pages, for the claimant, in that tab, for 30 minutes —
any other page runs the wizard) and the escape hatch's "Only here to deploy
apps? Skip" (`markForgeSetupSkipped`, this browser, per user). Neither completes onboarding on the server. After `forge login` the
CLI's loopback page goes on to `<forge site>/apps` (forge `LoginSuccessHTML`).
The wizard's GitHub step returns through Reliant's callback via a
`/__forge/…` returnTo (`lib/crossShellReturn.ts`).
