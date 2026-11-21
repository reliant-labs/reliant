// Copyright (c) 2025 Reliant Labs

/**
 * Projects (`/o/<org>/projects`): the org's projects and its apps, one list
 * (control-plane docs/design/unified-shell-and-projects.md §8.3, R4).
 *
 *   Your projects          name, repos, the machines it is on, its apps as
 *                          chips with live state, last activity
 *   Shared with the org    teammates' projects shared with you (read-only)
 *   Apps without code here org apps none of those projects contain, each
 *                          with its environments, its source remote and
 *                          "Get the code"
 *
 * Filters All / With apps / Sites / Code only (with counts), a search, and
 * the order every project surface uses: pinned first, then recent activity
 * (services/projects/catalog.ts). Pin and hide are the person's own, per org,
 * in the `sidebar.projects` setting the sidebar reads too; a hidden row stays
 * reachable here behind "Show hidden". The data is one request,
 * `ListProjectsWithApps`, through useProjectCatalog; the link between a
 * project's code and an org app is the server's, never re-derived here.
 *
 * A row opens the project's page (projectPath), or for an app-only row the
 * app's console (appPath). One responsive page: no `/m` twin (§10.5).
 *
 * In the one shell (U6) this is the nav's Projects entry. With several
 * projects chosen it is their Home, the summary rows alone (ProjectsOverview),
 * and this list is one click away at `?browse=1`. A person who cannot run a
 * chat yet lands here (the daemonless home), so the way to set chats up leads
 * the page. The filters keep U6's first Projects page's values
 * (`?show=apps|sites`), which the retired /apps and /sites redirect to.
 */

import { useMemo, useState } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Activity, AppWindow, Download, Eye, EyeOff, FolderGit2, FolderPlus, MessageSquare, MoreHorizontal, Pin, PinOff, Plus, Settings2 } from "lucide-react";

import AlertBanner from "@/components/forge-ui/alert_banner";
import Button from "@/components/forge-ui/button";
import DropdownMenu, { type MenuItem } from "@/components/forge-ui/dropdown_menu";
import { FilterBar, FilterChip, FilterSearch } from "@/components/forge-ui/filter_bar";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { isCloudDaemon } from "@/components/Projects/picker/format";
import { useProjectCatalog } from "@/hooks/project-catalog-queries";
import { useSidebarProjectPrefs } from "@/hooks/sidebar-project-queries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { appPath, projectChatsPath, projectPath, projectsPath } from "@/lib/paths";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import {
  CATALOG_FILTERS,
  filterCounts,
  listCatalog,
  rowApps,
  type AppChipModel,
  type AppOnlyCatalogRow,
  type CatalogFilter,
  type HostedAppsState,
  type ListedRow,
} from "@/services/projects/catalog";
import { appCount, appSourceRemote, cloneUrlOf, projectsPageSections, type ProjectsSection } from "@/services/projects/projectsPage";
import { openAddProject } from "@/store/addProjectDialogStore";

import { useStartBuildingApp } from "../Apps/startBuilding";
import { orgPath } from "../Forge/Console/paths";
import { ProjectsOverview } from "../Forge/Console/ProjectsOverview";
import { AreaShell } from "../Layout/AreaShell";
import { useShellSeveral } from "../Layout/sidebar/shellProject";
import { chatSetupNeeded } from "../OnboardingFlow/onboardingGate";
import { SetUpChatsCard } from "../OnboardingFlow/SetUpChatsCard";
import { useChatSetup } from "../OnboardingFlow/useChatSetup";
import { AppChip } from "./AppChip";
import { ProjectRowSummary } from "./ProjectRowSummary";
import { ADD_PROJECT_LABEL, CLONE_REPOSITORY_LABEL, OPEN_FOLDER_LABEL } from "./projectCopy";

export function ProjectsPage() {
  return (
    <AreaShell areaPath={projectsPath()} areaLabel="Projects" areaNoun="projects" learn="overview">
      <ProjectsBody />
    </AreaShell>
  );
}

