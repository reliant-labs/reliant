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

// Language Server Configuration
export interface LanguageServerConfig {
  id: string;
  name: string;
  language: string;
  command: string;
  args: string[];
  extensions: string[];
  enabled: boolean;
  isCustom?: boolean;
}

const DEFAULT_LANGUAGE_SERVERS: LanguageServerConfig[] = [
  {
    id: 'gopls',
    name: 'Go (gopls)',
    language: 'go',
    command: 'gopls',
    args: ['serve'],
    extensions: ['.go'],
    enabled: true,
  },
  {
    id: 'pyright',
    name: 'Python (pyright)',
    language: 'python',
    command: 'pyright-langserver',
    args: ['--stdio'],
    extensions: ['.py', '.pyi'],
    enabled: true,
  },
  {
    id: 'rust-analyzer',
    name: 'Rust (rust-analyzer)',
    language: 'rust',
    command: 'rust-analyzer',
    args: [],
    extensions: ['.rs'],
    enabled: false,
  },
  {
    id: 'clangd',
    name: 'C/C++ (clangd)',
    language: 'c',
    command: 'clangd',
    args: [],
    extensions: ['.c', '.cpp', '.h', '.hpp', '.cc', '.cxx'],
    enabled: false,
  },
];

interface EditorState {
  settings: EditorSettings;
  languageServers: LanguageServerConfig[];
  updateSettings: (settings: Partial<EditorSettings>) => void;
  updateLanguageServers: (servers: LanguageServerConfig[]) => void;
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

// Helper to get initial language server settings
const getInitialLanguageServers = (): LanguageServerConfig[] => {
  const saved = settingsSync.getJSONSetting<LanguageServerConfig[] | null>(
    'language_servers' as any,
    null
  );
  return saved || DEFAULT_LANGUAGE_SERVERS;
};

export const useEditorStore = create<EditorState>()(
  persist(
    (set) => ({
      settings: getInitialEditorSettings(),
      languageServers: getInitialLanguageServers(),
      
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

      updateLanguageServers: (servers: LanguageServerConfig[]) => {
        set({ languageServers: servers });
        // Sync to database
        settingsSync.setJSONSetting('language_servers' as any, servers).catch((e) => logger.error('Failed to sync language server settings:', e));
      },
      
      resetSettings: () => {
        set({ settings: DEFAULT_SETTINGS, languageServers: DEFAULT_LANGUAGE_SERVERS });
        // Sync reset to database
        settingsSync.setJSONSetting(SETTINGS_KEYS.EDITOR_SETTINGS, DEFAULT_SETTINGS).catch((e) => logger.error('Failed to reset editor settings:', e));
        settingsSync.setJSONSetting('language_servers' as any, DEFAULT_LANGUAGE_SERVERS).catch((e) => logger.error('Failed to reset language server settings:', e));
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
          languageServers: persisted?.languageServers || DEFAULT_LANGUAGE_SERVERS,
        };
      },
    }
  )
);
