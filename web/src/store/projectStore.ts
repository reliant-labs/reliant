import { logger } from "../lib/logger";
import { create } from "zustand";

import { projectGrpc, type Project as GrpcProject } from "../api/project-grpc";
import { toast } from "../lib/toast-manager";
import { useWorktreeStore } from "./worktreeStore";
import { useChatStore } from "./chatStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useViewerStore } from "./viewerStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useGlobalDataStore } from "./globalDataStore";

export interface Project {
  id: string;
  name: string;
  path: string;
  description?: string;
  is_git_repo: boolean;
  default_branch?: string;
  worktree_count: number;
  last_active: string;
  created_at: string;
  updated_at: string;
}

interface ProjectStore {
  projects: Project[];
  currentProject: Project | null;
  isLoading: boolean;
  error: string | null;

  loadProjects: () => Promise<void>;
  selectProject: (project: Project, options?: { skipClear?: boolean; skipWorkspaceStateSave?: boolean }) => Promise<void>;
  restoreLastProject: () => Promise<boolean>;
  refreshCurrentProject: () => Promise<void>;
  createProject: (data: Partial<Project>) => Promise<Project>;
  updateProject: (id: string, data: Partial<Project>) => Promise<void>;
  deleteProject: (id: string) => Promise<void>;
  touchProject: (id: string) => Promise<void>;
  reset: () => void;
}

// Convert gRPC Project to store Project (adds worktree_count)
function grpcToStoreProject(p: GrpcProject): Project {
  return {
    ...p,
    // Ensure last_active is set (fallback to updated_at or created_at if needed)
    last_active: p.last_active || p.updated_at || p.created_at,
  };
}

let loadProjectsPromise: Promise<void> | null = null;
let lastLoadedAt = 0;
const LOAD_COOLDOWN_MS = 5000;

