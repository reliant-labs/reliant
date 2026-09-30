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
 * ── WHY MATCH ON THE MESSAGE ──────────────────────────────────────────
 *
 * There is no structured field to read. forge surfaces
 * internal/cloud.ErrNoCredential's text, whose wording is deliberately stable
 * and actionable, and control-plane's permission refusals arrive as Connect
 * errors whose code is already flattened into the message by the time the plan
 * call rejects. Matching is therefore the honest option, and it is written to
 * FAIL OPEN: anything unrecognised stays a transport error, so a real
 * unreachable daemon is never relabelled as a permissions problem.
 */

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
  const message = (error?.message ?? "").toLowerCase();
  if (message === "") return null;

  // PERMISSION IS CHECKED FIRST. A permission refusal can mention a
  // credential in passing, but a missing credential can never name a
  // permission — so the more specific test has to run first or the two
  // collapse back into one.
  if (
    message.includes("permission_denied") ||
    message.includes("permission denied") ||
    message.includes("permissions do not include")
  ) {
    return { kind: "permission-denied", permission: extractPermission(error?.message ?? "") };
  }

  if (
    message.includes("no control-plane credential") ||
    message.includes("forge login") ||
    message.includes("unauthenticated")
  ) {
    return { kind: "not-authorized" };
  }

  return null;
}

/**
 * extractPermission pulls the scope out of a refusal.
 *
 * Scopes are `family:verb` from a closed vocabulary
 * (forge/pkg/accesstoken.AllScopes), which is distinctive enough to find
 * without parsing the sentence around it. An empty string when none is named —
 * the panel then asks for "the permission this deploy needs", which is still
 * more use than a generic failure.
 */
function extractPermission(message: string): string {
  const match = message.match(
    /\b(deploy|secret|domain|cluster|token|llm|proxy|mcp|daemon|reliant):[a-z]+\b/
  );
  return match ? match[0] : "";
}
