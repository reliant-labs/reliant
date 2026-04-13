/**
 * Spawn display mode settings
 * Controls how spawn thread content is displayed in the timeline
 */
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";

export type SpawnDisplayMode = "inline" | "preview";

const DEFAULT_MODE: SpawnDisplayMode = "preview";

/**
 * Get the current spawn display mode
 */
export function getSpawnDisplayMode(): SpawnDisplayMode {
  const value = settingsSync.getSetting(SETTINGS_KEYS.SPAWN_DISPLAY_MODE, DEFAULT_MODE);
  return value === "inline" ? "inline" : "preview";
}

/**
 * Set the spawn display mode
 */
export async function setSpawnDisplayMode(mode: SpawnDisplayMode): Promise<void> {
  await settingsSync.setSetting(SETTINGS_KEYS.SPAWN_DISPLAY_MODE, mode);
}
