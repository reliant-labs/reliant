/**
 * Unified processes
 *
 * The daemon tracks one table of background processes; the app reads it
 * through two services. PackageCommandsService lists the Commands panel's own
 * (with the workspace each ran for); BackgroundService lists every one,
 * agents' included (with ports, and the chat and thread that started it).
 * This merges the two into one list, once, for every reader: the Commands
 * panel, its counts and the log viewer.
 */

import { useMemo } from "react";
import { BackgroundProcessStatus, useBackgroundProcesses, type BackgroundProcess } from "./background-queries";
import { usePackageProcesses, type PackageProcess } from "./package-queries";

/** A process as every process surface reads it, whichever service listed it. */
export interface UnifiedProcess {
  id: string;
  command: string;
  status: BackgroundProcessStatus;
  start_time: string;
  end_time?: string;
  exit_code?: number;
  working_dir: string;
  /** The workspace a Commands-panel process ran for. */
  worktree_id?: string;
  /** The chat (and thread) whose agent started it. */
  chat_id?: string;
  thread_id?: string;
  /** The machine it runs on: its output, stream and stop go there. */
  daemon_id?: string;
  ports?: { port: number; protocol?: string }[];
  /** "package": the Commands panel started it; "background": an agent did. */
  source: "package" | "background";
}

function fromPackage(p: PackageProcess, background?: BackgroundProcess): UnifiedProcess {
  return {
    id: p.id,
    command: p.command,
    status: p.status,
    start_time: p.start_time,
    end_time: p.end_time,
    exit_code: p.exit_code,
    working_dir: p.working_dir,
    worktree_id: p.worktree_id,
    daemon_id: p.daemon_id || background?.daemon_id,
    ports: background?.ports?.length ? background.ports.map(({ port, protocol }) => ({ port, protocol })) : undefined,
    source: "package",
  };
}

function fromBackground(p: BackgroundProcess): UnifiedProcess {
  return {
    id: p.id,
    command: p.command,
    status: p.status,
    start_time: p.start_time,
    end_time: p.end_time,
    exit_code: p.exit_code,
    working_dir: p.working_dir,
    worktree_id: p.worktree_id,
    chat_id: p.chat_id,
    thread_id: p.thread_id,
    daemon_id: p.daemon_id,
    ports: p.ports?.map(({ port, protocol }) => ({ port, protocol })),
    source: "background",
  };
}

function startedAt(p: UnifiedProcess): number {
  const t = new Date(p.start_time).getTime();
  return Number.isNaN(t) ? 0 : t;
}

/** Merge the two lists: a package process wins (it names its workspace), enriched with what BackgroundService knows. */
export function mergeProcesses(packageProcesses: PackageProcess[], backgroundProcesses: BackgroundProcess[]): UnifiedProcess[] {
  const backgroundById = new Map(backgroundProcesses.map((p) => [p.id, p]));
  const packageIds = new Set(packageProcesses.map((p) => p.id));
  const merged = [
    ...packageProcesses.map((p) => fromPackage(p, backgroundById.get(p.id))),
    ...backgroundProcesses.filter((p) => !packageIds.has(p.id)).map(fromBackground),
  ];
  return merged.sort((a, b) => startedAt(b) - startedAt(a));
}

/** Every process on the user's machines, newest first. */
export function useUnifiedProcesses() {
  const background = useBackgroundProcesses();
  const packages = usePackageProcesses();
  const processes = useMemo(() => mergeProcesses(packages.data, background.data), [packages.data, background.data]);
  return {
    processes,
    isLoading: background.isLoading || packages.isLoading,
    error: background.error ?? packages.error ?? null,
    refetch: () => Promise.all([background.refetch(), packages.refetch()]),
  };
}

interface UnifiedProcessCounts {
  // Running process counts
  currentWorkspaceRunning: number;
  otherWorkspacesRunning: number;
  totalRunning: number;

  // Total process counts (all statuses)
  currentWorkspaceTotal: number;
  otherWorkspacesTotal: number;
  totalProcesses: number;

  // Convenience booleans
  hasRunningInCurrentWorkspace: boolean;
  hasRunningInOtherWorkspaces: boolean;
  hasAnyProcesses: boolean;
}

/**
 * Process counts for the current workspace, the others, and in total.
 *
 * @param currentWorktreeId - The current workspace's worktree ID (optional)
 */
export function useUnifiedProcessCounts(currentWorktreeId?: string): UnifiedProcessCounts {
  const { processes } = useUnifiedProcesses();

  return useMemo(() => {
    const current = currentWorktreeId ? processes.filter((p) => p.worktree_id === currentWorktreeId) : processes;
    const others = currentWorktreeId
      ? processes.filter((p) => p.worktree_id && p.worktree_id !== currentWorktreeId)
      : [];
    const running = (list: UnifiedProcess[]) => list.filter((p) => p.status === BackgroundProcessStatus.RUNNING).length;

    const currentWorkspaceRunning = running(current);
    const otherWorkspacesRunning = running(others);
    return {
      currentWorkspaceRunning,
      otherWorkspacesRunning,
      totalRunning: running(processes),
      currentWorkspaceTotal: current.length,
      otherWorkspacesTotal: others.length,
      totalProcesses: processes.length,
      hasRunningInCurrentWorkspace: currentWorkspaceRunning > 0,
      hasRunningInOtherWorkspaces: otherWorkspacesRunning > 0,
      hasAnyProcesses: processes.length > 0,
    };
  }, [processes, currentWorktreeId]);
}

/**
 * Simplified hook that just returns running count for current workspace.
 * Use this when you only need to show an indicator dot.
 */
export function useCurrentWorkspaceRunningCount(currentWorktreeId?: string): number {
  const { currentWorkspaceRunning } = useUnifiedProcessCounts(currentWorktreeId);
  return currentWorkspaceRunning;
}
