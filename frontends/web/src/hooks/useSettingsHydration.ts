/**
 * Hook to hydrate Zustand stores with settings from database
 *
 * This ensures database settings override localStorage values loaded by Zustand persist
 */

import { useEffect } from "react";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import { useUIStore } from "../store/uiStore";
import { useEditorStore, type EditorSettings } from "../store/editorStore";

/** Copy the database-backed settings settingsSync mirrored into localStorage into the stores. */
export function hydrateStoresFromSettings(): void {
  // Hydrate UI store
  const showHiddenFiles = settingsSync.getSetting(SETTINGS_KEYS.SHOW_HIDDEN_FILES, "");
  if (showHiddenFiles === "true" || showHiddenFiles === "false") {
    const value = showHiddenFiles === "true";
    const currentValue = useUIStore.getState().showHiddenFiles;
    if (currentValue !== value) {
      useUIStore.setState({ showHiddenFiles: value });
    }
  }

  // Hydrate editor store
  const editorSettings = settingsSync.getJSONSetting<EditorSettings | null>(
    SETTINGS_KEYS.EDITOR_SETTINGS,
    null
  );
  if (editorSettings) {
    const currentSettings = useEditorStore.getState().settings;
    const hasChanges = JSON.stringify(currentSettings) !== JSON.stringify(editorSettings);
    if (hasChanges) {
      useEditorStore.setState({ settings: editorSettings });
    }
  }
}

export function useSettingsHydration() {
  // settingsSync initializes after sign-in (AuthInitializer, a child of App),
  // so on App's mount it never has yet. Checking isInitialized() here used to
  // bail with "settingsSync not initialized (unexpected)" on every startup and
  // the stores were never hydrated from the database. Hydrate when it
  // completes instead — at once if it already has.
  useEffect(() => settingsSync.onInitialized(hydrateStoresFromSettings), []);
}
