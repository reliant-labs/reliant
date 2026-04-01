import { create } from "zustand";
import type { FileChange } from "../components/Chat/RecentChanges";
import type { FileNode } from "../components/FileBrowser";
import { useBrowserStore } from "./browserStore";
import { useWorkspaceStateStore, type SerializedViewer } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";
import { logger } from "../lib/logger";
import { api } from "../api/client";
import { FileChangeStatus } from "../gen/reliant/v1/common_pb";

export type ViewerType = "diff" | "file" | "worktrees" | "projects" | "agents" | "commands" | "browser" | "workflow";

export interface DiffViewer {
  id: string;
  type: "diff";
  title: string;
  file: FileChange;
  projectId: string; // Associate viewer with project
}

export interface FileViewer {
  id: string;
  type: "file";
  title: string;
  file: FileNode;
  projectId: string; // Associate viewer with project
  worktreeId?: string; // Optional worktree ID for file context
}

export interface WorktreesViewer {
  id: string;
  type: "worktrees";
  title: string;
  projectId: string;
}

export interface ProjectsViewer {
  id: string;
  type: "projects";
  title: string;
  projectId: string;
}

export interface CommandsViewer {
  id: string;
  type: "commands";
  title: string;
  projectId: string;
  worktreeId?: string;
  processId?: string; // Optional: if set, shows logs for this process
}

export interface BrowserViewer {
  id: string;
  type: "browser";
  title: string;
  projectId: string;
  worktreeId: string; // Required: workspace this browser belongs to
  browserTabId: string; // ID of the browser tab from browserStore
}

export interface WorkflowViewer {
  id: string;
  type: "workflow";
  title: string;
  projectId: string;
  chatId: string; // The chat whose workflow execution to display
  workflowName: string; // The workflow name
}

export type Viewer = DiffViewer | FileViewer | WorktreesViewer | ProjectsViewer | CommandsViewer | BrowserViewer | WorkflowViewer;

interface ViewerState {
  viewers: Viewer[];
  activeViewerId: string | null;
  currentProjectId: string | null;
  // NOTE: currentWorktreeId removed - use worktreeStore.currentWorktree?.id as single source of truth
  isFullscreen: boolean;
  isWorkflowMode: boolean; // Controls full-screen workflow builder mode
  workflowToOpen?: string; // Workflow name to load when entering workflow mode
  workflowViewKey: number; // Key to force remount of WorkflowBuilderPage
  isSettingsMode: boolean; // Controls full-screen settings mode
  settingsSection?: string; // Initial section to show when opening settings
  returnToWorkflow: boolean; // Track if settings was opened from workflow mode
  closedFiles: FileViewer[]; // History of closed file viewers for Cmd+Shift+T

  // Context management
  setCurrentProject: (projectId: string | null) => void;
  switchWorktreeContext: (projectId: string, worktreeId: string | null) => void;
  
  // Helper to get current worktree ID from worktreeStore (single source of truth)
  getCurrentWorktreeId: () => string | null;
  
  // Viewer management
  toggleFullscreen: () => void;
  openDiffViewer: (file: FileChange, projectId: string) => void;
  openFileViewer: (file: FileNode, projectId: string, worktreeId?: string, shouldFocus?: boolean) => void;
  openWorktreesViewer: (projectId: string) => void;
  openProjectsViewer: (projectId: string) => void;
  openCommandsViewer: (projectId: string, worktreeId?: string, processId?: string) => void;
  openBrowserViewer: (projectId: string, worktreeId: string, browserTabId?: string) => Promise<void>;
  openWorkflowViewer: (projectId: string, chatId: string, workflowName: string) => void;
  closeViewer: (viewerId: string, options?: { skipBrowserTabClose?: boolean }) => void;
  closeActiveViewer: () => boolean; // Returns true if a viewer was closed
  closeAllViewers: () => void;
  setActiveViewer: (viewerId: string | null) => void;
  updateViewerTitle: (viewerId: string, title: string) => void;
  reopenLastClosedFile: () => boolean; // Returns true if a file was reopened
  
