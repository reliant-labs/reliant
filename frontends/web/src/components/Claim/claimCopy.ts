// Copyright (c) 2025 Reliant Labs

/**
 * The words of the claim page, apart from its layout, so the states can be
 * checked against the design without rendering and so W11 changes one block.
 */

import type { ClaimRefusal } from "@/services/controlPlane/claim";

export interface RefusalCopy {
  title: string;
  body: string;
  /** What the person can do next, as a link. Absent when there is nothing to do. */
  next?: { label: string; href: string };
}

export const REFUSAL_COPY: Record<ClaimRefusal, RefusalCopy> = {
  claimable_claimed: {
    title: "This site has already been claimed",
    body: "Someone claimed it first, so it now belongs to their account. If that was you, sign in to see it.",
    next: { label: "Go to Reliant", href: "/apps" },
  },
  claimable_expired: {
    title: "This site has expired",
    body: "It was not claimed in time, so it and its data were deleted. Ask your agent to deploy it again, and claim the new link within 72 hours.",
  },
  claimable_taken_down: {
    title: "This site was removed",
    body: "An administrator took this site down, so it cannot be claimed.",
  },
  claimable_not_found: {
    title: "We can't find this claim",
    body: "The link is incomplete or mistyped. Ask your agent for the claim link again, and open it exactly as it was given to you.",
  },
  claimable_session_released: {
    title: "This claim is open in another browser",
    body: "A claim can be started in one browser only. Continue there, or sign in to an existing account.",
    next: { label: "Sign in", href: "/auth" },
  },
};

/** CompleteClaim said no for a reason this page does not know. Nothing was changed. */
export const FINISH_DECLINED_COPY = {
  title: "We couldn't complete this claim",
  body: "Reliant declined to finish it, and your site was not changed. Try again, or ask your agent for a new claim link.",
} as const;

/** CompleteClaim keeps asking for an upgrade the page already sent the person to make. */
export const UPGRADE_STILL_REQUIRED_MESSAGE =
  "We couldn't confirm your email yet. Check that you've verified it, then try again.";

/**
 * What an existing Reliant user is told. The way in for them (joining the
 * site's org) needs users to belong to more than one org, which is not built
 * (design §9, slice M1a); a signed-in redeploy gets them the same site.
 *
 * ONE place, so W11 replaces this block and nothing else.
 */
export const EXISTING_ACCOUNT_COPY = {
  heading: "Already on Reliant?",
  command: "forge login",
} as const;

/** Why signing in as a different account does not claim the site. */
export const REPLACE_ANONYMOUS_COPY = {
  heading: "You're in a temporary account",
  body: "Claiming this site signs this browser out of that temporary account, and anything in it that has not been saved is lost.",
} as const;
