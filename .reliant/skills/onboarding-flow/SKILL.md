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

## The daemonless home: Projects first, a machine when they go for a chat

Owner rule (2026-11-19): don't push people through machine setup up front.
`chatSetup` (`onboardingGate.ts`) says what chats need from the person:
`onboard` (onboarding not completed), `connect-machine` (completed, but the
daemon list settled with nothing passing `hasUsableDaemonForOnboarding`,
`OnboardingFlow/usableDaemon.ts`), `ready`, or `checking`/`unknown` (the user
record or daemon list is loading or failed — nothing moves on a guess, so
nothing flaps). On BOTH brands:

- The desktop home (`/`, `/o/<org>`) sends `onboard` and `connect-machine` to
  Projects (`homeTarget` → `projectsPath()`), unless the URL asks for a chat
  (`?plan=`, `?start=`, `?reset-onboarding`), which onboards as before.
  ModernApp runs this through `useChatRouteRedirect` (`useChatSetup.ts`).
- The nav's Chats section becomes the "Set up chats" card
  (`SetUpChatsCard`), and so does the top of Projects; New chat goes where
  the card does (`chatSetupPath`): `/onboarding` for `onboard`, Settings ›
  Machines for `connect-machine` — never the wizard for someone already
  complete, because OnboardingRoute leaves at once for them
  (`useArrivedComplete`) and the home would send them straight back.
- A project's chats (`/project/<id>`) and a chat by its URL (U5's
  `/c/<chatId>[/t/<threadId>]`) still send a person who has not onboarded
  through `/onboarding?returnTo=…` and back; an onboarded person with no
  machine stays on the chat (it has its own Connect a machine).
- The new-chat view by its URL (U5's `/new`) is New chat: a person who has
  not onboarded goes through `/onboarding?returnTo=…`; an onboarded person
  with no machine goes to Settings › Machines (`chatSetupPath`), held while
  the daemon list is still loading (`chatRouteAction`).
- The phone keeps its rule: on the forge brand its chat list (`/m`,
  `/m/chats`) sends a person who has not onboarded to `/apps`; otherwise the
  chat-route redirect applies (MobileShell).

None of this mounts OnboardingRoute or calls CompleteOnboarding:
`src/__tests__/daemonlessShell.routes.test.tsx` renders the production tree on
both brands × `web.org_routes` off/on and proves it for Projects, Apps, an
app's console, the Inbox, Workflows, Usage, Settings and the home redirect.
"Why wasn't a forge user onboarded?" — because they never opened a chat; they
are onboarded the first time they do.
