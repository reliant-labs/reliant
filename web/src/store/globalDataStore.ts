import React from "react";
import { create } from "zustand";
import { api } from "../api/client";
import { presetGrpc, type Preset } from "../api/preset-grpc";
import { logger } from "../lib/logger";
import { waitForConfig } from "../lib/configReady";
import { useProjectStore } from "./projectStore";

// Re-export Preset type for consumers
export type { Preset } from "../api/preset-grpc";

// Type definitions
export interface WorkflowDef {
  name: string;
  filename: string; // Filename/slug used for API calls
  description: string;
  step_count: number;
  source: "builtin" | "user" | "project"; // Where the workflow comes from
  updated_at?: string;
  is_hidden?: boolean; // Whether the workflow is hidden from the workflow dropdown
  is_valid?: boolean; // Whether the workflow passes validation
}

interface Model {
  id: string;
  name: string;
  provider: string;
  driverId?: string; // The actual API driver to use (e.g., "openrouter", "anthropic")
  canReason?: boolean;
  supportedThinkingLevels?: string[];
  capabilities: string[];
  tags: string[];
  metadata?: Record<string, unknown>;
}

interface GlobalDataState {
  // Data
  models: Model[];
  workflows: WorkflowDef[];
  presets: Preset[];

  // Loading states
  modelsLoading: boolean;
  workflowsLoading: boolean;
  presetsLoading: boolean;

  // Error states
  modelsError: string | null;
  workflowsError: string | null;
  presetsError: string | null;

  // Initialization
  isInitialized: boolean;
  isPrefetching: boolean; // Track if prefetch is in progress

  // Version counter to trigger re-fetches in dependent hooks
  presetsVersion: number;

  // Actions
  prefetch: () => Promise<void>;
  refetchModels: () => Promise<void>;
  refetchWorkflows: (projectId: string) => Promise<void>;
  refetchPresets: (projectId: string) => Promise<void>;
}

// In-flight dedup: collapse concurrent calls for the same projectId into one request
let pendingWorkflowFetch: { projectId: string; promise: Promise<void> } | null = null;
let pendingPresetFetch: { projectId: string; promise: Promise<void> } | null = null;

export const useGlobalDataStore = create<GlobalDataState>((set, get) => ({
  // Initial state
  models: [],
  workflows: [],
  presets: [],

  modelsLoading: false,
  workflowsLoading: false,
  presetsLoading: false,

  modelsError: null,
  workflowsError: null,
  presetsError: null,

  isInitialized: false,
  isPrefetching: false,
  presetsVersion: 0,

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
        presetsLoading: true,
      });

      // Fetch models only - workflows and presets require project context
      const [modelsResult] =
        await Promise.allSettled([
        api.models.list().catch((error) => {
          logger.error("[GlobalDataStore] Failed to fetch models:", error);
          throw error;
        }),
      ]);

      // Workflows and presets are fetched per-project, not on prefetch
      const workflowsResult = { status: "fulfilled" as const, value: { workflows: [] } };

    // Update store with results
    const newState = {
      models:
        modelsResult.status === "fulfilled"
          ? modelsResult.value.models || []
          : [],
      workflows:
        workflowsResult.status === "fulfilled"
          ? workflowsResult.value.workflows || []
          : [],
      presets: [],

      modelsLoading: false,
      workflowsLoading: false,
      presetsLoading: false,

      modelsError:
        modelsResult.status === "rejected"
          ? modelsResult.reason?.message || "Failed to fetch models"
          : null,
      workflowsError: null, // Workflows are fetched per-project, not on prefetch
      presetsError: null,

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
        presetsLoading: false,
      });
    }
  },

  // Individual refetch methods (for manual refresh)
  refetchModels: async () => {
    set({ modelsLoading: true, modelsError: null });
    try {
      const response = await api.models.list();
      set({ models: response.models || [], modelsLoading: false });
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to fetch models";
      logger.error("[GlobalDataStore] Failed to refetch models:", error);
      set({ modelsError: errorMessage, modelsLoading: false });
    }
  },

  refetchWorkflows: async (projectId: string) => {
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

  refetchPresets: async (projectId: string) => {
    // Deduplicate concurrent calls for the same project
    if (pendingPresetFetch && pendingPresetFetch.projectId === projectId) {
      return pendingPresetFetch.promise;
    }
    set({ presetsLoading: true, presetsError: null });
    const promise = (async () => {
      try {
        const presets = await presetGrpc.listPresets(projectId);
        // Increment presetsVersion to trigger re-fetch in usePresetsForWorkflow
        set((state) => ({
          presets,
          presetsLoading: false,
          presetsVersion: state.presetsVersion + 1,
        }));
      } catch (error) {
        const errorMessage =
          error instanceof Error ? error.message : "Failed to fetch presets";
        logger.error("[GlobalDataStore] Failed to refetch presets:", error);
        set({ presetsError: errorMessage, presetsLoading: false });
      } finally {
        pendingPresetFetch = null;
      }
    })();
    pendingPresetFetch = { projectId, promise };
    return promise;
  },
}));

// React context for preloading - NOT CURRENTLY USED but keeping for future optimization
export const GlobalDataContext = React.createContext<boolean>(false);

// Selector hooks for convenience - returns data with loading/error states
export const useModels = () => {
  const models = useGlobalDataStore((state) => state.models);
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

  return { models, loading: modelsLoading || isPrefetching, error: modelsError };
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

export const usePresets = () => {
  const presets = useGlobalDataStore((state) => state.presets);
  const presetsLoading = useGlobalDataStore((state) => state.presetsLoading);
  const presetsError = useGlobalDataStore((state) => state.presetsError);
  return { presets, loading: presetsLoading, error: presetsError };
};

// Get presets filtered for a specific workflow
// This calls the ListPresetsForWorkflow gRPC endpoint which validates
// that preset params exist in the workflow and match tags.
export const usePresetsForWorkflow = (workflowName: string) => {
  const [presets, setPresets] = React.useState<Preset[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);

  // Get current project from project store
  const projectId = useProjectStore((state) => state.currentProject?.id);

  // Subscribe to presetsVersion to re-fetch when presets are updated
  const presetsVersion = useGlobalDataStore((state) => state.presetsVersion);

  React.useEffect(() => {
    if (!projectId || !workflowName) {
      setPresets([]);
      setLoading(false);
      return;
    }

    let cancelled = false;
    setLoading(true);
    setError(null);

    presetGrpc.listPresetsForWorkflow(projectId, workflowName)
      .then((result) => {
        if (!cancelled) {
          setPresets(result);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          logger.error("[usePresetsForWorkflow] Failed to load presets", { error: err, workflowName });
          setError(err instanceof Error ? err.message : "Failed to load presets");
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [projectId, workflowName, presetsVersion]);

  return { presets, loading, error };
};

// Usage:
// 1. In App.tsx or root component, add useGlobalDataStore().prefetch() in useEffect
// 2. Components can use useGlobalDataStore() to access cached data