import { logger } from "../lib/logger";
import { isWorkflowSurfacePath } from "../lib/workflowsArea";
import { create } from "zustand";

import {
  createProject as createProjectRpc,
  deleteProject as deleteProjectRpc,
  fetchProjects,
  refreshProject,
  sameProject,
  subscribeToProjectRows,
  touchProject,
  updateProject as updateProjectRpc,
  type Project,
} from "../hooks/project-queries";
import { useWorktreeStore } from "./worktreeStore";
import { useChatStore } from "./chatStore";
import { resolveChat } from "../hooks/chat-queries";
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

// Carry the existing search params across a pathname-only navigation.
//
// tanstack-router REPLACES search when `search` is omitted, so a bare
// `navigate({ to, params })` silently drops every param on the URL. This
// function only decides which *path* the user should be on; the params
// belong to whoever put them there.
//
// The param that made this bite: the post-onboarding tour lives entirely in
// `?tour=<step-id>` (see useTourNavigation). Onboarding hands off to
// `/?tour=<first-step>`, then workspace restore selects the project it just
// created, and this sync moved / → /project/$id and erased the param — so
// OnboardingWizard read no step and rendered nothing. The tour appeared to
// "sometimes not trigger", with no error anywhere, purely on whether the
// restore landed after the handoff.
const preserveSearch = (prev: Record<string, unknown>) => prev;
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
        // The Workflows area and the builder. The area spans projects and
        // selects a chat's project as background context (loadRunContext,
        // RunRouteLoader — a run's page selects the run's project so its
        // transcript can read it); the user stays where they are.
        isWorkflowSurfacePath(currentPath) ||
        // The Inbox spans projects the same way.
        currentPath === "/inbox" ||
        // Forge owns its own project scope: the `project` search param, kept
        // in sync by ForgeLayout. Its header switcher selects a project to
        // CHANGE that scope, and navigating to /project/$id here threw the
        // user out of Forge into the chat view on every switch.
        currentPath === "/forge" ||
        currentPath.startsWith("/forge/") ||
        currentPath.startsWith("/auth") ||
        currentPath.startsWith("/reset-password") ||
        currentPath.startsWith("/verify-email") ||
        currentPath.startsWith("/design-sandbox") ||
        // Onboarding creates and selects a project mid-flow (ensureProject),
        // which would otherwise yank the user off /onboarding and into the
        // app before the flow had finished. OnboardingRoute owns when to
        // leave; the selection is background context until then.
        currentPath.startsWith("/onboarding") ||
        // The mobile surface has its own project handling (MobileShell picks a
        // fallback project itself). Without this, MobileShell's own
        // selectProject call navigated the user out of /m/* and into the
        // desktop shell — which also made every /m/* URL unbookmarkable and
        // unopenable from a notification.
        currentPath === "/m" ||
        currentPath.startsWith("/m/")
      ) {
        return;
      }
      router.navigate({
        to: "/project/$projectId",
        params: { projectId },
        search: preserveSearch,
      });
    } else {
      if (currentPath === "/" || currentPath.startsWith("/settings") || isWorkflowSurfacePath(currentPath)) {
        return;
      }
      router.navigate({ to: "/", search: preserveSearch });
    }
  } catch (err) {
    logger.warn("[ProjectStore] Failed to sync project URL", err);
  }
}

export type { Project };

interface ProjectStore {
  /**
   * The selected project — client state. The row itself is server state: it
   * is reconciled from the project cache entries (hooks/project-queries.ts)
   * whenever one of them changes, so it never drifts from what the list
   * shows.
   */
  currentProject: Project | null;

  selectProject: (project: Project, options?: { skipClear?: boolean; skipWorkspaceStateSave?: boolean }) => Promise<void>;
  restoreLastProject: () => Promise<boolean>;
  /** Re-read the current project from the server (after a git init, a new workspace). */
  refreshCurrentProject: () => Promise<void>;
  /** Create a project and make it current. */
  createProject: (data: Partial<Project>) => Promise<Project>;
  updateProject: (id: string, data: Partial<Project>) => Promise<void>;
  /** Delete a project; deleting the current one returns to the picker. */
  deleteProject: (id: string) => Promise<void>;
  reset: () => void;
}

