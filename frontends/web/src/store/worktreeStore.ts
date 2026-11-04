import { create } from 'zustand';
import { useChatStore } from './chatStore';
import { useChatNavigationStore } from './chatNavigationStore';
import { useWorkspaceStateStore } from './workspaceStateStore';
import { logger } from '../lib/logger';

/**
 * WHICH workspace is selected — client state only.
 *
 * The workspace rows themselves live in the query cache
 * (hooks/worktree-queries.ts): read the selection with `useCurrentWorktree()`
 * in a component or `getCurrentWorktree()` elsewhere, both of which resolve
 * this id against the cache, so the selection always shows the newest row.
 * Lifecycle actions (create, archive, delete, restore) are in
 * hooks/worktree-actions.ts.
 */
interface WorktreeStore {
  // The selected workspace's id; null selects the project's main checkout.
  currentWorktreeId: string | null;
  // The project the selected workspace belongs to, so switching back to main
  // records the choice against the right project.
  currentWorktreeProjectId: string | null;
  // The workspace an archive/delete is in flight for (row spinners).
  deletingId: string | null;

  selectWorktree: (
    worktree: { id: string; project_id?: string } | null,
    options?: { skipWorkspaceStateSave?: boolean },
  ) => void;
  /**
   * Switch worktree context - saves current context and restores new context.
   * This triggers viewer store and chat navigation to switch contexts.
   */
  switchWorktreeContext: (
    projectId: string,
    worktree: { id: string; project_id?: string } | null,
    options?: {
      openFreshNewChat?: boolean;
      /** Rechecked after async preparation, before any context is committed. */
      isCurrent?: () => boolean;
    },
  ) => Promise<void>;
  setDeletingId: (id: string | null) => void;
  reset: () => void;
}

export const useWorktreeStore = create<WorktreeStore>((set, get) => ({
  currentWorktreeId: null,
  currentWorktreeProjectId: null,
  deletingId: null,

  selectWorktree: (worktree, options) => {
    const previousId = get().currentWorktreeId;
    const previousProjectId = get().currentWorktreeProjectId;

    // Don't do anything if selecting the same worktree
    if (previousId === (worktree?.id ?? null)) {
      return;
    }

    logger.info('[WorktreeStore] selectWorktree', {
      from: previousId || 'main',
      to: worktree?.id || 'main',
      skipWorkspaceStateSave: options?.skipWorkspaceStateSave,
    });

    set({ currentWorktreeId: worktree?.id ?? null, currentWorktreeProjectId: worktree?.project_id ?? null });

    // Save to workspace state for persistence (unless restoring)
    if (!options?.skipWorkspaceStateSave && worktree?.project_id) {
      useWorkspaceStateStore.getState().setLastWorktree(worktree.project_id, worktree.id);
    } else if (!options?.skipWorkspaceStateSave && previousProjectId) {
      // Switching to main branch (null worktree)
      useWorkspaceStateStore.getState().setLastWorktree(previousProjectId, null);
    }
  },

  switchWorktreeContext: async (projectId, worktree, options) => {
    if (options?.isCurrent && !options.isCurrent()) return;
    const newWorktreeId = worktree?.id ?? null;
    const currentWorktreeId = get().currentWorktreeId;

    // Don't do anything if same worktree
    if (newWorktreeId === currentWorktreeId) {
      if (options?.openFreshNewChat) {
        useChatStore.getState().clearCurrentChat(newWorktreeId);
        useWorkspaceStateStore.getState().setActiveChatId(projectId, newWorktreeId, null);
      }
      return;
    }

    logger.info('[WorktreeStore] switchWorktreeContext', {
      from: currentWorktreeId || 'main',
      to: newWorktreeId || 'main',
      openFreshNewChat: options?.openFreshNewChat,
    });

    // Import viewerStore dynamically to avoid circular dependency. Selection
    // can change while this yields; check the caller's fence before ANY writes.
    const { useViewerStore } = await import('./viewerStore');
    if (options?.isCurrent && !options.isCurrent()) return;

    // Everything below commits synchronously, with no further async gap.
    // Save current viewer state and switch context
    useViewerStore.getState().switchWorktreeContext(projectId, newWorktreeId);

    if (options?.openFreshNewChat) {
      useChatStore.getState().clearCurrentChat(newWorktreeId);
      useWorkspaceStateStore.getState().setActiveChatId(projectId, newWorktreeId, null);
    } else {
      // Restore chat navigation state for new worktree
      useChatNavigationStore.getState().restoreFromWorkspaceState(projectId, newWorktreeId);
    }

    // Select the worktree (this saves to workspace state)
    get().selectWorktree(worktree ? { id: worktree.id, project_id: worktree.project_id ?? projectId } : null);
  },

  setDeletingId: (id) => set({ deletingId: id }),

  reset: () => {
    set({ currentWorktreeId: null, currentWorktreeProjectId: null, deletingId: null });
  },
}));