  // Workflow mode (full-screen)
  setWorkflowMode: (open: boolean, workflowName?: string) => void;
  clearWorkflowToOpen: () => void;
  
  // Settings mode (full-screen)
  setSettingsMode: (open: boolean, section?: string) => void;
  
  // Utility
  getActiveViewer: () => Viewer | null;
  hasOpenViewers: () => boolean;
  nextViewer: () => void;
  prevViewer: () => void;
  getProjectViewers: () => Viewer[];
  
  // Persistence
  saveToWorkspaceState: () => void;
  restoreFromWorkspaceState: (projectId: string, worktreeId: string | null) => void;
  serializeViewers: () => SerializedViewer[];
}

const generateViewerId = () =>
  `viewer_${Date.now()}_${Math.random().toString(36).substr(2, 9)}`;

// Note: Removed anti-pattern that cleared localStorage on load
// Viewer state is now managed through workspaceStateStore

export const useViewerStore = create<ViewerState>()((set, get) => ({
      viewers: [],
      activeViewerId: null,
      currentProjectId: null,
      isFullscreen: false,
      isWorkflowMode: false,
      workflowToOpen: undefined,
      workflowViewKey: 0,
      isSettingsMode: false,
      settingsSection: undefined,
      returnToWorkflow: false,
      closedFiles: [],
      
      getCurrentWorktreeId: () => {
        return useWorktreeStore.getState().currentWorktree?.id ?? null;
      },

      toggleFullscreen: () => {
        set((state) => ({ isFullscreen: !state.isFullscreen }));
      },

      setCurrentProject: (projectId: string | null) => {
        const currentId = get().currentProjectId;
        
        // Skip if project hasn't changed
        if (projectId === currentId) {
          return;
        }
        
        // Save current state before switching
        if (currentId) {
          get().saveToWorkspaceState();
        }
        
        set({ currentProjectId: projectId });
        
        // Clear viewers when project changes
        if (currentId !== null) {
          set({ viewers: [], activeViewerId: null });
        }
      },
      
      switchWorktreeContext: (projectId: string, worktreeId: string | null) => {
        const state = get();
        const currentWorktreeId = get().getCurrentWorktreeId();
        const isSameProject = state.currentProjectId === projectId;
        const isSameWorktree = currentWorktreeId === worktreeId;
        
        // No change needed
        if (isSameProject && isSameWorktree) {
          return;
        }
        
        logger.info("[ViewerStore] Switching worktree context", {
          from: { projectId: state.currentProjectId, worktreeId: currentWorktreeId },
          to: { projectId, worktreeId },
        });
        
        // Save current state before switching (if we have a valid context)
        if (state.currentProjectId) {
          get().saveToWorkspaceState();
        }
        
        // Update context - only projectId is stored here, worktreeId comes from worktreeStore
        set({ 
          currentProjectId: projectId,
          // Clear viewers - they'll be restored from workspace state
          viewers: [],
          activeViewerId: null,
        });
        
        // Restore viewers for new context
        get().restoreFromWorkspaceState(projectId, worktreeId);
      },

      openDiffViewer: (file: FileChange, projectId: string) => {
        // Check if this specific file+status combination is already open
        const existing = get().viewers.find(
          (v) => v.type === "diff" && v.file.path === file.path && v.file.status === file.status
        );

        if (existing) {
          // Just activate the existing viewer
          set({ activeViewerId: existing.id });
          return;
        }

        // Create title with status indicator
        const fileName = file.path.split("/").pop() || file.path;
        const statusSuffix =
          file.status === FileChangeStatus.STAGED
            ? " (Index)"
            : file.status === FileChangeStatus.MODIFIED
              ? " (Working Tree)"
              : "";
        const title = `${fileName}${statusSuffix}`;

        // Create new viewer
        const viewerId = generateViewerId();
        const newViewer: DiffViewer = {
          id: viewerId,
          type: "diff",
          title,
          file,
          projectId,
        };

        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
        
        // Immediately persist viewer state
        get().saveToWorkspaceState();
      },

      openFileViewer: (file: FileNode, projectId: string, worktreeId?: string, shouldFocus: boolean = false) => {
        // Check if this file is already open
        const existing = get().viewers.find(
          (v) => v.type === "file" && v.file.path === file.path
        );

        if (existing) {
          // Update the existing viewer with new line information and activate it
          set((state) => ({
            viewers: state.viewers.map((v) =>
              v.id === existing.id && v.type === "file"
                ? { ...v, file: { ...v.file, line: file.line, lineEnd: file.lineEnd, column: file.column } }
                : v
            ),
            activeViewerId: existing.id,
          }));
          
          // Dispatch event to trigger scroll in the viewer component
          window.dispatchEvent(new CustomEvent('file-viewer-scroll-to-line', { 
            detail: { viewerId: existing.id, line: file.line, lineEnd: file.lineEnd, column: file.column } 
          }));
          
          // Dispatch focus event if shouldFocus is true
          if (shouldFocus) {
            setTimeout(() => {
              window.dispatchEvent(new CustomEvent('file-viewer-focus', { 
                detail: { viewerId: existing.id } 
              }));
            }, 100);
          }
          return;
        }

        // Create new viewer
        const viewerId = generateViewerId();
        const newViewer: FileViewer = {
          id: viewerId,
          type: "file",
          title: file.name,
          file,
          projectId,
          worktreeId,
        };

        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
        
        // Immediately persist viewer state
        get().saveToWorkspaceState();
        
        // Dispatch focus event if shouldFocus is true
        if (shouldFocus) {
          setTimeout(() => {
            window.dispatchEvent(new CustomEvent('file-viewer-focus', { 
              detail: { viewerId } 
            }));
          }, 100);
        }
      },

      openWorktreesViewer: (projectId: string) => {
        const existing = get().viewers.find((v) => v.type === "worktrees");
        if (existing) {
          set({ activeViewerId: existing.id });
          return;
        }
        const viewerId = generateViewerId();
        const newViewer: WorktreesViewer = {
          id: viewerId,
          type: "worktrees",
          title: "Workspaces",
          projectId,
        };
        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
      },

      openProjectsViewer: (projectId: string) => {
        const existing = get().viewers.find((v) => v.type === "projects");
        if (existing) {
          set({ activeViewerId: existing.id });
          return;
        }
        const viewerId = generateViewerId();
        const newViewer: ProjectsViewer = {
          id: viewerId,
          type: "projects",
          title: "Projects",
          projectId,
        };
        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
      },

      setWorkflowMode: (open: boolean, workflowName?: string) => {
        const previousMode = get().isWorkflowMode ? (get().workflowToOpen ? 'workflow_builder' : 'workflow_hub') : 'chat';
        
        // Increment key when showing hub (no workflow name) to force remount
        const newKey = workflowName ? get().workflowViewKey : get().workflowViewKey + 1;
        set({ isWorkflowMode: open, workflowToOpen: workflowName, workflowViewKey: newKey });

        // Track page visit
        if (open) {
          const pageName = workflowName ? 'workflow_builder' : 'workflow_hub';
          api.settings.trackPageVisited(pageName, previousMode);
        } else {
          api.settings.trackPageVisited('chat', previousMode);
        }

        // Persist to workspace state
        const projectId = get().currentProjectId;
        const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setWorkflowState(
            projectId,
            worktreeId,
            open,
            workflowName ?? null
          );
        }
      },

      clearWorkflowToOpen: () => {
        set({ workflowToOpen: undefined });
      },

      setSettingsMode: (open: boolean, section?: string) => {
        const { isWorkflowMode, workflowToOpen } = get();
        const previousMode = isWorkflowMode 
          ? (workflowToOpen ? 'workflow_builder' : 'workflow_hub') 
          : 'chat';
        
        if (open) {
          // When opening settings, track if we came from workflow mode
          set({ 
            isSettingsMode: true, 
            settingsSection: section,
            returnToWorkflow: isWorkflowMode,
            isWorkflowMode: false, // Exit workflow mode while in settings
          });
          // Track page visit
          api.settings.trackPageVisited('settings', previousMode);
        } else {
          // When closing settings, return to workflow mode if that's where we came from
          const { returnToWorkflow } = get();
          set({ 
            isSettingsMode: false, 
            settingsSection: undefined,
            isWorkflowMode: returnToWorkflow,
            returnToWorkflow: false,
          });
          // Track page visit
          const nextPage = returnToWorkflow ? 'workflow_hub' : 'chat';
          api.settings.trackPageVisited(nextPage, 'settings');
        }
      },

      openCommandsViewer: (projectId: string, worktreeId?: string, processId?: string) => {
        const existing = get().viewers.find((v) => v.type === "commands");
        if (existing) {
          // Update the existing viewer with new worktreeId/processId if provided
          set((state) => ({
            viewers: state.viewers.map((v) =>
              v.id === existing.id && v.type === "commands"
                ? { ...v, worktreeId: worktreeId ?? (v as CommandsViewer).worktreeId, processId: processId ?? (v as CommandsViewer).processId }
                : v
            ),
            activeViewerId: existing.id,
          }));
          return;
        }
        const viewerId = generateViewerId();
        const newViewer: CommandsViewer = {
          id: viewerId,
          type: "commands",
          title: "Package Commands",
          projectId,
          worktreeId,
          processId,
        };
        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
      },

      openBrowserViewer: async (projectId: string, worktreeId: string, browserTabId?: string) => {
        // Get browser store state
        const { createTab: createBrowserTab } = useBrowserStore.getState();
        
        // If browserTabId provided, check if viewer already exists for that tab
        if (browserTabId) {
          const existing = get().viewers.find(
            (v) => v.type === "browser" && v.browserTabId === browserTabId
          ) as BrowserViewer | undefined;
          if (existing) {
            set({ activeViewerId: existing.id });
            return;
          }
        }
        
        // Create new browser tab if not provided (worktreeId is now required)
        const actualBrowserTabId = browserTabId || await createBrowserTab(worktreeId, undefined, projectId);
        
        // Get the fresh tab after creation
        const browserTab = useBrowserStore.getState().tabs.find((t) => t.id === actualBrowserTabId);
        
        const viewerId = generateViewerId();
        const newViewer: BrowserViewer = {
          id: viewerId,
          type: "browser",
          title: browserTab?.title || "New Tab",
          projectId,
          worktreeId,
          browserTabId: actualBrowserTabId,
        };
        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
        
        // Immediately persist viewer state
        get().saveToWorkspaceState();
      },

      openWorkflowViewer: (projectId: string, chatId: string, workflowName: string) => {
        // Check if viewer for this chat's workflow already exists
        const existing = get().viewers.find(
          (v) => v.type === "workflow" && (v as WorkflowViewer).chatId === chatId
        ) as WorkflowViewer | undefined;
        
        if (existing) {
          // Update the workflow name and activate it
          set((state) => ({
            viewers: state.viewers.map((v) =>
              v.id === existing.id && v.type === "workflow"
                ? { ...v, workflowName }
                : v
            ),
            activeViewerId: existing.id,
          }));
          return;
        }
        
        const viewerId = generateViewerId();
        // Extract friendly name from workflow name (e.g., "builtin://agent" -> "agent")
        const friendlyName = workflowName.includes('://') 
          ? workflowName.split('://').pop() || workflowName
          : workflowName;
        const newViewer: WorkflowViewer = {
          id: viewerId,
          type: "workflow",
          title: friendlyName,
          projectId,
          chatId,
          workflowName,
        };
        set((state) => ({
          viewers: [...state.viewers, newViewer],
          activeViewerId: viewerId,
        }));
        
        // Immediately persist viewer state
        get().saveToWorkspaceState();
      },

      closeViewer: (viewerId: string, options?: { skipBrowserTabClose?: boolean }) => {
        // Find the viewer being closed to check if it's a browser viewer
        const viewerToClose = get().viewers.find((v) => v.id === viewerId);
        
        // If it's a browser viewer, also close the browser tab (unless skipped to prevent recursion)
        if (viewerToClose?.type === "browser" && !options?.skipBrowserTabClose) {
          const browserViewer = viewerToClose as BrowserViewer;
          useBrowserStore.getState().closeTab(browserViewer.browserTabId);
        }
        
        set((state) => {
          const newViewers = state.viewers.filter((v) => v.id !== viewerId);
          let newActiveViewerId = state.activeViewerId;
          let newClosedFiles = [...state.closedFiles];

          // If we're closing a file viewer, add it to closed files history
          if (viewerToClose?.type === "file") {
            const fileViewer = viewerToClose as FileViewer;
            // Add to the beginning of the array (most recent first)
            // Limit to last 50 closed files to prevent memory issues
            newClosedFiles = [fileViewer, ...state.closedFiles].slice(0, 50);
          }

          // If we're closing the active viewer, switch to another one
          if (state.activeViewerId === viewerId) {
            if (newViewers.length > 0) {
              // Find the index of the closed viewer
              const closedIndex = state.viewers.findIndex((v) => v.id === viewerId);
              // Try to activate the next viewer, or the previous one if at the end
              const nextIndex = Math.min(closedIndex, newViewers.length - 1);
              newActiveViewerId = newViewers[nextIndex]?.id || null;
            } else {
              newActiveViewerId = null;
            }
          }

          return {
            viewers: newViewers,
            activeViewerId: newActiveViewerId,
            closedFiles: newClosedFiles,
          };
        });
        
        // Immediately persist viewer state
        get().saveToWorkspaceState();
      },

      closeActiveViewer: () => {
        const { activeViewerId, closeViewer } = get();

        if (activeViewerId) {
          closeViewer(activeViewerId);
          return true;
        }

        return false;
      },

      closeAllViewers: () => {
        set({ viewers: [], activeViewerId: null });
        
        // Immediately persist viewer state
        get().saveToWorkspaceState();
      },

      setActiveViewer: (viewerId: string | null) => {
        if (viewerId === null) {
          set({ activeViewerId: null });
          return;
        }
        const viewer = get().viewers.find((v) => v.id === viewerId);
        if (viewer) {
          set({ activeViewerId: viewerId });
        }
      },

      updateViewerTitle: (viewerId: string, title: string) => {
        set((state) => ({
          viewers: state.viewers.map((v) =>
            v.id === viewerId ? { ...v, title } : v
          ),
        }));
      },

      reopenLastClosedFile: () => {
        const state = get();
        if (state.closedFiles.length === 0) {
          return false;
        }

        // Get the most recently closed file (first in array)
        const lastClosedFile = state.closedFiles[0];
        
        // Remove it from closed files
        set((s) => ({
          closedFiles: s.closedFiles.slice(1),
        }));

        // Reopen the file
        get().openFileViewer(
          lastClosedFile.file,
          lastClosedFile.projectId,
          lastClosedFile.worktreeId,
          true // Auto-focus when reopening
        );

        return true;
      },

      getActiveViewer: () => {
        const { viewers, activeViewerId } = get();
        return viewers.find((v) => v.id === activeViewerId) || null;
      },

      hasOpenViewers: () => {
        // Reuse getProjectViewers which already handles worktree filtering for browsers
        return get().getProjectViewers().length > 0;
      },

      getProjectViewers: () => {
        const { viewers, currentProjectId, getCurrentWorktreeId } = get();
        const currentWorktreeId = getCurrentWorktreeId();
        if (!currentProjectId) {
          return viewers;
        }
        // Filter by project, and for browser viewers also filter by worktree
        return viewers.filter(v => {
          if (v.projectId !== currentProjectId) return false;
          // Browser viewers should only show for the current worktree
          if (v.type === "browser") {
            return (v as BrowserViewer).worktreeId === currentWorktreeId;
          }
          return true;
        });
      },

      nextViewer: () => {
        const { getProjectViewers, activeViewerId, setActiveViewer } = get();
        const projectViewers = getProjectViewers();
        if (projectViewers.length <= 1) return;
        
        const currentIndex = projectViewers.findIndex((v) => v.id === activeViewerId);
        const nextIndex = currentIndex === -1 ? 0 : (currentIndex + 1) % projectViewers.length;
        setActiveViewer(projectViewers[nextIndex].id);
      },

      prevViewer: () => {
        const { getProjectViewers, activeViewerId, setActiveViewer } = get();
        const projectViewers = getProjectViewers();
        if (projectViewers.length <= 1) return;
        
        const currentIndex = projectViewers.findIndex((v) => v.id === activeViewerId);
        const prevIndex = currentIndex === -1
          ? projectViewers.length - 1
          : (currentIndex - 1 + projectViewers.length) % projectViewers.length;
        setActiveViewer(projectViewers[prevIndex].id);
      },
      
      // === Persistence Methods ===
      
      serializeViewers: (): SerializedViewer[] => {
        const { viewers, getCurrentWorktreeId } = get();
        const currentWorktreeId = getCurrentWorktreeId();
        
        // Filter viewers to only include those for the current worktree context
        // Browser viewers are worktree-specific, so only include those matching current worktree
        const filteredViewers = viewers.filter(v => {
          if (v.type === "browser") {
            return v.worktreeId === currentWorktreeId;
          }
          // Other viewer types are not worktree-specific, include them all
          return true;
        });
        
        return filteredViewers.map((viewer): SerializedViewer => {
          const base: SerializedViewer = {
            type: viewer.type,
            title: viewer.title,
          };
          
          switch (viewer.type) {
            case "file":
              return {
                ...base,
                filePath: viewer.file.path,
                worktreeId: viewer.worktreeId,
              };
            case "diff":
              return {
                ...base,
                diffPath: viewer.file.path,
              };
            case "browser":
              return {
                ...base,
                browserTabId: viewer.browserTabId,
              };
            case "commands":
              return {
                ...base,
                worktreeId: viewer.worktreeId,
                processId: viewer.processId,
              };
            case "workflow":
              return {
                ...base,
                chatId: viewer.chatId,
                workflowName: viewer.workflowName,
              };
            default:
              return base;
          }
        });
      },
      
      saveToWorkspaceState: () => {
        const { currentProjectId, viewers, activeViewerId, getCurrentWorktreeId } = get();
        const currentWorktreeId = getCurrentWorktreeId();
        
        if (!currentProjectId) {
          logger.debug("[ViewerStore] No project context, skipping save");
          return;
        }
        
        // Filter viewers to only include those for the current worktree context
        const filteredViewers = viewers.filter(v => {
          if (v.type === "browser") {
            return v.worktreeId === currentWorktreeId;
          }
          return true;
        });
        
        const serializedViewers = get().serializeViewers();
        
        // Calculate activeIndex based on filtered viewers
        const activeIndex = activeViewerId 
          ? filteredViewers.findIndex(v => v.id === activeViewerId)
          : null;
        
        logger.debug("[ViewerStore] Saving to workspace state", {
          projectId: currentProjectId,
          worktreeId: currentWorktreeId,
          viewerCount: serializedViewers.length,
          activeIndex,
        });
        
        useWorkspaceStateStore.getState().setOpenViewers(
          currentProjectId,
          currentWorktreeId,
          serializedViewers,
          activeIndex !== -1 ? activeIndex : null
        );
      },
      
      restoreFromWorkspaceState: (projectId: string, worktreeId: string | null) => {
        const workspaceState = useWorkspaceStateStore.getState();
        const worktreeState = workspaceState.getWorktreeState(projectId, worktreeId);
        
        const { openViewers, activeViewerIndex } = worktreeState;
        
        if (!openViewers || openViewers.length === 0) {
          logger.debug("[ViewerStore] No viewers to restore", { projectId, worktreeId });
          return;
        }
        
        logger.info("[ViewerStore] Restoring viewers from workspace state", {
          projectId,
          worktreeId,
          viewerCount: openViewers.length,
          activeViewerIndex,
        });
        
        // Restore viewers by recreating them
        const restoredViewers: Viewer[] = [];
        
        for (const serialized of openViewers) {
          const viewerId = generateViewerId();
          
          switch (serialized.type) {
            case "file":
              if (serialized.filePath) {
                // Create a minimal FileNode for the file viewer
                const fileName = serialized.filePath.split('/').pop() || serialized.filePath;
                restoredViewers.push({
                  id: viewerId,
                  type: "file",
                  title: serialized.title || fileName,
                  file: {
                    name: fileName,
                    path: serialized.filePath,
                    type: "file",
                  },
                  projectId,
                  worktreeId: serialized.worktreeId,
                });
              }
              break;
              
            // Settings is now full-screen mode, skip viewer restoration
            case "settings":
              logger.debug("[ViewerStore] Skipping settings viewer restoration - now full-screen mode");
              break;
              
            case "worktrees":
              restoredViewers.push({
                id: viewerId,
                type: "worktrees",
                title: "Workspaces",
                projectId,
              });
              break;
              
            case "projects":
              restoredViewers.push({
                id: viewerId,
                type: "projects",
                title: "Projects",
                projectId,
              });
              break;
              
            // Workflows are now full-screen mode, not tabs - skip restoration
            case "workflows":
              logger.debug("[ViewerStore] Skipping workflows viewer restoration - now full-screen mode");
              break;
              
            case "agents":
              // Agents viewer removed - skip restoration
              logger.debug("[ViewerStore] Skipping agents viewer restoration - feature removed");
              break;
              
            case "sandbox":
              // Sandbox viewer removed - skip restoration (now accessed via Settings)
              logger.debug("[ViewerStore] Skipping sandbox viewer restoration - moved to settings");
              break;
              
            case "commands":
              restoredViewers.push({
                id: viewerId,
                type: "commands",
                title: "Package Commands",
                projectId,
                worktreeId: serialized.worktreeId,
                processId: serialized.processId,
              });
              break;
              
            case "browser":
              if (serialized.browserTabId) {
                // Verify browser tab still exists
                const browserTab = useBrowserStore.getState().tabs.find(
                  t => t.id === serialized.browserTabId
                );
                if (browserTab) {
                  restoredViewers.push({
                    id: viewerId,
                    type: "browser",
                    title: browserTab.title || "Browser",
                    projectId,
                    worktreeId: browserTab.worktreeId, // Get worktreeId from the browser tab
                    browserTabId: serialized.browserTabId,
                  });
                }
              }
              break;
              
            case "workflow":
              if (serialized.chatId && serialized.workflowName) {
                // Extract friendly name from workflow name
                const friendlyName = serialized.workflowName.includes('://')
                  ? serialized.workflowName.split('://').pop() || serialized.workflowName
                  : serialized.workflowName;
                restoredViewers.push({
                  id: viewerId,
                  type: "workflow",
                  title: friendlyName,
                  projectId,
                  chatId: serialized.chatId,
                  workflowName: serialized.workflowName,
                });
              }
              break;
              
            // Skip diff viewers - they're typically stale
            case "diff":
              logger.debug("[ViewerStore] Skipping diff viewer restoration", {
                path: serialized.diffPath,
              });
              break;
          }
        }
        
        // Determine active viewer
        let newActiveViewerId: string | null = null;
        if (activeViewerIndex !== null && activeViewerIndex < restoredViewers.length) {
          newActiveViewerId = restoredViewers[activeViewerIndex]?.id || null;
        } else if (restoredViewers.length > 0) {
          newActiveViewerId = restoredViewers[0].id;
        }
        
        set({
          viewers: restoredViewers,
          activeViewerId: newActiveViewerId,
        });
        
        logger.info("[ViewerStore] Restored viewers", {
          count: restoredViewers.length,
          activeViewerId: newActiveViewerId,
        });
      },
}));
