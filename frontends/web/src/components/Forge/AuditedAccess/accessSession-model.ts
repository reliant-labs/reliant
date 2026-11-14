// Copyright (c) 2025 Reliant Labs

/**
 * THE AUDITED-ENVIRONMENT CONFIRMATION, once per tab session — for every
 * console surface that reads an environment's contents under its recorded
 * forge.DataAccess policy: the data browser (db_query) and the storage
 * browser (storage).
 *
 * An audited environment refuses those reads without confirm_env_id equal to
 * the environment and a reason; each read is recorded with that reason in the
 * audit log (and, for some, the Activity feed). The surface asks once, in a
 * dialog naming the environment (AccessReasonDialog), and then sends both on
 * every read.
 *
 * The answer is kept per surface, environment, authenticated principal and
 * in-memory auth epoch. Identity changes and reloads ask again. Legacy keys
 * are discarded, never migrated as another person's authority. A reason is
 * about one piece of work; the namespaces keep rows and files separate.
 *
 * Nothing else here: no policy is decided client-side. The server decides
 * from the recorded release shape, and a `confirmation_required` refusal
 * (policy changed, storage cleared) clears the kept answer and asks again.
 */

import { getReaderSession, isCurrentReader, readerStorageScope, type ReaderSession } from "./readerSession";

/** A recorded data-access policy, as the console reads it. `unknown`: not recorded or not understood, refused like deny. */
export type AccessPolicyName = "open" | "audited" | "deny" | "unknown";

/** What an audited environment needs on every read: confirm_env_id (the environment's own id) and a reason. */
export interface AccessConfirmation {
  confirmEnvId: string;
  reason: string;
}

/** Which surface's answer: "data" (the data browser), "storage" (the storage browser). */
export type AccessNamespace = "data" | "storage";

/** The servers' bound on reason (every audited request's reason max_len). */
export const REASON_MAX = 500;

export const accessStorageKey = (namespace: AccessNamespace, environmentId: string, reader = getReaderSession()) => `forge.${namespace}.access.${readerStorageScope(reader)}.${environmentId}`;

export function readAccess(namespace: AccessNamespace, environmentId: string, reader = getReaderSession()): AccessConfirmation | null {
  if (!isCurrentReader(reader)) return null;
  try {
    window.sessionStorage.removeItem(`forge.${namespace}.access.${environmentId}`);
    const raw = window.sessionStorage.getItem(accessStorageKey(namespace, environmentId, reader));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { reason?: unknown };
    const reason = typeof parsed.reason === "string" ? parsed.reason.trim() : "";
    return validReason(reason) ? { confirmEnvId: environmentId, reason } : null;
  } catch {
    return null;
  }
}

export function writeAccess(namespace: AccessNamespace, environmentId: string, reason: string, reader: ReaderSession = getReaderSession()): AccessConfirmation {
  const trimmed = reason.trim().slice(0, REASON_MAX);
  if (!isCurrentReader(reader)) throw new DOMException("Reader session changed", "AbortError");
  try {
    window.sessionStorage.setItem(accessStorageKey(namespace, environmentId, reader), JSON.stringify({ reason: trimmed }));
  } catch {
    // Storage disabled: the confirmation holds for this page only.
  }
  return { confirmEnvId: environmentId, reason: trimmed };
}

export function clearAccess(namespace: AccessNamespace, environmentId: string): void {
  try {
    window.sessionStorage.removeItem(accessStorageKey(namespace, environmentId));
  } catch {
    // Nothing kept.
  }
}

/** Whether a read under `policy` must carry a confirmation. */
export function needsConfirmation(policy: AccessPolicyName): boolean {
  return policy === "audited";
}

/** Whether reads under `policy` are refused outright (deny, or a policy that was not recorded). */
export function isDenied(policy: AccessPolicyName): boolean {
  return policy === "deny" || policy === "unknown";
}

/** The reason a person typed, as the server will take it: trimmed, non-empty, bounded. */
export function validReason(reason: string): string | null {
  const trimmed = reason.trim();
  return trimmed && trimmed.length <= REASON_MAX ? trimmed : null;
}
