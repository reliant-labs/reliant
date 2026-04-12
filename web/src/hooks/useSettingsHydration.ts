/**
 * Hook to hydrate Zustand stores with settings from database
 *
 * This ensures database settings override localStorage values loaded by Zustand persist
 */

import { useEffect } from "react";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import { useUIStore } from "../store/uiStore";
import { useEditorStore, type EditorSettings } from "../store/editorStore";

export function useSettingsHydration() {
  useEffect(() => {
    // Root.tsx guarantees settingsSync is initialized before App renders
    if (!settingsSync.isInitialized()) {
      console.warn("[SettingsHydration] settingsSync not initialized (unexpected)");
      return;
    }

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

    // Preferences loading is now done in Root.tsx after settingsSync initializes
  }, []);
}