export const useProjectStore = create<ProjectStore>((set, get) => ({
  projects: [],
  currentProject: null,
  isLoading: false,
  error: null,

  loadProjects: async () => {
    // Return if recently loaded successfully (prevents sequential duplicate calls)
    const now = Date.now();
    if (now - lastLoadedAt < LOAD_COOLDOWN_MS && get().projects.length > 0) {
      return;
    }
    if (loadProjectsPromise) return loadProjectsPromise;
    loadProjectsPromise = (async () => {
      try {
        set({ isLoading: true, error: null });
        const response = await projectGrpc.list();
        const projects = response.projects.map(grpcToStoreProject);

        set({
          projects,
          isLoading: false,
        });
        lastLoadedAt = Date.now();
      } catch (error) {
        logger.error("[ProjectStore] Failed to load projects", error);
        const errorMessage =
          error instanceof Error ? error.message : "Failed to load projects";
        set({
          error: errorMessage,
          isLoading: false,
        });
        throw error;
      } finally {
        loadProjectsPromise = null;
      }
    })();
    return loadProjectsPromise;
  },

  selectProject: async (project: Project, options?: { skipClear?: boolean; skipWorkspaceStateSave?: boolean }) => {
    // Clear worktrees and chats FIRST to avoid showing stale data
    // Skip clearing if this is initial window setup with context
    if (!options?.skipClear) {
      const worktreeStore = useWorktreeStore.getState();
      worktreeStore.worktrees = [];
      worktreeStore.currentWorktree = null;

      const chatStore = useChatStore.getState();
      chatStore.chats = new Map();
    }

    set({ currentProject: project });
    
    // Notify electron of the project change for window state persistence
    if (window.electronAPI?.setWindowProject) {
      window.electronAPI.setWindowProject({
        projectPath: project.path,
        projectName: project.name,
        projectId: project.id,
      }).catch((err) => {
        logger.warn("[ProjectStore] Failed to update window project", err);
      });
    }
    
    // Save to workspace state store (unless restoring)
    if (!options?.skipWorkspaceStateSave) {
      useWorkspaceStateStore.getState().setLastProject(project.id);
      logger.info("[ProjectStore] Saved lastProjectId to workspace state", { projectId: project.id });
    }
    
    // Touch the project to update last active
    get().touchProject(project.id);
    
    // Always load workflows and presets for the project (they are project-scoped)
    const globalDataStore = useGlobalDataStore.getState();
    globalDataStore.refetchWorkflows(project.id).catch((err) => {
      logger.warn("[ProjectStore] Failed to load workflows for project", { projectId: project.id, error: err });
    });
    globalDataStore.refetchPresets(project.id).catch((err) => {
      logger.warn("[ProjectStore] Failed to load presets for project", { projectId: project.id, error: err });
    });

    // Only reload data if we're not skipping clear (user initiated project switch)
    if (!options?.skipClear) {
      // Load worktrees and chats in parallel (they're independent)
      await Promise.all([
        useWorktreeStore.getState().loadWorktrees(project.id),
        useChatStore.getState().loadChats(),
      ]);
      
      // Restore last worktree for this project
      useWorktreeStore.getState().restoreLastWorktree(project.id);
      const currentWorktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
      
      // Restore viewer state
      useViewerStore.getState().setCurrentProject(project.id);
      useViewerStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore chat navigation state
      useChatNavigationStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore active chat from workspace state
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(project.id, currentWorktreeId);
      if (worktreeState.activeChatId) {
        const chat = worktreeState.activeChatId
          ? useChatStore.getState().chats.get(worktreeState.activeChatId)
          : undefined;
        if (chat) {
          logger.info("[ProjectStore] Restoring active chat on project switch", {
            chatId: chat.id,
            title: chat.title,
          });
          if (chat.worktreeId) {
            const targetWorktree = useWorktreeStore.getState().worktrees.find((worktree) => worktree.id === chat.worktreeId) ?? null;
            if (targetWorktree) {
              await useWorktreeStore.getState().switchWorktreeContext(project.id, targetWorktree);
            }
          } else {
            await useWorktreeStore.getState().switchWorktreeContext(project.id, null);
          }
          useChatStore.getState().selectChat(chat);
        }
      }
      
      logger.info("[ProjectStore] Restored workspace state for project", {
        projectId: project.id,
        worktreeId: currentWorktreeId,
        activeChatId: worktreeState.activeChatId,
      });
    }
  },

  restoreLastProject: async () => {
    const { lastProjectId } = useWorkspaceStateStore.getState();
    
    if (!lastProjectId) {
      logger.info("[ProjectStore] No lastProjectId to restore");
      return false;
    }
    
    const { projects } = get();
    const project = projects.find((p) => p.id === lastProjectId);
    
    if (!project) {
      logger.warn("[ProjectStore] Last project no longer exists", { lastProjectId });
      // Clear invalid reference
      useWorkspaceStateStore.getState().setLastProject(null);
      return false;
    }
    
    logger.info("[ProjectStore] Restoring last project", { projectId: project.id, name: project.name });
    
    // Select project with skipWorkspaceStateSave since we're restoring
    await get().selectProject(project, { skipWorkspaceStateSave: true });
    
    return true;
  },

  refreshCurrentProject: async () => {
    const current = get().currentProject;
    if (!current) return;

    try {
      const grpcProject = await projectGrpc.get(current.id);
      const project = grpcToStoreProject(grpcProject);

      // Skip update if nothing has changed to avoid creating new object
      // references that cascade through useEffect dependencies
      const hasChanged =
        current.name !== project.name ||
        current.path !== project.path ||
        current.description !== project.description ||
        current.is_git_repo !== project.is_git_repo ||
        current.default_branch !== project.default_branch ||
        current.worktree_count !== project.worktree_count ||
        current.updated_at !== project.updated_at;

      if (!hasChanged) {
        logger.debug("[ProjectStore] refreshCurrentProject: no changes detected, skipping update");
        return;
      }

      set((state) => ({
        currentProject: project,
        projects: state.projects.map((p) =>
          p.id === project.id ? project : p
        ),
      }));
    } catch (error) {
      logger.error("[ProjectStore] Failed to refresh current project", error);
    }
  },

  createProject: async (data: Partial<Project>) => {
    set({ isLoading: true, error: null });
    try {
      const grpcProject = await projectGrpc.create(
        data.name || "",
        data.path || "",
        data.description,
        data.default_branch
      );
      const project = grpcToStoreProject(grpcProject);

      // Add to projects list
      set((state) => ({
        projects: [...state.projects, project],
        currentProject: project,
        isLoading: false,
      }));

      toast.success(`Project "${project.name}" created successfully`);
      return project;
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to create project";
      set({
        error: errorMessage,
        isLoading: false,
      });
      // Error toast will be shown by error handler
      throw error;
    }
  },

  updateProject: async (id: string, data: Partial<Project>) => {
    set({ isLoading: true, error: null });
    try {
      await projectGrpc.update(id, {
        name: data.name,
        description: data.description,
        defaultBranch: data.default_branch,
      });

      // Update in local state
      set((state) => ({
        projects: state.projects.map((p) =>
          p.id === id ? { ...p, ...data } : p
        ),
        currentProject:
          state.currentProject?.id === id
            ? { ...state.currentProject, ...data }
            : state.currentProject,
        isLoading: false,
      }));

      toast.success("Project updated successfully");
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to update project";
      set({
        error: errorMessage,
        isLoading: false,
      });
      // Error toast will be shown by error handler
      throw error;
    }
  },

  deleteProject: async (id: string) => {
    set({ isLoading: true, error: null });
    try {
      await projectGrpc.delete(id);

      // Remove from local state
      set((state) => ({
        projects: state.projects.filter((p) => p.id !== id),
        currentProject:
          state.currentProject?.id === id ? null : state.currentProject,
        isLoading: false,
      }));
      
      // Clean up workspace state for deleted project
      useWorkspaceStateStore.getState().removeProjectState(id);

      toast.success("Project deleted successfully");
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to delete project";
      set({
        error: errorMessage,
        isLoading: false,
      });
      // Error toast will be shown by error handler
      throw error;
    }
  },

  touchProject: async (id: string) => {
    const now = new Date().toISOString();

    // Optimistic update - update locally immediately for responsive UI
    set((state) => ({
      projects: state.projects.map((p) =>
        p.id === id ? { ...p, last_active: now } : p
      ),
      currentProject:
        state.currentProject?.id === id
          ? { ...state.currentProject, last_active: now }
          : state.currentProject,
    }));

    // Fire-and-forget: update backend without blocking UI
    // Backend will persist this for next reload
    projectGrpc.touch(id).catch((error) => {
      logger.error("[ProjectStore] Failed to touch project on backend", {
        id,
        error,
      });
      // Don't revert optimistic update - we'll get correct data on next reload
    });
  },

  reset: () => {
    set({
      projects: [],
      currentProject: null,
      isLoading: false,
      error: null,
    });
  },
}));