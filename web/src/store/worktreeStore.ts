import { create } from 'zustand';
import {
  worktreeGrpc,
  type Worktree as GrpcWorktree,
  type DiscoveredWorktree as GrpcDiscoveredWorktree,
  type CleanupMetadata,
} from '../api/worktree-grpc';
import { WorktreeStatus } from '../gen/reliant/v1/worktree_pb';
import { toast } from '../lib/toast-manager';
import { useChatStore } from './chatStore';
import { useChatNavigationStore } from './chatNavigationStore';
import { useWorkspaceStateStore } from './workspaceStateStore';
import { useBrowserStore } from './browserStore';
import { ConnectError } from "@connectrpc/connect";
import { logger } from '../lib/logger';
import { singleflight } from '../lib/singleflight';

// Re-export CleanupMetadata from gRPC types
export type { CleanupMetadata };

export interface Worktree {
  id: string;
  name: string;
  path: string;
  branch: string;
  base_branch: string;
  project_id?: string;
  chat_id?: string;
  session_id?: string;
  status: WorktreeStatus;
  is_main: boolean; // TRUE for the main/default project worktree
  copy_files?: string[]; // Files to copy from source repo (e.g., .env, .env.local)
  created_at: string;
  updated_at: string;
  last_active: string;
  deleted_at?: string | null; // Archive timestamp
  cleanup_metadata?: CleanupMetadata | null; // Tracks what was deleted during archive
}

export interface DiscoveredWorktree {
  path: string;
  name: string;
  branch: string;
  is_imported: boolean;
  is_prunable: boolean;
  imported_id?: string;
}

// Convert gRPC worktree to store worktree format
function grpcToStore(grpc: GrpcWorktree): Worktree {
  return {
    id: grpc.id,
    name: grpc.name,
    path: grpc.path,
    branch: grpc.branch,
    base_branch: grpc.base_branch,
    project_id: grpc.project_id,
    chat_id: grpc.chat_id,
    status: grpc.status,
    is_main: grpc.is_main,
    created_at: grpc.created_at,
    updated_at: grpc.updated_at,
    last_active: grpc.last_active,
    deleted_at: grpc.deleted_at || null,
    cleanup_metadata: grpc.cleanup_metadata || null,
  };
}

// Convert gRPC discovered worktree to store format
function grpcDiscoveredToStore(grpc: GrpcDiscoveredWorktree): DiscoveredWorktree {
  return {
    path: grpc.path,
    name: grpc.name,
    branch: grpc.branch,
    is_imported: grpc.is_imported,
    is_prunable: grpc.is_prunable,
    imported_id: grpc.imported_id,
  };
}

