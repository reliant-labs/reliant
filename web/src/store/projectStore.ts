import { logger } from "../lib/logger";
import { create } from "zustand";
import { ConnectError, Code } from "@connectrpc/connect";

import { projectGrpc, type Project as GrpcProject } from "../api/project-grpc";
import { toast } from "../lib/toast-manager";
import { useWorktreeStore } from "./worktreeStore";
import { useChatStore } from "./chatStore";
import { getChatFromCache } from "../hooks/chat-queries";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useViewerStore } from "./viewerStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useGlobalDataStore } from "./globalDataStore";
import { trackEvent } from "../lib/analytics";

// Lazy router accessor — importing from ../routes at module-init time creates
// a cycle (routes.tsx → App → ModernApp → projectStore → routes.tsx), which
// reordered the module-init graph and broke an unrelated TDZ in chatStore.
// By the time syncProjectUrl() runs (in response to a user action), routes.tsx
// has long since finished loading; we read window.__RELIANT_ROUTER set by
// routes.tsx during its own module init, or fall back to a dynamic import.
//
// The static-import-of-router pattern was the wrong answer; this accessor
// + the registration on the router-side (see routes.tsx) is the right one.
type RouterLike = {
  state: { location: { pathname: string } };
  navigate: (opts: { to: string; params?: unknown; search?: unknown }) => unknown;
};
function getRouter(): RouterLike | null {
  const r = (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER;
  return r ?? null;
}

// Drive the URL to match the current project selection. The URL is the
// canonical source of "which project am I in" now that `/project/$projectId`
// is a real route. We compare the current pathname before navigating so we
// don't loop when the route-param effect calls selectProject in response to a
// URL change.
function syncProjectUrl(projectId: string | null) {
  try {
    const router = getRouter();
    if (!router) return; // routes.tsx hasn't registered yet — startup window
    const currentPath = router.state.location.pathname;
    if (projectId) {
      const targetPrefix = `/project/${projectId}`;
      if (currentPath === targetPrefix || currentPath.startsWith(`${targetPrefix}/`)) {
        return;
      }
      // Skip navigating away from settings/workflow/auth routes — the user is
      // doing something else and the project is just background context.
      if (
        currentPath.startsWith("/settings") ||
        currentPath.startsWith("/workflow") ||
        currentPath.startsWith("/auth") ||
        currentPath.startsWith("/reset-password") ||
        currentPath.startsWith("/verify-email") ||
        currentPath.startsWith("/design-sandbox")
      ) {
        return;
      }
      router.navigate({ to: "/project/$projectId", params: { projectId } });
    } else {
      if (currentPath === "/" || currentPath.startsWith("/settings") || currentPath.startsWith("/workflow")) {
        return;
      }
      router.navigate({ to: "/", search: {} });
    }
  } catch (err) {
    logger.warn("[ProjectStore] Failed to sync project URL", err);
  }
}

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
  // Canonical git remote URL — present for git-backed projects whose root
  // repo's remote has been resolved. NULL for non-git / local-only
  // projects. The picker uses this to know whether a project can be
  // re-cloned to another daemon.
  remote_url?: string;
  // True when the project's repo root contains a forge.yaml. Populated at
  // clone / project-create time by the server.
  is_forge: boolean;
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
    const prevProjectId = get().currentProject?.id ?? null;
    const isProjectSwitch = prevProjectId !== null && prevProjectId !== project.id;

    // Clear worktrees and chats FIRST to avoid showing stale data
    // Skip clearing if this is initial window setup with context
    if (!options?.skipClear) {
      const worktreeStore = useWorktreeStore.getState();
      worktreeStore.worktrees = [];
      worktreeStore.currentWorktree = null;

      // Chats now live in the React Query cache, keyed by projectId — the
      // list query for the new project scopes naturally and loadChats seeds
      // its detail entries, so no manual clear is needed here.
    }

    set({ currentProject: project });

    // Clear viewers when switching between projects (replaces the old
    // setCurrentProject side-effect in viewerStore).
    if (isProjectSwitch) {
      useViewerStore.getState().clearViewersForProjectSwitch();
    }

    // Drive the URL to /project/$projectId so deep-linking + refresh works.
    syncProjectUrl(project.id);

    trackEvent("project_opened", {
      projectId: project.id,
      isGitRepo: project.is_git_repo,
    });

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

    // Always load chats — even with skipClear (e.g. Electron window-context path)
    // loadChats uses singleflight so concurrent calls are deduplicated.
    await useChatStore.getState().loadChats();

    // Only reload worktrees and restore workspace state when not skipping clear
    // (user-initiated project switch). The Electron/skipClear path handles
    // worktree loading separately via useWindowContext.
    if (!options?.skipClear) {
      await useWorktreeStore.getState().loadWorktrees(project.id);
      
      // Restore last worktree for this project
      useWorktreeStore.getState().restoreLastWorktree(project.id);
      const currentWorktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
      
      // Restore viewer state (project context is now read from projectStore.currentProject)
      useViewerStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore chat navigation state
      useChatNavigationStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore active chat from workspace state
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(project.id, currentWorktreeId);
      if (worktreeState.activeChatId) {
        const chat = worktreeState.activeChatId
          ? getChatFromCache(worktreeState.activeChatId)
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

      trackEvent("project_created", {
        is_forge: project.is_forge,
        is_git_repo: project.is_git_repo,
        has_remote: Boolean(project.remote_url),
      });
      toast.success(`Project "${project.name}" created successfully`);
      return project;
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : "Failed to create project";
      set({
        error: errorMessage,
        isLoading: false,
      });
      // Surface unexpected failures as a toast so the user sees something.
      // AlreadyExists is a normal "open existing project" path that callers
      // handle themselves — don't double-notify there.
      const isAlreadyExists =
        error instanceof ConnectError && error.code === Code.AlreadyExists;
      if (!isAlreadyExists) {
        toast.error(error);
      }
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
      const wasCurrent = get().currentProject?.id === id;
      set((state) => ({
        projects: state.projects.filter((p) => p.id !== id),
        currentProject:
          state.currentProject?.id === id ? null : state.currentProject,
        isLoading: false,
      }));

      // If we deleted the current project, push the URL back to the picker.
      if (wasCurrent) {
        syncProjectUrl(null);
      }

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