/** "Get the code" for an app: clone its recorded source when a cloud machine can, else the Add project choices. */
export function useGetTheCode(): (app: AppChipModel) => void {
  const { daemons } = useDaemonStatus();
  const canClone = daemons.some((daemon) => isCloudDaemon(daemon.daemonType));
  return (app) => {
    const remote = appSourceRemote(app);
    // A remote that is not a canonical host/owner/repo pre-fills nothing.
    const repoUrl = remote ? cloneUrlOf(remote) : null;
    openAddProject(repoUrl && canClone ? "clone" : undefined, repoUrl ? { repoUrl } : undefined);
  };
}

/**
 * The filter in the URL (`?show=apps|sites|code`), so a refresh, Back and a
 * shared link keep it. `apps` and `sites` are the values U6's first Projects
 * page used, kept so its links still open the same view.
 */
const SHOW_OF: Record<CatalogFilter, string | undefined> = { all: undefined, with_apps: "apps", sites: "sites", code_only: "code" };

export function filterOfShow(show: unknown): CatalogFilter {
  if (show === "apps") return "with_apps";
  if (show === "sites") return "sites";
  if (show === "code") return "code_only";
  return "all";
}

export function ProjectsBody() {
  const { catalog, isLoading, error, refetch } = useProjectCatalog();
  const { prefs, writable, setPinned, setHidden } = useSidebarProjectPrefs();
  const { show, browse } = useSearch({ strict: false }) as { show?: unknown; browse?: unknown };
  // Several projects in the shell: this page is their Home, the summary rows alone (U6b).
  const { mode: several } = useShellSeveral();
  const navigate = useNavigate();
  const [filter, setFilterState] = useState<CatalogFilter>(() => filterOfShow(show));
  const setFilter = (next: CatalogFilter) => {
    setFilterState(next);
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, show: SHOW_OF[next] }),
      replace: true,
    } as never);
  };
  const [query, setQuery] = useState("");
  const [showHidden, setShowHidden] = useState(false);
  const getTheCode = useGetTheCode();
  // A person who cannot run a chat yet lands here (the daemonless home, U6).
  const setup = useChatSetup();
  const startBuilding = useStartBuildingApp();

  const listed = useMemo(
    () => (catalog ? listCatalog(catalog, prefs, { filter, query, showHidden }) : []),
    [catalog, prefs, filter, query, showHidden],
  );
  const counts = useMemo(
    () => (catalog ? filterCounts(catalog, prefs, { query, showHidden }) : null),
    [catalog, prefs, query, showHidden],
  );
  const everything = useMemo(() => (catalog ? listCatalog(catalog, prefs, { showHidden: true }) : []), [catalog, prefs]);
  const hiddenCount = everything.filter((l) => l.hidden).length;
  const sections = useMemo(() => projectsPageSections(listed), [listed]);

  const projectTotal = everything.filter((l) => l.row.kind === "project").length;
  const apps = appCount(everything);
  const empty = !!catalog && catalog.rows.length === 0;
  const filtered = filter !== "all" || query.trim() !== "";

  // One action in the header (it must fit a phone beside the title); the
  // org's Observability and Build an app (the apps list's own action, U6) are
  // links in the meta line, not more buttons.
  const actions = [
    { label: ADD_PROJECT_LABEL, onClick: () => openAddProject(), variant: "primary" as const, icon: <Plus className="h-4 w-4" aria-hidden="true" /> },
  ];
  const observability = (
    <Link
      to={orgPath({ kind: "observability" })}
      className="no-color inline-flex items-center gap-1 font-medium text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Activity className="h-3.5 w-3.5" aria-hidden="true" />
      Observability
    </Link>
  );
  const buildAnApp = (
    <button
      type="button"
      onClick={startBuilding}
      className="inline-flex items-center gap-1 font-medium text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="projects-build-app"
    >
      <AppWindow className="h-3.5 w-3.5" aria-hidden="true" />
      Build an app
    </button>
  );

  // A filter in the URL (`?show=…`: the retired /apps and /sites pages'
  // redirects, a claim's landing) is a list the person asked for, so it shows
  // the list whatever the shell's selection.
  if (several && browse === undefined && filterOfShow(show) === "all") {
    return (
      <div className="space-y-4" data-testid="projects-page" data-mode="home">
        <div className="forge-ui">
          <ProjectsOverview />
        </div>
        <div className="mx-auto max-w-[var(--page-max)] text-right">
          <a
            href={`${projectsPath()}?browse=1`}
            onClick={(event) => {
              if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
              event.preventDefault();
              void navigate({ href: `${projectsPath()}?browse=1` });
            }}
            className="no-color text-meta text-muted-foreground hover:text-foreground hover:underline"
            data-testid="projects-browse-all"
          >
            Browse all projects
          </a>
        </div>
      </div>
    );
  }

  return (
    <div className="forge-ui flex flex-col gap-5" data-testid="projects-page">
      <PageHeader
        title="Projects"
        meta={
          <>
            {catalog && !empty ? (
              <span data-testid="projects-counts">
                {projectTotal} {projectTotal === 1 ? "project" : "projects"}
                {apps > 0 && ` · ${apps} ${apps === 1 ? "app" : "apps"}`}
              </span>
            ) : (
              <span>Your code, the apps it deploys, and what your org shares with you.</span>
            )}
            {buildAnApp}
            {observability}
          </>
        }
        actions={actions}
        className="mb-0"
      />

      {chatSetupNeeded(setup) && <SetUpChatsCard setup={setup} variant="page" onNavigate={(href) => void navigate({ href })} />}

      {catalog && <HostedAppsBanner state={catalog.hostedAppsState} onRetry={refetch} />}

      {isLoading && !catalog ? (
        <LoadingList />
      ) : error && !catalog ? (
        <AlertBanner
          variant="error"
          title="Couldn’t load your projects"
          message={error.message || "The server did not answer."}
          action={{ label: "Try again", onClick: refetch }}
          data-testid="projects-error"
        />
      ) : empty ? (
        <NoProjects />
      ) : catalog ? (
        <>
          <FilterBar
            label="Filter projects"
            testId="projects-filters"
            trailing={
              hiddenCount > 0 ? (
                <FilterChip on={showHidden} count={hiddenCount} onClick={() => setShowHidden((on) => !on)} testId="projects-show-hidden">
                  Show hidden
                </FilterChip>
              ) : undefined
            }
          >
            <FilterSearch value={query} onChange={setQuery} placeholder="Search projects and apps" testId="projects-search" />
            <div role="group" aria-label="Show" className="flex flex-wrap items-center gap-2">
              {CATALOG_FILTERS.map((f) => (
                <FilterChip key={f.id} on={filter === f.id} count={counts?.[f.id]} onClick={() => setFilter(f.id)} testId={`projects-filter-${f.id}`}>
                  {f.label}
                </FilterChip>
              ))}
            </div>
          </FilterBar>

          {sections.length === 0 ? (
            <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed border-border px-4 py-10 text-center" data-testid="projects-no-match">
              <p className="text-ui text-muted-foreground">
                {query.trim() ? `Nothing matches “${query.trim()}”` : "Nothing in this filter"}
                {filtered && filter !== "all" ? ` under ${CATALOG_FILTERS.find((f) => f.id === filter)?.label}.` : "."}
              </p>
              {filtered && (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => {
                    setFilter("all");
                    setQuery("");
                  }}
                >
                  Clear filters
                </Button>
              )}
            </div>
          ) : (
            sections.map((section) => (
              <ProjectsSectionList
                key={section.id}
                section={section}
                writable={writable}
                onPin={setPinned}
                onHide={setHidden}
                onGetTheCode={getTheCode}
              />
            ))
          )}
        </>
      ) : null}
    </div>
  );
}

