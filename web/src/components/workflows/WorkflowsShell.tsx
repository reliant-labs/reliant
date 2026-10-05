// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's chrome (WORKFLOW_UI.md §1.2): the exit, a tab bar —
 * Library · Runs · Automations — and project resolution. It is the layout
 * route every `/workflows/*` page renders inside, so a cross-link between tabs
 * (workflow → its runs, run → its automation, automation → its workflow) stays
 * inside one shell and keeps its back button.
 *
 * PROJECT RESOLUTION is the same ladder as ForgeLayout, for the same reason:
 * these routes sit under the bare `_authenticated` layout, which never mounts
 * ModernApp, and ModernApp is the only thing that selects a project on a page
 * load. Without this a hard refresh on /workflows/library had no project and
 * the Library — whose definitions are per project — could not load.
 *
 *   a. currentProject is set       → reflect it into `?project=`.
 *   b. `?project=` is set          → load projects, select that one.
 *   c. neither                     → restoreLastProject(), as the app shell does.
 *   d. still nothing               → the content renders its own empty state
 *                                    (Runs and Automations span projects and
 *                                    work without one; the Library says
 *                                    "Workflows run inside a project").
 *
 * The URL is reflected with `replace`, so it never adds a history entry. A
 * project is background context here — the Runs and Automations tabs list
 * across projects (decision 10) — so a missing project is never a dead end.
 */

import { useCallback, useEffect, useRef, type ReactNode } from "react";
import { Link, Outlet, useLocation, useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";
import {
  WORKFLOWS_AREA_PATH,
  WORKFLOWS_TABS,
  isWorkflowsDetailPath,
  workflowsTabForPath,
} from "@/lib/workflowsArea";
import { cn } from "@/lib/utils";
import { AreaShell } from "../Layout/AreaShell";

/** Pages that host a transcript own their height and scrolling. */
function isFillLayout(pathname: string): boolean {
  return pathname.startsWith("/workflows/runs/");
}

export function WorkflowsLayout() {
  const { pathname } = useLocation();
  useWorkflowsProjectResolution();
  return (
    <WorkflowsShell pathname={pathname}>
      <Outlet />
    </WorkflowsShell>
  );
}

/**
 * The chrome alone, with no router-driven project resolution: what the
 * layout renders, and what a test can mount around a single page.
 */
export function WorkflowsShell({ pathname, children }: { pathname: string; children: ReactNode }) {
  const activeTab = workflowsTabForPath(pathname);
  const tab = WORKFLOWS_TABS.find((candidate) => candidate.key === activeTab);
  // A detail page's exit steps back into its tab; a tab's list exits the area.
  const areaPath = tab && isWorkflowsDetailPath(pathname) ? tab.path : pathname;

  return (
    <AreaShell
      areaPath={areaPath}
      areaLabel={tab?.listLabel ?? "Workflows"}
      areaNoun="workflows"
      layout={isFillLayout(pathname) ? "fill" : "column"}
      nav={<WorkflowsTabBar activeTab={activeTab} />}
    >
      {children}
    </AreaShell>
  );
}

function WorkflowsTabBar({ activeTab }: { activeTab: string | null }) {
  // The project rides along between tabs, so switching tabs keeps context.
  const search = useSearch({ strict: false }) as { project?: string };
  const tabSearch = search.project ? { project: search.project } : {};
  return (
    <nav
      aria-label="Workflows"
      data-onboarding="workflows-tabs"
      className="ml-2 flex items-center gap-1 border-l border-border/60 pl-3"
    >
      {WORKFLOWS_TABS.map((tab) => {
        const active = tab.key === activeTab;
        return (
          <Link
            key={tab.key}
            to={tab.path}
            search={tabSearch}
            aria-current={active ? "page" : undefined}
            data-testid={`workflows-tab-${tab.key}`}
            className={cn(
              "inline-flex h-8 items-center rounded-md px-3 text-sm font-medium transition-colors",
              "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
              active
                ? "bg-muted text-foreground"
                : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
            )}
          >
            {tab.label}
          </Link>
        );
      })}
    </nav>
  );
}

/** The resolution ladder in the header comment. Runs once per mount of the area. */
function useWorkflowsProjectResolution() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const search = useSearch({ strict: false }) as { project?: string };
  const projectParam = search.project;

  const currentProject = useProjectStore((state) => state.currentProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);
  const restoreLastProject = useProjectStore((state) => state.restoreLastProject);
  const attempted = useRef(false);

  const syncProjectParam = useCallback(
    (projectId: string) => {
      if (!pathname.startsWith(WORKFLOWS_AREA_PATH)) return;
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, project: projectId }),
        replace: true,
      });
    },
    [navigate, pathname],
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
        // Resolution is best-effort: every tab renders without a project.
      }
    })();
  }, [currentProject, projectParam, loadProjects, selectProject, restoreLastProject, syncProjectParam]);

  // Keep the URL honest after the first resolution: a project selected later
  // (a run's page selects its run's project) must follow into the param, or a
  // refresh would resolve back to the previous one.
  useEffect(() => {
    if (!currentProject || projectParam === currentProject.id) return;
    syncProjectParam(currentProject.id);
  }, [currentProject, projectParam, syncProjectParam]);
}
