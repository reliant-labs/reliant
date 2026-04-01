// =============================================================================
// Keyboard Shortcuts Store
// =============================================================================
//
// This store manages keyboard shortcuts state. The default shortcuts are
// generated from the source of truth at: config/shortcuts.yaml
//
// To modify shortcuts, edit config/shortcuts.yaml and run: make generate-shortcuts
//
// =============================================================================

import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";
import { defaultShortcuts } from "./shortcutsData.generated";

export interface KeyBinding {
  key: string;
  ctrl?: boolean;
  meta?: boolean;
  shift?: boolean;
  alt?: boolean;
}

export interface ShortcutDefinition {
  id: string;
  name: string;
  description: string;
  category: string;
  defaultBinding: KeyBinding;
  currentBinding: KeyBinding;
  handler: string; // Name of the handler function
}

interface ShortcutsState {
  shortcuts: Record<string, ShortcutDefinition>;
  isEditing: string | null;
  isLoading: boolean;
  _initialized: boolean;
  
  // Actions
  initializeShortcuts: () => Promise<void>;
  updateShortcut: (id: string, binding: KeyBinding) => Promise<void>;
  resetShortcut: (id: string) => Promise<void>;
  resetAllShortcuts: () => Promise<void>;
  setEditing: (id: string | null) => void;
  getShortcutByHandler: (handler: string) => ShortcutDefinition | undefined;
  isKeyComboTaken: (binding: KeyBinding, excludeId?: string) => boolean;
}

// Re-export defaultShortcuts for consumers that need it
export { defaultShortcuts };

export const useShortcutsStore = create<ShortcutsState>()((set, get) => ({
  shortcuts: {},
  isEditing: null,
  isLoading: false,
  _initialized: false,

  initializeShortcuts: async () => {
    // Guard: skip if already initialized or currently loading
    if (get()._initialized || get().isLoading) return;

    try {
      set({ isLoading: true });

      // If we're in Electron, wait for backend config to be ready
      if (typeof window !== 'undefined' && window.location.protocol === 'file:') {
        // Wait for RELIANT_CONFIG to be available (max 5 seconds)
        const maxWaitTime = 5000;
        const startTime = Date.now();
        while (!window.RELIANT_CONFIG?.backendUrl && (Date.now() - startTime) < maxWaitTime) {
          await new Promise(resolve => setTimeout(resolve, 100));
        }

        if (!window.RELIANT_CONFIG?.backendUrl) {
          logger.warn('Backend config not available after waiting, using defaults');
          throw new Error('Backend config not ready');
        }
      }

      const response = await api.settings.getShortcuts();
      let savedShortcuts: Record<string, ShortcutDefinition> = {};

      if (response.shortcuts && response.shortcuts !== '{}') {
        try {
          savedShortcuts = JSON.parse(response.shortcuts);
        } catch (error) {
          logger.error('Failed to parse shortcuts from backend', error);
        }
      }

      const initialized: Record<string, ShortcutDefinition> = {};
      Object.entries(defaultShortcuts).forEach(([id, shortcut]) => {
        initialized[id] = {
          ...shortcut,
          currentBinding: savedShortcuts[id]?.currentBinding || shortcut.defaultBinding
        };
      });

      set({ shortcuts: initialized, isLoading: false, _initialized: true });
    } catch (error) {
      logger.error('Failed to load shortcuts from backend', error);

      const initialized: Record<string, ShortcutDefinition> = {};
      Object.entries(defaultShortcuts).forEach(([id, shortcut]) => {
        initialized[id] = {
          ...shortcut,
          currentBinding: shortcut.defaultBinding
        };
      });

      set({ shortcuts: initialized, isLoading: false, _initialized: true });
    }
  },

  updateShortcut: async (id: string, binding: KeyBinding) => {
    const currentShortcuts = get().shortcuts;
    const updated = {
      ...currentShortcuts,
      [id]: {
        ...currentShortcuts[id],
        currentBinding: binding
      }
    };
    
    set({ shortcuts: updated });
    
    try {
      await api.settings.updateShortcuts(JSON.stringify(updated));
    } catch (error) {
      logger.error('Failed to save shortcuts to backend', error);
      set({ shortcuts: currentShortcuts });
      throw error;
    }
  },

  resetShortcut: async (id: string) => {
    const currentShortcuts = get().shortcuts;
    const updated = {
      ...currentShortcuts,
      [id]: {
        ...currentShortcuts[id],
        currentBinding: currentShortcuts[id].defaultBinding
      }
    };
    
    set({ shortcuts: updated });
    
    try {
      await api.settings.updateShortcuts(JSON.stringify(updated));
    } catch (error) {
      logger.error('Failed to save shortcuts to backend', error);
      set({ shortcuts: currentShortcuts });
      throw error;
    }
  },

  resetAllShortcuts: async () => {
    const currentShortcuts = get().shortcuts;
    const reset: Record<string, ShortcutDefinition> = {};
    
    Object.entries(currentShortcuts).forEach(([id, shortcut]) => {
      reset[id] = {
        ...shortcut,
        currentBinding: shortcut.defaultBinding
      };
    });
    
    set({ shortcuts: reset });
    
    try {
      await api.settings.updateShortcuts(JSON.stringify(reset));
    } catch (error) {
      logger.error('Failed to save shortcuts to backend', error);
      set({ shortcuts: currentShortcuts });
      throw error;
    }
  },

  setEditing: (id: string | null) => {
    set({ isEditing: id });
  },

  getShortcutByHandler: (handler: string) => {
    const shortcuts = get().shortcuts;
    return Object.values(shortcuts).find(s => s.handler === handler);
  },

  isKeyComboTaken: (binding: KeyBinding, excludeId?: string) => {
    const shortcuts = get().shortcuts;
    
    // Normalize keys to uppercase for letter keys to ensure case-insensitive matching
    const normalizeKey = (key: string): string => {
      if (key.length === 1 && /[a-zA-Z]/.test(key)) {
        return key.toUpperCase();
      }
      return key;
    };
    
    const normalizedBindingKey = normalizeKey(binding.key);
    
    return Object.values(shortcuts).some(s => {
      if (excludeId && s.id === excludeId) return false;
      const current = s.currentBinding;
      if (!current || !current.key) return false;
      
      const normalizedCurrentKey = normalizeKey(current.key);
      
      return (
        normalizedCurrentKey === normalizedBindingKey &&
        !!current.ctrl === !!binding.ctrl &&
        !!current.meta === !!binding.meta &&
        !!current.shift === !!binding.shift &&
        !!current.alt === !!binding.alt
      );
    });
  }
}));