// Copyright (c) 2025 Reliant Labs

/**
 * OBSERVABILITY › DASHBOARDS: the dashboard browser. Every dashboard of the
 * project in the console's scope, as the reader looks for one, and nothing
 * else: what is wrong right now (health, issues, failing jobs, releases) is
 * the project Overview's, so this page does not repeat it.
 *
 *   toolbar         search (across every section: title, key, tags, group),
 *                   Group, Source (release / workspace), Owner (forge, the
 *                   repo, or a person) — narrowed in place, in the URL.
 *   Starred         one row of chips, the caller's stars; the first three
 *                   also sit in the nav.
 *   the list        ONE table, a section per group (dashboardGroups.ts
 *                   listSections): Built-in first when there is one, then
 *                   Overview, Services, Jobs, Product, Platform, Releases,
 *                   Custom. A section folds; an empty group is no section.
 *                   A row is one line: the name (with the Forge workload it
 *                   is about and its tags, small), where it came from, when
 *                   the caller last viewed it, when it last changed, its
 *                   panels, and the star. The whole row opens it.
 *   Templates       the gallery to start from (and a blank dashboard).
 *
 * The page used to open on an "Overview" tab of health panels: the project
 * Overview's KPI strip, a process table, open issues, failing jobs and
 * recent releases. They live on the Overview alone now; an old `?view=all`
 * or `?tab=overview` link lands here, on the list.
 *
 * "New dashboard" offers a blank one, a template, or a forge dashboard file
 * to import. Authoring needs the admin role (the index says, `canWrite`):
 * without it the write actions stay visible, disabled, with the reason.
 *
 * ORG MODE (`project` undefined): the dashboards of EVERY app, as a read-only
 * list. Each row names its app and opens in that app's console; creating,
 * copying and saving belong to an app, so the list offers none of them.
 */

import { useMemo, useRef, useState, type ReactNode } from "react";
import { Boxes, ChevronDown, ChevronRight, LayoutDashboard, Lock, Plus, Search, Star } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { GettingStarted } from "@/components/Forge/GettingStarted";
import Mono from "@/components/forge-ui/mono";
import Tag from "@/components/forge-ui/tag";
import Tooltip from "@/components/forge-ui/tooltip";
import { useDashboardIndex, useDashboardMutation, useDashboardTemplates, useStarDashboard } from "@/hooks/dashboard-queries";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Dashboard } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { DeployWithAgentButton } from "../AskReliant/AskReliant";
import { formatAgo } from "../format";
import { appLabel, type ObsEnvironment } from "../obsPlace";
import { OBS_TOPICS, ObsError } from "../ObsStates";
import { DashboardHeader } from "./DashboardHeader";
import { DASHBOARD_GROUPS, GROUP_LABELS, listSections, SECTION_ABOUT, SECTION_LABELS, workloadOf, type DashboardGroup, type ListSection } from "./dashboardGroups";
import { filterIndex, galleryTemplates, isStandardBuiltin, NAV_STARRED, ownerOf, ownersOf, SOURCE_LABELS, starredOf, type SourceFilter } from "./dashboardModel";
import { NewDashboardDialog, SaveCopyDialog, envScopeOf } from "./dialogs";
import { BUTTON_PRIMARY, INK_LINK, Menu, StarButton } from "./parts";
import { useObsReadOnly } from "../obsReadOnly";
import { FIELD_BARE, FIELD_SHELL, toggleTone } from "../../controlStyles";

export interface DashboardsSearch {
  q?: string;
  group?: DashboardGroup;
  source?: SourceFilter;
  owner?: string;
}

/** The app filter of the org list: every app that has a dashboard, and the ones picked. */
export interface DashboardsAppFilter {
  selected: string[];
  onChange: (apps: string[]) => void;
}

