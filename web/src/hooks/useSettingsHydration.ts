/**
 * Hook to hydrate Zustand stores with settings from database
 *
 * This ensures database settings override localStorage values loaded by Zustand persist
 */

import { useEffect } from "react";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import { useUIStore } from "../store/uiStore";
import { useEditorStore, type EditorSettings } from "../store/editorStore";
import { useSettingsStore } from "../store/settingsStore";
import { usePreferencesStore } from "../store/preferencesStore";

export function useSettingsHydration() {
  useEffect(() => {
    let cancelled = false;

    const hydrateStores = async () => {
      // Wait for settingsSync to initialize
      let attempts = 0;
      while (!settingsSync.isInitialized() && attempts < 50) {
        await new Promise(resolve => setTimeout(resolve, 100));
        if (cancelled) return;
        attempts++;
      }

      if (cancelled) return;

      if (!settingsSync.isInitialized()) {
        console.warn("[SettingsHydration] Timeout waiting for settingsSync to initialize");
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

      // Hydrate settings store (user preferences like defaultAutoApprove)
      // This ensures the latest preferences are loaded from the server
      useSettingsStore.getState().loadPreferences();

      // Hydrate preferences store (workflow preferences like defaultWorkflow)
      // This ensures workflow preferences are loaded from the server
      usePreferencesStore.getState().loadPreferences();
    };

    hydrateStores();

    return () => {
      cancelled = true;
    };
  }, []);
}
