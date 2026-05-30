/**
 * useWorkspaceRestore Hook
 *
 * Orchestrates full workspace state restoration on app load.
 * Handles restoring:
 * - Last selected project
 * - Last selected worktree within project
 * - Active chat and chat queue
 * - Open viewers
 * - Panel states (sidebar, file browser, terminal)
 * - Scroll positions
 */

import { useEffect, useState, useCallback, useRef } from "react";
import { logger } from "../lib/logger";
import { useWorkspaceStateStore } from "../store/workspaceStateStore";
import { useProjectStore } from "../store/projectStore";
import { useWorktreeStore } from "../store/worktreeStore";
import { useViewerStore } from "../store/viewerStore";
import { useChatStore } from "../store/chatStore";
import { useChatNavigationStore } from "../store/chatNavigationStore";
import { useTerminalStore } from "../store/terminalStore";

export interface WorkspaceRestoreResult {
  /** Whether restoration is in progress */
  isRestoring: boolean;
  /** Whether restoration completed (success or failure) */
  isComplete: boolean;
  /** Whether restoration succeeded */
  isSuccess: boolean;
  /** Error message if restoration failed */
  error: string | null;
  /** Warnings encountered during restoration */
  warnings: string[];
  /** The restored project (if any) */
  restoredProject: { id: string; name: string } | null;
  /** The restored worktree (if any) */
  restoredWorktree: { id: string; name: string } | null;
}

export interface UseWorkspaceRestoreOptions {
  /** Whether to automatically restore on mount (default: true) */
  autoRestore?: boolean;
  /** Skip project restoration (just restore worktree/chat state for current project) */
  skipProjectRestore?: boolean;
  /** Callback when restoration completes */
  onComplete?: (result: WorkspaceRestoreResult) => void;
}

/**
 * Hook to restore workspace state on app load.
 * 
 * Usage:
 * ```tsx
 * const { isRestoring, isComplete, error } = useWorkspaceRestore();
 * 
 * if (isRestoring) {
 *   return <LoadingSpinner />;
 * }
 * ```
 */
