// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's chrome (WORKFLOW_UI.md §1.2): a left sidebar —
 * Workflows · Runs — a sidebar-brand exit, a header bar with the section and
 * project scope, and project resolution. It is the layout route every
 * `/workflows/*` page renders inside, so a cross-link between sections
 * (workflow → its runs, run → its workflow's trigger) stays inside one shell.
 *
 * No badge on the nav: the Inbox is the ONE place that says something needs
 * you (WORKFLOW_UI.md §8), and its sidebar badge is the one count. The
 * Workflows list pins what needs you on top instead.
 *
 * Every page opens the automation dialog through openAutomationDialog();
 * its single host lives in the root shell, shared with chat and builder entry points.
 *
 * WHY A SIDEBAR. The sections used to be tab links squeezed into the title
 * bar beside "Close", where they read as window chrome rather than as the
 * area's navigation. A labelled vertical nav is the app's standard for a
 * full-page area — Settings and the org console (Forge/Org/OrgShell) both use one — so
 * the three full-page areas now share a shape. Like the console, the brand
 * row owns the way out; the title bar owns the breadcrumb and scope.
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
 *   d. still nothing               → every page reads every project (the
 *                                    scope falls back to All projects).
 *
 * The URL is reflected with `replace`, so it never adds a history entry. A
 * project is background context here — the header's project menu also offers
 * All projects, which every page honours (useWorkflowsScope) — so a missing
 * project is never a dead end.
 */

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Outlet, useLocation, useNavigate, useSearch } from "@tanstack/react-router";
import { Activity, ArrowLeft, ChevronDown, Workflow, X } from "lucide-react";

import SidebarLayout from "@/components/forge-ui/sidebar_layout";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";
import { useRouteProjectResolution } from "@/hooks/useRouteProjectResolution";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { cn } from "@/lib/utils";
import {
  WORKFLOWS_AREA_PATH,
  WORKFLOWS_TABS,
  isWorkflowsDetailPath,
  workflowsTabForPath,
  type WorkflowsTab,
} from "@/lib/workflowsArea";
import { ShellPageHeader, useShellFrame } from "../Layout/shell/shellFrame";
import { LearnLink } from "@/lib/learnLinks";
import { useProjectStore } from "@/store/projectStore";
import { useProjects } from "@/hooks/project-queries";
import { ProjectSelector } from "../Projects/ProjectSelector";
import Tooltip from "../forge-ui/tooltip";
import { useWorkflowsScope } from "./useWorkflowsScope";
import { homePath } from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";
import { PageMain } from "@/components/Layout/shell/shellFrame";

const TAB_ICONS: Record<WorkflowsTab, typeof Workflow> = {
  workflows: Workflow,
  runs: Activity,
};