/** Why app chips say less than usual: the org's apps could not be read. */
function HostedAppsBanner({ state, onRetry }: { state: HostedAppsState; onRetry: () => void }) {
  if (state === "no_access") {
    return (
      <AlertBanner
        role="status"
        variant="info"
        title="You can’t see this organization’s apps"
        message="Your projects are listed, but which of their apps are deployed needs deploy access in this org. Ask an org owner for it."
        data-testid="projects-apps-no-access"
      />
    );
  }
  if (state === "unavailable") {
    return (
      <AlertBanner
        role="status"
        variant="warning"
        title="App status is unavailable"
        message="Reliant couldn’t reach the control plane, so apps show without their deploy state and apps without code here are not listed."
        action={{ label: "Try again", onClick: onRetry }}
        data-testid="projects-apps-unavailable"
      />
    );
  }
  return null;
}

function LoadingList() {
  return (
    <div className="rounded-xl border border-border bg-card" data-testid="projects-loading" aria-busy="true">
      <SkeletonLoader variant="list-item" count={4} />
    </div>
  );
}

/** No project and no org app: the two ways to bring code in. */
function NoProjects() {
  return (
    <div className="flex flex-col items-center gap-4 rounded-xl border border-dashed border-border px-6 py-14 text-center" data-testid="projects-empty">
      <span className="flex h-12 w-12 items-center justify-center rounded-full border border-border bg-background text-muted-foreground">
        <FolderGit2 className="h-6 w-6" aria-hidden="true" />
      </span>
      <div className="flex max-w-md flex-col gap-1">
        <h2 className="text-section text-foreground">No projects yet</h2>
        <p className="text-ui text-muted-foreground">
          A project is a codebase Reliant works in. Clone a repository or open a folder on one of your machines; the apps its code deploys show up beside it.
        </p>
      </div>
      <div className="flex flex-wrap justify-center gap-2">
        <Button variant="secondary" size="sm" leftIcon={<Download className="h-4 w-4" />} onClick={() => openAddProject("clone")}>
          {CLONE_REPOSITORY_LABEL}
        </Button>
        <Button variant="secondary" size="sm" leftIcon={<FolderPlus className="h-4 w-4" />} onClick={() => openAddProject("folder")}>
          {OPEN_FOLDER_LABEL}
        </Button>
      </div>
    </div>
  );
}

