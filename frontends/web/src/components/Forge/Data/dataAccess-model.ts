// Copyright (c) 2025 Reliant Labs

/**
 * THE AUDITED-ENVIRONMENT CONFIRMATION, once per tab session.
 *
 * An audited environment (forge.DataAccess db_query "audited", the default
 * for a real one) refuses DescribeDatabase and BrowseTable without
 * confirm_env_id equal to the environment and a reason; each read is recorded
 * with that reason in the audit log and the Activity feed. The browser asks
 * once, in a dialog naming the environment, and then sends both on every read.
 *
 * The answer is kept in sessionStorage, per environment id: it lasts as long
 * as the tab (a reload keeps it, a new tab asks again) and never outlives the
 * session the person confirmed in. A reason is about one piece of work; a
 * permanent one would turn every later read into a misleading audit row.
 *
 * Nothing else here: no policy is decided client-side. The server decides
 * from the recorded release shape, and a `confirmation_required` refusal
 * (policy changed, storage cleared) clears the kept answer and asks again.
 */

import type { DataAccessConfirmation, DataAccessPolicyName } from "@/services/controlPlane/observability/appDatabaseApi";

/** The server's bound on reason (DescribeDatabaseRequest.reason max_len). */
export const REASON_MAX = 500;

const storageKey = (environmentId: string) => `forge.data.access.${environmentId}`;

export function readAccess(environmentId: string): DataAccessConfirmation | null {
  try {
    const raw = window.sessionStorage.getItem(storageKey(environmentId));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { reason?: unknown };
    const reason = typeof parsed.reason === "string" ? parsed.reason.trim() : "";
    return reason ? { confirmEnvId: environmentId, reason } : null;
  } catch {
    return null;
  }
}

export function writeAccess(environmentId: string, reason: string): DataAccessConfirmation {
  const trimmed = reason.trim().slice(0, REASON_MAX);
  try {
    window.sessionStorage.setItem(storageKey(environmentId), JSON.stringify({ reason: trimmed }));
  } catch {
    // Storage disabled: the confirmation holds for this page only.
  }
  return { confirmEnvId: environmentId, reason: trimmed };
}

export function clearAccess(environmentId: string): void {
  try {
    window.sessionStorage.removeItem(storageKey(environmentId));
  } catch {
    // Nothing kept.
  }
}

/** Whether a read under `policy` must carry a confirmation. */
export function needsConfirmation(policy: DataAccessPolicyName): boolean {
  return policy === "audited";
}

/** Whether reads under `policy` are refused outright (deny, or a policy that was not recorded). */
export function isDenied(policy: DataAccessPolicyName): boolean {
  return policy === "deny" || policy === "unknown";
}

/** The reason a person typed, as the server will take it: trimmed, non-empty, bounded. */
export function validReason(reason: string): string | null {
  const trimmed = reason.trim();
  return trimmed && trimmed.length <= REASON_MAX ? trimmed : null;
}