/** Pages that host a transcript own their height and scrolling. */
function isFillLayout(pathname: string): boolean {
  return orgFreePath(pathname).startsWith("/workflows/runs/");
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
  const navigate = useNavigate();
  const activeTab = workflowsTabForPath(pathname);
  // The project and the scope ride along between sections, so switching keeps context.
  const search = useSearch({ strict: false }) as { project?: string; allProjects?: boolean };
  const { trafficLightPadding, dragRegionStyle } = useTitleBarChrome({ collapsedPadding: "0px" });
  const [collapsed, setCollapsed] = useState(false);
  const narrow = useNarrowViewport();
  // A 56px rail cannot contain the native macOS traffic-light inset. Keep
  // collapse available, but put its exit in the main header in that case.
  const railExitInHeader = !narrow && collapsed && trafficLightPadding !== "0px";

  // The button always leaves the area. Escape steps to the logical parent: a
  // detail page's list, or out of the area from a list.
  const leave = useCallback(() => void navigate({ to: homePath(), search: {} }), [navigate]);
  const stepBack = useCallback(() => void navigate(getParentRouteNavigateOptions(pathname)), [navigate, pathname]);
  // Inside the one shell the nav is the way around: Escape only steps a detail page back to its list.
  const { inShell } = useShellFrame();
  useEscapeToLeave(inShell && !isWorkflowsDetailPath(pathname) ? null : stepBack);

  const navItems = useMemo(() => {
    const params = new URLSearchParams();
    if (search.project) params.set("project", search.project);
    if (search.allProjects) params.set("allProjects", "true");
    const query = params.size > 0 ? `?${params.toString()}` : "";
    return WORKFLOWS_TABS.map((tab) => {
      const Icon = TAB_ICONS[tab.key];
      return {
        label: tab.label,
        href: `${tab.href()}${query}`,
        active: tab.key === activeTab,
        icon: <Icon className="h-4 w-4" aria-hidden="true" />,
      };
    });
  }, [activeTab, search.project, search.allProjects]);

  if (inShell) {
    // The area's sections are rows of the one nav (Layout/Sidebar, under
    // Workflows): no rail and no exit here, just the breadcrumb and the scope.
    const section = WORKFLOWS_TABS.find((tab) => tab.key === activeTab);
    const fill = isFillLayout(pathname);
    return (
      <div className="flex h-full min-h-0 w-full flex-col bg-background" data-testid="workflows-shell">
        <ShellPageHeader
          testId="workflows-header"
          actions={
            <>
              <LearnLink section="workflows" />
              <div className="flex min-w-0 max-w-xs items-center max-md:max-w-48">
                <ProjectSwitcher />
              </div>
            </>
          }
        >
          <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-ui text-muted-foreground">
            <span className="hidden md:inline">Workflows</span>
            <span aria-hidden="true" className="hidden md:inline">›</span>
            <span aria-current="page" className="truncate font-medium text-foreground">
              {section?.key === "workflows" ? "Library" : (section?.label ?? "Workflows")}
            </span>
          </nav>
        </ShellPageHeader>
        {fill ? (
          <div className="flex min-h-0 flex-1 flex-col">{children}</div>
        ) : (
          <PageMain className="relative min-h-0 flex-1 overflow-y-auto px-8 pb-12 pt-2 max-md:px-4">
            <div className="mx-auto w-full max-w-6xl">{children}</div>
          </PageMain>
        )}
      </div>
    );
  }

  return (
    <div className="h-screen w-full" data-testid="workflows-shell">
      <SidebarLayout
        navLabel="Workflows"
        // `forge-ui` on the sidebar only: inside that scope `accent` is the
        // primary action colour (the active nav item). Scoping the whole page
        // would also repaint every reliant Button's accent hover.
        sidebarClassName="forge-ui"
        brand={
          <div
            className="flex min-w-0 items-center gap-1 transition-[padding] duration-200 ease-in-out"
            style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}
            data-testid="workflows-brand"
          >
            <WorkflowsCloseButton onClose={leave} />
            <span className="truncate text-sm font-semibold tracking-tight text-foreground">Workflows</span>
          </div>
        }
        brandCollapsed={
          <div data-testid="workflows-brand-collapsed">
            {!railExitInHeader && <WorkflowsCloseButton onClose={leave} />}
          </div>
        }
        navItems={navItems}
        collapsed={collapsed}
        onToggle={() => setCollapsed((c) => !c)}
        headerContent={
          <WorkflowsHeader
            onClose={leave}
            activeTab={activeTab}
            showExit={narrow || railExitInHeader}
          />
        }
        contentLayout={isFillLayout(pathname) ? "fill" : "padded"}
      >
        {isFillLayout(pathname) ? children : <div className="mx-auto w-full max-w-6xl">{children}</div>}
      </SidebarLayout>
    </div>
  );
}

/**
 * Escape leaves, matching SettingsPage — except in a field, or while a dialog
 * or menu is open: that Escape is theirs.
 *
 * The open-overlay check runs in the CAPTURE phase, before anything handles
 * the key. ui/Dropdown closes its menu on a document-level keydown, which
 * runs before a window bubble listener — so a bubble-phase check saw the menu
 * already gone, and one Escape closed the filter menu AND left the area.
 */
function useEscapeToLeave(onLeave: (() => void) | null) {
  useEffect(() => {
    if (!onLeave) return;
    let overlayOwnsEscape = false;
    const onCapture = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      overlayOwnsEscape = Boolean(
        document.querySelector('[aria-modal="true"], [role="menu"], [role="listbox"], [role="dialog"]'),
      );
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented || overlayOwnsEscape) return;
      const target = event.target as HTMLElement | null;
      if (
        target?.tagName === "INPUT" ||
        target?.tagName === "TEXTAREA" ||
        target?.tagName === "SELECT" ||
        target?.isContentEditable
      ) {
        return;
      }
      event.preventDefault();
      onLeave();
    };
    window.addEventListener("keydown", onCapture, true);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onCapture, true);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [onLeave]);
}

