import { api } from "../api/client";
import { settingsSync } from "../services/settingsSync";
import { logger } from "./logger";

export interface PersistedStringSetting {
  value: string;
}

export async function safeGetSetting(
  key: string,
): Promise<PersistedStringSetting | null> {
  // Fast path: read from the in-memory cache populated by ListSettings
  if (settingsSync.isInitialized()) {
    const cached = settingsSync.getSettingFromCache(key);
    // Cache hit → return it (null means the key genuinely doesn't exist)
    return cached;
  }

  // Fallback: cache not ready yet, do an individual RPC
  try {
    return await api.settings.getSetting(key);
  } catch {
    return null;
  }
}

export async function upsertStringSetting(
  key: string,
  value: string,
): Promise<void> {
  // Update the in-memory cache immediately so subsequent reads see the new value
  settingsSync.updateCache(key, value);

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