export interface DashboardsListProps {
  /** The app whose dashboards these are. Undefined: the whole org (read-only, see the header). */
  project?: string;
  /** The console scope: an environment name or "all". Ignored in org mode, where an environment name is not unique. */
  env: string;
  environments: readonly ObsEnvironment[];
  search: DashboardsSearch;
  onSearch: (next: Partial<DashboardsSearch>) => void;
  /** An empty `dashboard` href is a dashboard with no console to open (a project-less one). */
  hrefs: { dashboard: (ref: DashboardRef) => string };
  navigateTo: (href: string) => void;
  /** Org mode: narrow to some apps. */
  appFilter?: DashboardsAppFilter;
}

function requireProject(project: string | undefined): string {
  if (project === undefined) throw new Error("dashboards are written in an app, not from the org list");
  return project;
}

const NO_FILTERS: Partial<DashboardsSearch> = { q: undefined, group: undefined, source: undefined, owner: undefined };

export function DashboardsList({ project, env, environments, search, onSearch, hrefs, navigateTo, appFilter }: DashboardsListProps) {
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const orgWide = project === undefined;
  const envId = orgWide || env === ENV_ALL ? undefined : environments.find((candidate) => candidate.name === env)?.id;
  const index = useDashboardIndex(orgId, project, envId);
  const templates = useDashboardTemplates(orgId, !orgWide);
  const star = useStarDashboard(orgId);
  const galleryRef = useRef<HTMLDivElement | null>(null);
  const [creating, setCreating] = useState<"blank" | "import" | null>(null);
  const [fromTemplate, setFromTemplate] = useState<DashboardSummary | null>(null);
  const [collapsed, setCollapsed] = useState<ReadonlySet<ListSection>>(new Set());

  const envIdOf = (name: string) => environments.find((candidate) => candidate.name === name)?.id;
  const create = useDashboardMutation(orgId, (api, org, input: { slug: string; spec: Dashboard; scope: "scope" | "all" }) =>
    api.saveDashboard({ orgId: org, projectId: requireProject(project), slug: input.slug, spec: input.spec, envScope: envScopeOf(input.scope, env, envIdOf), baseVersion: 0, message: "Created" })
  );
  const copy = useDashboardMutation(orgId, (api, org, input: { from: DashboardRef; title: string; scope: "scope" | "all" }) =>
    api.saveDashboardAsCopy({ orgId: org, from: input.from, projectId: requireProject(project), title: input.title, envScope: envScopeOf(input.scope, env, envIdOf) })
  );

  const all = useMemo(() => index.data?.dashboards ?? [], [index.data]);
  // The demo console writes nothing, whatever the index says (obsReadOnly.tsx).
  const obsLock = useObsReadOnly();
  const canWrite = !!index.data?.canWrite && !obsLock.readOnly;
  const pickedApps = appFilter?.selected;
  const shown = useMemo(
    () => filterIndex(all, search).filter((d) => !pickedApps?.length || pickedApps.includes(d.ref.projectId)),
    [all, search, pickedApps]
  );
  const sections = useMemo(() => listSections(shown), [shown]);
  const apps = useMemo(() => [...new Set(all.map((d) => d.ref.projectId))].sort(compareApps), [all]);
  const filtered = !!(search.q || search.group || search.source || search.owner || pickedApps?.length);
  const clearFilters = () => {
    onSearch(NO_FILTERS);
    appFilter?.onChange([]);
  };
  const onToolbar = (next: Partial<DashboardsSearch>) => {
    // A new search shows every match: no section stays folded over one.
    if (next.q && !search.q) setCollapsed(new Set());
    onSearch(next);
  };
  const toggleSection = (id: ListSection) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const starred = starredOf(shown);
  const owners = ownersOf(all);
  const toggleStar = (d: DashboardSummary) => star.mutate({ ref: d.ref, starred: !d.starred });
  // The demo's reason first (obsReadOnly.tsx): there, nobody can, admin or not.
  const writeHint = canWrite ? undefined : obsLock.reason ?? "Only organization admins can create dashboards.";

  const header = (
    <DashboardHeader
      title="Dashboards"
      meta={
        orgWide ? (
          <>
            <span>
              {index.data
                ? `${all.length} dashboard${all.length === 1 ? "" : "s"} across ${apps.length} app${apps.length === 1 ? "" : "s"}`
                : "Every app in your organization"}
            </span>
            <span>Open one to edit it, or to copy it, in its app</span>
          </>
        ) : (
          <>
            <span>
              {index.data ? `${all.length} dashboard${all.length === 1 ? "" : "s"} in ${project}` : project}
              {env === ENV_ALL ? " · all environments" : ""}
            </span>
            {env !== ENV_ALL && <Tag>{env}</Tag>}
            <span className="hidden sm:inline">Release dashboards are code; workspace dashboards are edited here</span>
          </>
        )
      }
      actions={
        orgWide ? undefined : (
          <Menu
            label="New dashboard"
            trigger={
              <>
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                New dashboard
                <ChevronDown className="h-3 w-3" aria-hidden="true" />
              </>
            }
            triggerClassName={BUTTON_PRIMARY}
            testId="dashboards-new"
            entries={[
              { label: "Blank dashboard", onSelect: () => setCreating("blank"), disabled: !canWrite, hint: writeHint, testId: "dashboards-new-blank" },
              // The gallery sits under the list.
              { label: "From a template…", onSelect: () => galleryRef.current?.scrollIntoView({ block: "start", behavior: "smooth" }), testId: "dashboards-new-template" },
              { label: "Import JSON…", detail: "a forge dashboard file", onSelect: () => setCreating("import"), disabled: !canWrite, hint: writeHint, testId: "dashboards-new-import" },
            ]}
          />
        )
      }
    />
  );

  const gallery = (
    <section ref={galleryRef} className="scroll-mt-6 space-y-3 pt-6" aria-labelledby="dashboards-templates-heading">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <h2 id="dashboards-templates-heading" className="text-sm font-medium text-ink">
          Start from a template
        </h2>
        <span className="text-xs text-ink-muted">Filled with this project&apos;s services and environments</span>
      </div>
      {templates.error ? (
        <p className="text-sm text-ink-muted">The template gallery could not be read.</p>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4" data-testid="dashboards-templates">
          <button
            type="button"
            disabled={!canWrite}
            title={writeHint}
            onClick={() => setCreating("blank")}
            className="flex min-h-16 items-center justify-center gap-2 rounded-lg border border-dashed border-border-strong px-4 py-3 text-sm text-ink-muted transition-colors hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
            data-testid="dashboards-template-blank"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            Blank dashboard
          </button>
          {templates.isLoading
            ? [0, 1, 2].map((i) => <div key={i} className="h-16 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none" />)
            : galleryTemplates(templates.data ?? [], all).map((template) => (
                <button
                  key={template.ref.key}
                  type="button"
                  disabled={!canWrite}
                  title={writeHint}
                  onClick={() => setFromTemplate(template)}
                  className="flex min-h-16 flex-col items-start gap-0.5 rounded-lg border border-border bg-surface px-3 py-2.5 text-left transition-colors hover:border-border-strong hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
                  data-testid={`dashboards-template-${template.ref.key}`}
                >
                  <span className="flex w-full items-center justify-between gap-2">
                    <span className="truncate text-sm font-medium text-ink">{template.title}</span>
                    {template.category === "platform" && <Tag>platform</Tag>}
                  </span>
                  <span className="line-clamp-2 text-xs text-ink-muted">{template.description || `${template.panelCount} panels`}</span>
                </button>
              ))}
        </div>
      )}
    </section>
  );

  const empty = (
    <div data-testid="dashboards-empty">
      <GettingStarted
        testId="dashboards-getting-started"
        icon={<LayoutDashboard className="h-4 w-4" />}
        title="No dashboards yet"
        about={OBS_TOPICS.dashboards.about}
        agent={<DeployWithAgentButton env={env === ENV_ALL ? null : env} />}
        steps={[
          {
            text: "Deploy a release. Forge generates a dashboard for every service and frontend, and they appear here with it.",
            command: `reliant forge env deploy ${env === ENV_ALL ? "<env>" : env}`,
          },
          {
            text: "Keep one in your repository: add a panel to a dashboard under deploy/observability/dashboards, and it ships with the next release.",
            command: "reliant forge obs dashboards add-panel <slug> --panel <panel.json>",
          },
          // The org-wide list has no app to start one in: that is each app's own Dashboards page.
          { text: orgWide ? "Or open an app and start one there, from a template or blank." : "Or start one here, from a template or blank, below." },
        ]}
        docs={OBS_TOPICS.dashboards.docs}
      >
        <p>
          Dashboards ship with your releases, so none is here until one is promoted{env === ENV_ALL ? "" : ` to ${env}`}.
        </p>
      </GettingStarted>
    </div>
  );

  let list: ReactNode;
  if (index.isLoading || org.isLoading) {
    list = <ListSkeleton />;
  } else if (index.error || (!orgId && org.error)) {
    list = <ObsError error={index.error ?? org.error} subject={project ?? "your apps"} onRetry={() => void (index.error ? index.refetch() : org.refetch())} retrying={index.isFetching} />;
  } else if (all.length === 0) {
    list = empty;
  } else {
    list = (
      <div className="space-y-4">
        <Toolbar search={search} onSearch={onToolbar} owners={owners} filtered={filtered} apps={appFilter ? apps : undefined} appFilter={appFilter} onClear={clearFilters} />
        {shown.length === 0 ? (
          <Card className="py-8 text-center text-sm text-ink-muted" data-testid="dashboards-no-match">
            No dashboards match.{" "}
            <button type="button" className={INK_LINK} onClick={clearFilters}>
              Clear the filters
            </button>
            {orgWide ? "." : " or start from a template below."}
          </Card>
        ) : (
          <>
            <StarredStrip dashboards={starred} href={hrefs.dashboard} filtered={filtered} />
            <DashboardTable
              sections={sections}
              orgWide={orgWide}
              environments={environments}
              collapsed={collapsed}
              onToggle={toggleSection}
              href={hrefs.dashboard}
              onStar={toggleStar}
            />
          </>
        )}
      </div>
    );
  }

  const ready = !index.isLoading && !org.isLoading && !index.error;
  return (
    <div className="space-y-2" data-testid="dashboards" data-state={index.isLoading ? "loading" : index.error ? "error" : "ready"}>
      {header}
      <div className="pt-4">{list}</div>
      {!orgWide && ready && gallery}
      {!orgWide && !canWrite && index.data && (
        <p className="pt-2 text-xs text-ink-muted" data-testid="dashboards-read-only">
          You can view and star dashboards. Creating, copying and changing them needs an organization admin.
        </p>
      )}
      {!orgWide && creating && (
        <NewDashboardDialog
          mode={creating}
          env={env}
          pending={create.isPending}
          error={create.error}
          onCancel={() => {
            create.reset();
            setCreating(null);
          }}
          onCreate={(input) =>
            create.mutate(input, {
              onSuccess: (saved) => {
                setCreating(null);
                navigateTo(hrefs.dashboard(saved.summary.ref));
              },
            })
          }
        />
      )}
      {!orgWide && fromTemplate && (
        <SaveCopyDialog
          sourceTitle={fromTemplate.title}
          what="template"
          env={env}
          pending={copy.isPending}
          error={copy.error}
          onCancel={() => {
            copy.reset();
            setFromTemplate(null);
          }}
          onSave={(input) =>
            copy.mutate(
              { from: fromTemplate.ref, ...input },
              {
                onSuccess: (saved) => {
                  setFromTemplate(null);
                  navigateTo(hrefs.dashboard(saved.summary.ref));
                },
              }
            )
          }
        />
      )}
    </div>
  );
}

