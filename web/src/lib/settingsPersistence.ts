import { api } from "../api/client";
import { logger } from "./logger";

export interface PersistedStringSetting {
  value: string;
}

export async function safeGetSetting(
  key: string,
): Promise<PersistedStringSetting | null> {
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
  try {
    await api.settings.deleteSetting(key);
  } catch {
    // Ignore missing setting or transient errors.
  }
}