export function useWorkspaceRestore(
  options: UseWorkspaceRestoreOptions = {}
): WorkspaceRestoreResult & { restore: () => Promise<void> } {
  const { autoRestore = true, skipProjectRestore = false, onComplete } = options;

  const [state, setState] = useState<WorkspaceRestoreResult>({
    isRestoring: autoRestore,
    isComplete: false,
    isSuccess: false,
    error: null,
    warnings: [],
    restoredProject: null,
    restoredWorktree: null,
  });

  const hasRestoredRef = useRef(false);
  const onCompleteRef = useRef(onComplete);
  onCompleteRef.current = onComplete;

  // Sync isRestoring with autoRestore when it changes from false to true
  // This ensures we show loading state immediately when autoRestore becomes true,
  // not just when the restore() function is called
  useEffect(() => {
    if (autoRestore && !hasRestoredRef.current && !state.isRestoring) {
      setState((prev) => ({ ...prev, isRestoring: true }));
    }
  }, [autoRestore, state.isRestoring]);

  const restore = useCallback(async () => {
    // Prevent double restoration
    if (hasRestoredRef.current) {
      logger.debug("[WorkspaceRestore] Already restored, skipping");
      return;
    }

    logger.info("[WorkspaceRestore] Starting workspace restoration");
    const warnings: string[] = [];
    let restoredProject: { id: string; name: string } | null = null;
    let restoredWorktree: { id: string; name: string } | null = null;

    setState((prev) => ({ ...prev, isRestoring: true, error: null }));

    // Safety timeout: if restoration hangs (e.g. daemon unreachable), force-complete after 10s
    const timeoutId = setTimeout(() => {
      if (!hasRestoredRef.current) {
        logger.warn("[WorkspaceRestore] Timed out after 10s — forcing completion");
        hasRestoredRef.current = true;
        const result: WorkspaceRestoreResult = {
          isRestoring: false,
          isComplete: true,
          isSuccess: false,
          error: "Restoration timed out",
          warnings,
          restoredProject,
          restoredWorktree,
        };
        setState(result);
        onCompleteRef.current?.(result);
      }
    }, 10_000);

    try {
      // Wait for workspace state to be hydrated from localStorage.
      // Zustand persist hydrates synchronously for localStorage, so hasHydrated()
      // is typically true by the time any component code runs. If not (e.g. hydration
      // error), proceed with defaults after a short yield rather than deadlocking.
      if (!useWorkspaceStateStore.persist.hasHydrated()) {
        logger.info("[WorkspaceRestore] Waiting for workspace state hydration...");
        // Yield to microtask queue — gives synchronous hydration a chance to complete
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        if (!useWorkspaceStateStore.persist.hasHydrated()) {
          logger.warn("[WorkspaceRestore] Hydration not complete after yield — proceeding with defaults");
        }
      }

      const projectStore = useProjectStore.getState();
      const worktreeStore = useWorktreeStore.getState();
      const viewerStore = useViewerStore.getState();
      const chatNavStore = useChatNavigationStore.getState();
      const workspaceState = useWorkspaceStateStore.getState();

      // Step 1: Restore project (if not skipped)
      if (!skipProjectRestore) {
        // Check if projects are loaded, if not wait a bit
        if (projectStore.projects.length === 0 && !projectStore.isLoading) {
          logger.info("[WorkspaceRestore] Waiting for projects to load...");
          await projectStore.loadProjects();
        }

        // Attempt to restore last project
        const restored = await projectStore.restoreLastProject();
        // Get fresh state after restoration (the original projectStore reference is stale)
        const freshProjectStore = useProjectStore.getState();
        if (restored && freshProjectStore.currentProject) {
          restoredProject = {
            id: freshProjectStore.currentProject.id,
            name: freshProjectStore.currentProject.name,
          };
          logger.info("[WorkspaceRestore] Restored project", restoredProject);
        } else {
          logger.info("[WorkspaceRestore] No project to restore");
          // No project to restore - complete early
          hasRestoredRef.current = true;
          const result: WorkspaceRestoreResult = {
            isRestoring: false,
            isComplete: true,
            isSuccess: true,
            error: null,
            warnings,
            restoredProject: null,
            restoredWorktree: null,
          };
          setState(result);
          onCompleteRef.current?.(result);
          window.dispatchEvent(new CustomEvent("workspace-restored", { detail: result }));
          return;
        }
      }

      // Get fresh state again (the reference at line 143 may have been reassigned)
      const currentProject = useProjectStore.getState().currentProject;
      if (!currentProject) {
        logger.warn("[WorkspaceRestore] No current project after restoration attempt");
        hasRestoredRef.current = true;
        const result: WorkspaceRestoreResult = {
          isRestoring: false,
          isComplete: true,
          isSuccess: true,
          error: null,
          warnings,
          restoredProject: null,
          restoredWorktree: null,
        };
        setState(result);
        onCompleteRef.current?.(result);
        return;
      }

      // Step 2: Load worktrees if not loaded
      if (worktreeStore.worktrees.length === 0) {
        await worktreeStore.loadWorktrees(currentProject.id);
      } else {
        logger.debug(`[WorkspaceRestore] Worktrees already loaded (${worktreeStore.worktrees.length})`);
      }

      // Step 3: Restore last worktree
      const worktreeRestored = worktreeStore.restoreLastWorktree(currentProject.id);
      // Get fresh worktree state after restoration
      const freshWorktreeStore = useWorktreeStore.getState();
      if (worktreeRestored && freshWorktreeStore.currentWorktree) {
        restoredWorktree = {
          id: freshWorktreeStore.currentWorktree.id,
          name: freshWorktreeStore.currentWorktree.name,
        };
        logger.info("[WorkspaceRestore] Restored worktree", restoredWorktree);
      }

      const currentWorktreeId = freshWorktreeStore.currentWorktree?.id ?? null;

      // Step 4: Get workspace state for this project/worktree
      const worktreeState = workspaceState.getWorktreeState(currentProject.id, currentWorktreeId);

      // Step 5: Restore viewers
      // NOTE: viewerStore reads project context from projectStore (single owner)
      // and worktree context from worktreeStore — no setCurrentProject call needed.
      viewerStore.restoreFromWorkspaceState(currentProject.id, currentWorktreeId);
      // Get fresh viewer state after restoration
      const freshViewerStore = useViewerStore.getState();
      logger.info("[WorkspaceRestore] Restored viewers", {
        count: freshViewerStore.viewers.length,
      });

      // Step 6: Restore chat navigation state
      chatNavStore.restoreFromWorkspaceState(currentProject.id, currentWorktreeId);
      logger.info("[WorkspaceRestore] Restored chat navigation state");

      // Step 7: Always load chats (needed for sidebar even without an active chat)
      // Use hasLoaded instead of chats.size — a project may genuinely have 0 chats,
      // and an earlier loadChats failure could leave size=0 with hasLoaded=false.
      if (!useChatStore.getState().hasLoaded) {
        await useChatStore.getState().loadChats();
      }

      // Restore active chat if one was saved
      if (worktreeState.activeChatId) {
        // Get fresh chat state after loading
        const freshChatStore = useChatStore.getState();
        // Find and select the chat
        const chatToRestore = freshChatStore.chats.get(worktreeState.activeChatId);

        if (chatToRestore) {
          logger.info("[WorkspaceRestore] Restoring active chat", {
            chatId: chatToRestore.id,
            title: chatToRestore.title,
          });
          if (chatToRestore.worktreeId) {
            const targetWorktree = useWorktreeStore.getState().worktrees.find((worktree) => worktree.id === chatToRestore.worktreeId) ?? null;
            if (targetWorktree) {
              await useWorktreeStore.getState().switchWorktreeContext(currentProject.id, targetWorktree);
            }
          } else {
            await useWorktreeStore.getState().switchWorktreeContext(currentProject.id, null);
          }
          freshChatStore.selectChat(chatToRestore);

          // Step 8: Schedule scroll position restoration (after chat renders)
          const scrollPosition = worktreeState.scrollPositions[chatToRestore.id];
          if (scrollPosition) {
            // Use requestAnimationFrame to wait for render
            requestAnimationFrame(() => {
              window.dispatchEvent(
                new CustomEvent("restore-chat-scroll", {
                  detail: { chatId: chatToRestore.id, position: scrollPosition },
                })
              );
            });
          }
        } else {
          warnings.push(`Active chat no longer exists: ${worktreeState.activeChatId}`);
          // Clear the stale reference
          workspaceState.setActiveChatId(currentProject.id, currentWorktreeId, null);
        }
      }

      // Step 9: Restore terminal open state
      if (worktreeState.terminalOpen) {
        useTerminalStore.getState().showTerminal();
        logger.info("[WorkspaceRestore] Restored terminal open state");
      }

      // Step 10: Workflow mode restoration removed — the URL (/workflow,
      // /workflow/$name) is now the source of truth for the workflow view.
      // The browser preserves the URL across reloads, so no manual restore is
      // needed.

      hasRestoredRef.current = true;
      const result: WorkspaceRestoreResult = {
        isRestoring: false,
        isComplete: true,
        isSuccess: true,
        error: null,
        warnings,
        restoredProject,
        restoredWorktree,
      };

      setState(result);
      onCompleteRef.current?.(result);
      window.dispatchEvent(new CustomEvent("workspace-restored", { detail: result }));

      logger.info("[WorkspaceRestore] Restoration complete", {
        restoredProject: restoredProject?.name,
        restoredWorktree: restoredWorktree?.name,
        warnings: warnings.length,
      });
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Unknown error";
      logger.error("[WorkspaceRestore] Restoration failed", { error });

      hasRestoredRef.current = true;
      const result: WorkspaceRestoreResult = {
        isRestoring: false,
        isComplete: true,
        isSuccess: false,
        error: errorMessage,
        warnings,
        restoredProject,
        restoredWorktree,
      };

      setState(result);
      onCompleteRef.current?.(result);
    } finally {
      clearTimeout(timeoutId);
    }
  }, [skipProjectRestore]);

  // Auto-restore on mount
  useEffect(() => {
    if (autoRestore && !hasRestoredRef.current) {
      restore();
    }
  }, [autoRestore, restore]);

  return { ...state, restore };
}

