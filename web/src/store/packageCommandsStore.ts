import { create } from "zustand";
import {
  packageCommandsGrpc,
  type PackageCommand,
  type PackageProcess,
  type PackageType,
  type ProcessLogsResponse,
} from "../api/package-commands-grpc";
import { BackgroundProcessStatus } from "../api/background-grpc";
import { logger } from "../lib/logger";

interface PackageCommandsState {
  // State
  commands: Record<PackageType, PackageCommand[]>;
  detectedTypes: PackageType[];
  processes: PackageProcess[];
  processLogs: Map<string, ProcessLogsResponse>;
  selectedProcessId: string | null;
  isLoadingCommands: boolean;
  isLoadingProcesses: boolean;
  error: string | null;
  currentWorktreeId: string | null;
  currentPath: string | null; // Project path when no worktree is selected

  // Actions
  loadCommands: (options: { worktreeId?: string; path?: string; customDirectories?: string[] }) => Promise<void>;
  runCommand: (commandName: string, packageType: PackageType, workingDir?: string, env?: Record<string, string>) => Promise<string | null>;
  loadProcesses: (worktreeId?: string) => Promise<void>;
  loadProcessLogs: (processId: string) => Promise<void>;
  killProcess: (processId: string) => Promise<void>;
  selectProcess: (processId: string | null) => void;
  clearError: () => void;
  reset: () => void;

  // Process updates from WebSocket
  handleProcessStarted: (process: Partial<PackageProcess>) => void;
  handleProcessCompleted: (processId: string, exitCode?: number, endTime?: string, processData?: {
    command: string;
    working_dir: string;
    start_time: string;
    worktree_id?: string;
  }) => void;
  handleProcessFailed: (processId: string, exitCode?: number, endTime?: string, processData?: {
    command: string;
    working_dir: string;
    start_time: string;
    worktree_id?: string;
  }) => void;

  // Getters
  getCommandsByCategory: (packageType: PackageType) => Map<string, PackageCommand[]>;
  getRunningProcesses: () => PackageProcess[];
}

