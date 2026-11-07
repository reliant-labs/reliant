// Copyright (c) 2025 Reliant Labs

/**
 * The storage browser's two per-tab-session facts:
 *
 *   - the audited environment's reason (AuditedAccess/accessSession-model,
 *     namespace "storage": a reason for reading files is not one for reading
 *     rows), and
 *   - which buckets this tab has opened. ListObjects records an Activity line
 *     only when `open_bucket` is set, and the console sets it on its FIRST
 *     listing of a bucket in a tab session, whatever the prefix (a deep link
 *     opens a bucket below its root): the proto's ListObjects comment says
 *     why it is a flag. Kept in sessionStorage beside the reason, so a reload
 *     is not a second open and a new tab is.
 */

import { clearAccess, readAccess, writeAccess, type AccessConfirmation } from "../AuditedAccess/accessSession-model";

export const readStorageAccess = (environmentId: string): AccessConfirmation | null => readAccess("storage", environmentId);
export const writeStorageAccess = (environmentId: string, reason: string): AccessConfirmation => writeAccess("storage", environmentId, reason);
export const clearStorageAccess = (environmentId: string): void => clearAccess("storage", environmentId);

const openedKey = (environmentId: string, bucket: string) => `forge.storage.opened.${environmentId}.${bucket}`;
// If storage is blocked, preserve once-per-tab behaviour for this page's life.
const memoryOpened = new Set<string>();

/** Whether this tab already recorded opening the bucket, with a page-lifetime fallback when storage is blocked. */
export function wasBucketOpened(environmentId: string, bucket: string): boolean {
  try {
    return window.sessionStorage.getItem(openedKey(environmentId, bucket)) === "1";
  } catch {
    return memoryOpened.has(openedKey(environmentId, bucket));
  }
}

/** Record that the open was sent (after the listing that carried it succeeded). */
export function markBucketOpened(environmentId: string, bucket: string): void {
  memoryOpened.add(openedKey(environmentId, bucket));
  try {
    window.sessionStorage.setItem(openedKey(environmentId, bucket), "1");
  } catch {
    // Storage disabled: memoryOpened holds the marker until reload.
  }
}
