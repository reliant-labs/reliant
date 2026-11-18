import { batchUpsertSettings, deleteSetting, fetchSettings } from "../hooks/settings-queries";
import { getAuthTokenProvider } from "../api/authProvider";
import { isWriteOwnerMismatch } from "../api/writeOwner";
import { settingsSync } from "../services/settingsSync";
import {
  credentialBelongsTo,
  currentSessionOwner,
  type SessionOwner,
} from "./auth/sessionOwner";
import { logger } from "./logger";

/**
 * Whether a settings RPC can possibly succeed right now.
 *
 * Every SettingsService RPC requires auth. While signed out the app was still
 * firing them — measured, 57 doomed calls over 11 minutes after one sign-out,
 * 36 of them GetSetting — and each 401 feeds the auto-sign-out handler, so a
 * logged-out storm is a session-stability foot-gun, not just log noise.
 *
 * This gate lives here, in the settings layer, rather than in the transport:
 * a blanket transport gate would have to know which RPCs are legitimately
 * unauthenticated and must not deadlock the sign-in flow itself. Settings are
 * unambiguously auth-only, so the narrow gate is both safe and sufficient.
 */
async function currentToken(): Promise<string | null> {
  try {
    return await getAuthTokenProvider().getToken();
  } catch {
    return null;
  }
}

async function hasAuthToken(): Promise<boolean> {
  return (await currentToken()) !== null;
}

/**
 * Whether a write queued for `owner` is worth sending: there is a credential,
 * and it still belongs to `owner`. Its token wait can span an identity change
 * (another host signing in as someone else through the shared session
 * cookie). This is the cheap early answer only: the transport resolves the
 * credential again when the request leaves, and that second wait can span a
 * change too, so the write also carries `owner` to the transport
 * (api/writeOwner.ts), which refuses to send it with anyone else's credential.
 */
async function mayWriteFor(owner: SessionOwner, keys: string[]): Promise<boolean> {
  const token = await currentToken();
  if (token === null) {
    logger.warn("[settingsPersistence] Skipping setting writes while unauthenticated", { keys });
    return false;
  }
  if (!credentialBelongsTo(owner, token)) {
    logger.warn("[settingsPersistence] Dropping setting writes queued for a previous identity", {
      keys,
    });
    return false;
  }
  return true;
}

export interface PersistedStringSetting {
  value: string;
}

/**
 * The outcome of a settings read. "missing" and "error" are deliberately
 * distinct: a caller deciding whether to re-show a UI the user already
 * dismissed must not treat a failed RPC as "no preference recorded".
 */
export type SettingRead =
  | { status: "found"; value: string }
  | { status: "missing" }
  | { status: "error" };

export async function readSetting(key: string): Promise<SettingRead> {
  // Fast path: read from the in-memory cache populated by ListSettings.
  if (settingsSync.isInitialized()) {
    const cached = settingsSync.getSettingFromCache(key);
    return cached ? { status: "found", value: cached.value } : { status: "missing" };
  }

  // Cache not ready. Rather than a per-key GetSetting — which is what made a
  // handful of stores hydrating five keys each into dozens of RPCs — wait for
  // the single ListSettings that settingsSync.initialize() already performs,
  // then serve from the cache it fills.
  if (!(await hasAuthToken())) return { status: "error" };

  try {
    await settingsSync.initialize();
    const cached = settingsSync.getSettingFromCache(key);
    return cached ? { status: "found", value: cached.value } : { status: "missing" };
  } catch {
    return { status: "error" };
  }
}

export async function safeGetSetting(
  key: string,
): Promise<PersistedStringSetting | null> {
  const read = await readSetting(key);
  return read.status === "found" ? { value: read.value } : null;
}

/**
 * Per-tick coalescing of setting writes.
 *
 * Writers naturally save several keys at once — the tour persists three on
 * every step transition, the tool-call panel one per category toggle. Each was
 * its own RPC. This collects every write issued during the current microtask
 * and sends ONE BatchUpsertSettings.
 *
 * Later writes to the same key within a window replace earlier ones: the last
 * value is the one the caller meant, and sending both would restore the
 * fan-out this exists to remove.
 */
interface PendingWrites {
  // The account the writes were made for: a batch never mixes two.
  owner: SessionOwner;
  values: Map<string, string>;
  waiters: Array<() => void>;
}