/**
 * Hook to save current workspace state.
 * Call this before navigation away or on unmount.
 */
export function useSaveWorkspaceState() {
  return useCallback(() => {
    const projectStore = useProjectStore.getState();
    const worktreeStore = useWorktreeStore.getState();
    const viewerStore = useViewerStore.getState();

    const projectId = projectStore.currentProject?.id;
    const worktreeId = worktreeStore.currentWorktree?.id ?? null;

    if (!projectId) {
      logger.debug("[SaveWorkspaceState] No project to save");
      return;
    }

    // Save viewer state
    viewerStore.saveToWorkspaceState();

    logger.info("[SaveWorkspaceState] Saved workspace state", {
      projectId,
      worktreeId,
    });
  }, []);
}

/**
 * Hook that automatically saves workspace state on visibility change or unmount.
 */
export function useAutoSaveWorkspaceState() {
  const saveState = useSaveWorkspaceState();

  useEffect(() => {
    // Save on visibility change (tab switch, minimize)
    const handleVisibilityChange = () => {
      if (document.visibilityState === "hidden") {
        saveState();
      }
    };

    // Save on beforeunload (window close, refresh)
    const handleBeforeUnload = () => {
      saveState();
    };

    document.addEventListener("visibilitychange", handleVisibilityChange);
    window.addEventListener("beforeunload", handleBeforeUnload);

    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      window.removeEventListener("beforeunload", handleBeforeUnload);
      // Save on unmount
      saveState();
    };
  }, [saveState]);
}