// Copyright (c) 2025 Reliant Labs

/**
 * Selects a project on a page load for routes that sit under the bare
 * `_authenticated` layout — the Workflows area and the workflow builder.
 * ModernApp is the only other thing that selects a project on a page load,
 * and it never mounts on these routes, so without this a hard refresh (or a
 * pasted link) had no project: the Library could not load, `/workflow/new`
 * sat on its loading screen forever, and `/workflow/<name>` bounced to `/`.
 *
 *   a. currentProject is set       → reflect it into `?project=`.
 *   b. `?project=` is set          → load projects, select that one.
 *   c. neither                     → restoreLastProject(), as the app shell does.
 *   d. still nothing               → `resolved` with no project; the page
 *                                    renders its own no-project state.
 *
 * The URL is reflected with `replace`, so it never adds a history entry.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";

export function useRouteProjectResolution({ reflectInUrl }: { reflectInUrl: boolean }): { resolved: boolean } {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { project?: string };
  const projectParam = search.project;

  const currentProject = useProjectStore((state) => state.currentProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);
  const restoreLastProject = useProjectStore((state) => state.restoreLastProject);
  const attempted = useRef(false);
  const [resolved, setResolved] = useState(() => !!useProjectStore.getState().currentProject);

  const syncProjectParam = useCallback(
    (projectId: string) => {
      if (!reflectInUrl) return;
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, project: projectId }),
        replace: true,
      } as never);
    },
    [navigate, reflectInUrl],
  );

  useEffect(() => {
    if (attempted.current) return;
    attempted.current = true;
    void (async () => {
      try {
        if (currentProject) {
          if (projectParam !== currentProject.id) syncProjectParam(currentProject.id);
          return;
        }
        await loadProjects();
        if (projectParam) {
          const match = useProjectStore.getState().projects.find((p) => p.id === projectParam);
          if (match) {
            await selectProject(match);
            return;
          }
        }
        if (await restoreLastProject()) {
          const restoredId = useProjectStore.getState().currentProject?.id;
          if (restoredId) syncProjectParam(restoredId);
        }
      } catch {
        // Resolution is best-effort: the page renders its no-project state.
      } finally {
        setResolved(true);
      }
    })();
  }, [currentProject, projectParam, loadProjects, selectProject, restoreLastProject, syncProjectParam]);

  // Keep the URL honest after the first resolution: a project selected later
  // must follow into the param, or a refresh would resolve back to the old one.
  useEffect(() => {
    if (!currentProject || projectParam === currentProject.id) return;
    syncProjectParam(currentProject.id);
  }, [currentProject, projectParam, syncProjectParam]);

  return { resolved: resolved || !!currentProject };
}
