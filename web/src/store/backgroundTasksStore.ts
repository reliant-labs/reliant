import { create } from "zustand";
import {
  backgroundGrpc,
  type BackgroundProcess,
  type ProcessOutput,
} from "../api/background-grpc";
import { BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";
import { logger } from "../lib/logger";

interface BackgroundTasksState {
  // State
  processes: BackgroundProcess[];
  processOutputs: Map<string, ProcessOutput>;
  selectedProcessId: string | null;
  isLoading: boolean;
  error: string | null;
  lastFetch: number;
  currentWorktreeId: string | null;

  // Actions
  fetchProcesses: (worktreeId?: string) => Promise<void>;
  fetchProcessOutput: (processId: string) => Promise<void>;
  killProcess: (processId: string) => Promise<void>;
  selectProcess: (processId: string | null) => void;
  clearError: () => void;

  // Getters - all process filtering is now by worktree, not chat
  getProcessesByWorktree: (worktreeId?: string) => BackgroundProcess[];
  getRunningProcesses: () => BackgroundProcess[];
  getProcessStats: (worktreeId?: string) => { total: number; running: number; completed: number; failed: number };
  reset: () => void;
}

export const useBackgroundTasksStore = create<BackgroundTasksState>((set, get) => ({
  // Initial state
  processes: [],
  processOutputs: new Map(),
  selectedProcessId: null,
  isLoading: false,
  error: null,
  lastFetch: 0,
  currentWorktreeId: null,

  // Fetch all processes for a worktree
  fetchProcesses: async (worktreeId?: string) => {
    try {
      set({ isLoading: true, error: null });
      // Filter by worktree_id instead of chat_id
      const filters = worktreeId ? { worktree_id: worktreeId } : undefined;
      const processes = await backgroundGrpc.listProcesses(filters);

      // Evict processOutputs for processes no longer in the active list
      const activeIds = new Set(processes.map(p => p.id));
      const currentOutputs = get().processOutputs;
      const newOutputs = new Map<string, ProcessOutput>();
      for (const [id, output] of currentOutputs) {
        if (activeIds.has(id)) {
          newOutputs.set(id, output);
        }
      }

      set({
        processes,
        processOutputs: newOutputs,
        lastFetch: Date.now(),
        currentWorktreeId: worktreeId || null,
        isLoading: false
      });

      logger.info("Fetched background processes", { count: processes.length, worktreeId });
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
        logger.debug("[BackgroundTasksStore] Daemon not reachable, skipping process fetch", { error: errorMessage });
        set({ processes: [], isLoading: false });
      } else {
        logger.error("[BackgroundTasksStore] Failed to fetch background processes:", error);
        set({ error: errorMessage, isLoading: false });
      }
    }
  },

  // Fetch output for a specific process
  fetchProcessOutput: async (processId: string) => {
    try {
      const output = await backgroundGrpc.getProcessOutput(processId);

      set((state) => {
        const newOutputs = new Map(state.processOutputs);
        newOutputs.set(processId, output);
        return { processOutputs: newOutputs };
      });

      logger.info("Fetched process output", { processId });
    } catch (error) {
      logger.error("Failed to fetch process output", error);
      set({
        error: error instanceof Error ? error.message : "Failed to fetch output"
      });
    }
  },

  // Kill a process
  killProcess: async (processId: string) => {
    try {
      await backgroundGrpc.killProcess(processId);

      // Update the process status locally
      set((state) => ({
        processes: state.processes.map(p =>
          p.id === processId
            ? { ...p, status: BackgroundProcessStatus.KILLED }
            : p
        )
      }));

      logger.info("Killed process", { processId });

      // Refresh to get latest status - use worktree_id from the process
      const state = get();
      const worktreeId = state.processes.find(p => p.id === processId)?.worktree_id;
      await state.fetchProcesses(worktreeId);
    } catch (error) {
      logger.error("Failed to kill process", error);
      set({
        error: error instanceof Error ? error.message : "Failed to kill process"
      });
    }
  },

  // Select a process for viewing details
  selectProcess: (processId: string | null) => {
    set({ selectedProcessId: processId });

    // Fetch output when selecting a process
    if (processId) {
      get().fetchProcessOutput(processId);
    }
  },

  clearError: () => {
    set({ error: null });
  },

  // Getters - filter by worktree, not chat
  getProcessesByWorktree: (worktreeId?: string) => {
    const state = get();
    if (!worktreeId) return state.processes;
    return state.processes.filter(p => p.worktree_id === worktreeId);
  },

  getRunningProcesses: () => {
    const state = get();
    return state.processes.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING,
    );
  },

  getProcessStats: (worktreeId?: string) => {
    const processes = worktreeId ? get().getProcessesByWorktree(worktreeId) : get().processes;

    return {
      total: processes.length,
      running: processes.filter(
        (p) => p.status === BackgroundProcessStatus.RUNNING,
      ).length,
      completed: processes.filter(
        (p) => p.status === BackgroundProcessStatus.COMPLETED,
      ).length,
      failed: processes.filter(
        (p) =>
          p.status === BackgroundProcessStatus.FAILED ||
          p.status === BackgroundProcessStatus.KILLED ||
          p.status === BackgroundProcessStatus.KILLED_EXTERNALLY,
      ).length,
    };
  },

  reset: () => {
    set({
      processes: [],
      processOutputs: new Map(),
      selectedProcessId: null,
      isLoading: false,
      error: null,
      lastFetch: 0,
      currentWorktreeId: null,
    });
  },
}));