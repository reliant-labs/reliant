import { useEffect, useRef } from "react";
import { useLocation, useParams } from "@tanstack/react-router";
import { syncProjectFilterUrl, useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";

/** Startup restoration and the filter bridge read the same route identity. */
export function useRouteProjectId(): string | undefined {
  const { pathname } = useLocation();
  const { projectId } = useParams({ strict: false }) as { projectId?: string };
  return pathname === "/" ? undefined : projectId;
}

/** The chat shell's URL/filter bridge, mounted once by ModernApp. */
export function ProjectFilterRouteSync() {
  const { pathname } = useLocation();
  const { projectId } = useParams({ strict: false }) as { projectId?: string };
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
