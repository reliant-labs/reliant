// Copyright (c) 2025 Reliant Labs

/**
 * ClaimableService — the three rpcs the /claim page makes
 * (controlplane.v1.ClaimableService; design: control-plane
 * docs/design/claimable-deploys.md §4).
 *
 *   GetClaim          unauthenticated. What is being claimed, and whether it
 *                     still can be. Returns no secret.
 *   OpenClaimSession  unauthenticated. Releases the anonymous GoTrue session
 *                     that deployed the site, AT MOST ONCE.
 *   CompleteClaim     authenticated as the upgraded (non-anonymous) user.
 *
 * The claim token rides in the request BODY of the first two, never in a URL
 * or a header. The refusals the page must tell apart are named by stable
 * reason codes in `x-forge-error-reason` (lib/errorReasons.ts), so this module
 * answers in a closed `ClaimRefusal` vocabulary rather than in server prose.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  ClaimableService,
  ClaimableState,
} from "@/gen/controlplane/services/claimable/v1/claimable_pb";
import { errorReason, hasErrorReason, type ErrorReason } from "@/lib/errorReasons";

import { getControlPlaneClient } from "./client";

/** A claim the page cannot go ahead with, and why. One word per UI state. */
export const CLAIM_REFUSALS = [
  "claimable_claimed",
  "claimable_expired",
  "claimable_taken_down",
  "claimable_not_found",
  "claimable_session_released",
] as const satisfies readonly ErrorReason[];

export type ClaimRefusal = (typeof CLAIM_REFUSALS)[number];

/** What GetClaim says about the deploy: the page's preview. */
export interface ClaimSite {
  state: "live" | "suspended";
  project: string;
  env: string;
  /** Public hostnames, as the server names them (no scheme). */
  hostnames: string[];
  /** When the site goes offline. */
  expiresAt?: Date;
  /** When the offline site is deleted for good. */
  purgeAfter?: Date;
}

/** GetClaim's answer: a site that can be claimed, or the reason it cannot. */
export type ClaimPreview =
  | { claimable: true; site: ClaimSite }
  | { claimable: false; refusal: ClaimRefusal };

/** The released anonymous session, for supabase.auth.setSession. */
export interface ReleasedSession {
  accessToken: string;
  refreshToken: string;
  /** Seconds until accessToken expires. */
  expiresIn: number;
}

export interface CompletedClaim {
  orgId: string;
  /** "upgrade" (the same user, now with an email) or "membership" (an existing user joined the org as owner). */
  claimPath: string;
  hostnames: string[];
  /** Deploys held by billing: the claimed org is ordinary from here on. */
  holds: number;
  /**
   * Membership claims only: the claimer now owns more static sites than the
   * one free site per person allows, so Reliant serves their oldest free and
   * pauses the rest until they subscribe or delete one.
   */
  exceedsFreeSiteAllowance: boolean;
}

function date(ts: Timestamp | undefined): Date | undefined {
  return ts ? timestampDate(ts) : undefined;
}

const REFUSAL_OF_STATE: Partial<Record<ClaimableState, ClaimRefusal>> = {
  [ClaimableState.CLAIMED]: "claimable_claimed",
  [ClaimableState.TAKEN_DOWN]: "claimable_taken_down",
  [ClaimableState.EXPIRED]: "claimable_expired",
};

/**
 * The refusal an error carries, or null when it is not one the page knows
 * (a transport failure, an outage, a server that answers Unimplemented).
 * A bare NotFound is "no such claim" even without a reason: the server makes
 * that one answer for "never existed" and "wrong token".
 */
export function claimRefusalOf(err: unknown): ClaimRefusal | null {
  for (const reason of CLAIM_REFUSALS) {
    if (hasErrorReason(err, reason)) return reason;
  }
  if (err instanceof ConnectError && err.code === Code.NotFound) return "claimable_not_found";
  return null;
}

/**
 * What CompleteClaim can ask the page to DO rather than just report: it is
 * PermissionDenied, and it does not end the claim.
 */
export const COMPLETE_ACTIONS = ["claimable_upgrade_required"] as const satisfies readonly ErrorReason[];

export type CompleteAction = (typeof COMPLETE_ACTIONS)[number];

export function completeActionOf(err: unknown): CompleteAction | null {
  for (const reason of COMPLETE_ACTIONS) {
    if (hasErrorReason(err, reason)) return reason;
  }
  return null;
}

/** The server answered and said no, in a way this page has no name for (not an outage). */
export function isDeclined(err: unknown): boolean {
  return err instanceof ConnectError && (err.code === Code.PermissionDenied || err.code === Code.FailedPrecondition);
}

/** The reason for the claim_failed funnel event: a server reason, a code, or "unknown". */
export function failureReasonOf(err: unknown): string {
  const reason = errorReason(err);
  if (reason) return reason;
  if (err instanceof ConnectError) return Code[err.code]?.toLowerCase() ?? "unknown";
  return "unknown";
}

export async function getClaim(claimToken: string): Promise<ClaimPreview> {
  let res;
  try {
    res = await getControlPlaneClient(ClaimableService).getClaim({ claimToken });
  } catch (err) {
    const refusal = claimRefusalOf(err);
    if (refusal) return { claimable: false, refusal };
    throw err;
  }

  const refused = REFUSAL_OF_STATE[res.state];
  if (refused) return { claimable: false, refusal: refused };
  if (res.state !== ClaimableState.LIVE && res.state !== ClaimableState.SUSPENDED) {
    throw new Error(`GetClaim answered an unknown state (${res.state})`);
  }
  // `claimable` is the server's verdict on whether CompleteClaim can succeed
  // now; a live or suspended deploy that says no is not one to offer.
  if (!res.claimable) return { claimable: false, refusal: "claimable_expired" };

  return {
    claimable: true,
    site: {
      state: res.state === ClaimableState.LIVE ? "live" : "suspended",
      project: res.project,
      env: res.env,
      hostnames: res.hostnames,
      expiresAt: date(res.expiresAt),
      purgeAfter: date(res.purgeAfter),
    },
  };
}

export async function openClaimSession(claimToken: string): Promise<ReleasedSession> {
  const res = await getControlPlaneClient(ClaimableService).openClaimSession({ claimToken });
  return {
    accessToken: res.accessToken,
    refreshToken: res.refreshToken,
    expiresIn: Number(res.expiresIn),
  };
}

export async function completeClaim(claimToken: string): Promise<CompletedClaim> {
  const res = await getControlPlaneClient(ClaimableService).completeClaim({ claimToken });
  return {
    orgId: res.orgId,
    claimPath: res.claimPath,
    hostnames: res.hostnames,
    holds: res.holds.length,
    exceedsFreeSiteAllowance: res.exceedsFreeSiteAllowance,
  };
}
