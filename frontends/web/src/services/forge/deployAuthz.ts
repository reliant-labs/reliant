// Copyright (c) 2025 Reliant Labs

/**
 * WHY A DEPLOY WAS BLOCKED BEFORE IT PLANNED — and the two answers that are not
 * the same problem.
 *
 * A hosted deploy authenticates to the control plane before it can produce a
 * plan, so both of these arrive as a FAILED PLAN CALL rather than as a plan
 * document with blockers in it. That is why they are classified here from the
 * error rather than read off a report: at this point there is no report.
 *
 *   not-authorized      forge has no usable control-plane credential on this
 *                       machine. The user can fix it themselves.
 *   permission-denied   forge authenticated fine; the acting user's ORG
 *                       PERMISSIONS do not include what the deploy needs. No
 *                       amount of signing in changes this.
 *
 * Collapsing them loses the only thing that matters — who can unblock it. And
 * the previous copy ("could not reach your daemon") was worse than vague: the
 * daemon WAS reached and did answer, so it sent people to debug connectivity
 * over a permissions problem.
 *
 * ── READ BY REASON ─────────────────────────────────────────────────────
 *
 * The plan call's error names which with a reason (lib/errorReasons.ts):
 * `scope_missing`, with the scopes in `x-forge-error-scopes`, or
 * `no_control_plane_credential`. Until the servers send them (R3s-rel /
 * R3s-cp) the old message markers answer instead — forge's
 * internal/cloud.ErrNoCredential text, and control-plane's permission refusal
 * flattened into the message — and that fallback lives in errorReasons, not
 * here. Either way it FAILS OPEN: anything unrecognised stays a transport
 * error, so a real unreachable daemon is never relabelled as a permissions
 * problem.
 */

import { errorScopes, hasErrorReason } from "@/lib/errorReasons";

/** How a pre-plan failure should be presented. */
export type DeployAuthzBlock =
  /** No usable control-plane credential. Signing in to Reliant fixes it. */
  | { kind: "not-authorized" }
  /**
   * Authenticated, but the acting user lacks an org permission. `permission`
   * is the scope when forge/control-plane named one — it usually does, and
   * naming it is what lets the user ask for the right grant.
   */
  | { kind: "permission-denied"; permission: string };

/**
 * classifyDeployAuthzError inspects a failed plan call.
 *
 * Returns null for anything it does not positively recognise, which the caller
 * renders as the transport error it has always been.
 */
export function classifyDeployAuthzError(
  error: Error | null | undefined
): DeployAuthzBlock | null {
  if (!error) return null;

  // PERMISSION IS CHECKED FIRST. A permission refusal can mention a
  // credential in passing, but a missing credential can never name a
  // permission — so the more specific test has to run first or the two
  // collapse back into one.
  if (hasErrorReason(error, "scope_missing")) {
    return { kind: "permission-denied", permission: errorScopes(error).join(", ") };
  }

  if (hasErrorReason(error, "no_control_plane_credential")) {
    return { kind: "not-authorized" };
  }

  return null;
}