// ── Pieces ──────────────────────────────────────────────────────────────────

const rowKey = (d: DashboardSummary) => `${d.ref.kind}/${d.ref.projectId}/${d.ref.key}`;

/** The caller's stars as one row of chips, pinned above the list. */
function StarredStrip({ dashboards, href, filtered }: { dashboards: DashboardSummary[]; href: (ref: DashboardRef) => string; filtered: boolean }) {
  // A filter that hides every star says nothing about stars: the strip goes with them.
  if (dashboards.length === 0 && filtered) return null;
  return (
    <section className="flex min-w-0 flex-wrap items-center gap-2" aria-labelledby="dashboards-starred-heading" data-testid="dashboards-starred">
      <h2 id="dashboards-starred-heading" className="mr-1 flex items-center gap-1.5 text-sm font-medium text-ink" title={`The first ${NAV_STARRED} also sit in the nav`}>
        <Star className="h-3.5 w-3.5 fill-current text-warning-ink" aria-hidden="true" />
        Starred
      </h2>
      {dashboards.length === 0 ? (
        <p className="text-sm text-ink-muted">Star a dashboard to pin it here and in the nav.</p>
      ) : (
        <ul className="contents">
          {dashboards.map((d) => {
            const target = href(d.ref);
            const chip = "inline-flex h-7 max-w-[16rem] items-center rounded-md border border-border bg-surface px-2.5 text-sm text-ink";
            return (
              <li key={rowKey(d)} className="min-w-0" data-testid={`dashboards-card-${d.ref.key}`}>
                {target ? (
                  <Link
                    href={target}
                    title={d.title}
                    className={cn(chip, "transition-colors hover:border-border-strong hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
                  >
                    <span className="truncate">{d.title}</span>
                  </Link>
                ) : (
                  <span className={chip}>
                    <span className="truncate">{d.title}</span>
                  </span>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/**
 * A column of the list. A secondary column collapses to zero width below its
 * breakpoint rather than leaving the table (display: none would): the
 * section headers span every column, and a colSpan wider than the columns
 * left on screen makes phantom columns that squeeze the name on a phone.
 */
interface Column {
  key: string;
  header: string;
  /** The header cell's width classes (table-fixed reads the first row). */
  width?: string;
  /** Below its breakpoint: cell padding and content, shown from it. */
  pad?: string;
  show?: string;
  numeric?: boolean;
  sr?: boolean;
}

const COLLAPSE = {
  sm: { pad: "px-0 sm:px-3", show: "hidden sm:inline" },
  md: { pad: "px-0 md:px-3", show: "hidden md:inline" },
  lg: { pad: "px-0 lg:px-3", show: "hidden lg:inline" },
} as const;

function DashboardTable({
  sections,
  orgWide,
  environments,
  collapsed,
  onToggle,
  href,
  onStar,
}: {
  sections: Array<{ id: ListSection; dashboards: DashboardSummary[] }>;
  orgWide: boolean;
  environments: readonly ObsEnvironment[];
  collapsed: ReadonlySet<ListSection>;
  onToggle: (id: ListSection) => void;
  href: (ref: DashboardRef) => string;
  onStar: (d: DashboardSummary) => void;
}) {
  // Fixed widths, so every section's columns line up under the one header; a phone keeps the name and the star.
  const columns: Column[] = [
    { key: "name", header: "Name" },
    ...(orgWide ? [{ key: "app", header: "App", width: "w-0 sm:w-36", ...COLLAPSE.sm }] : []),
    { key: "source", header: "Source", width: "w-0 md:w-28", ...COLLAPSE.md },
    { key: "viewed", header: "Viewed", width: "w-0 md:w-24", ...COLLAPSE.md },
    { key: "updated", header: "Updated", width: "w-0 lg:w-24", ...COLLAPSE.lg },
    { key: "panels", header: "Panels", width: "w-0 sm:w-16", numeric: true, ...COLLAPSE.sm },
    { key: "star", header: "Star", width: "w-11", sr: true },
  ];
  const col = (key: string) => columns.find((c) => c.key === key)!;
  const cell = (key: string, extra: string | false, content: ReactNode) => {
    const c = col(key);
    return (
      <td className={cn("overflow-hidden", c.pad ?? "px-3", c.numeric && "text-right tabular-nums", extra)}>
        <span className={c.show}>{content}</span>
      </td>
    );
  };
  return (
    <Card padding="none" className="overflow-hidden" data-testid="dashboards-list">
      <table className="w-full table-fixed border-collapse text-sm" aria-label="Dashboards">
        <thead>
          <tr className="h-8 border-b border-border text-left text-xs text-ink-muted">
            {columns.map((c) => (
              <th key={c.key} scope="col" className={cn("overflow-hidden font-medium", c.width, c.pad ?? "px-3", c.numeric && "text-right")}>
                <span className={cn(c.sr ? "sr-only" : c.show)}>{c.header}</span>
              </th>
            ))}
          </tr>
        </thead>
        {sections.map(({ id, dashboards }) => {
          const open = !collapsed.has(id);
          return (
            <tbody key={id} data-testid={`dashboards-group-${id}`} data-open={open} className="border-b border-border last:border-b-0">
              <tr>
                <th scope="rowgroup" colSpan={columns.length} className="px-1.5 pb-1 pt-3 text-left font-normal">
                  <button
                    type="button"
                    aria-expanded={open}
                    onClick={() => onToggle(id)}
                    className="group flex max-w-full items-baseline gap-2 rounded-sm px-1.5 py-0.5 text-left hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    data-testid={`dashboards-group-${id}-toggle`}
                  >
                    <ChevronRight className={cn("h-3.5 w-3.5 shrink-0 self-center text-ink-muted transition-transform motion-reduce:transition-none", open && "rotate-90")} aria-hidden="true" />
                    <span className="text-sm font-medium text-ink">{SECTION_LABELS[id]}</span>
                    <span className="text-xs tabular-nums text-ink-muted" data-testid={`dashboards-group-${id}-count`}>
                      {dashboards.length}
                    </span>
                    <span className="hidden truncate text-xs text-ink-muted sm:inline">{SECTION_ABOUT[id]}</span>
                  </button>
                </th>
              </tr>
              {open &&
                dashboards.map((d) => {
                  const target = href(d.ref);
                  return (
                    <tr key={rowKey(d)} className={cn("h-9", target && "relative hover:bg-ink/5")} data-testid="dashboards-row">
                      <td className="min-w-0 px-3">
                        <NameCell d={d} href={target} />
                      </td>
                      {orgWide && cell("app", false, <AppTag app={d.ref.projectId} />)}
                      {cell("source", "relative z-10", <SourceCell d={d} environments={environments} />)}
                      {cell("viewed", "text-xs text-ink-muted", d.lastViewedAtMs !== null ? formatAgo(d.lastViewedAtMs) : "—")}
                      {cell("updated", "text-xs text-ink-muted", updatedOf(d))}
                      {cell("panels", "text-ink-muted", d.panelCount || "—")}
                      <td className="relative z-10 pr-2 text-right">
                        <StarButton starred={d.starred} title={d.title} onToggle={() => onStar(d)} />
                      </td>
                    </tr>
                  );
                })}
            </tbody>
          );
        })}
      </table>
    </Card>
  );
}

/**
 * A row's name: the title as plain ink (the row is the link: hover lifts it,
 * no underline), then — small, on the same line — the Forge workload it is
 * about and its tags, or what a built-in needs.
 */
function NameCell({ d, href }: { d: DashboardSummary; href: string }) {
  const workload = workloadOf(d);
  const tags = d.tags ?? [];
  const title = (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      {/* The lock keeps its slot on every row, so names start in one column. */}
      {d.readOnly ? <Lock className="h-3 w-3 shrink-0 text-ink-muted" aria-label="read-only" /> : <span className="w-3 shrink-0" aria-hidden="true" />}
      <span className="truncate font-medium text-ink">{d.title}</span>
    </span>
  );
  // The name keeps its room; what follows it on the line truncates first.
  const detail = workload ? (
    <span className="inline-flex shrink-0 items-center gap-1" data-testid="dashboards-row-workload">
      <span className="sr-only">Workload</span>
      <Mono value={workload} copy={false} className="text-xs text-ink-muted" />
    </span>
  ) : d.source === "builtin" && d.viewKind !== "llm-usage" && !isStandardBuiltin(d) ? (
    // LLM usage is the caller's own: it needs no billing permission.
    <span className="truncate text-xs text-ink-muted">Requires cost-read permission</span>
  ) : d.description && tags.length === 0 ? (
    <span className="hidden min-w-0 flex-1 truncate text-xs text-ink-muted sm:block">{d.description}</span>
  ) : null;
  return (
    <span className="flex min-w-0 items-center gap-2">
      {href ? (
        // The anchor stretches across the row (after:inset-0): the whole row opens it, and its name is the title.
        <Link
          href={href}
          aria-label={d.title}
          className="flex min-w-0 max-w-full shrink-0 items-center text-ink after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-ring"
        >
          {title}
        </Link>
      ) : (
        <span className="flex min-w-0 max-w-full shrink-0">{title}</span>
      )}
      {detail}
      {tags.length > 0 && (
        <span className="hidden min-w-0 items-center gap-1 overflow-hidden sm:inline-flex">
          {tags.slice(0, 3).map((tag) => (
            <Tag key={tag}>{tag}</Tag>
          ))}
        </span>
      )}
    </span>
  );
}

/** Where a dashboard came from, said quietly: the word, with the release, file and scope in its tooltip. */
function SourceCell({ d, environments }: { d: DashboardSummary; environments: readonly ObsEnvironment[] }) {
  return (
    <Tooltip content={originOf(d, environments)}>
      <span className="text-xs text-ink-muted underline decoration-dotted decoration-ink-muted/50 underline-offset-2" data-testid="dashboards-row-source">
        {SOURCE_LABELS[d.source]}
      </span>
    </Tooltip>
  );
}

/** The tooltip's words: which release shipped it and from where, or who saved it and for which environments. */
function originOf(d: DashboardSummary, environments: readonly ObsEnvironment[]): string {
  if (isStandardBuiltin(d)) return "Built in for every project, over this project's data. Read-only here; save a copy to change it.";
  if (d.source === "builtin") return "Organization accounting from canonical billing sources.";
  if (d.release) {
    const shipped = d.release.releaseVersion ? `Shipped in ${d.release.releaseVersion}` : "Shipped in a release";
    const when = d.release.shippedAtMs ? `, ${formatAgo(d.release.shippedAtMs)}` : "";
    const from = d.source === "repo" ? (d.release.filePath ? ` from ${d.release.filePath}` : " from your repo") : " · generated by forge";
    return `${shipped}${when}${from}. Read-only here.`;
  }
  if (d.workspace) {
    const envs = d.workspace.envIds.length
      ? d.workspace.envIds.map((id) => environments.find((candidate) => candidate.id === id)?.name ?? id).join(", ")
      : "every environment";
    return `Saved in Reliant by ${ownerOf(d)} · v${d.workspace.version} · for ${envs}.`;
  }
  return SOURCE_LABELS[d.source];
}

function updatedOf(d: DashboardSummary): string {
  const at = d.workspace?.updatedAtMs ?? d.release?.shippedAtMs ?? null;
  return at ? formatAgo(at) : "—";
}

const CHIP = "inline-flex h-8 items-center gap-1 whitespace-nowrap rounded-md px-2.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function Toolbar({
  search,
  onSearch,
  owners,
  filtered,
  apps,
  appFilter,
  onClear,
}: {
  search: DashboardsSearch;
  onSearch: (next: Partial<DashboardsSearch>) => void;
  owners: string[];
  filtered: boolean;
  /** Org mode: the apps that have a dashboard, for the App filter. */
  apps?: string[];
  appFilter?: DashboardsAppFilter;
  onClear: () => void;
}) {
  const chip = (on: boolean) => cn(CHIP, toggleTone(on));
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="dashboards-toolbar">
      <label className={cn(FIELD_SHELL, "w-full sm:w-auto sm:min-w-72")}>
        <Search className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span className="sr-only">Search dashboards</span>
        <input
          value={search.q ?? ""}
          onChange={(event) => onSearch({ q: event.target.value || undefined })}
          placeholder="Search dashboards, workloads, tags"
          className={FIELD_BARE}
          data-testid="dashboards-search"
        />
      </label>
      <Menu
        label="Group"
        align="left"
        trigger={
          <>
            Group: {search.group ? GROUP_LABELS[search.group] : "all"}
            <ChevronDown className="h-3 w-3" aria-hidden="true" />
          </>
        }
        triggerClassName={chip(!!search.group)}
        testId="dashboards-chip-group"
        entries={[
          { label: "all", checked: !search.group, onSelect: () => onSearch({ group: undefined }) },
          ...DASHBOARD_GROUPS.map((group) => ({ label: GROUP_LABELS[group], checked: search.group === group, onSelect: () => onSearch({ group }) })),
        ]}
      />
      <button type="button" aria-pressed={search.source === "release"} className={chip(search.source === "release")} onClick={() => onSearch({ source: search.source === "release" ? undefined : "release" })} data-testid="dashboards-chip-release">
        Source: Release
      </button>
      <button type="button" aria-pressed={search.source === "workspace"} className={chip(search.source === "workspace")} onClick={() => onSearch({ source: search.source === "workspace" ? undefined : "workspace" })} data-testid="dashboards-chip-workspace">
        Source: Workspace
      </button>
      <Menu
        label="Owner"
        align="left"
        trigger={
          <>
            Owner: {search.owner ?? "anyone"}
            <ChevronDown className="h-3 w-3" aria-hidden="true" />
          </>
        }
        triggerClassName={chip(!!search.owner)}
        testId="dashboards-chip-owner"
        entries={[{ label: "anyone", checked: !search.owner, onSelect: () => onSearch({ owner: undefined }) }, ...owners.map((owner) => ({ label: owner, checked: search.owner === owner, onSelect: () => onSearch({ owner }) }))]}
      />
      {apps && appFilter && (
        <Menu
          label="App"
          align="left"
          trigger={
            <>
              <Boxes className="h-3.5 w-3.5" aria-hidden="true" />
              App: {appFilter.selected.length === 0 ? "every app" : appFilter.selected.length === 1 ? appLabel(appFilter.selected[0]!) : `${appFilter.selected.length} apps`}
              <ChevronDown className="h-3 w-3" aria-hidden="true" />
            </>
          }
          triggerClassName={chip(appFilter.selected.length > 0)}
          testId="dashboards-chip-app"
          entries={[
            { label: "every app", checked: appFilter.selected.length === 0, onSelect: () => appFilter.onChange([]) },
            ...apps.map((app) => ({
              label: appLabel(app),
              checked: appFilter.selected.includes(app),
              onSelect: () =>
                appFilter.onChange(appFilter.selected.includes(app) ? appFilter.selected.filter((name) => name !== app) : [...appFilter.selected, app]),
            })),
          ]}
        />
      )}
      {filtered && (
        <button type="button" className="h-8 px-2 text-sm text-ink-muted hover:text-ink" onClick={onClear} data-testid="dashboards-clear">
          Clear
        </button>
      )}
    </div>
  );
}

/** The app a dashboard belongs to (org mode). Unassigned is a dashboard with no app: it has no console. */
function AppTag({ app }: { app: string }) {
  return (
    <Tag title={app ? undefined : "Saved before projects existed: it has no app console to open in"}>
      {appLabel(app)}
    </Tag>
  );
}

/** The apps in display order: named ones alphabetically, Unassigned last. */
function compareApps(a: string, b: string): number {
  if (a === b) return 0;
  if (a === "") return 1;
  if (b === "") return -1;
  return a.localeCompare(b);
}

function ListSkeleton() {
  return (
    <div className="space-y-4" aria-busy="true" aria-label="Loading dashboards" data-testid="dashboards-loading">
      <div className="h-8 w-96 max-w-full animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
      <div className="h-7 w-72 max-w-full animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
      <div className="space-y-px overflow-hidden rounded-lg border border-border bg-surface">
        {[0, 1, 2, 3, 4, 5].map((i) => (
          <div key={i} className="h-9 animate-pulse bg-ink/5 motion-reduce:animate-none" />
        ))}
      </div>
    </div>
  );
}
