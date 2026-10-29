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
    body: "A claim can be started in one browser only. Continue there, or sign in to an account you already have and claim it into that.",
    // Returns here, where the claim is still in storage; the site is claimed
    // into the account that signs in, which needs no session from this link.
    next: { label: "Sign in to claim", href: "/auth?redirect=%2Fclaim" },
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
 * Someone who may already have a Reliant account, and is not signed in to it
 * here (signed out, or holding the site's temporary account). They sign in and
 * come back to this page, where ClaimIntoAccount takes over.
 */
export const EXISTING_ACCOUNT_COPY = {
  heading: "Already on Reliant?",
  body: "Sign in to your account and this site is added to it, as an organization you own.",
  button: "Sign in to claim",
  /** Said only when leaving would drop the site's temporary account. */
  temporary: "This signs out of the site's temporary account. The site is still yours to claim from your own account.",
} as const;

/**
 * Someone signed in to a real account: claiming joins the site's organization
 * as its owner. Said before they confirm, because it is a different kind of
 * claim from "keep it as a new account".
 */
export const CLAIM_INTO_ACCOUNT_COPY = {
  heading: "Claim it into your account",
  /** Takes the signed-in email. */
  signedInAs: (email: string) => `You're signed in as ${email}.`,
  body: "The site comes with its own organization. You'll own it, and it appears beside your other organizations; your own stays your default.",
  freeSite: "The free plan includes one static site. If you already have one, Reliant keeps your oldest live and pauses this one until you subscribe or delete a site.",
  button: "Claim into my account",
  switchAccount: "Not you? Use a different account",
} as const;

/** Why signing in as a different account does not claim the site. */
export const REPLACE_ANONYMOUS_COPY = {
  heading: "You're in a temporary account",
  body: "Claiming this site signs this browser out of that temporary account, and anything in it that has not been saved is lost.",
} as const;
