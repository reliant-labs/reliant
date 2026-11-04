import { logger } from "../lib/logger";
import { isWorkflowSurfacePath } from "../lib/workflowsArea";
import { create } from "zustand";

import {
  createProject as createProjectRpc,
  deleteProject as deleteProjectRpc,
  fetchProjects,
  getCachedProjects,
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
import { trackEvent } from "../lib/analytics";
import { restoreLastWorktree } from "../hooks/worktree-actions";
import { openChatInWorkspace, beginWorkspaceOpen } from "../lib/openChatInWorkspace";

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

// Drive the URL to match the sidebar's project filter: `/project/$projectId`
// while it shows one project, `/` for "All projects". We compare the current
// pathname before navigating so we don't loop when the route-param effect
// sets the filter in response to a URL change.
export function syncProjectFilterUrl(projectId: string | null) {
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
        // The Apps list spans projects, like the Inbox.
        currentPath === "/apps" ||
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
      // Only the chat shell's own URL carries the filter; every other route
      // keeps its path.
      if (currentPath === "/project" || currentPath.startsWith("/project/")) {
        router.navigate({ to: "/", search: preserveSearch });
      }
    }
  } catch (err) {
    logger.warn("[ProjectStore] Failed to sync project URL", err);
  }
}

export type { Project };

/** The most recently used project of a list (by last_active), if any. */
export function mostRecentProject(projects: readonly Project[]): Project | undefined {
  let best: Project | undefined;
  for (const project of projects) {
    if (!best || (project.last_active ?? "") > (best.last_active ?? "")) best = project;
  }
  return best;
}

/** Options of selectProject. */
export interface SelectProjectOptions {
  skipClear?: boolean;
  skipWorkspaceStateSave?: boolean;
  /**
   * Restore the desktop workspace for the project: its last worktree, its
   * viewers and chat queue, and its last ACTIVE CHAT, which is selected
   * (marked read, subscribed on the update stream, its plan loaded). Default
   * true.
   *
   * The mobile surface passes false. Its route names the chat on screen, so
   * the desktop's remembered chat is one the user is not looking at — and
   * restoring it cost several serial RPCs (worktree, main worktree, chat)
   * before the switch could finish, then dismissed that chat's unread state
   * and tore the update stream down to subscribe it.
   */
  restoreWorkspace?: boolean;
  /**
   * Reopen the chat this project last had open (default true). False when the
   * caller is about to open a chat of its own (openChat), or wants the new-chat
   * view (the new-chat project chip).
   */
  restoreChat?: boolean;
  /** Continue the caller's logical navigation rather than starting a new one. */
  isCurrent?: () => boolean;
}

interface ProjectStore {
  /**
   * The project the main pane works in — the open chat's project, or the one
   * a new chat will start in. Files, terminal, git and viewers follow it.
   * It is NOT the sidebar filter (workspaceStateStore.projectFilterId): the
   * sidebar can list every project while this is one of them. The row itself
   * is server state: it is reconciled from the project cache entries
   * (hooks/project-queries.ts) whenever one of them changes, so it never
   * drifts from what the list shows.
   */
  currentProject: Project | null;

  /**
   * Make `project` the context project. Switching never touches the sidebar:
   * the chat list, its activity and the update stream span every project.
   */
  selectProject: (project: Project, options?: SelectProjectOptions) => Promise<void>;
  /**
   * Set the sidebar's project filter (null: All projects) and reflect it in
   * the URL. Filtering to a project other than the context one also moves the
   * context there, reopening that project's last chat.
   */
  setProjectFilter: (projectId: string | null, options?: Pick<SelectProjectOptions, "restoreWorkspace">) => Promise<void>;
  restoreLastProject: (options?: Pick<SelectProjectOptions, "restoreWorkspace">) => Promise<boolean>;
  /** Re-read the current project from the server (after a git init, a new workspace). */
  refreshCurrentProject: () => Promise<void>;
  /** Create a project and make it current. */
  createProject: (data: Partial<Project>) => Promise<Project>;
  updateProject: (id: string, data: Partial<Project>) => Promise<void>;
  /** Delete a project; deleting the current one moves to the most recent other one. */
  deleteProject: (id: string) => Promise<void>;
  reset: () => void;
}