interface SectionListProps {
  section: ProjectsSection;
  writable: boolean;
  onPin: (key: string, on: boolean) => void;
  onHide: (key: string, on: boolean) => void;
  onGetTheCode: (app: AppChipModel) => void;
}

function ProjectsSectionList({ section, writable, onPin, onHide, onGetTheCode }: SectionListProps) {
  const headingId = `projects-section-${section.id}`;
  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-2" data-testid={headingId}>
      <h2 id={headingId} className="flex items-baseline gap-2 px-1 text-ui font-medium text-muted-foreground">
        {section.label}
        <span className="text-meta tabular-nums">{section.rows.length}</span>
      </h2>
      <ul className="divide-y divide-border-subtle overflow-hidden rounded-xl border border-border bg-card">
        {section.rows.map((entry) => (
          <CatalogRowItem key={entry.row.key} entry={entry} writable={writable} onPin={onPin} onHide={onHide} onGetTheCode={onGetTheCode} />
        ))}
      </ul>
    </section>
  );
}

function activityText(ms: number): string {
  return ms > 0 ? formatRelativeTime(new Date(ms).toISOString()) : "";
}

interface RowProps {
  entry: ListedRow;
  writable: boolean;
  onPin: (key: string, on: boolean) => void;
  onHide: (key: string, on: boolean) => void;
  onGetTheCode: (app: AppChipModel) => void;
}

function CatalogRowItem({ entry, writable, onPin, onHide, onGetTheCode }: RowProps) {
  const { row, pinned, hidden } = entry;
  const href = row.kind === "project" ? projectPath(row.key) : appPath(row.name);
  const activity = activityText(row.activityAt);
  return (
    <li
      className={cn("group flex min-h-14 items-center gap-1 pr-2 hover:bg-foreground/5", hidden && "opacity-60")}
      data-testid="projects-row"
      data-row-key={row.key}
      data-hidden={hidden || undefined}
    >
      <Link
        to={href}
        className="no-color flex min-w-0 flex-1 items-center gap-3 self-stretch rounded-md py-2.5 pl-4 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        aria-label={row.kind === "project" ? `Open ${row.name}` : `Open the ${row.name} app`}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          {row.kind === "project" ? (
            <ProjectRowSummary row={row} pinned={pinned} maxChips={3} chipsClassName="max-sm:hidden" />
          ) : (
            <AppOnlyRowSummary row={row} pinned={pinned} />
          )}
          <NarrowChips apps={rowApps(row)} />
        </div>
        <span className="hidden w-24 shrink-0 text-right text-meta text-muted-foreground sm:block" title={row.activityAt > 0 ? formatAbsoluteTime(new Date(row.activityAt).toISOString()) : undefined}>
          {hidden ? "Hidden" : activity}
        </span>
      </Link>
      {row.kind === "app" && (
        <Button
          variant="ghost"
          size="sm"
          leftIcon={<Download className="h-4 w-4" />}
          onClick={() => onGetTheCode(row.app)}
          data-testid="projects-get-code"
          className="max-sm:hidden"
        >
          Get the code
        </Button>
      )}
      <RowMenu entry={entry} writable={writable} onPin={onPin} onHide={onHide} onGetTheCode={onGetTheCode} />
    </li>
  );
}

