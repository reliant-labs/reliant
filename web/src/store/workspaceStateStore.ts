/**
 * Workspace State Store
 *
 * Centralizes all UI/layout state that should persist across sessions.
 * Uses a three-level hierarchy: Global → Project → Worktree
 *
 * This enables:
 * - Restore last project on app load
 * - Restore last worktree when switching projects
 * - Each worktree maintains independent UI state (viewers, chat, panels)
 * - Seamless switching between worktrees with full state restoration
 */

import { create } from "zustand";
import { persist } from "zustand/middleware";
import { logger } from "../lib/logger";
import type { NavigationTab } from "../components/Layout/NavigationBar";
import type { ViewerType } from "./viewerStore";

// ============================================================================
// Types
// ============================================================================

/** Key used for main branch (null worktreeId) in the worktrees record */
export const MAIN_WORKTREE_KEY = "__main__";

/** Legacy viewer types that are no longer used but may exist in persisted data */
type LegacyViewerType = "settings" | "workflows" | "sandbox" | "agents";

/** Serializable viewer configuration for persistence */
export interface SerializedViewer {
  type: ViewerType | LegacyViewerType;
  title: string;
  // Type-specific data
  filePath?: string; // for file viewers
  diffPath?: string; // for diff viewers
  section?: string; // for settings viewer
  browserTabId?: string; // for browser viewer
  worktreeId?: string; // for file context
  processId?: string; // for commands viewer
  chatId?: string; // for workflow viewer
  workflowName?: string; // for workflow viewer
}

/** Right sidebar tab options */
export type RightSidebarTab = "files" | "changes" | "tasks" | "processes" | "browser";

/** UI state for a specific worktree */
export interface WorktreeState {
  // Chat state
  activeChatId: string | null;
  chatQueue: string[]; // LRU navigation history

  // Workflow state removed — the URL (/workflow, /workflow/$name) is now the
  // source of truth. Persisted blobs from older versions may still contain
  // `isWorkflowMode` and `activeWorkflowName`; they're ignored.

  // Viewer state
  openViewers: SerializedViewer[];
  activeViewerIndex: number | null; // Index into openViewers, not ID

  // Panel state (per-worktree)
  // Note: leftSidebarExpanded (chat sidebar) is now global, not per-worktree
  rightPanelState: {
    fileBrowser: boolean;
  };
  rightSidebarTab: RightSidebarTab; // Active tab in right sidebar (files, changes, tasks, processes)
  terminalOpen: boolean;

  // UI state per chat
  scrollPositions: Record<string, number>; // chatId -> scroll position
  showTasksPanel: Record<string, boolean>;
  showRecentChanges: Record<string, boolean>;
  chatDrafts: Record<string, string>; // chatId -> draft input text (persists across navigation)
  
  // Workflow viewer state per chat/workflow
  expandedLoops: Record<string, string[]>; // workflowKey -> array of expanded loop node IDs
  workflowViewerOpen: Record<string, boolean>; // chatId -> whether workflow viewer is open
  workflowViewerMode: Record<string, 'inline' | 'side'>; // chatId -> workflow viewer mode
  workflowLayoutDirection: Record<string, 'horizontal' | 'vertical'>; // workflowKey -> layout direction
}

/** Project-level workspace state */
export interface ProjectWorkspaceState {
  lastWorktreeId: string | null; // null = main branch
  activeView: NavigationTab; // For navigation tab state
  worktrees: Record<string, WorktreeState>; // key: worktreeId or MAIN_WORKTREE_KEY
  /** Draft text for new chat - persists across worktree switches within the project */
  newChatDraft?: string;
  /** Last used workflow viewer mode - applies to new chats */
  lastWorkflowViewerMode?: 'inline' | 'side';
}

/** Full workspace state store shape */
interface WorkspaceStateStore {
  // === Schema version for migrations ===
  version: number;

  // === Global State ===
  lastProjectId: string | null;
  /** Global left sidebar (chat sidebar) expanded state - same across all workspaces */
  leftSidebarExpanded: boolean;

  // === Per-Project State ===
  projects: Record<string, ProjectWorkspaceState>;

  // === Global Actions ===
  setLastProject: (projectId: string | null) => void;
  setLeftSidebarExpandedGlobal: (expanded: boolean) => void;

