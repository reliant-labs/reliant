// Copyright (c) 2025 Reliant Labs

/**
 * What the forge shell does with the journey the landing page carried
 * (`?journey=forge|workflows`, held through sign-in by lib/landingAttribution.ts).
 *
 *   forge      starts the forge journey. The demo console is public, so a
 *              signed-out visitor is taken straight to it rather than to
 *              sign-in; a signed-in one starts it on arrival, or after
 *              signing in when they came in through the sign-in page.
 *   workflows  is offered as a link to Reliant: workflows need a daemon and
 *              chats, which this shell does not have, so that journey never
 *              runs here (its `shells` exclude forge).
 *
 * The decision is split from ForgeLandingJourney so the auth guard can make
 * the same call: it must hold its sign-in redirect for exactly the visitor
 * the journey is about to take to the demo, or the two navigations race.
 */

import { isJourneyAvailable } from "@/components/Onboarding/journeys/registry";
import type { JourneyId } from "@/components/Onboarding/tourParam";
import { readLandingJourney } from "@/lib/landingAttribution";

import type { ForgeAuthDecision } from "./ForgeAuthGuard";

/** The id of the layout route ForgeAuthGuard renders (forgeShellRoutes.tsx `_authenticated`). */
export const GUARDED_ROUTE_ID = "/_authenticated";

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
  /** On a page behind ForgeAuthGuard. */
  guarded: boolean;
  auth: ForgeAuthDecision;
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
