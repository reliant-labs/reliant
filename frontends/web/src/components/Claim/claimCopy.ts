// Copyright (c) 2025 Reliant Labs

/**
 * The words of the claim page, apart from its layout, so the states can be
 * checked against the design without rendering and so W11 changes one block.
 */

import type { ClaimRefusal } from "@/services/controlPlane/claim";
import { appsPath, authPath, claimPath } from "@/lib/paths";

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
    next: { label: "Go to Reliant", href: appsPath() },
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
    body: "A claim can be started in one browser only, so you can keep going in that one. You can also claim it from here: enter your email and we'll send a code. That creates your account, or signs you in if you have one, and the site is added to it.",
    // Returns here, where the claim is still in storage. The site is claimed
    // into the account that signs in or is created (CompleteClaim's membership
    // path), which needs no session from this link: the token still has to be
    // in this browser, so only the holder of the claim link can do this.
    next: { label: "Create an account or sign in to claim", href: `${authPath()}?redirect=${encodeURIComponent(claimPath())}` },
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
  freeSite: "If this claim affects sites you already host for free, we'll ask where you want free hosting to stay before you confirm.",
  button: "Claim into my account",
  switchAccount: "Not you? Use a different account",
} as const;

/** Why signing in as a different account does not claim the site. */
export const REPLACE_ANONYMOUS_COPY = {
  heading: "You're in a temporary account",
  body: "Claiming this site signs this browser out of that temporary account, and anything in it that has not been saved is lost.",
} as const;

/**
 * The signed-in choice (FreeSiteDialog) works with both the legacy site choice
 * and the organization allowance. The server names a site on each side, not
 * every affected site or the names of the organizations.
 */
export const FREE_SITE_COPY = {
  title: "Choose where free hosting stays",
  intro: "Claiming this site can affect which sites stay live. Review what may pause before you continue.",
  sitesLabel: "Sites in this choice",
  previewLabel: "Open a site to check it",
  /** Takes the site's address. */
  open: (address: string) => `Open ${address}`,
  yourSite: "Your existing site",
  thisSite: "Site you're claiming",
  legend: "Where free hosting stays",
  keepNew: {
    title: "Use free hosting for the site you’re claiming",
    /** Takes the existing site's address. */
    detail: (existing: string) => `${existing} may pause, along with other sites relying on free hosting in its organization.`,
  },
  keepExisting: {
    title: "Keep free hosting where it is",
    /** Takes the claimed site's address. */
    detail: (claimed: string) => `${claimed} is added to your account but may pause because free hosting stays with your existing site.`,
  },
  subscribe: {
    title: "Review billing before claiming",
    detail: "Go to billing. Your claim waits here and you can finish it when you come back.",
  },
  cancel: "Cancel",
  continue: "Continue",
  continueSubscribe: "Go to billing",
  /** Takes the date and whether the site is already offline. */
  waits: (date: string, offline: boolean) =>
    offline ? `Claim it by ${date} or it is deleted.` : `The claim link works until ${date}.`,
} as const;
