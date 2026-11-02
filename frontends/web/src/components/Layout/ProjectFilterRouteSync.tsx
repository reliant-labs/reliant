import { useEffect, useRef } from "react";
import { useLocation } from "@tanstack/react-router";
import { syncProjectFilterUrl, useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";

const PROJECT_PATH = /^\/project\/([^/]+)\/?$/;

/**
 * The project a chat-shell path names (`/project/$projectId`), else undefined.
 *
 * Read from the pathname, never from useParams: the router moves its location
 * the moment a navigation starts but commits the new match (and so the
 * params) only once it has loaded. Paired, the two disagreed mid-navigation —
 * `/project/B` with the param still `A` — and the bridge below filtered back
 * to A, undoing every project → project pick in the sidebar.
 */
export function projectIdOfPath(pathname: string): string | undefined {
  const match = PROJECT_PATH.exec(pathname);
  if (!match) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return match[1];
  }
}

/** Startup restoration and the filter bridge read the same route identity. */
export function useRouteProjectId(): string | undefined {
  return projectIdOfPath(useLocation().pathname);
}

/** The chat shell's URL/filter bridge, mounted once by ModernApp. */
export function ProjectFilterRouteSync() {
  const { pathname } = useLocation();
  const projectId = projectIdOfPath(pathname);
  const previousPath = useRef<string | null>(null);
  useEffect(() => {
    const previous = previousPath.current;
    if (previous === pathname) return;
    previousPath.current = pathname;
    if (pathname !== "/" && !projectId) return;
    const filter = useWorkspaceStateStore.getState().projectFilterId;
    // Only the initial root restores a persisted filter. A subsequent root
    // is a route intent (Back included), not a request to push that filter.
    if (previous === null && pathname === "/" && filter) {
      syncProjectFilterUrl(filter);
      return;
    }
    const target = pathname === "/" ? null : projectId ?? null;
    if (filter !== target) void useProjectStore.getState().setProjectFilter(target);
  }, [pathname, projectId]);
  return null;
}
