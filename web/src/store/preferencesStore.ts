// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";
import { api } from "../api/client";
import { ConfigScope } from "../gen/reliant/v1/common_pb";
import { HiddenItemType } from "../gen/reliant/v1/settings_pb";
import { logger } from "../lib/logger";

// Default workflow constant - matches backend
export const DEFAULT_WORKFLOW = "builtin://agent";

interface ConfigScopePreferences {
  defaultMcpScope: ConfigScope;
  defaultWorkflowScope: ConfigScope;
  defaultWorkflow: string;
  hideBuiltinWorkflows: boolean;
  hideBuiltinPresets: boolean;
}

interface PreferencesStore {
  preferences: ConfigScopePreferences | null;
  hiddenWorkflowSlugs: Set<string>;
  hiddenPresetSlugs: Set<string>;
  isLoading: boolean;
  error: string | null;

  loadPreferences: () => Promise<void>;
  updatePreferences: (prefs: Partial<ConfigScopePreferences>) => Promise<void>;
  toggleWorkflowVisibility: (slug: string) => Promise<void>;
  togglePresetVisibility: (slug: string) => Promise<void>;
  isWorkflowHidden: (slug: string) => boolean;
  isPresetHidden: (slug: string) => boolean;
}

export const usePreferencesStore = create<PreferencesStore>((set, get) => ({
  preferences: null,
  hiddenWorkflowSlugs: new Set<string>(),
  hiddenPresetSlugs: new Set<string>(),
  isLoading: false,
  error: null,

  loadPreferences: async () => {
    if (get().isLoading) return;
    
    set({ isLoading: true, error: null });
    try {
      const response = await api.settings.getPreferences();
      const newDefaultWorkflow = response.default_workflow || DEFAULT_WORKFLOW;
      
      // Hidden items now come directly from the API (no JSON parsing needed)
      const hiddenWorkflows = response.hidden_workflow_slugs ?? [];
      const hiddenPresets = response.hidden_preset_slugs ?? [];
      
      set({
        preferences: {
          defaultMcpScope: response.default_mcp_scope ?? ConfigScope.PROJECT,
          defaultWorkflowScope: response.default_workflow_scope ?? ConfigScope.PROJECT,
          defaultWorkflow: newDefaultWorkflow,
          hideBuiltinWorkflows: response.hide_builtin_workflows ?? false,
          hideBuiltinPresets: response.hide_builtin_presets ?? false,
        },
        hiddenWorkflowSlugs: new Set(hiddenWorkflows),
        hiddenPresetSlugs: new Set(hiddenPresets),
        isLoading: false,
      });
    } catch (error) {
      logger.error("Failed to load scope preferences:", error);
      set({
        error: error instanceof Error ? error.message : "Failed to load preferences",
        isLoading: false,
        // Set defaults on error
        preferences: {
          defaultMcpScope: ConfigScope.PROJECT,
          defaultWorkflowScope: ConfigScope.PROJECT,
          defaultWorkflow: DEFAULT_WORKFLOW,
          hideBuiltinWorkflows: false,
          hideBuiltinPresets: false,
        },
        hiddenWorkflowSlugs: new Set(),
        hiddenPresetSlugs: new Set(),
      });
    }
  },

  updatePreferences: async (prefs: Partial<ConfigScopePreferences>) => {
    let currentPrefs = get().preferences;
    
    // If preferences haven't loaded yet, load them first
    if (!currentPrefs) {
      await get().loadPreferences();
      currentPrefs = get().preferences;
      // If still null after loading, use defaults
      if (!currentPrefs) {
        currentPrefs = {
          defaultMcpScope: ConfigScope.PROJECT,
          defaultWorkflowScope: ConfigScope.PROJECT,
          defaultWorkflow: DEFAULT_WORKFLOW,
          hideBuiltinWorkflows: false,
          hideBuiltinPresets: false,
        };
      }
    }

    // Optimistic update
    set({
      preferences: { ...currentPrefs, ...prefs },
    });

    try {
      await api.settings.updatePreferences({
        default_mcp_scope: prefs.defaultMcpScope,
        default_workflow_scope: prefs.defaultWorkflowScope,
        default_workflow: prefs.defaultWorkflow,
        hide_builtin_workflows: prefs.hideBuiltinWorkflows,
        hide_builtin_presets: prefs.hideBuiltinPresets,
      });
    } catch (error) {
      // Revert on error
      set({ preferences: currentPrefs });
      logger.error("Failed to update scope preferences:", error);
      throw error;
    }
  },

  toggleWorkflowVisibility: async (slug: string) => {
    const { hiddenWorkflowSlugs } = get();
    const isCurrentlyHidden = hiddenWorkflowSlugs.has(slug);
    const newHidden = new Set(hiddenWorkflowSlugs);
    
    if (isCurrentlyHidden) {
      newHidden.delete(slug);
    } else {
      newHidden.add(slug);
    }
    
    // Optimistic update
    set({ hiddenWorkflowSlugs: newHidden });
    
    try {
      // Use the dedicated setHiddenItem API
      await api.settings.setHiddenItem(
        HiddenItemType.WORKFLOW,
        slug,
        !isCurrentlyHidden,
      );
    } catch (error) {
      // Revert on error
      set({ hiddenWorkflowSlugs });
      logger.error("Failed to update hidden workflows:", error);
      throw error;
    }
  },

  togglePresetVisibility: async (slug: string) => {
    const { hiddenPresetSlugs } = get();
    const isCurrentlyHidden = hiddenPresetSlugs.has(slug);
    const newHidden = new Set(hiddenPresetSlugs);
    
    if (isCurrentlyHidden) {
      newHidden.delete(slug);
    } else {
      newHidden.add(slug);
    }
    
    // Optimistic update
    set({ hiddenPresetSlugs: newHidden });
    
    try {
      // Use the dedicated setHiddenItem API
      await api.settings.setHiddenItem(
        HiddenItemType.PRESET,
        slug,
        !isCurrentlyHidden,
      );
    } catch (error) {
      // Revert on error
      set({ hiddenPresetSlugs });
      logger.error("Failed to update hidden presets:", error);
      throw error;
    }
  },

  isWorkflowHidden: (slug: string) => {
    return get().hiddenWorkflowSlugs.has(slug);
  },

  isPresetHidden: (slug: string) => {
    return get().hiddenPresetSlugs.has(slug);
  },
}));