  // === Project-level Actions ===
  getProjectState: (projectId: string) => ProjectWorkspaceState;
  setLastWorktree: (projectId: string, worktreeId: string | null) => void;
  setActiveView: (projectId: string, view: NavigationTab) => void;

  // === Worktree-level Actions ===
  getWorktreeState: (
    projectId: string,
    worktreeId: string | null
  ) => WorktreeState;
  setWorktreeState: (
    projectId: string,
    worktreeId: string | null,
    state: Partial<WorktreeState>
  ) => void;
  updateWorktreeState: (
    projectId: string,
    worktreeId: string | null,
    updater: (state: WorktreeState) => Partial<WorktreeState>
  ) => void;

  // === Convenience Actions ===
  setActiveChatId: (
    projectId: string,
    worktreeId: string | null,
    chatId: string | null
  ) => void;
  addToChatQueue: (
    projectId: string,
    worktreeId: string | null,
    chatId: string
  ) => void;
  removeFromChatQueue: (
    projectId: string,
    worktreeId: string | null,
    chatId: string
  ) => void;
  setOpenViewers: (
    projectId: string,
    worktreeId: string | null,
    viewers: SerializedViewer[],
    activeIndex: number | null
  ) => void;
  setScrollPosition: (
    projectId: string,
    worktreeId: string | null,
    chatId: string,
    position: number
  ) => void;
  setTerminalOpen: (
    projectId: string,
    worktreeId: string | null,
    open: boolean
  ) => void;
  setRightPanelState: (
    projectId: string,
    worktreeId: string | null,
    state: Partial<WorktreeState["rightPanelState"]>
  ) => void;
  setRightSidebarTab: (
    projectId: string,
    worktreeId: string | null,
    tab: RightSidebarTab
  ) => void;
  setChatDraft: (
    projectId: string,
    worktreeId: string | null,
    chatId: string,
    text: string
  ) => void;
  clearChatDraft: (
    projectId: string,
    worktreeId: string | null,
    chatId: string
  ) => void;

  // === Right Sidebar Tab Navigation ===
  nextRightSidebarTab: (
    projectId: string,
    worktreeId: string | null
  ) => void;
  prevRightSidebarTab: (
    projectId: string,
    worktreeId: string | null
  ) => void;

  // === Expanded Loops State Actions ===
  getExpandedLoops: (
    projectId: string,
    worktreeId: string | null,
    workflowKey: string
  ) => string[];
  setExpandedLoops: (
    projectId: string,
    worktreeId: string | null,
    workflowKey: string,
    loopNodeIds: string[]
  ) => void;

  // === Workflow Viewer State Actions ===
  getWorkflowViewerOpen: (
    projectId: string,
    worktreeId: string | null,
    chatId: string
  ) => boolean;
  setWorkflowViewerOpen: (
    projectId: string,
    worktreeId: string | null,
    chatId: string,
    open: boolean
  ) => void;
  getWorkflowViewerMode: (
    projectId: string,
    worktreeId: string | null,
    chatId: string
  ) => 'inline' | 'side' | null;
  setWorkflowViewerMode: (
    projectId: string,
    worktreeId: string | null,
    chatId: string,
    mode: 'inline' | 'side'
  ) => void;
  clearWorkflowViewerMode: (
    projectId: string,
    worktreeId: string | null,
    chatId: string
  ) => void;

  // === Workflow Layout Direction Actions ===
  getWorkflowLayoutDirection: (
    projectId: string,
    worktreeId: string | null,
    workflowKey: string
  ) => 'horizontal' | 'vertical';
  setWorkflowLayoutDirection: (
    projectId: string,
    worktreeId: string | null,
    workflowKey: string,
    direction: 'horizontal' | 'vertical'
  ) => void;

  // === Project-level Draft Actions (for new chats) ===
  setNewChatDraft: (projectId: string, text: string) => void;
  getNewChatDraft: (projectId: string) => string;
  clearNewChatDraft: (projectId: string) => void;

  // === Last Workflow Viewer Mode (project-level, for new chats) ===
  getLastWorkflowViewerMode: (projectId: string) => 'inline' | 'side' | null;
  setLastWorkflowViewerMode: (projectId: string, mode: 'inline' | 'side') => void;

