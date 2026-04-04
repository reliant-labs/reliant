import { create } from "zustand";
import { logger } from "../lib/logger";
import {
  backgroundGrpc,
  BackgroundProcessStatus,
  type BackgroundProcess,
  type PortInfo,
  type ProcessOutput,
} from "../api/background-grpc";

// Re-export types for consumers
export type { BackgroundProcess, PortInfo, ProcessOutput };

interface ProcessState {
  processes: BackgroundProcess[];
  isLoading: boolean;
  isRefreshing: boolean; // Background refresh without UI blocking
  error: string | null;
  lastFetched: number | null;
  currentWorktreeId: string | null; // Track which worktree we're viewing
  
  // Selected process for detail view
  selectedProcessId: string | null;
  processOutput: ProcessOutput | null;
  isLoadingOutput: boolean;
  
  // Actions
  fetchProcesses: (worktreeId?: string, isBackgroundRefresh?: boolean, options?: { force?: boolean }) => Promise<void>;
  fetchProcessOutput: (processId: string, isBackgroundRefresh?: boolean) => Promise<void>;
  killProcess: (processId: string, worktreeId?: string) => Promise<void>;
  selectProcess: (processId: string | null) => void;
  clearError: () => void;
  
  // Selectors
  getRunningProcesses: () => BackgroundProcess[];
  getProcessById: (id: string) => BackgroundProcess | undefined;
  getProcessCount: () => number;
  getRunningCount: () => number;
  reset: () => void;
}

export const useProcessStore = create<ProcessState>((set, get) => ({
  processes: [],
  isLoading: false,
  isRefreshing: false,
  error: null,
  lastFetched: null,
  currentWorktreeId: null,
  selectedProcessId: null,
  processOutput: null,
  isLoadingOutput: false,

  fetchProcesses: async (worktreeId?: string, isBackgroundRefresh = false, options?: { force?: boolean }) => {
    const state = get();
    
    // Dedup: skip redundant fetches within a short window (e.g. multiple components mounting)
    if (!options?.force && state.lastFetched && Date.now() - state.lastFetched < 3000) {
      logger.debug("[ProcessStore] Skipping fetch (dedup window)", { lastFetched: state.lastFetched });
      return;
    }
    
    // If switching worktrees, show loading. Otherwise, just refresh quietly.
    const isWorktreeChange = worktreeId !== state.currentWorktreeId;
    const shouldShowLoading = !isBackgroundRefresh && (state.processes.length === 0 || isWorktreeChange);
    
    if (shouldShowLoading) {
      set({ isLoading: true, error: null, currentWorktreeId: worktreeId || null });
    } else {
      set({ isRefreshing: true });
    }
    
    try {
      const response = await backgroundGrpc.listProcesses(
        worktreeId ? { worktree_id: worktreeId } : undefined
      );
      
      set({
        processes: response || [],
        isLoading: false,
        isRefreshing: false,
        lastFetched: Date.now(),
        currentWorktreeId: worktreeId || null,
      });
      
      logger.debug("[ProcessStore] Fetched processes", { count: response?.length || 0, worktreeId });
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to fetch processes";
      // Connection errors (daemon not available) are expected in some environments —
      // log at debug level instead of error to avoid noisy console output.
      const isConnectionError =
        errorMessage.includes("ERR_CONNECTION_REFUSED") ||
        errorMessage.includes("Failed to fetch") ||
        errorMessage.includes("Daemon transport not available") ||
        errorMessage.includes("NetworkError") ||
        errorMessage.includes("CORS");
      if (isConnectionError) {
        logger.debug("[ProcessStore] Daemon not reachable, skipping process fetch", { error: errorMessage });
        set({ processes: [], isLoading: false, isRefreshing: false });
      } else {
        logger.error("[ProcessStore] Failed to fetch processes", { error });
        set({ error: errorMessage, isLoading: false, isRefreshing: false });
      }
    }
  },

  fetchProcessOutput: async (processId: string, isBackgroundRefresh = false) => {
    const state = get();
    
    // Only show loading on initial fetch, not on background refresh
    if (!isBackgroundRefresh && !state.processOutput) {
      set({ isLoadingOutput: true });
    }
    
    try {
      const response = await backgroundGrpc.getProcessOutput(processId);
      
      set({
        processOutput: response,
        isLoadingOutput: false,
      });
      
      logger.debug("[ProcessStore] Fetched process output", { processId });
    } catch (error) {
      logger.error("[ProcessStore] Failed to fetch process output", { processId, error });
      // Only set error output on initial fetch
      if (!isBackgroundRefresh) {
        set({
          processOutput: { stdout: "", stderr: "Failed to fetch output" },
          isLoadingOutput: false,
        });
      }
    }
  },

  killProcess: async (processId: string, worktreeId?: string) => {
    try {
      await backgroundGrpc.killProcess(processId);

      logger.info("[ProcessStore] Killed process", { processId });

      // Update local state immediately
      set((state) => ({
        processes: state.processes.map((p) =>
          p.id === processId
            ? {
                ...p,
                status: BackgroundProcessStatus.KILLED,
                end_time: new Date().toISOString(),
              }
            : p
        ),
      }));

      // Refresh to get accurate state from server
      await get().fetchProcesses(worktreeId, false, { force: true });
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Failed to kill process";

      // If process was already stopped, just refetch to sync state (not a real error)
      const isAlreadyStopped = errorMessage.toLowerCase().includes("not running") ||
                               errorMessage.toLowerCase().includes("already stopped");

      if (isAlreadyStopped) {
        logger.info("[ProcessStore] Process was already stopped", { processId });
        await get().fetchProcesses(worktreeId, false, { force: true });
        return;
      }

      logger.error("[ProcessStore] Failed to kill process", { processId, error });
      set({ error: errorMessage });
      throw error;
    }
  },

  selectProcess: (processId: string | null) => {
    set({ selectedProcessId: processId, processOutput: null });
    
    // Fetch output when selecting a process
    if (processId) {
      get().fetchProcessOutput(processId);
    }
  },

  clearError: () => set({ error: null }),

  // Selectors
  getRunningProcesses: () => {
    return get().processes.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING,
    );
  },

  getProcessById: (id: string) => {
    return get().processes.find((p) => p.id === id);
  },

  getProcessCount: () => {
    return get().processes.length;
  },

  getRunningCount: () => {
    return get().processes.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING,
    ).length;
  },

  reset: () => {
    set({
      processes: [],
      isLoading: false,
      isRefreshing: false,
      error: null,
      lastFetched: null,
      currentWorktreeId: null,
      selectedProcessId: null,
      processOutput: null,
      isLoadingOutput: false,
    });
  },
}));