// Extract error message from various error types
function getErrorMessage(error: unknown, defaultMsg: string): string {
  if (error instanceof ConnectError) {
    return error.message;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return defaultMsg;
}

interface WorktreeStore {
  worktrees: Worktree[];
  currentWorktree: Worktree | null;
  discoveredWorktrees: DiscoveredWorktree[];
  isLoading: boolean;
  hasLoaded: boolean;
  isDiscovering: boolean;
  deletingId: string | null;
  error: string | null;
  lastLoadIncludedArchived: boolean;

  loadWorktrees: (projectId?: string, options?: { includeArchived?: boolean }) => Promise<void>;
  selectWorktree: (worktree: Worktree | null, options?: { skipWorkspaceStateSave?: boolean }) => void;
  createWorktree: (data: Partial<Worktree> & { force?: boolean; source_worktree_id?: string }) => Promise<Worktree>;
  importWorktree: (data: { path: string; name?: string; project_id: string }) => Promise<Worktree>;
  discoverWorktrees: (projectId: string) => Promise<void>;
  // ONLY archives (sets deleted_at). Never permanently deletes.
  // Optional cleanup: delete local directory and/or git branch
  archiveWorktree: (id: string, options?: { deleteGitBranch?: boolean; deleteLocalDirectory?: boolean }) => Promise<void>;
  // Archives worktree (sets deleted_at) if not already archived.
  // Permanently deletes from database if already archived.
  // Optional cleanup: delete local directory and/or git branch
  deleteWorktree: (id: string, options?: { deleteGitBranch?: boolean; deleteLocalDirectory?: boolean }) => Promise<void>;
  unarchiveWorktree: (id: string) => Promise<void>;
  updateWorktreeStatus: (id: string, status: Worktree['status']) => Promise<void>;
  restoreLastWorktree: (projectId: string) => boolean;
  switchWorktreeContext: (projectId: string, worktree: Worktree | null, options?: { openFreshNewChat?: boolean }) => Promise<void>;
  reset: () => void;
}

function getCurrentLoadOptions(): { includeArchived: boolean } {
  return { includeArchived: useWorktreeStore.getState().lastLoadIncludedArchived };
}

type ArchivedWorktreeActiveChatSnapshot = {
  activeChatId: string | null;
  activeChatWasInWorktree: boolean;
};

function getArchivedWorktreeActiveChatSnapshot(worktreeId: string): ArchivedWorktreeActiveChatSnapshot {
  const chatStore = useChatStore.getState();
  const activeChatId = chatStore.activeChatId;
  const activeChat = activeChatId ? chatStore.chats.get(activeChatId) : null;

  return {
    activeChatId,
    activeChatWasInWorktree: activeChat?.worktreeId === worktreeId,
  };
}

async function clearArchivedWorktreeActiveChat(
  worktreeId: string,
  snapshot: ArchivedWorktreeActiveChatSnapshot,
  options?: { clearMissingChat?: boolean }
): Promise<void> {
  const { activeChatId, activeChatWasInWorktree } = snapshot;
  if (!activeChatId) return;

  const chatStore = useChatStore.getState();
  if (chatStore.activeChatId !== activeChatId) return;

  const activeChat = chatStore.chats.get(activeChatId);
  const shouldClearActiveChat =
    activeChatWasInWorktree ||
    activeChat?.worktreeId === worktreeId ||
    (options?.clearMissingChat && !activeChat);

  if (!shouldClearActiveChat) return;

  logger.info('[WorktreeStore] Active chat was archived, clearing it', {
    activeChatId,
    worktreeId,
    activeChatStillLoaded: Boolean(activeChat),
  });

  chatStore.clearCurrentChat(worktreeId);
  await useChatNavigationStore.getState().removeFromQueue(activeChatId);
}

export const useWorktreeStore = create<WorktreeStore>((set) => ({
  worktrees: [],
  currentWorktree: null,
  discoveredWorktrees: [],
  isLoading: false,
  hasLoaded: false,
  isDiscovering: false,
  deletingId: null,
  error: null,
  lastLoadIncludedArchived: false,

  loadWorktrees: async (projectId?: string, options?: { includeArchived?: boolean }) => {
    if (!projectId) {
      // gRPC requires projectId - return empty if not provided
      set({ worktrees: [], isLoading: false, hasLoaded: true, lastLoadIncludedArchived: false });
      return;
    }
    set({ isLoading: true, error: null });
    try {
      // Use singleflight to deduplicate concurrent calls for the same project
      // (e.g., selectProject + useWorkspaceRestore both calling loadWorktrees)
      const includeArchived = options?.includeArchived ?? false;
      const loadedWorktrees = await singleflight(`loadWorktrees:${projectId}:${includeArchived ? "with-archived" : "active"}`, async () => {
        const response = await worktreeGrpc.list(projectId, { includeArchived });
        return response.worktrees.map(grpcToStore);
      });
      set({
        worktrees: loadedWorktrees,
        isLoading: false,
        hasLoaded: true,
        lastLoadIncludedArchived: includeArchived,
      });
      
      // Auto-select main worktree if no worktree is currently selected
      const currentWorktree = useWorktreeStore.getState().currentWorktree;
      if (!currentWorktree) {
        const mainWorktree = loadedWorktrees.find(w => w.is_main && w.project_id === projectId);
        if (mainWorktree) {
          logger.info('[WorktreeStore] Auto-selecting main worktree after load', {
            worktreeId: mainWorktree.id,
            name: mainWorktree.name,
          });
          useWorktreeStore.getState().selectWorktree(mainWorktree, { skipWorkspaceStateSave: true });
        }
      }
    } catch (error) {
      set({
        error: getErrorMessage(error, 'Failed to load worktrees'),
        isLoading: false,
        hasLoaded: true,
      });
    }
  },

  selectWorktree: (worktree: Worktree | null, options?: { skipWorkspaceStateSave?: boolean }) => {
    const previousWorktree = useWorktreeStore.getState().currentWorktree;
    
    // Don't do anything if selecting the same worktree
    if (previousWorktree?.id === worktree?.id) {
      return;
    }
    
    logger.info('[WorktreeStore] selectWorktree', {
      from: previousWorktree?.name || 'main',
      to: worktree?.name || 'main',
      skipWorkspaceStateSave: options?.skipWorkspaceStateSave,
    });
    
    set({ currentWorktree: worktree });
    
    // NOTE: viewerStore.currentWorktreeId removed - viewerStore now reads from worktreeStore.currentWorktree directly
    // This eliminates synchronization issues between stores
    
    // Save to workspace state for persistence (unless restoring)
    if (!options?.skipWorkspaceStateSave && worktree?.project_id) {
      useWorkspaceStateStore.getState().setLastWorktree(
        worktree.project_id,
        worktree.id
      );
    } else if (!options?.skipWorkspaceStateSave && previousWorktree?.project_id) {
      // Switching to main branch (null worktree)
      useWorkspaceStateStore.getState().setLastWorktree(
        previousWorktree.project_id,
        null
      );
    }
  },

  createWorktree: async (data: Partial<Worktree> & { force?: boolean; source_worktree_id?: string }) => {
    set({ isLoading: true, error: null });
    try {
      if (!data.project_id || !data.name || !data.branch) {
        throw new Error('project_id, name, and branch are required');
      }

      const worktree = await worktreeGrpc.create(
        data.project_id,
        data.name,
        data.branch,
        {
          baseBranch: data.base_branch,
          chatId: data.chat_id,
          copyFiles: data.copy_files,
          force: data.force,
          sourceWorktreeId: data.source_worktree_id,
        }
      );

      const storeWorktree = grpcToStore(worktree);

      // Add to worktrees list
      set(state => ({
        worktrees: [...state.worktrees, storeWorktree],
        currentWorktree: storeWorktree,
        isLoading: false
      }));

      toast.success(`Workspace "${storeWorktree.name}" created successfully`, {
        duration: 4000,
      });

      return storeWorktree;
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to create workspace');
      set({
        error: errorMessage,
        isLoading: false
      });
      throw new Error(errorMessage);
    }
  },

  // ONLY archives a worktree (sets deleted_at). Never permanently deletes.
  // This is the function that should be used by the UI archive button.
  // Options control whether to cleanup local directory and/or git branch
  archiveWorktree: async (id: string, options?: { deleteGitBranch?: boolean; deleteLocalDirectory?: boolean }) => {
    set({ deletingId: id, error: null });
    try {
      // Get worktree info BEFORE archiving
      const worktree = useWorktreeStore.getState().worktrees.find(w => w.id === id);
      const worktreeName = worktree?.name || 'Workspace';
      const wasCurrentWorktree = useWorktreeStore.getState().currentWorktree?.id === id;
      const activeChatSnapshot = getArchivedWorktreeActiveChatSnapshot(id);

      await worktreeGrpc.archive(id, {
        deleteGitBranch: options?.deleteGitBranch,
        deleteLocalDirectory: options?.deleteLocalDirectory,
      });

      await clearArchivedWorktreeActiveChat(id, activeChatSnapshot);

      // Reload worktrees to get updated state
      const currentProject = worktree?.project_id;
      if (currentProject) {
        await useWorktreeStore.getState().loadWorktrees(currentProject, getCurrentLoadOptions());
        // Reload chats to remove archived ones from the list
        await useChatStore.getState().loadChats();

        await clearArchivedWorktreeActiveChat(id, activeChatSnapshot, { clearMissingChat: true });
        
        // Clean up workspace state for this worktree
        useWorkspaceStateStore.getState().removeWorktreeState(currentProject, id);
        
        // Clean up browser tabs for this worktree
        useBrowserStore.getState().closeWorktreeTabs(id);
        logger.info('[WorktreeStore] Cleaned up workspace state and browser tabs for archived worktree', { worktreeId: id });
        
        // If we archived the currently selected worktree, switch to main worktree
        if (wasCurrentWorktree) {
          const worktrees = useWorktreeStore.getState().worktrees;
          const mainWorktree = worktrees.find(w => w.is_main && w.project_id === currentProject);
          if (mainWorktree) {
            logger.info('[WorktreeStore] Switching to main worktree after archiving current', {
              archivedWorktreeId: id,
              mainWorktreeId: mainWorktree.id,
              openFreshNewChat: true,
            });
            await useWorktreeStore.getState().switchWorktreeContext(currentProject, mainWorktree, { openFreshNewChat: true });
          } else {
            // No main worktree found, just clear selection
            set({ currentWorktree: null });
          }
        }
      }

      set({ deletingId: null });

      // Show toast with cleanup info
      const cleanupParts = [];
      if (options?.deleteLocalDirectory) cleanupParts.push('directory cleaned up');
      if (options?.deleteGitBranch) cleanupParts.push('branch deleted');
      const cleanupMsg = cleanupParts.length > 0 ? ` (${cleanupParts.join(', ')})` : '';
      toast.success(`${worktreeName} archived${cleanupMsg}`, {
        duration: 4000,
      });
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to archive workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        deletingId: null
      });
    }
  },

  // Archives or permanently deletes a worktree based on its current state:
  // - If not archived (deleted_at is NULL): Archives it (sets deleted_at)
  // - If already archived (deleted_at is set): Permanently deletes from database
  // Options control whether to cleanup local directory and/or git branch
  deleteWorktree: async (id: string, options?: { deleteGitBranch?: boolean; deleteLocalDirectory?: boolean }) => {
    set({ deletingId: id, error: null });
    try {
      // Get worktree info BEFORE deleting
      const worktree = useWorktreeStore.getState().worktrees.find(w => w.id === id);
      const isPermanentDelete = worktree?.deleted_at !== null && worktree?.deleted_at !== undefined;
      const worktreeName = worktree?.name || 'Workspace';
      const wasCurrentWorktree = useWorktreeStore.getState().currentWorktree?.id === id;
      const currentProject = worktree?.project_id;
      const activeChatSnapshot = getArchivedWorktreeActiveChatSnapshot(id);

      await worktreeGrpc.delete(id, {
        deleteGitBranch: options?.deleteGitBranch,
        deleteLocalDirectory: options?.deleteLocalDirectory,
      });

      if (!isPermanentDelete) {
        await clearArchivedWorktreeActiveChat(id, activeChatSnapshot);
      }

      set(state => ({
        // If permanent delete, remove from list; otherwise reload to get updated deleted_at
        worktrees: isPermanentDelete
          ? state.worktrees.filter(w => w.id !== id)
          : state.worktrees,
        // Don't clear currentWorktree here - we'll handle it below with proper switch to main
        deletingId: null
      }));

      // Show appropriate toast with cleanup info
      if (isPermanentDelete) {
        const cleanupParts = [];
        if (options?.deleteLocalDirectory) cleanupParts.push('directory deleted');
        if (options?.deleteGitBranch) cleanupParts.push('branch deleted');
        const cleanupMsg = cleanupParts.length > 0 ? ` (${cleanupParts.join(', ')})` : '';
        toast.success(`${worktreeName} deleted permanently${cleanupMsg}`, {
          duration: 4000,
        });
      } else {
        const cleanupParts = [];
        if (options?.deleteLocalDirectory) cleanupParts.push('directory cleaned up');
        if (options?.deleteGitBranch) cleanupParts.push('branch deleted');
        const cleanupMsg = cleanupParts.length > 0 ? ` (${cleanupParts.join(', ')})` : '';
        toast.success(`${worktreeName} archived${cleanupMsg}`, {
          duration: 4000,
        });
      }

      // Clean up workspace state and browser tabs for this worktree (for both archive and permanent delete)
      if (currentProject) {
        useWorkspaceStateStore.getState().removeWorktreeState(currentProject, id);
        useBrowserStore.getState().closeWorktreeTabs(id);
        logger.info('[WorktreeStore] Cleaned up workspace state and browser tabs for deleted worktree', { worktreeId: id, isPermanentDelete });
      }

      // If it was an archive (not permanent delete), reload worktrees and chats to get updated state
      if (!isPermanentDelete) {
        if (currentProject) {
          await useWorktreeStore.getState().loadWorktrees(currentProject, getCurrentLoadOptions());
          // Reload chats to remove archived ones from the list
          await useChatStore.getState().loadChats();

          await clearArchivedWorktreeActiveChat(id, activeChatSnapshot, { clearMissingChat: true });
        }
      }

      // If we deleted/archived the currently selected worktree, switch to main worktree
      if (wasCurrentWorktree && currentProject) {
        const worktrees = useWorktreeStore.getState().worktrees;
        const mainWorktree = worktrees.find(w => w.is_main && w.project_id === currentProject);
        if (mainWorktree) {
          const openFreshNewChat = !isPermanentDelete;
          logger.info('[WorktreeStore] Switching to main worktree after deleting current', {
            deletedWorktreeId: id,
            mainWorktreeId: mainWorktree.id,
            isPermanentDelete,
            openFreshNewChat,
          });
          await useWorktreeStore.getState().switchWorktreeContext(currentProject, mainWorktree, { openFreshNewChat });
        } else {
          // No main worktree found, just clear selection
          set({ currentWorktree: null });
        }
      }
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to delete workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        deletingId: null
      });
    }
  },

  unarchiveWorktree: async (id: string) => {
    set({ isLoading: true, error: null });
    try {
      const worktree = useWorktreeStore.getState().worktrees.find(w => w.id === id);
      const worktreeName = worktree?.name || 'Workspace';

      await worktreeGrpc.unarchive(id);

      // Reload worktrees and chats to get updated state
      if (worktree?.project_id) {
        await useWorktreeStore.getState().loadWorktrees(worktree.project_id, getCurrentLoadOptions());
        // Reload chats to bring back unarchived ones
        await useChatStore.getState().loadChats();
      }

      toast.success(`${worktreeName} restored`, {
        duration: 4000,
      });

      set({ isLoading: false });
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to unarchive workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        isLoading: false
      });
      throw error;
    }
  },

  updateWorktreeStatus: async (id: string, status: Worktree['status']) => {
    set({ isLoading: true, error: null });
    try {
      await worktreeGrpc.update(id, { status });

      // Update in local state
      set(state => ({
        worktrees: state.worktrees.map(w =>
          w.id === id ? { ...w, status } : w
        ),
        currentWorktree: state.currentWorktree?.id === id
          ? { ...state.currentWorktree, status }
          : state.currentWorktree,
        isLoading: false
      }));
    } catch (error) {
      set({
        error: getErrorMessage(error, 'Failed to update workspace status'),
        isLoading: false
      });
    }
  },

  discoverWorktrees: async (projectId: string) => {
    set({ isDiscovering: true, error: null });
    try {
      const response = await worktreeGrpc.discover(projectId);
      set({
        discoveredWorktrees: response.discovered.map(grpcDiscoveredToStore),
        isDiscovering: false
      });
    } catch (error) {
      set({
        error: getErrorMessage(error, 'Failed to discover workspaces'),
        isDiscovering: false,
        discoveredWorktrees: []
      });
    }
  },

  importWorktree: async (data: { path: string; name?: string; project_id: string }) => {
    set({ isLoading: true, error: null });
    try {
      const worktree = await worktreeGrpc.import(data.project_id, data.path, {
        name: data.name,
      });

      const storeWorktree = grpcToStore(worktree);

      // Add to worktrees list and mark as imported in discovered list
      set(state => ({
        worktrees: [...state.worktrees, storeWorktree],
        currentWorktree: storeWorktree,
        discoveredWorktrees: state.discoveredWorktrees.map(w =>
          w.path === data.path ? { ...w, is_imported: true, imported_id: storeWorktree.id } : w
        ),
        isLoading: false
      }));

      toast.success(`Workspace "${storeWorktree.name}" imported successfully`, {
        duration: 4000,
      });

      return storeWorktree;
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to import workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        isLoading: false
      });
      throw new Error(errorMessage);
    }
  },

  /**
   * Restore the last selected worktree for a project from workspace state.
   * Called after worktrees are loaded for a project.
   * If no last worktree is saved, selects the main worktree by default.
   * @returns true if a worktree was restored/selected, false if no worktrees available
   */
  restoreLastWorktree: (projectId: string) => {
    const workspaceState = useWorkspaceStateStore.getState();
    const projectState = workspaceState.getProjectState(projectId);
    const lastWorktreeId = projectState.lastWorktreeId;
    const worktrees = useWorktreeStore.getState().worktrees;
    
    // Helper to select the main worktree as fallback
    const selectMainWorktree = () => {
      const mainWorktree = worktrees.find(w => w.is_main && w.project_id === projectId);
      if (mainWorktree) {
        logger.info('[WorktreeStore] Selecting main worktree as default', {
          worktreeId: mainWorktree.id,
          name: mainWorktree.name,
          branch: mainWorktree.branch,
        });
        useWorktreeStore.getState().selectWorktree(mainWorktree, { skipWorkspaceStateSave: true });
        return true;
      }
      logger.warn('[WorktreeStore] No main worktree found for project', { projectId });
      set({ currentWorktree: null });
      return false;
    };
    
    if (!lastWorktreeId) {
      logger.info('[WorktreeStore] No last worktree to restore, selecting main worktree');
      return selectMainWorktree();
    }
    
    // Find the worktree in the loaded list
    const worktree = worktrees.find(w => w.id === lastWorktreeId);
    
    if (!worktree) {
      logger.warn('[WorktreeStore] Last worktree no longer exists, selecting main worktree', { lastWorktreeId });
      // Clear the stale reference
      workspaceState.setLastWorktree(projectId, null);
      return selectMainWorktree();
    }
    
    logger.info('[WorktreeStore] Restoring last worktree', {
      worktreeId: worktree.id,
      name: worktree.name,
    });
    
    // Select without saving (we're restoring, not changing)
    useWorktreeStore.getState().selectWorktree(worktree, { skipWorkspaceStateSave: true });
    return true;
  },

  /**
   * Switch worktree context - saves current context and restores new context.
   * This triggers viewer store and chat navigation to switch contexts.
   */
  switchWorktreeContext: async (
    projectId: string,
    worktree: Worktree | null,
    options?: { openFreshNewChat?: boolean }
  ) => {
    const currentWorktree = useWorktreeStore.getState().currentWorktree;
    const newWorktreeId = worktree?.id ?? null;
    const currentWorktreeId = currentWorktree?.id ?? null;

    // Don't do anything if same worktree
    if (newWorktreeId === currentWorktreeId) {
      if (options?.openFreshNewChat) {
        useChatStore.getState().clearCurrentChat(newWorktreeId);
        useWorkspaceStateStore.getState().setActiveChatId(projectId, newWorktreeId, null);
      }
      return;
    }

    logger.info('[WorktreeStore] switchWorktreeContext', {
      from: currentWorktree?.name || 'main',
      to: worktree?.name || 'main',
      openFreshNewChat: options?.openFreshNewChat,
    });

    // Import viewerStore dynamically to avoid circular dependency
    // IMPORTANT: Must use sync import since we need this to complete before selectWorktree
    const { useViewerStore } = await import('./viewerStore');
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
    useWorktreeStore.getState().selectWorktree(worktree);
  },

  reset: () => {
    set({
      worktrees: [],
      currentWorktree: null,
      discoveredWorktrees: [],
      isLoading: false,
      hasLoaded: false,
      isDiscovering: false,
      deletingId: null,
      error: null,
      lastLoadIncludedArchived: false,
    });
  },
}));

/**
 * Get the active worktree ID for global workspace context (terminal, file browser, search, etc).
 *
 * This is the single source of truth for "which workspace are we working in".
 * Falls back to the main worktree if no worktree is explicitly selected.
 *
 * NOTE: Do NOT use this for chat-specific context (e.g. rendering file links in messages).
 * For that, use the chat's own worktreeId.
 */
export function useActiveWorktreeId(): string | undefined {
  const currentWorktreeId = useWorktreeStore((state) => state.currentWorktree?.id);
  const mainWorktreeId = useWorktreeStore((state) => state.worktrees.find((w) => w.is_main)?.id);
  return currentWorktreeId || mainWorktreeId;
}