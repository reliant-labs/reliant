// Copyright (c) 2025 Reliant Labs

/**
 * The layout route shared by /forge/topology, /forge/status and /forge/secrets.
 *
 * It owns the three things none of the individual screens could own, because
 * each of them is a property of the forge SURFACE rather than of one tab:
 *
 * 1. CHROME AND AN EXIT. The forge routes sit under the bare `_authenticated`
 *    layout, which renders no app chrome, so before this existed there was no
 *    header, no back button and no Escape handler on any of the three screens —
 *    a user who opened one could only leave by editing the URL. ForgeHeader and
 *    the Escape binding here are modelled on SettingsPage, which solves the
 *    identical problem for /settings.
 *
 * 2. NAVIGATION. The screens were siblings with no links between them; status
 *    and secrets were reachable only by typing their URLs. The sidebar carries
 *    the `project` and `env` params across, so switching screens keeps context
 *    instead of resetting it.
 *
 *    This is a LEFT SIDEBAR, not the tab strip it replaces. A tab strip says
 *    "these are views of one page"; a sidebar says "these are the places this
 *    product has", which is what the forge surface actually is — four screens
 *    that answer different questions about a project, not four slices of one.
 *    The strip also had nowhere to grow: every screen added made it longer
 *    horizontally until it ran out of bar, whereas a sidebar has a whole
 *    column and room for section labels that group what a strip cannot.
 *
 * 3. PROJECT RESOLUTION — this is the refresh bug. `currentProject` is only ever
 *    set by projectStore.selectProject, and on a page load that call comes from
 *    exactly two places, both inside ModernApp: the /project/$projectId param
 *    effect, and useWorkspaceRestore's restoreLastProject(). ModernApp mounts
 *    only under the `_app` layout route, and the forge routes are not under it.
 *    So on a hard refresh at /forge/topology neither ran, currentProject stayed
 *    null forever, and the screen rendered a dead-end sentence telling the user
 *    to select a project with nothing on the page that could select one.
 *
 *    The resolution ladder below fixes that without touching ModernApp or the
 *    stores — every rung is an existing store call, just made from a route that
 *    previously made none:
 *
 *      a. currentProject is set        → reflect it into the URL.
 *      b. the `project` param is set   → loadProjects(), then selectProject the
 *                                        matching row. This is what makes a
 *                                        pasted or bookmarked forge URL work.
 *      c. neither                      → restoreLastProject(), the same call
 *                                        useWorkspaceRestore makes, reading the
 *                                        lastProjectId that IS still persisted
 *                                        in localStorage and was simply never
 *                                        read on a forge route.
 *      d. all of it came back empty    → render ForgeProjectPicker. A picker,
 *                                        never a dead-end sentence.
 *
 *    The URL is the source of truth once resolved, matching the reasoning on
 *    `projectRoute` in routes.tsx. Syncing is `replace: true` so reflecting the
 *    param does not push a history entry the user has to press Back through.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Outlet, useLocation, useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore, type Project } from "@/store/projectStore";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";

import { ForgeHeader } from "./ForgeHeader";
import { ForgeProjectPicker } from "./ForgeProjectPicker";
import { FORGE_NAV, ForgeShell } from "./ForgeShell";

export function ForgeLayout() {
  const navigate = useNavigate();
  const { pathname } = useLocation();

  // `strict: false` because this one component renders under all three child
  // routes, whose search schemas differ (topology has no `env`).
  const search = useSearch({ strict: false }) as {
    project?: string;
    env?: string;
  };
  const projectParam = search.project;
  const envParam = search.env;

  const currentProject = useProjectStore((state) => state.currentProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);
  const restoreLastProject = useProjectStore((state) => state.restoreLastProject);

  /**
   * Whether the automatic ladder has finished. Until it has, the picker must NOT
   * render: resolution is async (it may fetch the project list), and showing a
   * picker in that window would flash the exact dead-end-looking screen this
   * component exists to remove, then replace it a tick later.
   */
  const [resolved, setResolved] = useState(false);

  /** The ladder runs once per mount. A ref, not state, so it cannot re-trigger the effect. */
  const attempted = useRef(false);

  const onClose = useCallback(() => {
    navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);

  /** Reflect a resolved project into the URL without pushing a history entry. */
  const syncProjectParam = useCallback(
    (projectId: string) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, project: projectId }),
        replace: true,
      });
    },
    [navigate]
  );

  useEffect(() => {
    if (attempted.current) return;
    attempted.current = true;

    void (async () => {
      try {
        // (a) The store already knows. Nothing to fetch; just make the URL say so.
        if (currentProject) {
          if (projectParam !== currentProject.id) syncProjectParam(currentProject.id);
          return;
        }

        // (b) The URL names one. This is the pasted/bookmarked/refreshed case.
        if (projectParam) {
          await loadProjects();
          const match = useProjectStore
            .getState()
            .projects.find((candidate) => candidate.id === projectParam);
          if (match) {
            await selectProject(match);
            return;
          }
          // The id is stale (project deleted, or a different account). Fall
          // through to the restore rung rather than dead-ending on it.
        }

        // (c) The persisted last project. restoreLastProject reads from the
        // already-loaded list, so the list has to exist first.
        await loadProjects();
        const restored = await restoreLastProject();
        if (restored) {
          const restoredId = useProjectStore.getState().currentProject?.id;
          if (restoredId) syncProjectParam(restoredId);
        }
      } finally {
        // (d) Whatever happened, resolution is over — the render below now
        // decides between the screens and the picker on real information.
        setResolved(true);
      }
    })();
  }, [
    currentProject,
    projectParam,
    loadProjects,
    selectProject,
    restoreLastProject,
    syncProjectParam,
  ]);

  /**
   * Keep the URL honest after the initial ladder — the header's switcher and the
   * picker both change the store, and the param has to follow or a refresh would
   * resolve back to the previous project.
   */
  useEffect(() => {
    if (!currentProject) return;
    if (projectParam === currentProject.id) return;
    syncProjectParam(currentProject.id);
  }, [currentProject, projectParam, syncProjectParam]);

  // Escape closes the surface, matching SettingsPage.tsx. Capture-phase, and it
  // steps aside for text entry so it cannot eat an Escape meant for a field.
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      const target = event.target as HTMLElement | null;
      if (
        target?.tagName === "INPUT" ||
        target?.tagName === "TEXTAREA" ||
        target?.contentEditable === "true"
      ) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      onClose();
    };
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [onClose]);

  const onProjectSelected = useCallback(
    (project: Project) => syncProjectParam(project.id),
    [syncProjectParam]
  );

  const showPicker = resolved && !currentProject;

  /**
   * How each nav href carries context across a tab change. Passed to ForgeShell
   * rather than built there, because only the layout knows the current params —
   * the shell is deliberately stateless so a preview can render it too.
   */
  const searchFor = useCallback(
    (item: (typeof FORGE_NAV)[number]) => {
      const params = new URLSearchParams();
      if (projectParam) params.set("project", projectParam);
      if (item.carriesEnv && envParam) params.set("env", envParam);
      return params.toString();
    },
    [projectParam, envParam]
  );

  // The chrome lives in ForgeShell so the dev preview harnesses render the
  // SAME sidebar and header the product does. A harness that drew the screen
  // bare looked like the product and was not, which cost two review cycles
  // spent asking where the left nav had gone. See ForgeShell's header comment.
  return (
    <ForgeShell
      activePath={pathname}
      searchFor={searchFor}
      headerContent={<ForgeHeader onClose={onClose} onProjectSelected={onProjectSelected} />}
    >
      {showPicker ? <ForgeProjectPicker onSelected={onProjectSelected} /> : <Outlet />}
    </ForgeShell>
  );
}