export const useProjectStore = create<ProjectStore>((set, get) => ({
  currentProject: null,

  selectProject: async (project: Project, options?: { skipClear?: boolean; skipWorkspaceStateSave?: boolean }) => {
    const prevProjectId = get().currentProject?.id ?? null;
    const isProjectSwitch = prevProjectId !== null && prevProjectId !== project.id;

    // Clear worktrees and chats FIRST to avoid showing stale data
    // Skip clearing if this is initial window setup with context
    if (!options?.skipClear) {
      useWorktreeStore.getState().reset();

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
    touchProject(project.id);
    
    // Always load workflows for the project (they are project-scoped). Presets
    // need no nudge: their reads are keyed by project (hooks/preset-queries).
    const globalDataStore = useGlobalDataStore.getState();
    globalDataStore.refetchWorkflows(project.id).catch((err) => {
      logger.warn("[ProjectStore] Failed to load workflows for project", { projectId: project.id, error: err });
    });

    // Always load chats — even with skipClear (e.g. Electron window-context path)
    // loadChats uses singleflight so concurrent calls are deduplicated.
    await useChatStore.getState().loadChats();

    // Only reload worktrees and restore workspace state when not skipping clear
    // (user-initiated project switch). The Electron/skipClear path handles
    // worktree loading separately via useWindowContext.
    if (!options?.skipClear) {
      // Restore last worktree for this project. Fetches only that workspace
      // (or main) — the sidebar groups from the chat list, so the project's
      // full worktree list is not loaded here.
      await useWorktreeStore.getState().restoreLastWorktree(project.id);
      const currentWorktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
      
      // Restore viewer state (project context is now read from projectStore.currentProject)
      useViewerStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore chat navigation state
      useChatNavigationStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore active chat from workspace state
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(project.id, currentWorktreeId);
      if (worktreeState.activeChatId) {
        const chat = worktreeState.activeChatId
          ? await resolveChat(worktreeState.activeChatId)
          : undefined;
        if (chat) {
          logger.info("[ProjectStore] Restoring active chat on project switch", {
            chatId: chat.id,
            title: chat.title,
          });
          if (chat.worktreeId) {
            const targetWorktree = await useWorktreeStore.getState().resolveWorktree(chat.worktreeId);
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

    let projects: Project[];
    try {
      projects = await fetchProjects();
    } catch (error) {
      // Not knowing the list is not evidence the project is gone: keep the
      // reference for the next attempt.
      logger.warn("[ProjectStore] Could not load projects to restore the last one", { lastProjectId, error });
      return false;
    }
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
      // The answer lands in the cache; the subscription below reconciles
      // currentProject from it, and only when something changed.
      await refreshProject(current.id);
    } catch (error) {
      logger.error("[ProjectStore] Failed to refresh current project", error);
    }
  },

  createProject: async (data: Partial<Project>) => {
    const project = await createProjectRpc({
      name: data.name || "",
      path: data.path || "",
      description: data.description,
      default_branch: data.default_branch,
    });
    set({ currentProject: project });
    return project;
  },

  updateProject: async (id: string, data: Partial<Project>) => {
    // The returned row reaches currentProject through the cache.
    await updateProjectRpc(id, {
      name: data.name,
      description: data.description,
      default_branch: data.default_branch,
    });
  },

  deleteProject: async (id: string) => {
    await deleteProjectRpc(id);

    const wasCurrent = get().currentProject?.id === id;
    if (wasCurrent) {
      set({ currentProject: null });
      // We deleted the current project: push the URL back to the picker.
      syncProjectUrl(null);
    }

    // Clean up workspace state for deleted project
    useWorkspaceStateStore.getState().removeProjectState(id);
  },

  reset: () => {
    set({ currentProject: null });
  },
}));

/**
 * Keep the selected project's row in step with the cache: whenever a list or
 * GetProject entry is written (a refetch, an invalidation after a write, the
 * stream's refetch after a clone settles), take the current project's row
 * from it. A project missing from a list is left alone — the list is capped,
 * and only an explicit delete deselects.
 */
subscribeToProjectRows((rows) => {
  const current = useProjectStore.getState().currentProject;
  if (!current) return;
  const fresh = rows.find((row) => row.id === current.id);
  if (fresh && !sameProject(current, fresh)) {
    useProjectStore.setState({ currentProject: fresh });
  }
});