let pendingWrites: PendingWrites | null = null;

async function flushWrites(batch: PendingWrites): Promise<void> {
  const settings = [...batch.values.entries()].map(([key, value]) => ({
    key,
    value,
    valueType: "string",
  }));

  try {
    // The auth gate runs HERE, once for the whole batch, rather than in
    // upsertStringSetting. Awaiting a token before enqueuing would put each
    // caller in a different microtask and defeat the coalescing entirely.
    if (!(await mayWriteFor(batch.owner, settings.map((s) => s.key)))) return;
    await batchUpsertSettings(settings, undefined, batch.owner);
  } catch (error) {
    if (isWriteOwnerMismatch(error)) {
      logger.warn("[settingsPersistence] Dropped setting writes queued for a previous identity", {
        keys: settings.map((s) => s.key),
      });
      return;
    }
    logger.error("[settingsPersistence] Failed to upsert settings batch", {
      keys: settings.map((s) => s.key),
      error,
    });
  } finally {
    for (const waiter of batch.waiters) waiter();
  }
}

export function upsertStringSetting(key: string, value: string): Promise<void> {
  // Update the in-memory cache immediately so subsequent reads see the new
  // value regardless of whether the write reaches the server.
  settingsSync.updateCache(key, value);

  // Enqueue SYNCHRONOUSLY — no await before this point, or callers in the same
  // tick would land in different batches.
  const owner = currentSessionOwner();
  if (!pendingWrites || pendingWrites.owner !== owner) {
    const batch: PendingWrites = { owner, values: new Map(), waiters: [] };
    pendingWrites = batch;
    void Promise.resolve().then(() => {
      if (pendingWrites === batch) pendingWrites = null;
      void flushWrites(batch);
    });
  }

  const batch = pendingWrites;
  batch.values.set(key, value);
  return new Promise<void>((resolve) => {
    batch.waiters.push(resolve);
  });
}

/**
 * The setting as the server holds it now: one ListSettings, never the
 * in-memory cache. "error" when it cannot be read (signed out, or the request
 * failed), so a caller about to rewrite a whole document never mistakes a
 * failed read for "nothing stored". A found or missing answer also brings the
 * cache up to date.
 */
export async function readSettingFromServer(key: string): Promise<SettingRead> {
  if (!(await hasAuthToken())) return { status: "error" };
  try {
    const response = await fetchSettings();
    const setting = response.settings.find((s) => s.key === key && !s.projectId);
    if (!setting) {
      settingsSync.removeFromCache(key);
      return { status: "missing" };
    }
    settingsSync.updateCache(key, setting.value);
    return { status: "found", value: setting.value };
  } catch (error) {
    logger.warn("[settingsPersistence] Failed to read setting from the server", { key, error });
    return { status: "error" };
  }
}

/** A setting write that did not reach the server (writeStringSetting). */
export class SettingWriteError extends Error {
  constructor(key: string, options?: { cause?: unknown }) {
    super(`Could not save setting ${key}`, options);
    this.name = "SettingWriteError";
  }
}

/**
 * Upsert one setting and say whether it landed: resolves once the server
 * stored it, and rejects (SettingWriteError) when it did not — signed out, a
 * credential that moved to another account, or a refused or failed request.
 * The cache changes only after the server has stored the value. For a caller
 * that must recover from a refused write; upsertStringSetting is the
 * fire-and-forget, batched writer every other setting uses.
 */
export async function writeStringSetting(key: string, value: string): Promise<void> {
  const owner = currentSessionOwner();
  if (!(await mayWriteFor(owner, [key]))) throw new SettingWriteError(key);
  try {
    await batchUpsertSettings([{ key, value, valueType: "string" }], undefined, owner);
  } catch (error) {
    throw new SettingWriteError(key, { cause: error });
  }
  settingsSync.updateCache(key, value);
}

export async function deleteSettingIfExists(key: string): Promise<void> {
  const owner = currentSessionOwner();
  settingsSync.removeFromCache(key);
  if (!(await mayWriteFor(owner, [key]))) return;
  try {
    await deleteSetting(key, undefined, owner);
  } catch {
    // Ignore missing setting, transient errors, and a delete the transport
    // dropped because the credential moved to another account.
  }
}