export const usePackageCommandsStore = create<PackageCommandsState>((set, get) => ({
  // Initial state
  commands: {} as Record<PackageType, PackageCommand[]>,
  detectedTypes: [],
  processes: [],
  processLogs: new Map(),
  selectedProcessId: null,
  isLoadingCommands: false,
  isLoadingProcesses: false,
  error: null,
  currentWorktreeId: null,
  currentPath: null,

  // Load available commands
  loadCommands: async (options) => {
    try {
      set({ isLoadingCommands: true, error: null });
      
      const response = await packageCommandsGrpc.listCommands({
        worktreeId: options.worktreeId,
        path: options.path,
        customDirectories: options.customDirectories,
      });
      
      set({
        commands: response.commands,
        detectedTypes: response.detected_types,
        currentWorktreeId: options.worktreeId || null,
        currentPath: options.path || null,
        isLoadingCommands: false,
      });

      logger.info("Loaded package commands", {
        types: response.detected_types,
        commandCounts: Object.entries(response.commands).map(([type, cmds]) => ({
          type,
          count: cmds.length,
        })),
      });
    } catch (error) {
      logger.error("Failed to load package commands", error);
      set({
        error: error instanceof Error ? error.message : "Failed to load commands",
        isLoadingCommands: false,
      });
    }
  },

  // Run a command
  runCommand: async (commandName, packageType, workingDir, env) => {
    const state = get();
    
    // Need either worktreeId or path to run a command
    if (!state.currentWorktreeId && !state.currentPath) {
      set({ error: "No worktree or project path selected" });
      return null;
    }

    try {
      const response = await packageCommandsGrpc.runCommand({
        worktree_id: state.currentWorktreeId || undefined,
        path: state.currentPath || undefined,
        command_name: commandName,
        package_type: packageType,
        working_dir: workingDir,
        env,
      });

      logger.info("Started package command", {
        command: commandName,
        workingDir,
        processId: response.process_id,
      });

      // Refresh processes list if we have a worktree
      if (state.currentWorktreeId) {
        await get().loadProcesses(state.currentWorktreeId);
      }

      return response.process_id;
    } catch (error) {
      logger.error("Failed to run package command", error);
      set({
        error: error instanceof Error ? error.message : "Failed to run command",
      });
      return null;
    }
  },

  // Load processes for a worktree, or all processes if no worktreeId is provided
  loadProcesses: async (worktreeId) => {
    try {
      set({ isLoadingProcesses: true, error: null });
      
      const processes = await packageCommandsGrpc.listProcesses(worktreeId);
      
      set({
        processes,
        isLoadingProcesses: false,
      });

      logger.info("Loaded package processes", { count: processes.length, worktreeId: worktreeId || "all" });
    } catch (error) {
      logger.error("Failed to load package processes", error);
      set({
        error: error instanceof Error ? error.message : "Failed to load processes",
        isLoadingProcesses: false,
      });
    }
  },

  // Load logs for a process
  loadProcessLogs: async (processId) => {
    try {
      const logs = await packageCommandsGrpc.getProcessLogs(processId);
      
      set((state) => {
        const newLogs = new Map(state.processLogs);
        newLogs.set(processId, logs);
        return { processLogs: newLogs };
      });

      logger.debug("Loaded process logs", { processId });
    } catch (error) {
      logger.error("Failed to load process logs", error);
      set({
        error: error instanceof Error ? error.message : "Failed to load logs",
      });
    }
  },

  // Kill a process
  killProcess: async (processId) => {
    try {
      await packageCommandsGrpc.killProcess(processId);
      
      logger.info("Killed process", { processId });

      // Update local state
      set((state) => ({
        processes: state.processes.map((p) =>
          p.id === processId
            ? { ...p, status: BackgroundProcessStatus.KILLED }
            : p
        ),
      }));
    } catch (error) {
      logger.error("Failed to kill process", error);
      set({
        error: error instanceof Error ? error.message : "Failed to kill process",
      });
    }
  },

  // Select a process for viewing
  selectProcess: (processId) => {
    set({ selectedProcessId: processId });
    
    // Load logs when selecting a process
    if (processId) {
      get().loadProcessLogs(processId);
    }
  },

  clearError: () => set({ error: null }),

  reset: () => {
    set({
      commands: {} as Record<PackageType, PackageCommand[]>,
      detectedTypes: [],
      processes: [],
      processLogs: new Map(),
      selectedProcessId: null,
      isLoadingCommands: false,
      isLoadingProcesses: false,
      error: null,
      currentWorktreeId: null,
      currentPath: null,
    });
  },

  // Handle process started from WebSocket
  // Always add the process so that subsequent updates (completed/failed) can find it
  // The UI will filter by worktree when displaying
  handleProcessStarted: (process) => {
    set((state) => {
      const newProcess: PackageProcess = {
        id: process.id || "",
        command: process.command || "",
        status: BackgroundProcessStatus.RUNNING,
        worktree_id: process.worktree_id,
        working_dir: process.working_dir || "",
        start_time: process.start_time || new Date().toISOString(),
      };

      return {
        processes: [newProcess, ...state.processes.filter((p) => p.id !== process.id)],
      };
    });
  },

  // Handle process completed from WebSocket - upsert pattern
  handleProcessCompleted: (processId, exitCode, endTime, processData) => {
    logger.info("[PackageCommandsStore] handleProcessCompleted called", {
      processId: processId.slice(0, 8),
      exitCode,
      hasProcessData: !!processData,
    });
    set((state) => {
      const exists = state.processes.some((p) => p.id === processId);
      logger.info("[PackageCommandsStore] Process exists check", {
        processId: processId.slice(0, 8),
        exists,
        storeCount: state.processes.length,
      });
      if (exists) {
        return {
          processes: state.processes.map((p) =>
            p.id === processId
              ? {
                  ...p,
                  status: BackgroundProcessStatus.COMPLETED,
                  exit_code: exitCode,
                  end_time: endTime,
                }
              : p
          ),
        };
      } else if (processData) {
        // Process doesn't exist but we have data to create it
        const newProcess: PackageProcess = {
          id: processId,
          command: processData.command,
          status: BackgroundProcessStatus.COMPLETED,
          working_dir: processData.working_dir,
          start_time: processData.start_time,
          end_time: endTime,
          exit_code: exitCode,
          worktree_id: processData.worktree_id,
        };
        return {
          processes: [newProcess, ...state.processes],
        };
      }
      return state;
    });
  },

  // Handle process failed from WebSocket - upsert pattern
  handleProcessFailed: (processId, exitCode, endTime, processData) => {
    logger.info("[PackageCommandsStore] handleProcessFailed called", {
      processId: processId.slice(0, 8),
      exitCode,
      hasProcessData: !!processData,
    });
    set((state) => {
      const exists = state.processes.some((p) => p.id === processId);
      logger.info("[PackageCommandsStore] Process exists check (failed)", {
        processId: processId.slice(0, 8),
        exists,
        storeCount: state.processes.length,
      });
      if (exists) {
        return {
          processes: state.processes.map((p) =>
            p.id === processId
              ? {
                  ...p,
                  status: BackgroundProcessStatus.FAILED,
                  exit_code: exitCode,
                  end_time: endTime,
                }
              : p
          ),
        };
      } else if (processData) {
        // Process doesn't exist but we have data to create it
        const newProcess: PackageProcess = {
          id: processId,
          command: processData.command,
          status: BackgroundProcessStatus.FAILED,
          working_dir: processData.working_dir,
          start_time: processData.start_time,
          end_time: endTime,
          exit_code: exitCode,
          worktree_id: processData.worktree_id,
        };
        return {
          processes: [newProcess, ...state.processes],
        };
      }
      return state;
    });
  },

  // Get commands grouped by category
  getCommandsByCategory: (packageType) => {
    const state = get();
    const commands = state.commands[packageType] || [];
    
    const byCategory = new Map<string, PackageCommand[]>();
    
    for (const cmd of commands) {
      const category = cmd.category || "other";
      const existing = byCategory.get(category) || [];
      existing.push(cmd);
      byCategory.set(category, existing);
    }
    
    return byCategory;
  },

  // Get running processes
  getRunningProcesses: () => {
    const state = get();
    return state.processes.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING,
    );
  },
}));
