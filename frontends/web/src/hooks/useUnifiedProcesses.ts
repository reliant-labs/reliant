/**
 * Unified Process Hook
 * 
 * Provides accurate process counts by merging and deduplicating processes
 * from processStore and packageCommandsStore. This ensures the UI shows
 * consistent counts across all components.
 */

import { useMemo } from "react";
import { useProcessStore } from "../store/processStore";
import { usePackageProcesses } from "./package-queries";
import { BackgroundProcessStatus } from "../api/background-grpc";

// Unified process type that works with both stores
interface UnifiedProcess {
  id: string;
  status: BackgroundProcessStatus;
  worktree_id?: string;
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
 * Hook that provides unified, deduplicated process counts across all stores.
 * 
 * @param currentWorktreeId - The current workspace's worktree ID (optional)
 * @returns Unified process counts for current workspace, other workspaces, and totals
 */
export function useUnifiedProcessCounts(currentWorktreeId?: string): UnifiedProcessCounts {
  // Get processes from both stores
  const backgroundProcesses = useProcessStore((state) => state.processes);
  const { data: packageProcesses = [] } = usePackageProcesses();

  return useMemo(() => {
    // Merge and deduplicate processes
    // Package processes take precedence (they may have more detailed info)
    const packageIds = new Set(packageProcesses.map((p) => p.id));
    
    const allProcesses: UnifiedProcess[] = [
      // All package processes
      ...packageProcesses.map((p): UnifiedProcess => ({
        id: p.id,
        status: p.status,
        worktree_id: p.worktree_id,
      })),
      // Background processes that aren't already in package processes
      ...backgroundProcesses
        .filter((p) => !packageIds.has(p.id))
        .map((p): UnifiedProcess => ({
          id: p.id,
          status: p.status,
          worktree_id: p.worktree_id,
        })),
    ];

    // Categorize by workspace
    const currentWorkspaceProcesses = currentWorktreeId
      ? allProcesses.filter((p) => p.worktree_id === currentWorktreeId)
      : allProcesses;
    
    const otherWorkspacesProcesses = currentWorktreeId
      ? allProcesses.filter((p) => p.worktree_id && p.worktree_id !== currentWorktreeId)
      : [];

    // Count running processes
    const currentWorkspaceRunning = currentWorkspaceProcesses.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING
    ).length;
    
    const otherWorkspacesRunning = otherWorkspacesProcesses.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING
    ).length;
    
    const totalRunning = allProcesses.filter(
      (p) => p.status === BackgroundProcessStatus.RUNNING
    ).length;

    return {
      currentWorkspaceRunning,
      otherWorkspacesRunning,
      totalRunning,
      
      currentWorkspaceTotal: currentWorkspaceProcesses.length,
      otherWorkspacesTotal: otherWorkspacesProcesses.length,
      totalProcesses: allProcesses.length,
      
      hasRunningInCurrentWorkspace: currentWorkspaceRunning > 0,
      hasRunningInOtherWorkspaces: otherWorkspacesRunning > 0,
      hasAnyProcesses: allProcesses.length > 0,
    };
  }, [backgroundProcesses, packageProcesses, currentWorktreeId]);
}

/**
 * Simplified hook that just returns running count for current workspace.
 * Use this when you only need to show an indicator dot.
 */
export function useCurrentWorkspaceRunningCount(currentWorktreeId?: string): number {
  const { currentWorkspaceRunning } = useUnifiedProcessCounts(currentWorktreeId);
  return currentWorkspaceRunning;
}