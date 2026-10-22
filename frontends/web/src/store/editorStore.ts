import { create } from "zustand";
import { persist } from "zustand/middleware";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import { logger } from "../lib/logger";

export interface EditorSettings {
  // Display
  minimap: boolean;
  lineNumbers: boolean;
  wordWrap: boolean;
  renderWhitespace: boolean;
  
  // Behavior
  fontSize: number;
  tabSize: number;
  autoSave: boolean;
  autoSaveDelay: number; // milliseconds
  
  // Advanced
  bracketPairColorization: boolean;
  guides: boolean;
  cursorBlinking: "blink" | "smooth" | "phase" | "expand" | "solid";
  cursorSmoothCaretAnimation: boolean;
  renderLineHighlight: "none" | "gutter" | "line" | "all";
  
  // Suggestions
  quickSuggestions: boolean;
  suggestOnTriggerCharacters: boolean;
  acceptSuggestionOnEnter: boolean;
  
  // Diff
  diffSideBySide: boolean; // true for side-by-side, false for inline
  diffHideUnchanged: boolean; // collapse unchanged regions
}

interface EditorState {
  settings: EditorSettings;
  updateSettings: (settings: Partial<EditorSettings>) => void;
  resetSettings: () => void;
}

const DEFAULT_SETTINGS: EditorSettings = {
  // Display
  minimap: true,
  lineNumbers: true,
  wordWrap: true,
  renderWhitespace: false,
  
  // Behavior
  fontSize: 13,
  tabSize: 2,
  autoSave: false,
  autoSaveDelay: 1000,
  
  // Advanced
  bracketPairColorization: true,
  guides: true,
  cursorBlinking: "blink",
  cursorSmoothCaretAnimation: true,
  renderLineHighlight: "line",
  
  // Suggestions
  quickSuggestions: true,
  suggestOnTriggerCharacters: true,
  acceptSuggestionOnEnter: true,
  
  // Diff
  diffSideBySide: true, // Default to side-by-side view
  diffHideUnchanged: false, // Default to showing all lines (user can use Monaco's collapse controls)
};

// Helper to get initial editor settings from database-synced localStorage
const getInitialEditorSettings = (): EditorSettings => {
  const saved = settingsSync.getJSONSetting<EditorSettings | null>(
    SETTINGS_KEYS.EDITOR_SETTINGS,
    null
  );
  return saved || DEFAULT_SETTINGS;
};

export const useEditorStore = create<EditorState>()(
  persist(
    (set) => ({
      settings: getInitialEditorSettings(),
      
      updateSettings: (newSettings: Partial<EditorSettings>) => {
        set((state) => {
          const updatedSettings = {
            ...state.settings,
            ...newSettings,
          };
          // Sync to database
          settingsSync.setJSONSetting(SETTINGS_KEYS.EDITOR_SETTINGS, updatedSettings).catch((e) => logger.error('Failed to sync editor settings:', e));
          return { settings: updatedSettings };
        });
      },

      resetSettings: () => {
        set({ settings: DEFAULT_SETTINGS });
        // Sync reset to database
        settingsSync.setJSONSetting(SETTINGS_KEYS.EDITOR_SETTINGS, DEFAULT_SETTINGS).catch((e) => logger.error('Failed to reset editor settings:', e));
      },
    }),
    {
      name: "editor-settings",
      // Merge persisted state with defaults to handle new fields
      merge: (persistedState, currentState) => {
        const persisted = persistedState as Partial<EditorState>;
        return {
          ...currentState,
          settings: {
            ...DEFAULT_SETTINGS,
            ...persisted?.settings,
          },
        };
      },
    }
  )
);
