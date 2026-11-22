// Copyright (c) 2025 Reliant Labs

/**
 * What the forge brand (lib/shell.ts) does with the journey the landing page carried
 * (`?journey=forge|workflows`, held through sign-in by lib/landingAttribution.ts).
 *
 *   forge      starts the forge journey. The demo console is public, so a
 *              signed-out visitor is taken straight to it rather than to
 *              sign-in; a signed-in one starts it on arrival, or after
 *              signing in when they came in through the sign-in page.
 *   workflows  is offered as a link to Reliant: that journey's `shells`
 *              exclude forge (Onboarding/journeys/workflows.tsx), so it never
 *              runs on the forge brand.
 *
 * The decision is split from ForgeLandingJourney so the auth guard
 * (AuthGuard, on the forge brand) can make the same call: it must hold its
 * sign-in redirect for exactly the visitor the journey is about to take to
 * the demo, or the two navigations race.
 */

import { isJourneyAvailable } from "@/components/Onboarding/journeys/registry";
import type { JourneyId } from "@/components/Onboarding/tourParam";
import type { AuthIdentity } from "@/lib/auth/use-auth";
import { readLandingJourney } from "@/lib/landingAttribution";

/**
 * The ids of the layout routes whose AuthGuard asks for sign-in (routes.tsx):
 * `_authenticated` (every app page but the chats) and `_app` (the chat home
 * and a project's chats). Read org-free (orgFreeRouteId), so the org tree's
 * twins match too.
 */
export const GUARDED_ROUTE_IDS: readonly string[] = ["/_authenticated", "/_app"];

/** What the sign-in guard does for an auth state, as far as the journey is concerned. */
export type LandingAuthDecision = "wait" | "sign-in" | "verify-email" | "allow";

type GuardIdentity = Pick<AuthIdentity, "isAnonymous" | "emailVerified">;

/**
 * What AuthGuard (requireAuth, requireEmailVerification) does for this auth
 * state. Pure. An anonymous session (a claim in flight) is let through, as the
 * guard does; an email account must have confirmed its address.
 */
export function landingAuthDecision(state: { initialized: boolean; loading: boolean; identity: GuardIdentity | null }): LandingAuthDecision {
  if (!state.initialized || state.loading) return "wait";
  if (!state.identity) return "sign-in";
  if (!state.identity.isAnonymous && !state.identity.emailVerified) return "verify-email";
  return "allow";
}

export type LandingJourneyAction =
  /** Nothing held. */
  | "none"
  /** Held for later: the visitor is signing in, or on a public page of their own (a claim). */
  | "wait"
  /** Forget it: a tour is already open, or the forge journey is switched off. */
  | "drop"
  | "start-forge"
  | "offer-reliant";

export interface LandingJourneyInputs {
  held: JourneyId | undefined;
  /** A `?tour=` is in the URL. */
  tourOpen: boolean;
  forgeAvailable: boolean;
  /** On the public demo console. */
  onDemo: boolean;
  /** On a page behind the sign-in guard (GUARDED_ROUTE_IDS). */
  guarded: boolean;
  auth: LandingAuthDecision;
}

/** What to do with the held journey here and now. Pure. */
export function landingJourneyAction({ held, tourOpen, forgeAvailable, onDemo, guarded, auth }: LandingJourneyInputs): LandingJourneyAction {
  if (!held) return "none";
  if (held === "workflows") return "offer-reliant";
  if (tourOpen || !forgeAvailable) return "drop";
  if (onDemo) return "start-forge";
  if (!guarded) return "wait";
  // Signed in: start it. Signed out: the demo needs no account, so start it
  // there instead of sending the visitor to sign in (the guard holds for it).
  return auth === "allow" || auth === "sign-in" ? "start-forge" : "wait";
}

/** Whether `href` carries a `?tour=`. */
export function hasTourParam(href: string): boolean {
  try {
    return new URL(href, "http://forge.invalid").searchParams.has("tour");
  } catch {
    return false;
  }
}

/**
 * Whether the guard, about to send a signed-out visitor at `href` to sign in,
 * should leave them to ForgeLandingJourney, which takes them to the demo.
 * Agrees with landingJourneyAction for a guarded page and `auth: "sign-in"`.
 */
export function landingJourneyHoldsSignIn(href: string, held: JourneyId | undefined = readLandingJourney()): boolean {
  return held === "forge" && !hasTourParam(href) && isJourneyAvailable("forge");
}
