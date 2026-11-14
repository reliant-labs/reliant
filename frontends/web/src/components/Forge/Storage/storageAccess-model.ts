// Copyright (c) 2025 Reliant Labs
import { clearAccess, readAccess, writeAccess, type AccessConfirmation } from "../AuditedAccess/accessSession-model";
import { getReaderSession, isCurrentReader, readerStorageScope } from "../AuditedAccess/readerSession";

export const readStorageAccess = (environmentId: string): AccessConfirmation | null => readAccess("storage", environmentId);
export const writeStorageAccess = (environmentId: string, reason: string): AccessConfirmation => writeAccess("storage", environmentId, reason);
export const clearStorageAccess = (environmentId: string): void => clearAccess("storage", environmentId);
const openedKey = (environmentId: string, bucket: string, reader = getReaderSession()) => `forge.storage.opened.${readerStorageScope(reader)}.${environmentId}.${bucket}`;
// Retain only this epoch's memory fallback; late completions never mark the next reader.
let memoryEpoch = "";
const memoryOpened = new Set<string>();
function memoryFor(reader = getReaderSession()) {
  if (memoryEpoch !== reader.epoch) { memoryOpened.clear(); memoryEpoch = reader.epoch; }
  return memoryOpened;
}
export function wasBucketOpened(environmentId: string, bucket: string, reader = getReaderSession()): boolean {
  if (!isCurrentReader(reader)) return false;
  try {
    window.sessionStorage.removeItem(`forge.storage.opened.${environmentId}.${bucket}`);
    return window.sessionStorage.getItem(openedKey(environmentId, bucket, reader)) === "1";
  } catch { return memoryFor(reader).has(openedKey(environmentId, bucket, reader)); }
}
export function markBucketOpened(environmentId: string, bucket: string, reader = getReaderSession()): void {
  if (!isCurrentReader(reader)) return;
  memoryFor(reader).add(openedKey(environmentId, bucket, reader));
  try { window.sessionStorage.setItem(openedKey(environmentId, bucket, reader), "1"); }
  catch { /* The current epoch's memory fallback remains. */ }
}