  // === Cleanup Actions ===
  removeProjectState: (projectId: string) => void;
  removeWorktreeState: (projectId: string, worktreeId: string) => void;
  cleanupStaleReferences: (
    validProjectIds: string[],
    validWorktreeIds: Record<string, string[]>,
    validChatIds: Record<string, string[]>
  ) => void;

  // === Reset ===
  reset: () => void;
}

// ============================================================================
// Default State Factories
// ============================================================================

/** Create default worktree state */
export function createDefaultWorktreeState(): WorktreeState {
  return {
    activeChatId: null,
    chatQueue: [],
    openViewers: [],
    activeViewerIndex: null,
    rightPanelState: {
      fileBrowser: true,
    },
    rightSidebarTab: "files",
    terminalOpen: false,
    scrollPositions: {},
    showTasksPanel: {},
    showRecentChanges: {},
    chatDrafts: {},
    expandedLoops: {}, // workflowKey -> array of expanded loop node IDs
    workflowViewerOpen: {}, // chatId -> whether workflow viewer is open
    workflowViewerMode: {}, // chatId -> workflow viewer mode
    workflowLayoutDirection: {}, // workflowKey -> layout direction
  };
}

/** Create default project workspace state */
export function createDefaultProjectState(): ProjectWorkspaceState {
  return {
    lastWorktreeId: null,
    activeView: "chats",
    worktrees: {
      [MAIN_WORKTREE_KEY]: createDefaultWorktreeState(),
    },
  };
}

/** Get worktree key from worktreeId (handles null -> MAIN_WORKTREE_KEY) */
function getWorktreeKey(worktreeId: string | null): string {
  return worktreeId ?? MAIN_WORKTREE_KEY;
}

// ============================================================================
// Store Implementation
// ============================================================================

const CURRENT_VERSION = 6;

