// Copyright (c) 2025 Reliant Labs

/**
 * Which claim situation this browser is in, from who is signed in and how far
 * the pending claim got. A pure function so every row of the table below is a
 * unit test rather than a manual walk through a Supabase flow.
 *
 * The anonymous user that deployed the site keeps ONE user id through the whole
 * upgrade (updateUser / linkIdentity attach an identity to it, they do not
 * create another user). So "is the signed-in user the claim's own user" is a
 * comparison of ids, and it tells the stages of the settled flow apart:
 *
 *   nobody signed in ................... signed-out          open the session, or sign in
 *   the claim's user, still anonymous .. resume-upgrade      go on to /upgrade
 *   the claim's user, upgraded ......... finish              CompleteClaim
 *   another anonymous user ............. replace-anonymous   warn, then switch
 *   another, upgraded user ............. existing-account    claim into their account
 *                                                            (join the org as its owner)
 */

import type { PendingClaim } from "@/lib/claimLink";

export type ClaimSituation =
  | "signed-out"
  | "resume-upgrade"
  | "finish"
  | "replace-anonymous"
  | "existing-account";

/** The slice of a Supabase user the decision reads. */
export interface ClaimUser {
  id: string;
  is_anonymous?: boolean;
}

export function claimSituation(user: ClaimUser | null, pending: Pick<PendingClaim, "anonUserId">): ClaimSituation {
  if (!user) return "signed-out";
  const isClaimsUser = pending.anonUserId !== undefined && pending.anonUserId === user.id;
  if (user.is_anonymous === true) return isClaimsUser ? "resume-upgrade" : "replace-anonymous";
  return isClaimsUser ? "finish" : "existing-account";
}
