import { api } from "../api/client";
import { settingsSync } from "../services/settingsSync";
import { logger } from "./logger";

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

  // Fallback: cache not ready yet, do an individual RPC.
  try {
    const setting = await api.settings.getSetting(key);
    return setting ? { status: "found", value: setting.value } : { status: "missing" };
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

export async function upsertStringSetting(
  key: string,
  value: string,
): Promise<void> {
  // Decide create-vs-update from the in-memory cache (populated by
  // ListSettings on init) rather than a per-key network GET — that extra
  // round-trip doubled the RPC count on every write. Snapshot existence
  // BEFORE updateCache, which would otherwise make every key look present.
  const existedInCache =
    settingsSync.isInitialized() &&
    settingsSync.getSettingFromCache(key) !== null;

  // Update the in-memory cache immediately so subsequent reads see the new value
  settingsSync.updateCache(key, value);

  // If the cache is ready we trust it; otherwise fall back to a network GET.
  if (settingsSync.isInitialized()) {
    try {
      if (existedInCache) {
        await api.settings.updateSetting(key, value, "string");
      } else {
        await api.settings.createSetting(key, value, "string");
      }
      return;
    } catch {
      // Cache disagreed with the server (e.g. created-elsewhere race). Fall
      // through to the create-then-update recovery below.
    }
  }

  try {
    const existing = await api.settings.getSetting(key);
    if (existing) {
      await api.settings.updateSetting(key, value, "string");
    } else {
      await api.settings.createSetting(key, value, "string");
    }
  } catch {
    try {
      await api.settings.createSetting(key, value, "string");
    } catch (error) {
      logger.error("[settingsPersistence] Failed to upsert setting", {
        key,
        error,
      });
    }
  }
}

export async function deleteSettingIfExists(key: string): Promise<void> {
  settingsSync.removeFromCache(key);
  try {
    await api.settings.deleteSetting(key);
  } catch {
    // Ignore missing setting or transient errors.
  }
}