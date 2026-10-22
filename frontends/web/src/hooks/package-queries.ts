import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  packageCommandsGrpc,
  type PackageCommand,
  type PackageProcess,
  type PackageType,
  type ProcessLogsResponse,
  type ListCommandsResponse,
} from "../api/package-commands-grpc";
import { usePackageCommandsStore } from "../store/packageCommandsStore";

// Re-export types for consumer convenience
export type { PackageCommand, PackageProcess, PackageType, ProcessLogsResponse, ListCommandsResponse };

// ── Query key factory ───────────────────────────────────────────────────────

export const packageKeys = {
  all: ["packages"] as const,
  commands: (worktreeId?: string) => [...packageKeys.all, "commands", worktreeId] as const,
  processes: (worktreeId?: string) => [...packageKeys.all, "processes", worktreeId] as const,
  processLogs: (processId: string) => [...packageKeys.all, "logs", processId] as const,
};

// ── Query hooks ─────────────────────────────────────────────────────────────

export function usePackageCommands(worktreeId?: string, path?: string) {
  return useQuery({
    queryKey: packageKeys.commands(worktreeId),
    queryFn: () => packageCommandsGrpc.listCommands({ worktreeId, path }),
    enabled: !!(worktreeId || path),
  });
}

export function usePackageProcesses(worktreeId?: string) {
  // Merge: React Query fetched data + real-time store updates from WebSocket
  const storeProcesses = usePackageCommandsStore((state) => state.processes);

  const query = useQuery({
    queryKey: packageKeys.processes(worktreeId),
    queryFn: () => packageCommandsGrpc.listProcesses(worktreeId),
  });

  // The store receives real-time updates via globalUpdatesStore (handleProcessStarted/Completed/Failed).
  // Merge store processes on top of query data so the UI reflects WebSocket events immediately.
  const mergedProcesses = (() => {
    const fetched = query.data ?? [];
    if (storeProcesses.length === 0) return fetched;

    // Build a map keyed by process ID; store wins on conflicts (fresher data).
    const byId = new Map<string, PackageProcess>();
    for (const p of fetched) byId.set(p.id, p);
    for (const p of storeProcesses) byId.set(p.id, p);
    return Array.from(byId.values());
  })();

  return {
    ...query,
    data: mergedProcesses,
  };
}

export function useProcessLogs(processId: string) {
  return useQuery({
    queryKey: packageKeys.processLogs(processId),
    queryFn: () => packageCommandsGrpc.getProcessLogs(processId),
    enabled: !!processId,
  });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useRunCommand() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (params: {
      worktreeId?: string;
      path?: string;
      commandName: string;
      packageType: PackageType;
      workingDir?: string;
      env?: Record<string, string>;
    }) =>
      packageCommandsGrpc.runCommand({
        worktree_id: params.worktreeId,
        path: params.path,
        command_name: params.commandName,
        package_type: params.packageType,
        working_dir: params.workingDir,
        env: params.env,
      }),
    onSuccess: (_data, variables) => {
      // Invalidate processes so the list refreshes
      queryClient.invalidateQueries({ queryKey: packageKeys.processes(variables.worktreeId) });
    },
  });
}

export function useKillProcess() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (processId: string) => packageCommandsGrpc.killProcess(processId),
    onSuccess: () => {
      // Invalidate all process queries
      queryClient.invalidateQueries({ queryKey: [...packageKeys.all, "processes"] });
    },
  });
}