export const useProjectStore = create<ProjectStore>((set, get) => ({
  currentProject: null,

  selectProject: async (project: Project, options?: SelectProjectOptions) => {
    const isLatest = options?.isCurrent ?? beginWorkspaceOpen();
    if (!isLatest()) return;
    const isCurrent = () => isLatest() && get().currentProject?.id === project.id;
    const prevProjectId = get().currentProject?.id ?? null;
    const isProjectSwitch = prevProjectId !== null && prevProjectId !== project.id;
    const restoreChat = options?.restoreChat ?? true;

    // The open chat belongs to the project being left: show the new project's
    // own last chat (restored below) or its new-chat view, never another
    // project's chat in this project's context.
    if (isProjectSwitch && restoreChat) {
      useChatStore.getState().clearCurrentChat(null);
    }

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
    
    // Workflows and presets need no nudge: their reads are keyed by project
    // (hooks/workflow-queries, hooks/preset-queries).

    // The chat list does not follow this: it spans every project (or the
    // sidebar filter's one) and is loaded once for that scope.

    // Only reload worktrees and restore workspace state when not skipping clear
    // (user-initiated project switch). The Electron/skipClear path handles
    // worktree loading separately via useWindowContext; the mobile surface
    // opts out with restoreWorkspace: false (see SelectProjectOptions).
    if (!options?.skipClear && options?.restoreWorkspace !== false) {
      // Restore last worktree for this project. Fetches only that workspace
      // (or main) — the sidebar groups from the chat list, so the project's
      // full worktree list is not loaded here.
      await restoreLastWorktree(project.id, undefined, { isCurrent });
      if (!isCurrent()) return;
      const currentWorktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
      
      // Restore viewer state (project context is now read from projectStore.currentProject)
      useViewerStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore chat navigation state
      useChatNavigationStore.getState().restoreFromWorkspaceState(project.id, currentWorktreeId);
      
      // Restore active chat from workspace state
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(project.id, currentWorktreeId);
      if (restoreChat && worktreeState.activeChatId) {
        const chat = worktreeState.activeChatId
          ? await resolveChat(worktreeState.activeChatId)
          : undefined;
        if (!isCurrent()) return;
        if (chat) {
          await openChatInWorkspace(chat, { isCurrent });
          if (!isCurrent()) return;
        }
      }
      
      logger.info("[ProjectStore] Restored workspace state for project", {
        projectId: project.id,
        worktreeId: currentWorktreeId,
        activeChatId: worktreeState.activeChatId,
      });
    }
  },

  setProjectFilter: async (projectId: string | null, options) => {
    // Even All projects supersedes a pending project/workspace restoration.
    const isCurrent = beginWorkspaceOpen();
    const workspaceState = useWorkspaceStateStore.getState();
    const changed = workspaceState.projectFilterId !== projectId;
    workspaceState.setProjectFilterId(projectId);
    syncProjectFilterUrl(projectId);
    if (changed) {
      trackEvent("project_filter_changed", { scope: projectId ? "project" : "all" });
    }

    if (projectId && get().currentProject?.id !== projectId) {
      let projects = getCachedProjects();
      if (!projects.some((p) => p.id === projectId)) {
        try {
          projects = await fetchProjects();
        } catch (error) {
          logger.warn("[ProjectStore] Could not load projects to filter by one", { projectId, error });
          return;
        }
      }
      if (!isCurrent()) return;
      const project = projects.find((p) => p.id === projectId);
      if (!project) {
        // The filter names a project that is gone: show them all.
        workspaceState.setProjectFilterId(null);
        syncProjectFilterUrl(null);
        return;
      }
      await get().selectProject(project, { ...options, isCurrent });
    }
    if (!isCurrent()) return;

    // The list of the new scope seeds activity and the stream's resume point.
    await useChatStore.getState().loadChats();
  },

  restoreLastProject: async (options) => {
    const isCurrent = beginWorkspaceOpen();
    const { lastProjectId } = useWorkspaceStateStore.getState();

    let projects: Project[];
    try {
      projects = await fetchProjects();
    } catch (error) {
      // Not knowing the list is not evidence the project is gone: keep the
      // reference for the next attempt.
      logger.warn("[ProjectStore] Could not load projects to restore the last one", { lastProjectId, error });
      return false;
    }
    if (!isCurrent()) return false;
    let project = lastProjectId ? projects.find((p) => p.id === lastProjectId) : undefined;

    if (lastProjectId && !project) {
      logger.warn("[ProjectStore] Last project no longer exists", { lastProjectId });
      // Clear invalid reference
      useWorkspaceStateStore.getState().setLastProject(null);
    }
    // There is no project picker to land on: with no (valid) last project,
    // the window opens on the most recently used one.
    project ??= mostRecentProject(projects);
    if (!project) {
      logger.info("[ProjectStore] No project to restore");
      return false;
    }

    logger.info("[ProjectStore] Restoring last project", { projectId: project.id, name: project.name });

    // Select project with skipWorkspaceStateSave since we're restoring
    await get().selectProject(project, { ...options, skipWorkspaceStateSave: true, isCurrent });

    return isCurrent();
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
    const isCurrent = beginWorkspaceOpen();
    const project = await createProjectRpc({
      name: data.name || "",
      path: data.path || "",
      description: data.description,
      default_branch: data.default_branch,
    });
    if (isCurrent()) await get().selectProject(project, { isCurrent });
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
    const wasCurrent = get().currentProject?.id === id;
    const isCurrent = wasCurrent ? beginWorkspaceOpen() : () => false;
    await deleteProjectRpc(id);

    const wasFiltered = useWorkspaceStateStore.getState().projectFilterId === id;
    // Clean up workspace state for deleted project (drops a filter on it too)
    useWorkspaceStateStore.getState().removeProjectState(id);
    if (wasFiltered) syncProjectFilterUrl(null);

    if (get().currentProject?.id === id) {
      // The context moves to the most recently used project left; with none,
      // the shell shows its first-run state.
      // Clean departure explicitly before nulling identity. No replacement
      // (or a replacement with no saved chat) must retain the deleted pane.
      useChatStore.getState().clearCurrentChat(null);
      useViewerStore.getState().clearViewersForProjectSwitch();
      useWorktreeStore.getState().reset();
      // Departure hooks save their pane state; discard that final snapshot of
      // the deleted project too, rather than recreating its persisted entry.
      useWorkspaceStateStore.getState().removeProjectState(id);
      set({ currentProject: null });
      let remaining: Project[] = [];
      try {
        remaining = (await fetchProjects()).filter((p) => p.id !== id);
      } catch (error) {
        logger.warn("[ProjectStore] Could not load projects after a delete", { error });
      }
      const next = mostRecentProject(remaining);
      if (next && isCurrent()) await get().selectProject(next, { isCurrent });
    }
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