export const useWorkspaceStateStore = create<WorkspaceStateStore>()(
  persist(
    (set, get) => ({
      version: CURRENT_VERSION,
      lastProjectId: null,
      leftSidebarExpanded: true,
      projects: {},

      // === Global Actions ===
      setLastProject: (projectId) => {
        logger.info("[WorkspaceState] setLastProject", { projectId });
        set({ lastProjectId: projectId });
      },

      setLeftSidebarExpandedGlobal: (expanded) => {
        logger.debug("[WorkspaceState] setLeftSidebarExpandedGlobal", { expanded });
        set({ leftSidebarExpanded: expanded });
      },

      // === Project-level Actions ===
      getProjectState: (projectId) => {
        const state = get();
        return state.projects[projectId] ?? createDefaultProjectState();
      },

      setLastWorktree: (projectId, worktreeId) => {
        logger.info("[WorkspaceState] setLastWorktree", {
          projectId,
          worktreeId,
        });
        set((state) => ({
          projects: {
            ...state.projects,
            [projectId]: {
              ...(state.projects[projectId] ?? createDefaultProjectState()),
              lastWorktreeId: worktreeId,
            },
          },
        }));
      },

      setActiveView: (projectId, view) => {
        logger.debug("[WorkspaceState] setActiveView", { projectId, view });
        set((state) => ({
          projects: {
            ...state.projects,
            [projectId]: {
              ...(state.projects[projectId] ?? createDefaultProjectState()),
              activeView: view,
            },
          },
        }));
      },

      // === Worktree-level Actions ===
      getWorktreeState: (projectId, worktreeId) => {
        const state = get();
        const projectState =
          state.projects[projectId] ?? createDefaultProjectState();
        const worktreeKey = getWorktreeKey(worktreeId);
        const worktreeState = projectState.worktrees[worktreeKey];
        
        if (!worktreeState) {
          return createDefaultWorktreeState();
        }
        
        // Merge with defaults to ensure all fields exist (for migration compatibility)
        return {
          ...createDefaultWorktreeState(),
          ...worktreeState,
          // Ensure nested objects are properly merged
          rightPanelState: {
            ...createDefaultWorktreeState().rightPanelState,
            ...(worktreeState.rightPanelState || {}),
          },
          expandedLoops: worktreeState.expandedLoops ?? {},
          workflowViewerOpen: worktreeState.workflowViewerOpen ?? {},
          workflowViewerMode: worktreeState.workflowViewerMode ?? {},
          workflowLayoutDirection: worktreeState.workflowLayoutDirection ?? {},
        };
      },

      setWorktreeState: (projectId, worktreeId, newState) => {
        const worktreeKey = getWorktreeKey(worktreeId);
        logger.debug("[WorkspaceState] setWorktreeState", {
          projectId,
          worktreeKey,
          keys: Object.keys(newState),
        });

        set((state) => {
          const projectState =
            state.projects[projectId] ?? createDefaultProjectState();
          const currentWorktreeState =
            projectState.worktrees[worktreeKey] ?? createDefaultWorktreeState();

          const newProjects = {
            ...state.projects,
            [projectId]: {
              ...projectState,
              worktrees: {
                ...projectState.worktrees,
                [worktreeKey]: {
                  ...currentWorktreeState,
                  ...newState,
                },
              },
            },
          };
          
          logger.debug("[WorkspaceState] setWorktreeState result", {
            projectsKeys: Object.keys(newProjects),
            worktreesKeys: Object.keys(newProjects[projectId]?.worktrees || {}),
          });

          return { projects: newProjects };
        });
      },

      updateWorktreeState: (projectId, worktreeId, updater) => {
        const currentState = get().getWorktreeState(projectId, worktreeId);
        const updates = updater(currentState);
        get().setWorktreeState(projectId, worktreeId, updates);
      },

      // === Convenience Actions ===
      setActiveChatId: (projectId, worktreeId, chatId) => {
        logger.debug("[WorkspaceState] setActiveChatId", { projectId, worktreeId, chatId });
        get().setWorktreeState(projectId, worktreeId, { activeChatId: chatId });
      },

      addToChatQueue: (projectId, worktreeId, chatId) => {
        get().updateWorktreeState(projectId, worktreeId, (state) => {
          // Remove if exists, then add to front (LRU)
          const newQueue = state.chatQueue.filter((id) => id !== chatId);
          newQueue.unshift(chatId);
          // Keep queue reasonable size (last 50 chats)
          return { chatQueue: newQueue.slice(0, 50) };
        });
      },

      removeFromChatQueue: (projectId, worktreeId, chatId) => {
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          chatQueue: state.chatQueue.filter((id) => id !== chatId),
          // Also clean up UI state for this chat
          scrollPositions: Object.fromEntries(
            Object.entries(state.scrollPositions).filter(
              ([id]) => id !== chatId
            )
          ),
          showTasksPanel: Object.fromEntries(
            Object.entries(state.showTasksPanel).filter(([id]) => id !== chatId)
          ),
          showRecentChanges: Object.fromEntries(
            Object.entries(state.showRecentChanges).filter(
              ([id]) => id !== chatId
            )
          ),
          chatDrafts: Object.fromEntries(
            Object.entries(state.chatDrafts).filter(
              ([id]) => id !== chatId
            )
          ),
          // Clear activeChatId if it was this chat
          activeChatId:
            state.activeChatId === chatId ? null : state.activeChatId,
        }));
      },

      setOpenViewers: (projectId, worktreeId, viewers, activeIndex) => {
        get().setWorktreeState(projectId, worktreeId, {
          openViewers: viewers,
          activeViewerIndex: activeIndex,
        });
      },

      setScrollPosition: (projectId, worktreeId, chatId, position) => {
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          scrollPositions: {
            ...state.scrollPositions,
            [chatId]: position,
          },
        }));
      },

      setTerminalOpen: (projectId, worktreeId, open) => {
        get().setWorktreeState(projectId, worktreeId, { terminalOpen: open });
      },

      setRightPanelState: (projectId, worktreeId, panelState) => {
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          rightPanelState: {
            ...state.rightPanelState,
            ...panelState,
          },
        }));
      },

      setRightSidebarTab: (projectId, worktreeId, tab) => {
        get().setWorktreeState(projectId, worktreeId, { rightSidebarTab: tab });
      },

      setChatDraft: (projectId, worktreeId, chatId, text) => {
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          chatDrafts: {
            ...state.chatDrafts,
            [chatId]: text,
          },
        }));
      },

      clearChatDraft: (projectId, worktreeId, chatId) => {
        get().updateWorktreeState(projectId, worktreeId, (state) => {
          const newDrafts = { ...state.chatDrafts };
          delete newDrafts[chatId];
          return { chatDrafts: newDrafts };
        });
      },

      // === Right Sidebar Tab Navigation ===
      // Tab order matches visual layout in RightSidebar.tsx
      nextRightSidebarTab: (projectId, worktreeId) => {
        const tabs: RightSidebarTab[] = ["files", "changes", "processes", "tasks", "browser"];
        const currentState = get().getWorktreeState(projectId, worktreeId);
        const currentIndex = tabs.indexOf(currentState.rightSidebarTab);
        const nextIndex = (currentIndex + 1) % tabs.length;
        get().setWorktreeState(projectId, worktreeId, { rightSidebarTab: tabs[nextIndex] });
      },

      prevRightSidebarTab: (projectId, worktreeId) => {
        const tabs: RightSidebarTab[] = ["files", "changes", "processes", "tasks", "browser"];
        const currentState = get().getWorktreeState(projectId, worktreeId);
        const currentIndex = tabs.indexOf(currentState.rightSidebarTab);
        const prevIndex = (currentIndex - 1 + tabs.length) % tabs.length;
        get().setWorktreeState(projectId, worktreeId, { rightSidebarTab: tabs[prevIndex] });
      },

      // === Expanded Loops State Actions ===
      getExpandedLoops: (projectId, worktreeId, workflowKey) => {
        const worktreeState = get().getWorktreeState(projectId, worktreeId);
        return worktreeState.expandedLoops[workflowKey] || [];
      },

      setExpandedLoops: (projectId, worktreeId, workflowKey, loopNodeIds) => {
        logger.debug("[WorkspaceState] setExpandedLoops", {
          projectId,
          worktreeId,
          workflowKey,
          loopCount: loopNodeIds.length,
        });
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          expandedLoops: {
            ...state.expandedLoops,
            [workflowKey]: loopNodeIds,
          },
        }));
      },

      // === Workflow Viewer State Actions ===
      getWorkflowViewerOpen: (projectId, worktreeId, chatId) => {
        const worktreeState = get().getWorktreeState(projectId, worktreeId);
        // Safety check: ensure workflowViewerOpen exists (for migration compatibility)
        if (!worktreeState.workflowViewerOpen) {
          return false;
        }
        return worktreeState.workflowViewerOpen[chatId] ?? false;
      },

      setWorkflowViewerOpen: (projectId, worktreeId, chatId, open) => {
        logger.debug("[WorkspaceState] setWorkflowViewerOpen", {
          projectId,
          worktreeId,
          chatId,
          open,
        });
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          workflowViewerOpen: {
            ...(state.workflowViewerOpen || {}),
            [chatId]: open,
          },
        }));
      },

      getWorkflowViewerMode: (projectId, worktreeId, chatId) => {
        const worktreeState = get().getWorktreeState(projectId, worktreeId);
        // Safety check: ensure workflowViewerMode exists (for migration compatibility)
        if (!worktreeState.workflowViewerMode) {
          return null;
        }
        return worktreeState.workflowViewerMode[chatId] ?? null;
      },

      setWorkflowViewerMode: (projectId, worktreeId, chatId, mode) => {
        logger.debug("[WorkspaceState] setWorkflowViewerMode", {
          projectId,
          worktreeId,
          chatId,
          mode,
        });
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          workflowViewerMode: {
            ...(state.workflowViewerMode || {}),
            [chatId]: mode,
          },
        }));
        // REMOVED: Don't save as last used mode - we want to use the setting instead
      },

      clearWorkflowViewerMode: (projectId, worktreeId, chatId) => {
        logger.debug("[WorkspaceState] clearWorkflowViewerMode", {
          projectId,
          worktreeId,
          chatId,
        });
        get().updateWorktreeState(projectId, worktreeId, (state) => {
          const newModes = { ...(state.workflowViewerMode || {}) };
          delete newModes[chatId];
          return { workflowViewerMode: newModes };
        });
      },

      // === Workflow Layout Direction Actions ===
      getWorkflowLayoutDirection: (projectId, worktreeId, workflowKey) => {
        const worktreeState = get().getWorktreeState(projectId, worktreeId);
        // Safety check: ensure workflowLayoutDirection exists (for migration compatibility)
        if (!worktreeState.workflowLayoutDirection) {
          return 'horizontal'; // Default to horizontal
        }
        return worktreeState.workflowLayoutDirection[workflowKey] ?? 'horizontal';
      },

      setWorkflowLayoutDirection: (projectId, worktreeId, workflowKey, direction) => {
        logger.debug("[WorkspaceState] setWorkflowLayoutDirection", {
          projectId,
          worktreeId,
          workflowKey,
          direction,
        });
        get().updateWorktreeState(projectId, worktreeId, (state) => ({
          workflowLayoutDirection: {
            ...(state.workflowLayoutDirection || {}),
            [workflowKey]: direction,
          },
        }));
      },

      // === Project-level Draft Actions (for new chats) ===
      setNewChatDraft: (projectId, text) => {
        set((state) => {
          const projectState = state.projects[projectId] ?? createDefaultProjectState();
          return {
            projects: {
              ...state.projects,
              [projectId]: {
                ...projectState,
                newChatDraft: text,
              },
            },
          };
        });
      },

      getNewChatDraft: (projectId) => {
        const state = get();
        return state.projects[projectId]?.newChatDraft ?? "";
      },

      getLastWorkflowViewerMode: (projectId) => {
        const projectState = get().projects[projectId];
        return projectState?.lastWorkflowViewerMode ?? null;
      },

      setLastWorkflowViewerMode: (projectId, mode) => {
        set((state) => ({
          projects: {
            ...state.projects,
            [projectId]: {
              ...(state.projects[projectId] ?? createDefaultProjectState()),
              lastWorkflowViewerMode: mode,
            },
          },
        }));
      },

      clearNewChatDraft: (projectId) => {
        set((state) => {
          const projectState = state.projects[projectId];
          if (!projectState) return state;
          
          const { newChatDraft: _newChatDraft, ...rest } = projectState;
          return {
            projects: {
              ...state.projects,
              [projectId]: rest as ProjectWorkspaceState,
            },
          };
        });
      },

      // === Cleanup Actions ===
      removeProjectState: (projectId) => {
        logger.info("[WorkspaceState] removeProjectState", { projectId });
        set((state) => {
          const newProjects = { ...state.projects };
          delete newProjects[projectId];
          return {
            projects: newProjects,
            // Clear lastProjectId if it was this project
            lastProjectId:
              state.lastProjectId === projectId ? null : state.lastProjectId,
          };
        });
      },

      removeWorktreeState: (projectId, worktreeId) => {
        logger.info("[WorkspaceState] removeWorktreeState", {
          projectId,
          worktreeId,
        });
        const worktreeKey = getWorktreeKey(worktreeId);

        set((state) => {
          const projectState = state.projects[projectId];
          if (!projectState) return state;

          const newWorktrees = { ...projectState.worktrees };
          delete newWorktrees[worktreeKey];

          return {
            projects: {
              ...state.projects,
              [projectId]: {
                ...projectState,
                worktrees: newWorktrees,
                // Clear lastWorktreeId if it was this worktree
                lastWorktreeId:
                  projectState.lastWorktreeId === worktreeId
                    ? null
                    : projectState.lastWorktreeId,
              },
            },
          };
        });
      },

      cleanupStaleReferences: (
        validProjectIds,
        validWorktreeIds,
        validChatIds
      ) => {
        logger.info("[WorkspaceState] cleanupStaleReferences");

        set((state) => {
          const newProjects: Record<string, ProjectWorkspaceState> = {};

          // Only keep valid projects
          for (const projectId of validProjectIds) {
            const projectState = state.projects[projectId];
            if (!projectState) continue;

            const validWorktrees = validWorktreeIds[projectId] ?? [];
            const validChats = validChatIds[projectId] ?? [];

            // Filter worktrees
            const newWorktrees: Record<string, WorktreeState> = {};
            for (const [worktreeKey, worktreeState] of Object.entries(
              projectState.worktrees
            )) {
              // Always keep main worktree, check others against valid list
              if (
                worktreeKey === MAIN_WORKTREE_KEY ||
                validWorktrees.includes(worktreeKey)
              ) {
                // Clean up chat references within worktree
                newWorktrees[worktreeKey] = {
                  ...worktreeState,
                  activeChatId: validChats.includes(
                    worktreeState.activeChatId ?? ""
                  )
                    ? worktreeState.activeChatId
                    : null,
                  chatQueue: worktreeState.chatQueue.filter((id) =>
                    validChats.includes(id)
                  ),
                  scrollPositions: Object.fromEntries(
                    Object.entries(worktreeState.scrollPositions).filter(
                      ([id]) => validChats.includes(id)
                    )
                  ),
                  showTasksPanel: Object.fromEntries(
                    Object.entries(worktreeState.showTasksPanel).filter(([id]) =>
                      validChats.includes(id)
                    )
                  ),
                  showRecentChanges: Object.fromEntries(
                    Object.entries(worktreeState.showRecentChanges).filter(
                      ([id]) => validChats.includes(id)
                    )
                  ),
                  chatDrafts: Object.fromEntries(
                    Object.entries(worktreeState.chatDrafts || {}).filter(
                      ([id]) => validChats.includes(id) || id === "temp-new-chat"
                    )
                  ),
                  workflowViewerOpen: Object.fromEntries(
                    Object.entries(worktreeState.workflowViewerOpen || {}).filter(
                      ([id]) => validChats.includes(id)
                    )
                  ),
                  workflowViewerMode: Object.fromEntries(
                    Object.entries(worktreeState.workflowViewerMode || {}).filter(
                      ([id]) => validChats.includes(id)
                    )
                  ),
                  workflowLayoutDirection: worktreeState.workflowLayoutDirection || {},
                };
              }
            }

            // Ensure main worktree always exists
            if (!newWorktrees[MAIN_WORKTREE_KEY]) {
              newWorktrees[MAIN_WORKTREE_KEY] = createDefaultWorktreeState();
            }

            newProjects[projectId] = {
              ...projectState,
              lastWorktreeId: validWorktrees.includes(
                projectState.lastWorktreeId ?? ""
              )
                ? projectState.lastWorktreeId
                : null,
              worktrees: newWorktrees,
            };
          }

          return {
            projects: newProjects,
            lastProjectId: validProjectIds.includes(state.lastProjectId ?? "")
              ? state.lastProjectId
              : null,
          };
        });
      },

      reset: () => {
        logger.info("[WorkspaceState] reset");
        set({
          version: CURRENT_VERSION,
          lastProjectId: null,
          projects: {},
        });
      },
    }),
    {
      name: "workspace-state",
      version: CURRENT_VERSION,
      partialize: (state) => ({
        version: state.version,
        lastProjectId: state.lastProjectId,
        leftSidebarExpanded: state.leftSidebarExpanded,
        projects: state.projects,
      }),
      onRehydrateStorage: () => (state, error) => {
        if (error) {
          logger.error("[WorkspaceState] Failed to rehydrate:", error);
          return;
        }
        if (state) {
          logger.info("[WorkspaceState] Rehydrated", {
            lastProjectId: state.lastProjectId,
            projectCount: Object.keys(state.projects).length,
          });
        }
        
        // Clean up deprecated localStorage keys from old tab system
        cleanupDeprecatedStorage();
      },
      migrate: (persistedState: unknown, version: number) => {
        logger.info("[WorkspaceState] Migrating from version", version);

        // Migration v1 -> v2: leftSidebarExpanded moved from per-worktree to global
        if (version < 2) {
          const state = persistedState as Partial<WorkspaceStateStore>;
          // Set global default if not present
          if (state.leftSidebarExpanded === undefined) {
            state.leftSidebarExpanded = true;
          }
          logger.info("[WorkspaceState] Migrated to v2: leftSidebarExpanded is now global");
        }

        // Migration v2 -> v3 was a no-op for the current schema. It used to add
        // isWorkflowMode/activeWorkflowName fields, which have since been
        // removed in favor of route-based navigation (/workflow/*). We keep
        // the version bump only so the migration chain is intact.
        if (version < 3) {
          logger.info("[WorkspaceState] Migrated to v3: (workflow state fields no longer used)");
        }

        // Migration v3 -> v4: Add expandedLoops field to worktrees
        if (version < 4) {
          const state = persistedState as Partial<WorkspaceStateStore>;
          // Add default expandedLoops to all existing worktrees
          if (state.projects) {
            for (const projectId of Object.keys(state.projects)) {
              const project = state.projects[projectId];
              if (project?.worktrees) {
                for (const worktreeKey of Object.keys(project.worktrees)) {
                  const worktree = project.worktrees[worktreeKey] as Partial<WorktreeState>;
                  if (worktree && !worktree.expandedLoops) {
                    worktree.expandedLoops = {};
                  }
                }
              }
            }
          }
          logger.info("[WorkspaceState] Migrated to v4: Added expandedLoops field");
        }

        // Migration v4 -> v5: Add workflowViewerOpen and workflowViewerMode fields to worktrees
        if (version < 5) {
          const state = persistedState as Partial<WorkspaceStateStore>;
          // Add default workflow viewer state to all existing worktrees
          if (state.projects) {
            for (const projectId of Object.keys(state.projects)) {
              const project = state.projects[projectId];
              if (project?.worktrees) {
                for (const worktreeKey of Object.keys(project.worktrees)) {
                  const worktree = project.worktrees[worktreeKey] as Partial<WorktreeState>;
                  if (worktree) {
                    if (!worktree.workflowViewerOpen) {
                      worktree.workflowViewerOpen = {};
                    }
                    if (!worktree.workflowViewerMode) {
                      worktree.workflowViewerMode = {};
                    }
                  }
                }
              }
            }
          }
          logger.info("[WorkspaceState] Migrated to v5: Added workflowViewerOpen and workflowViewerMode fields");
        }

        // Migration v5 -> v6: Add workflowLayoutDirection field to worktrees
        if (version < 6) {
          const state = persistedState as Partial<WorkspaceStateStore>;
          // Add default workflow layout direction to all existing worktrees
          if (state.projects) {
            for (const projectId of Object.keys(state.projects)) {
              const project = state.projects[projectId];
              if (project?.worktrees) {
                for (const worktreeKey of Object.keys(project.worktrees)) {
                  const worktree = project.worktrees[worktreeKey] as Partial<WorktreeState>;
                  if (worktree && !worktree.workflowLayoutDirection) {
                    worktree.workflowLayoutDirection = {};
                  }
                }
              }
            }
          }
          logger.info("[WorkspaceState] Migrated to v6: Added workflowLayoutDirection field");
        }

        return persistedState as WorkspaceStateStore;
      },
    }
  )
);

