import React from "react";
import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";
import { waitForConfig } from "../lib/configReady";
import { useProjectStore } from "./projectStore";
import { invalidateWorkflowConfig } from "../hooks/workflow-config-queries";

// Type definitions
export interface WorkflowDef {
  name: string;
  filename: string; // Filename/slug used for API calls
  description: string;
  step_count: number;
  source: "builtin" | "user" | "project"; // Where the workflow comes from
  updated_at?: string;
  title?: string; // Definition-level display name
  is_hidden?: boolean; // Whether the workflow is hidden from the workflow dropdown
  status?: "draft" | "complete"; // Lifecycle: drafts are never runnable
  automation_only?: boolean; // Chat trigger off: chat pickers must not offer it (isChatLaunchable)
}

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
  workflows: WorkflowDef[];

  // Loading states
  modelsLoading: boolean;
  workflowsLoading: boolean;

  // Error states
  modelsError: string | null;
  workflowsError: string | null;

  // Initialization
  isInitialized: boolean;
  isPrefetching: boolean; // Track if prefetch is in progress

  // Actions
  prefetch: () => Promise<void>;
  refetchModels: () => Promise<void>;
  refetchWorkflows: (projectId: string) => Promise<void>;
}

// In-flight dedup: collapse concurrent calls for the same projectId into one request
let pendingWorkflowFetch: { projectId: string; promise: Promise<void> } | null = null;

export const useGlobalDataStore = create<GlobalDataState>((set, get) => ({
  // Initial state
  models: [],
  tiers: {},
  workflows: [],

  modelsLoading: false,
  workflowsLoading: false,

  modelsError: null,
  workflowsError: null,

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
      set({
        modelsLoading: true,
        workflowsLoading: true,
      });

      // Fetch models only - workflows require project context
      const [modelsResult] =
        await Promise.allSettled([
        api.models.list().catch((error) => {
          logger.error("[GlobalDataStore] Failed to fetch models:", error);
          throw error;
        }),
      ]);

      // Workflows are fetched per-project, not on prefetch
      const workflowsResult = { status: "fulfilled" as const, value: { workflows: [] } };

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
      workflows:
        workflowsResult.status === "fulfilled"
          ? workflowsResult.value.workflows || []
          : [],
  
      modelsLoading: false,
      workflowsLoading: false,

      modelsError:
        modelsResult.status === "rejected"
          ? modelsResult.reason?.message || "Failed to fetch models"
          : null,
      workflowsError: null, // Workflows are fetched per-project, not on prefetch

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
        workflowsLoading: false,
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

  refetchWorkflows: async (projectId: string) => {
    // Every caller is a change (save, copy, delete, visibility) or a project
    // switch; the cached definitions and preset lists go stale with it.
    invalidateWorkflowConfig();
    // Deduplicate concurrent calls for the same project
    if (pendingWorkflowFetch && pendingWorkflowFetch.projectId === projectId) {
      return pendingWorkflowFetch.promise;
    }
    set({ workflowsLoading: true, workflowsError: null });
    const promise = (async () => {
      try {
        const response = await api.workflows.list(projectId);
        set({ workflows: response.workflows || [], workflowsLoading: false });
      } catch (error) {
        const errorMessage =
          error instanceof Error ? error.message : "Failed to fetch workflows";
        logger.error("[GlobalDataStore] Failed to refetch workflows:", error);
        set({ workflowsError: errorMessage, workflowsLoading: false });
      } finally {
        pendingWorkflowFetch = null;
      }
    })();
    pendingWorkflowFetch = { projectId, promise };
    return promise;
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

export const useWorkflows = () => {
  const workflows = useGlobalDataStore((state) => state.workflows);
  const workflowsLoading = useGlobalDataStore((state) => state.workflowsLoading);
  const workflowsError = useGlobalDataStore((state) => state.workflowsError);
  const isPrefetching = useGlobalDataStore((state) => state.isPrefetching);
  const refetchWorkflows = useGlobalDataStore((state) => state.refetchWorkflows);
  const projectId = useProjectStore((state) => state.currentProject?.id);

  // Fetch on demand: if workflows are empty and nothing is already loading, trigger a fetch.
  // This handles cases where the project store's refetchWorkflows call raced or failed.
  React.useEffect(() => {
    if (projectId && workflows.length === 0 && !workflowsLoading && !isPrefetching) {
      refetchWorkflows(projectId);
    }
  }, [projectId]); // eslint-disable-line react-hooks/exhaustive-deps

  return { workflows, loading: workflowsLoading || isPrefetching, error: workflowsError };
};

// Usage:
// 1. In App.tsx or root component, add useGlobalDataStore().prefetch() in useEffect
// 2. Components can use useGlobalDataStore() to access cached data