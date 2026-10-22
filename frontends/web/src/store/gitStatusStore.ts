/**
 * Git Status Store
 * 
 * VS Code-inspired git status management with debounced refresh triggers.
 * This store provides a centralized way to trigger git status refreshes
 * when file operations occur (save, create, delete, git operations).
 * 
 * Key features:
 * - Debounced refresh (1000ms) to batch rapid file changes
 * - Per-worktree refresh tracking
 * - Event-based notification for UI components
 * - Throttling to prevent concurrent git status calls
 */

import { create } from "zustand";
import { logger } from "../lib/logger";

// Debounce timeout in ms (matches VS Code's approach)
const DEBOUNCE_MS = 1000;

// Minimum time between refreshes per worktree
const THROTTLE_MS = 2000;

interface GitStatusState {
  // Track pending refresh requests per worktree
  pendingRefreshes: Map<string, NodeJS.Timeout>;
  
  // Track last refresh time per worktree (for throttling)
  lastRefreshTime: Map<string, number>;
  
  // Refresh counter - UI components can subscribe to this to know when to refresh
  // Incremented each time a refresh is triggered
  refreshTrigger: number;
  
  // Track which worktree was last refreshed
  lastRefreshedWorktreeId: string | null;
  
  // Actions
  triggerRefresh: (worktreeId: string | undefined, projectId: string) => void;
  triggerImmediateRefresh: (worktreeId: string | undefined, projectId: string) => void;
  
  // For components to check if they should refresh
  shouldRefresh: (worktreeId: string | undefined, projectId: string) => boolean;
}

// Create a stable key for worktree/project combination
function getRefreshKey(worktreeId: string | undefined, projectId: string): string {
  return worktreeId ? `worktree:${worktreeId}` : `project:${projectId}`;
}

export const useGitStatusStore = create<GitStatusState>((set, get) => ({
  pendingRefreshes: new Map(),
  lastRefreshTime: new Map(),
  refreshTrigger: 0,
  lastRefreshedWorktreeId: null,

  /**
   * Trigger a debounced refresh for a worktree/project.
   * Multiple calls within DEBOUNCE_MS will be batched into one refresh.
   * This is the primary method to call after file operations.
   */
  triggerRefresh: (worktreeId: string | undefined, projectId: string) => {
    const key = getRefreshKey(worktreeId, projectId);
    const state = get();
    
    // Clear any existing pending refresh for this key
    const existingTimeout = state.pendingRefreshes.get(key);
    if (existingTimeout) {
      clearTimeout(existingTimeout);
    }
    
    logger.debug("[GitStatusStore] Scheduling debounced refresh", { worktreeId, projectId, key });
    
    // Schedule new debounced refresh
    const timeout = setTimeout(() => {
      const currentState = get();
      
      // Check throttle - don't refresh if we just refreshed
      const lastRefresh = currentState.lastRefreshTime.get(key) || 0;
      const timeSinceLastRefresh = Date.now() - lastRefresh;
      
      if (timeSinceLastRefresh < THROTTLE_MS) {
        logger.debug("[GitStatusStore] Throttled refresh", { 
          worktreeId, 
          projectId, 
          timeSinceLastRefresh,
          throttleMs: THROTTLE_MS 
        });
        return;
      }
      
      // Execute refresh
      logger.debug("[GitStatusStore] Executing refresh", { worktreeId, projectId, key });
      
      // Update last refresh time
      const newLastRefreshTime = new Map(currentState.lastRefreshTime);
      newLastRefreshTime.set(key, Date.now());
      
      // Remove from pending
      const newPendingRefreshes = new Map(currentState.pendingRefreshes);
      newPendingRefreshes.delete(key);
      
      set({
        pendingRefreshes: newPendingRefreshes,
        lastRefreshTime: newLastRefreshTime,
        refreshTrigger: currentState.refreshTrigger + 1,
        lastRefreshedWorktreeId: worktreeId || null,
      });
    }, DEBOUNCE_MS);
    
    // Store the timeout
    const newPendingRefreshes = new Map(state.pendingRefreshes);
    newPendingRefreshes.set(key, timeout);
    set({ pendingRefreshes: newPendingRefreshes });
  },

  /**
   * Trigger an immediate refresh (bypasses debounce).
   * Use for operations that should be reflected immediately,
   * like git stage/unstage/commit/revert.
   */
  triggerImmediateRefresh: (worktreeId: string | undefined, projectId: string) => {
    const key = getRefreshKey(worktreeId, projectId);
    const state = get();
    
    // Clear any pending debounced refresh
    const existingTimeout = state.pendingRefreshes.get(key);
    if (existingTimeout) {
      clearTimeout(existingTimeout);
    }
    
    logger.debug("[GitStatusStore] Executing immediate refresh", { worktreeId, projectId, key });
    
    // Update last refresh time
    const newLastRefreshTime = new Map(state.lastRefreshTime);
    newLastRefreshTime.set(key, Date.now());
    
    // Remove from pending
    const newPendingRefreshes = new Map(state.pendingRefreshes);
    newPendingRefreshes.delete(key);
    
    set({
      pendingRefreshes: newPendingRefreshes,
      lastRefreshTime: newLastRefreshTime,
      refreshTrigger: state.refreshTrigger + 1,
      lastRefreshedWorktreeId: worktreeId || null,
    });
  },

  /**
   * Check if a component should refresh based on the last triggered refresh.
   * Components can use this to decide whether to re-fetch data.
   */
  shouldRefresh: (worktreeId: string | undefined, _projectId: string): boolean => {
    const state = get();

    // If no worktreeId specified, check if any refresh happened
    if (!worktreeId) {
      return state.lastRefreshedWorktreeId === null;
    }
    
    // Check if this specific worktree was refreshed
    return state.lastRefreshedWorktreeId === worktreeId;
  },
}));

/**
 * Hook to get the refresh trigger value.
 * Components can use this as a dependency to re-fetch when files change.
 */
export function useGitStatusRefreshTrigger(): number {
  return useGitStatusStore((state) => state.refreshTrigger);
}

/**
 * Convenience function to trigger a debounced refresh.
 * Can be called from non-React code (e.g., API functions).
 */
export function triggerGitStatusRefresh(worktreeId: string | undefined, projectId: string): void {
  useGitStatusStore.getState().triggerRefresh(worktreeId, projectId);
}

/**
 * Convenience function to trigger an immediate refresh.
 * Can be called from non-React code (e.g., API functions).
 */
export function triggerImmediateGitStatusRefresh(worktreeId: string | undefined, projectId: string): void {
  useGitStatusStore.getState().triggerImmediateRefresh(worktreeId, projectId);
}