// ============================================================================
// Deprecated Storage Cleanup
// ============================================================================

/** Keys from deprecated stores that should be removed */
const DEPRECATED_STORAGE_KEYS = [
  "tab-store", // Old tab system - replaced by workspaceStateStore
  "viewer-storage", // Old viewer persistence - now in workspaceStateStore
];

/**
 * Clean up localStorage keys from deprecated systems.
 * Called during rehydration to ensure old data doesn't accumulate.
 */
function cleanupDeprecatedStorage(): void {
  if (typeof window === "undefined" || !window.localStorage) return;
  
  for (const key of DEPRECATED_STORAGE_KEYS) {
    try {
      const existing = localStorage.getItem(key);
      if (existing) {
        logger.info(`[WorkspaceState] Removing deprecated storage: ${key}`);
        localStorage.removeItem(key);
      }
    } catch (e) {
      logger.warn(`[WorkspaceState] Failed to remove deprecated key ${key}:`, e);
    }
  }
}

// ============================================================================
// Selector Hooks for Common Patterns
// ============================================================================

// Stable default objects to avoid creating new references on every render
const DEFAULT_WORKTREE_STATE = createDefaultWorktreeState();
const DEFAULT_PROJECT_STATE = createDefaultProjectState();

/**
 * Hook to get current worktree state for active project/worktree
 * Usage: const worktreeState = useCurrentWorktreeState(projectId, worktreeId);
 * 
 * NOTE: Returns a stable default object when projectId is null to prevent infinite re-renders.
 */
export function useCurrentWorktreeState(
  projectId: string | null,
  worktreeId: string | null
): WorktreeState {
  return useWorkspaceStateStore((state) => {
    if (!projectId) return DEFAULT_WORKTREE_STATE;
    const projectState = state.projects[projectId];
    if (!projectState) return DEFAULT_WORKTREE_STATE;
    const worktreeKey = worktreeId ?? MAIN_WORKTREE_KEY;
    return projectState.worktrees[worktreeKey] ?? DEFAULT_WORKTREE_STATE;
  });
}

/**
 * Hook to get current project state
 * 
 * NOTE: Returns a stable default object when projectId is null to prevent infinite re-renders.
 */
export function useCurrentProjectState(
  projectId: string | null
): ProjectWorkspaceState {
  return useWorkspaceStateStore((state) => {
    if (!projectId) return DEFAULT_PROJECT_STATE;
    return state.projects[projectId] ?? DEFAULT_PROJECT_STATE;
  });
}