/** The console pattern: a quiet exit in the brand row, also on the rail. */
function WorkflowsCloseButton({ onClose }: { onClose: () => void }) {
  const { isElectron, noDragRegionStyle } = useTitleBarChrome({ alignedToWindowEdge: false });
  const exitLabel = isElectron ? "Close workflows" : "Back to app";
  return (
    <Tooltip content={`${exitLabel} (Esc)`} side="bottom" delay={300}>
      <button
        type="button"
        onClick={onClose}
        style={noDragRegionStyle}
        aria-label={exitLabel}
        data-testid="workflows-close"
        className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {isElectron ? <X className="h-4 w-4" aria-hidden="true" /> : <ArrowLeft className="h-4 w-4" aria-hidden="true" />}
      </button>
    </Tooltip>
  );
}

/** Breadcrumb and scope, leaving the way out on the left, never the right. */
function WorkflowsHeader({ onClose, activeTab, showExit }: {
  onClose: () => void;
  activeTab: WorkflowsTab | null;
  showExit: boolean;
}) {
  const { isElectron, trafficLightPadding, dragRegionStyle, noDragRegionStyle } = useTitleBarChrome({ collapsedPadding: "0px" });
  // The shell can remain mounted briefly while navigating out of the area.
  const section = WORKFLOWS_TABS.find((tab) => tab.key === activeTab);
  return (
    <div
      className={cn("flex min-w-0 flex-1 items-center gap-3", isElectron && "cursor-move")}
      // With no brand exit visible (mobile drawer or native macOS rail),
      // leave room for traffic lights before the leading header exit.
      style={{ ...dragRegionStyle, paddingLeft: showExit ? trafficLightPadding : undefined }}
    >
      {showExit && <WorkflowsCloseButton onClose={onClose} />}
      <nav aria-label="Breadcrumb" className="flex min-w-0 flex-1 items-center gap-1.5 text-sm text-muted-foreground">
        <span className="hidden md:inline">Workflows</span>
        <span aria-hidden="true" className="hidden md:inline">›</span>
        <span aria-current="page" className="truncate font-medium text-foreground">{section?.label ?? "Workflows"}</span>
      </nav>
      <div className="flex min-w-0 max-w-xs cursor-default items-center max-md:max-w-48" style={noDragRegionStyle}>
        <ProjectSwitcher />
      </div>
    </div>
  );
}

/**
 * The area's scope: one project, or All projects. Every page honours it
 * (useWorkflowsScope). The list is the project selector (Projects/ProjectSelector).
 * Choosing a project makes it current and narrows to it;
 * All projects keeps the current project as background (`?project=`) and sets
 * `?allProjects=true`.
 */
function ProjectSwitcher() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const { projects } = useProjects();
  const selectProject = useProjectStore((state) => state.selectProject);
  const navigate = useNavigate();
  const scope = useWorkflowsScope();
  const [open, setOpen] = useState(false);

  const setAllProjects = (allProjects: boolean) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, allProjects: allProjects || undefined }),
      replace: true,
    } as never);

  return (
    <ProjectSelector
      open={open}
      onOpenChange={setOpen}
      mode="chat"
      align="left"
      selectedKey={scope.allProjects ? ALL_PROJECTS_KEY : (currentProject?.id ?? ALL_PROJECTS_KEY)}
      leadItems={[
        {
          key: ALL_PROJECTS_KEY,
          label: "All projects",
          testId: "workflows-project-all",
          onSelect: () => {
            if (!scope.allProjects) setAllProjects(true);
          },
        },
      ]}
      onSelect={(selection) => {
        if (selection.kind !== "project") return;
        const project = projects.find((p) => p.id === selection.row.key) ?? selection.row.project;
        const active = !scope.allProjects && project.id === currentProject?.id;
        if (active) return;
        if (scope.allProjects) setAllProjects(false);
        if (project.id !== currentProject?.id) void selectProject(project);
      }}
      trigger={(props) => (
        <button
          type="button"
          {...props}
          data-testid="workflows-project-switcher"
          className="inline-flex h-8 max-w-[18rem] items-center gap-2 rounded-md border border-border px-2.5 text-sm text-foreground transition-colors hover:border-muted-foreground/50 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <span className="text-muted-foreground">Project</span>
          <span className="truncate font-medium">{scope.allProjects ? "All projects" : (currentProject?.name ?? "All projects")}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        </button>
      )}
    />
  );
}

/** The selector's key for All projects. */
const ALL_PROJECTS_KEY = "";

/** The resolution ladder in the header comment. Runs once per mount of the area. */
function useWorkflowsProjectResolution() {
  const { pathname } = useLocation();
  useRouteProjectResolution({ reflectInUrl: orgFreePath(pathname).startsWith(WORKFLOWS_AREA_PATH) });
}
