import React from "react";
import { create } from "zustand";
import { fetchModelList } from "../hooks/catalog-models";
import { logger } from "../lib/logger";
import { waitForConfig } from "../lib/configReady";
import { getActiveOrgId, useActiveOrgStore } from "./activeOrgStore";

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
  /**
   * The org the models were listed for (null: none named, the person's own
   * accounts). A chat of an org runs on that org's accounts under its policy,
   * so the picker lists the active org's.
   */
  modelsOrgId: string | null;

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
  /** Forget the signed-in user's models (an identity change: authStore). */
  reset: () => void;
  /**
   * The active org changed: drop another org's models and list this one's.
   * A no-op before anything was loaded (the first load reads the org) and
   * when the models are already this org's.
   */
  followActiveOrg: (orgId: string | null) => void;
}

// Bumped by reset(): a fetch started for the previous user drops its answer
// instead of installing that user's models (local daemons, machine names,
// endpoints) for the next one.
let generation = 0;

/**
 * Whether a fetch started in `startedIn` for `orgId` may install its answer:
 * no reset or org switch replaced it since (each bumps `generation` and
 * starts its own fetch), and requests still name the org it asked for.
 */
function stillCurrent(startedIn: number, orgId: string | null): boolean {
  return startedIn === generation && getActiveOrgId() === orgId;
}

export const useGlobalDataStore = create<GlobalDataState>((set, get) => ({
  // Initial state
  models: [],
  tiers: {},
  modelsOrgId: null,

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
    const startedIn = generation;

    try {
      // Wait for backend configuration to be ready (especially in Electron)
      try {
        await waitForConfig();
      } catch {
        // Continue anyway - the API calls will fail gracefully if backend isn't ready
      }
      // A reset or an org switch meanwhile started its own fetch, which owns
      // the loading flags now.
      if (startedIn !== generation) return;
      // The org is read once the config is ready, so it is the one the
      // requests below name.
      const orgId = getActiveOrgId();

      // Set all loading states
      set({ modelsLoading: true });

      const [modelsResult] =
        await Promise.allSettled([
        fetchModelList(orgId).catch((error) => {
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
      modelsOrgId: orgId,
      modelsLoading: false,
    
      modelsError:
        modelsResult.status === "rejected"
          ? modelsResult.reason?.message || "Failed to fetch models"
          : null,

      isInitialized: true,
      isPrefetching: false,
    };

    if (!stillCurrent(startedIn, orgId)) {
      // Another org's answer: never shown. A switch already started the
      // current org's fetch; anything else is re-listed here.
      if (startedIn === generation) {
        set({ isPrefetching: false, modelsLoading: false });
        void get().refetchModels();
      }
      return;
    }
    set(newState);
    } catch (error) {
      if (startedIn !== generation) return;
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
    const startedIn = generation;
    const orgId = getActiveOrgId();
    try {
      const response = await fetchModelList(orgId);
      if (!stillCurrent(startedIn, orgId)) {
        if (startedIn === generation) void get().refetchModels();
        return;
      }
      set({
        models: response.models || [],
        tiers: response.tiers || {},
        modelsOrgId: orgId,
        modelsLoading: false,
      });
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to fetch models";
      logger.error("[GlobalDataStore] Failed to refetch models:", error);
      if (startedIn !== generation) return;
      set({ modelsError: errorMessage, modelsLoading: false });
    }
  },

  reset: () => {
    generation++;
    set({
      models: [],
      tiers: {},
      modelsOrgId: null,
      modelsLoading: false,
      modelsError: null,
      isInitialized: false,
      isPrefetching: false,
    });
  },

  followActiveOrg: (orgId) => {
    const state = get();
    const loaded = state.isInitialized || state.isPrefetching || state.modelsLoading || state.models.length > 0;
    if (!loaded || (state.modelsOrgId === orgId && !state.isPrefetching && !state.modelsLoading)) return;
    // A fetch in flight was for the org being left: drop its answer.
    generation++;
    set({ models: [], tiers: {}, modelsOrgId: orgId, modelsError: null, isPrefetching: false, isInitialized: true });
    void get().refetchModels();
  },
}));

// The org requests name (the switcher, a reconcile onto the default org,
// another tab) decides which accounts the picker lists, so a change re-lists.
useActiveOrgStore.subscribe((state, prev) => {
  if (state.effectiveId !== prev.effectiveId) useGlobalDataStore.getState().followActiveOrg(state.effectiveId);
});

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