/** An app-only row: the app, its environments and the repository it was cut from. */
function AppOnlyRowSummary({ row, pinned }: { row: AppOnlyCatalogRow; pinned: boolean }) {
  const remote = appSourceRemote(row.app);
  const envs = row.app.environments;
  return (
    <div className="flex min-w-0 flex-1 items-center gap-3" data-testid="project-row-summary" data-row-key={row.key}>
      <AppWindow aria-hidden className="h-4 w-4 shrink-0 text-muted-foreground" />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-ui font-medium text-foreground">{row.name}</span>
          {pinned ? <Pin aria-label="Pinned" className="h-3 w-3 shrink-0 text-muted-foreground" /> : null}
        </span>
        <span className="flex min-w-0 items-center gap-1.5 text-meta text-muted-foreground">
          <span className="truncate">{remote ?? "No recorded source"}</span>
          <span aria-hidden>·</span>
          <span className="shrink-0">{envs.length > 0 ? envs.join(", ") : "No environments"}</span>
        </span>
      </div>
      <AppChip app={row.app} interactive={false} className="max-sm:hidden" />
    </div>
  );
}

/** At phone width the chips get their own line under the name, where they have room. */
function NarrowChips({ apps }: { apps: AppChipModel[] }) {
  if (apps.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1 pl-7 sm:hidden" data-testid="projects-row-chips-narrow">
      {apps.map((app) => (
        <AppChip key={`${app.repoId ?? ""}/${app.subpath ?? ""}/${app.name}`} app={app} interactive={false} />
      ))}
    </span>
  );
}

function RowMenu({ entry, writable, onPin, onHide, onGetTheCode }: RowProps) {
  const { row, pinned, hidden } = entry;
  const items: MenuItem[] = [];
  if (row.kind === "project") {
    items.push({ label: "Open chats", href: projectChatsPath(row.key), icon: <MessageSquare className="h-3.5 w-3.5" /> });
    if (!row.shared) items.push({ label: "Project settings", href: projectPath(row.key, "settings"), icon: <Settings2 className="h-3.5 w-3.5" /> });
  } else {
    items.push({ label: "Open console", href: appPath(row.name), icon: <AppWindow className="h-3.5 w-3.5" /> });
    items.push({ label: "Get the code", onClick: () => onGetTheCode(row.app), icon: <Download className="h-3.5 w-3.5" /> });
  }
  const prefs: MenuItem[] = [
    {
      label: pinned ? "Unpin" : "Pin to top",
      onClick: () => onPin(row.key, !pinned),
      icon: pinned ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />,
      disabled: !writable || hidden,
      testId: "projects-row-pin",
    },
    {
      label: hidden ? "Unhide" : "Hide",
      onClick: () => onHide(row.key, !hidden),
      icon: hidden ? <Eye className="h-3.5 w-3.5" /> : <EyeOff className="h-3.5 w-3.5" />,
      disabled: !writable,
      hint: hidden ? undefined : "From this list and the sidebar",
      testId: "projects-row-hide",
    },
  ];
  return (
    <DropdownMenu
      align="right"
      label={`${row.name} actions`}
      groups={[{ items }, { items: prefs }]}
      trigger={(props) => (
        <button
          type="button"
          {...props}
          aria-label={`More actions for ${row.name}`}
          data-testid="projects-row-menu"
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </button>
      )}
    />
  );
}
