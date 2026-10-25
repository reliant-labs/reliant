import React from "react";
import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";
import { waitForConfig } from "../lib/configReady";

// Type definitions
interface Model {
  id: string;
  name: string;
  provider: string;
  driverId?: string; // The actual API driver to use (e.g., "openrouter", "anthropic")
  canReason?: boolean;
  supportedThinkingLevels?: string[];
  /** False when temperature set for this model+driver never reaches the provider. Undefined = unknown (show). */
  supportsTemperature?: boolean;
  /** Tokens the model's server honors; 0/undefined = unknown. */
  contextWindow?: number;
  /** Token count an unpinned chat on this model+driver compacts at; 0/undefined = unknown. */
  defaultCompactionThreshold?: number;
  /** Largest compaction_threshold the server honors on this model+driver (a larger pin is capped); 0/undefined = unknown. */
  maxCompactionThreshold?: number;
  /** Set only for driverId "local": the machine whose server runs this model. */
  local?: {
    daemonId: string;
    machineName: string;
    endpointId: string;
    endpointKind: string;
    online: boolean;
  };
  capabilities: string[];
  tags: string[];
}

/** What a tag selector resolves to for this user: model + the tier's effort. */
export interface TierResolution {
  modelId: string; // "modelId@driverId", same format as Model.id
  thinkingLevel: string; // "" = the model cannot reason
}

interface GlobalDataState {
  // Data
  models: Model[];
  tiers: Record<string, TierResolution>;

  // Loading states
  modelsLoading: boolean;

  // Error states
  modelsError: string | null;

  // Initialization
  isInitialized: boolean;
  isPrefetching: boolean; // Track if prefetch is in progress

  // Actions
  prefetch: () => Promise<void>;
  refetchModels: () => Promise<void>;
}

export const useGlobalDataStore = create<GlobalDataState>((set, get) => ({
  // Initial state
  models: [],
  tiers: {},

  modelsLoading: false,

  modelsError: null,

  isInitialized: false,
  isPrefetching: false,

  // Prefetch all static data in parallel on app load
  prefetch: async () => {
    // Prevent duplicate prefetches
    const state = get();
    
    // If already initialized or prefetch in progress, skip
    if (state.isInitialized || state.isPrefetching) {
      return;
    }
    
    set({ isPrefetching: true });

    try {
      // Wait for backend configuration to be ready (especially in Electron)
      try {
        await waitForConfig();
      } catch {
        // Continue anyway - the API calls will fail gracefully if backend isn't ready
      }

      // Set all loading states
      set({ modelsLoading: true });

      const [modelsResult] =
        await Promise.allSettled([
        api.models.list().catch((error) => {
          logger.error("[GlobalDataStore] Failed to fetch models:", error);
          throw error;
        }),
      ]);

    // Update store with results
    const newState = {
      models:
        modelsResult.status === "fulfilled"
          ? modelsResult.value.models || []
          : [],
      tiers:
        modelsResult.status === "fulfilled"
          ? modelsResult.value.tiers || {}
          : {},
      modelsLoading: false,
    
      modelsError:
        modelsResult.status === "rejected"
          ? modelsResult.reason?.message || "Failed to fetch models"
          : null,

      isInitialized: true,
      isPrefetching: false,
    };

    set(newState);
    } catch (error) {
      // Unexpected error during prefetch - ensure state is reset
      logger.error("[GlobalDataStore] Unexpected error during prefetch:", error);
      set({ 
        isPrefetching: false,
        modelsLoading: false,
      });
    }
  },

  // Individual refetch methods (for manual refresh)
  refetchModels: async () => {
    set({ modelsLoading: true, modelsError: null });
    try {
      const response = await api.models.list();
      set({
        models: response.models || [],
        tiers: response.tiers || {},
        modelsLoading: false,
      });
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to fetch models";
      logger.error("[GlobalDataStore] Failed to refetch models:", error);
      set({ modelsError: errorMessage, modelsLoading: false });
    }
  },

}));

// React context for preloading - NOT CURRENTLY USED but keeping for future optimization
export const GlobalDataContext = React.createContext<boolean>(false);

// Selector hooks for convenience - returns data with loading/error states
export const useModels = () => {
  const models = useGlobalDataStore((state) => state.models);
  const tiers = useGlobalDataStore((state) => state.tiers);
  const modelsLoading = useGlobalDataStore((state) => state.modelsLoading);
  const modelsError = useGlobalDataStore((state) => state.modelsError);
  const isPrefetching = useGlobalDataStore((state) => state.isPrefetching);
  const refetchModels = useGlobalDataStore((state) => state.refetchModels);

  // Fetch on demand: if models are empty and nothing is already loading, trigger a fetch.
  // This handles cases where prefetch failed, hadn't run yet, or the component mounted
  // before AuthInitializer completed.
  React.useEffect(() => {
    if (models.length === 0 && !modelsLoading && !isPrefetching) {
      refetchModels();
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return { models, tiers, loading: modelsLoading || isPrefetching, error: modelsError };
};

// Usage:
// 1. In App.tsx or root component, add useGlobalDataStore().prefetch() in useEffect
// 2. Components can use useGlobalDataStore() to access cached data