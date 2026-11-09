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
import * as session from "../AuditedAccess/accessSession-model";
import { getReaderSession } from "../AuditedAccess/readerSession";

/** The server's bound on reason (DescribeDatabaseRequest.reason max_len). */
export const REASON_MAX = 500;

export function readAccess(environmentId: string, reader = getReaderSession()): DataAccessConfirmation | null {
  return session.readAccess("data", environmentId, reader);
}

export function writeAccess(environmentId: string, reason: string, reader = getReaderSession()): DataAccessConfirmation {
  return session.writeAccess("data", environmentId, reason, reader);
}

export function clearAccess(environmentId: string, reader = getReaderSession()): void {
  session.clearAccess("data", environmentId, reader);
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
