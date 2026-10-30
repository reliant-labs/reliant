// Copyright (c) 2025 Reliant Labs

/**
 * OBSERVABILITY › DASHBOARDS: every dashboard of the project in the console's
 * scope, as the reader looks for one. Two views, in the URL (`view`):
 *
 *   Overview          the landing view (DashboardsHub): health, failing
 *                     jobs, releases, issues, and a card per group.
 *   All dashboards    the grouped list:
 *     toolbar         search (across every group: title, key, tags, group),
 *                     Group, Source (release / workspace), Owner (forge, the
 *                     repo, or a person) — narrowed in place, in the URL.
 *     Starred         cards with up to three headline panels drawn small
 *                     (the server picks them: number and line panels first).
 *                     The first three also sit in the nav.
 *     groups          Overview, Services, Jobs, Product, Platform, Releases,
 *                     Custom (dashboardGroups.ts): collapsible, with counts.
 *                     A row names its Forge workload; where it came from
 *                     (source, release, file) is secondary, in a tooltip.
 *     Templates       the gallery to start from (and a blank dashboard).
 *
 * Searching or narrowing opens the list. "New dashboard" offers a blank one,
 * a template, or a forge dashboard file to import. Authoring needs the admin
 * role (the index says, `canWrite`): without it the write actions stay
 * visible, disabled, with the reason.
 *
 * ORG MODE (`project` undefined): the dashboards of EVERY app, as a read-only
 * grouped list with no Overview (health belongs to one app). Each row names
 * its app and opens in that app's console; creating, copying and saving
 * belong to an app, so the list offers none of them.
 */

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Boxes, ChevronDown, ChevronRight, LayoutDashboard, Lock, Plus, Search } from "lucide-react";

import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import { GettingStarted } from "@/components/Forge/GettingStarted";
import Mono from "@/components/forge-ui/mono";
import Tabs from "@/components/forge-ui/tabs";
import Tag from "@/components/forge-ui/tag";
import Tooltip from "@/components/forge-ui/tooltip";
import { useDashboardIndex, useDashboardMutation, useDashboardTemplates, useStarDashboard } from "@/hooks/dashboard-queries";
import { useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Dashboard, ObsScope, Panel } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { formatAgo } from "../format";
import { appLabel, type ObsEnvironment } from "../obsPlace";
import { OBS_TOPICS, ObsError } from "../ObsStates";
import { DashboardHeader } from "./DashboardHeader";
import { DASHBOARD_GROUPS, GROUP_ABOUT, GROUP_LABELS, groupIndex, groupOf, workloadOf, type DashboardGroup } from "./dashboardGroups";
import { filterIndex, NAV_STARRED, ownerOf, ownersOf, SOURCE_LABELS, starredOf, type SourceFilter } from "./dashboardModel";
import { DashboardsHub, type DashboardsHubHrefs } from "./DashboardsHub";
import { NewDashboardDialog, SaveCopyDialog, envScopeOf } from "./dialogs";
import { PanelBody } from "./PanelBody";
import { BUTTON_PRIMARY, INK_LINK, Menu, StarButton } from "./parts";

/** The list's own view: absent is the Overview (when the app has one). */
export type DashboardsView = "all";

export interface DashboardsSearch {
  view?: DashboardsView;
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
  /**
   * An empty `dashboard` href is a dashboard with no console to open (a
   * project-less one). `hub` is where the Overview links; without it (org
   * mode, a harness) the page is the list alone.
   */
  hrefs: { dashboard: (ref: DashboardRef) => string; hub?: DashboardsHubHrefs };
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
  const [collapsed, setCollapsed] = useState<ReadonlySet<DashboardGroup>>(new Set());
  const [scrollToGallery, setScrollToGallery] = useState(false);

  const envIdOf = (name: string) => environments.find((candidate) => candidate.name === name)?.id;
  const create = useDashboardMutation(orgId, (api, org, input: { slug: string; spec: Dashboard; scope: "scope" | "all" }) =>
    api.saveDashboard({ orgId: org, projectId: requireProject(project), slug: input.slug, spec: input.spec, envScope: envScopeOf(input.scope, env, envIdOf), baseVersion: 0, message: "Created" })
  );
  const copy = useDashboardMutation(orgId, (api, org, input: { from: DashboardRef; title: string; scope: "scope" | "all" }) =>
    api.saveDashboardAsCopy({ orgId: org, from: input.from, projectId: requireProject(project), title: input.title, envScope: envScopeOf(input.scope, env, envIdOf) })
  );

  // A card's sparklines read the app the dashboard belongs to, never the whole org.
  const scopeOf = useMemo(
    () => (appOf: string | undefined): ObsScope | null => (orgId ? { orgId, projectId: appOf || undefined, envIds: envId ? [envId] : [], filters: [] } : null),
    [orgId, envId]
  );
  const all = useMemo(() => index.data?.dashboards ?? [], [index.data]);
  const canWrite = !!index.data?.canWrite;
  const pickedApps = appFilter?.selected;
  const shown = useMemo(
    () => filterIndex(all, search).filter((d) => !pickedApps?.length || pickedApps.includes(d.ref.projectId)),
    [all, search, pickedApps]
  );
  const grouped = useMemo(() => groupIndex(shown).filter((g) => g.dashboards.length > 0), [shown]);
  const apps = useMemo(() => [...new Set(all.map((d) => d.ref.projectId))].sort(compareApps), [all]);
  const filtered = !!(search.q || search.group || search.source || search.owner || pickedApps?.length);
  const hub = !orgWide && hrefs.hub ? hrefs.hub : null;
  // Narrowing is a list question: a search typed on the Overview opens the list.
  const view: "overview" | "all" = hub && search.view !== "all" && !filtered ? "overview" : "all";
  useEffect(() => {
    if (!scrollToGallery || view !== "all" || !galleryRef.current) return;
    galleryRef.current.scrollIntoView({ block: "start", behavior: "smooth" });
    setScrollToGallery(false);
  }, [scrollToGallery, view]);
  const clearFilters = () => {
    onSearch(NO_FILTERS);
    appFilter?.onChange([]);
  };
  const onToolbar = (next: Partial<DashboardsSearch>) => {
    // A new search shows every match: no group stays folded over one.
    if (next.q && !search.q) setCollapsed(new Set());
    onSearch(next);
  };
  const openGroup = (group: DashboardGroup) => {
    setCollapsed(new Set());
    onSearch({ ...NO_FILTERS, view: "all", group });
  };
  const toggleGroup = (group: DashboardGroup) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(group)) next.delete(group);
      else next.add(group);
      return next;
    });
  const starred = starredOf(shown);
  const owners = ownersOf(all);
  const toggleStar = (d: DashboardSummary) => star.mutate({ ref: d.ref, starred: !d.starred });
  const writeHint = canWrite ? undefined : "Only organization admins can create dashboards.";

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
            <span>Release dashboards are code; workspace dashboards are edited here</span>
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
            {
              label: "From a template…",
              onSelect: () => {
                // The gallery sits under the list: open it there, then scroll.
                if (view === "all") galleryRef.current?.scrollIntoView({ block: "start", behavior: "smooth" });
                else {
                  setScrollToGallery(true);
                  onSearch({ ...NO_FILTERS, view: "all" });
                }
              },
              testId: "dashboards-new-template",
            },
            { label: "Import JSON…", detail: "a forge dashboard file", onSelect: () => setCreating("import"), disabled: !canWrite, hint: writeHint, testId: "dashboards-new-import" },
          ]}
        />
        )
      }
    />
  );

  const gallery = (
    <div ref={galleryRef} className="scroll-mt-6">
      <Section title="Start from a template" meta="Filled with this project's services and environments">
        {templates.error ? (
          <p className="text-sm text-ink-muted">The template gallery could not be read.</p>
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4" data-testid="dashboards-templates">
            <button
              type="button"
              disabled={!canWrite}
              title={writeHint}
              onClick={() => setCreating("blank")}
              className="flex min-h-24 items-center justify-center gap-2 rounded-lg border border-dashed border-border px-4 py-3 text-sm text-ink-muted transition-colors hover:border-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-60"
              data-testid="dashboards-template-blank"
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              Blank dashboard
            </button>
            {templates.isLoading
              ? [0, 1, 2].map((i) => <div key={i} className="h-24 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none" />)
              : (templates.data ?? []).map((template) => (
                  <button
                    key={template.ref.key}
                    type="button"
                    disabled={!canWrite}
                    title={writeHint}
                    onClick={() => setFromTemplate(template)}
                    className="flex min-h-24 flex-col items-start gap-1 rounded-lg border border-border bg-surface px-4 py-3 text-left transition-colors hover:border-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-60"
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
      </Section>
    </div>
  );

  // Every group's table shares its columns: the name (with the workload it is
  // about and its tags), its app in org mode, where it came from (secondary),
  // when it last changed, its panels, and the star.
  const columns: DataTableColumn<DashRow>[] = [
    {
      key: "name",
      header: "Name",
      render: (_, { d }) => <NameCell d={d} />,
    },
    ...(orgWide ? [{ key: "app", header: "App", render: (_: unknown, { d }: DashRow) => <AppTag app={d.ref.projectId} /> }] : []),
    {
      key: "source",
      header: "Source",
      interactive: true,
      headerClassName: "hidden md:table-cell",
      className: "hidden md:table-cell",
      render: (_, { d }) => <SourceCell d={d} environments={environments} />,
    },
    {
      key: "updated",
      header: "Updated",
      headerClassName: "hidden lg:table-cell",
      className: "hidden lg:table-cell",
      render: (_, { d }) => <span className="text-xs text-ink-muted">{updatedOf(d)}</span>,
    },
    { key: "panels", header: "Panels", numeric: true, render: (_, { d }) => <span className="text-ink-muted">{d.panelCount || "—"}</span> },
    {
      key: "star",
      header: "Star",
      srHeader: true,
      interactive: true,
      width: "2.75rem",
      className: "pl-0 pr-2 text-right",
      render: (_, { d }) => <StarButton starred={d.starred} title={d.title} onToggle={() => toggleStar(d)} />,
    },
  ];

  const empty = (
    <div data-testid="dashboards-empty">
      <GettingStarted
        testId="dashboards-getting-started"
        icon={<LayoutDashboard className="h-4 w-4" />}
        title="No dashboards yet"
        about={OBS_TOPICS.dashboards.about}
        steps={[
          {
            text: "Deploy a release. Forge generates a dashboard for every service and frontend, and they appear here with it.",
            command: `forge env deploy ${env === ENV_ALL ? "<env>" : env}`,
          },
          {
            text: "Keep one in your repository: add a panel to a dashboard under deploy/observability/dashboards, and it ships with the next release.",
            command: "forge obs dashboards add-panel <slug> --panel <panel.json>",
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
      <>
        <Toolbar search={search} onSearch={onToolbar} owners={owners} filtered={filtered} apps={appFilter ? apps : undefined} appFilter={appFilter} onClear={clearFilters} />
        {shown.length === 0 ? (
          <Card className="mt-4 py-8 text-center text-sm text-ink-muted" data-testid="dashboards-no-match">
            No dashboards match.{" "}
            <button type="button" className={INK_LINK} onClick={clearFilters}>
              Clear the filters
            </button>
            {orgWide ? "." : " or start from a template below."}
          </Card>
        ) : (
          <>
            {starred.length > 0 && (
              <Section title="Starred" meta={`${starred.length} · the first ${NAV_STARRED} also sit in the nav`}>
                <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3" data-testid="dashboards-starred">
                  {starred.map((d) => (
                    <StarredCard key={rowKey(d)} summary={d} scope={scopeOf(d.ref.projectId)} href={hrefs.dashboard(d.ref)} app={orgWide ? d.ref.projectId : undefined} onStar={() => toggleStar(d)} />
                  ))}
                </div>
              </Section>
            )}
            {grouped.map(({ group, dashboards }) => (
              <GroupSection key={group} group={group} count={dashboards.length} open={!collapsed.has(group)} onToggle={() => toggleGroup(group)}>
                <Card padding="none" className="overflow-hidden">
                  <DataTable<DashRow>
                    flush
                    stacked
                    ariaLabel={`${GROUP_LABELS[group]} dashboards`}
                    columns={columns}
                    data={dashboards.map((d) => ({ d }))}
                    getRowKey={({ d }) => rowKey(d)}
                    getRowHref={({ d }) => hrefs.dashboard(d.ref) || undefined}
                    getRowLabel={({ d }) => d.title}
                  />
                </Card>
              </GroupSection>
            ))}
          </>
        )}
      </>
    );
  }

  const ready = !index.isLoading && !org.isLoading && !index.error && !(!orgId && org.error);
  const body = hub ? (
    <Tabs
      ariaLabel="Dashboards views"
      activeTab={view}
      onChange={(tab) => onSearch(tab === "all" ? { view: "all" } : { ...NO_FILTERS, view: undefined })}
      tabs={[
        { id: "overview", label: "Overview" },
        { id: "all", label: "All dashboards", badge: index.data ? all.length : undefined },
      ]}
    >
      {(active) =>
        active === "overview" ? (
          !ready ? (
            list
          ) : (
            <DashboardsHub
              project={project!}
              env={env}
              environments={environments}
              dashboards={all}
              hrefs={{ ...hub, dashboard: hrefs.dashboard }}
              onOpenGroup={openGroup}
              onOpenAll={() => onSearch({ view: "all" })}
              empty={all.length === 0 ? empty : undefined}
            />
          )
        ) : (
          list
        )
      }
    </Tabs>
  ) : (
    list
  );

  return (
    <div className="space-y-2" data-testid="dashboards" data-view={view} data-state={index.isLoading ? "loading" : index.error ? "error" : "ready"}>
      {header}
      <div className="pt-4">{body}</div>
      {/* A list-view affordance (the Overview is about what is happening now), unless there is nothing yet to list. */}
      {!orgWide && (view === "all" || all.length === 0) && !index.isLoading && !index.error && gallery}
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

function Section({ title, meta, children }: { title: string; meta?: string; children: ReactNode }) {
  return (
    <section className="pt-8 first:pt-4">
      <div className="mb-3 flex items-baseline gap-2">
        <h2 className="text-sm font-medium text-ink">{title}</h2>
        {meta && <span className="text-xs text-ink-muted">{meta}</span>}
      </div>
      {children}
    </section>
  );
}

/** One group of the list: a heading that folds it, its count, and what it holds. */
function GroupSection({ group, count, open, onToggle, children }: { group: DashboardGroup; count: number; open: boolean; onToggle: () => void; children: ReactNode }) {
  const bodyId = `dashboards-group-${group}-body`;
  return (
    <section className="pt-8 first:pt-4" data-testid={`dashboards-group-${group}`} data-open={open}>
      <h2 className="mb-3">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={bodyId}
          onClick={onToggle}
          className="group flex items-baseline gap-2 rounded-sm text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          data-testid={`dashboards-group-${group}-toggle`}
        >
          <ChevronRight className={cn("h-3.5 w-3.5 shrink-0 self-center text-ink-muted transition-transform motion-reduce:transition-none", open && "rotate-90")} aria-hidden="true" />
          <span className="text-sm font-medium text-ink">{GROUP_LABELS[group]}</span>
          <span className="text-xs tabular-nums text-ink-muted" data-testid={`dashboards-group-${group}-count`}>
            {count}
          </span>
          <span className="hidden text-xs text-ink-muted group-hover:text-ink sm:inline">· {GROUP_ABOUT[group]}</span>
        </button>
      </h2>
      <div id={bodyId} hidden={!open}>
        {children}
      </div>
    </section>
  );
}

/** A row's name: the title, then — small — the Forge workload it is about and its tags. */
function NameCell({ d }: { d: DashboardSummary }) {
  const workload = workloadOf(d);
  const tags = d.tags ?? [];
  const secondary = workload || tags.length > 0;
  return (
    <span className="flex min-w-0 flex-col gap-0.5">
      <span className="inline-flex min-w-0 items-center gap-2 font-medium">
        {d.readOnly && <Lock className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-label="read-only" />}
        <span className="truncate">{d.title}</span>
      </span>
      {secondary ? (
        <span className="inline-flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-ink-muted">
          {workload && (
            <span className="inline-flex items-center gap-1" data-testid="dashboards-row-workload">
              <span className="sr-only">Workload</span>
              <Mono value={workload} copy={false} className="text-xs text-ink-muted" />
            </span>
          )}
          {tags.slice(0, 3).map((tag) => (
            <Tag key={tag}>{tag}</Tag>
          ))}
        </span>
      ) : d.source === "builtin" ? (
        <span className="text-xs text-ink-muted">Requires cost-read permission, not billing management.</span>
      ) : d.description ? (
        <span className="line-clamp-1 text-xs text-ink-muted">{d.description}</span>
      ) : null}
    </span>
  );
}

/** Where a dashboard came from, said quietly: the word, with the release, file and scope in its tooltip. */
function SourceCell({ d, environments }: { d: DashboardSummary; environments: readonly ObsEnvironment[] }) {
  return (
    <Tooltip content={originOf(d, environments)}>
      <span className="text-xs text-ink-muted underline decoration-dotted decoration-ink-muted/40 underline-offset-2" data-testid="dashboards-row-source">
        {SOURCE_LABELS[d.source]}
      </span>
    </Tooltip>
  );
}

/** The tooltip's words: which release shipped it and from where, or who saved it and for which environments. */
function originOf(d: DashboardSummary, environments: readonly ObsEnvironment[]): string {
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

const CHIP = "inline-flex h-8 items-center gap-1 whitespace-nowrap rounded-md border px-2.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";

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
  const chip = (on: boolean) => cn(CHIP, on ? "border-ink/40 bg-ink/8 text-ink" : "border-border bg-surface text-ink-muted hover:text-ink");
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="dashboards-toolbar">
      <label className="flex h-8 min-w-72 items-center gap-1.5 rounded-md border border-border bg-surface px-2.5 text-ink-muted focus-within:ring-2 focus-within:ring-accent">
        <Search className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span className="sr-only">Search dashboards</span>
        <input
          value={search.q ?? ""}
          onChange={(event) => onSearch({ q: event.target.value || undefined })}
          placeholder="Search dashboards, workloads, tags"
          className="min-w-0 flex-1 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none"
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

/** A dashboards table's row: the whole row links to the dashboard. */
type DashRow = { d: DashboardSummary };

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

function StarredCard({ summary, scope, href, app, onStar }: { summary: DashboardSummary; scope: ObsScope | null; href: string; app?: string; onStar: () => void }) {
  const workload = workloadOf(summary);
  return (
    <Card padding="none" className="relative min-w-0 transition-colors hover:border-ink-muted" data-testid={`dashboards-card-${summary.ref.key}`}>
      <div className="flex items-center justify-between gap-2 px-4 pt-3">
        {href ? (
          <Link href={href} className="no-color min-w-0 truncate text-sm font-medium text-ink after:absolute after:inset-0 after:content-['']">
            {summary.title}
          </Link>
        ) : (
          <span className="min-w-0 truncate text-sm font-medium text-ink">{summary.title}</span>
        )}
        <span className="relative z-10">
          <StarButton starred={summary.starred} title={summary.title} onToggle={onStar} />
        </span>
      </div>
      <div className="flex min-h-5 items-center gap-1.5 px-4 text-xs text-ink-muted">
        {app !== undefined && <AppTag app={app} />}
        <span>{GROUP_LABELS[groupOf(summary)]}</span>
        {workload && (
          <>
            <span aria-hidden="true">·</span>
            <Mono value={workload} copy={false} className="truncate text-xs text-ink-muted" />
          </>
        )}
      </div>
      <div className="grid grid-cols-3 gap-3 px-4 pb-3.5 pt-3">
        {summary.previewPanels.length ? (
          summary.previewPanels.slice(0, 3).map((panel, index) => <PreviewSpark key={`${panel.id}-${index}`} panel={panel} scope={scope} />)
        ) : (
          <p className="col-span-3 text-xs text-ink-muted">{summary.panelCount} panels</p>
        )}
      </div>
    </Card>
  );
}

/** One headline panel, small: its title and its last day, drawn by the same PanelBody as the dashboard. */
function PreviewSpark({ panel, scope }: { panel: Panel; scope: ObsScope | null }) {
  const query = usePanelQuery(scope, "24h", panel, []);
  return (
    <div className="min-w-0">
      <div className="mb-1 truncate text-xs text-ink-muted" title={panel.title}>
        {panel.title}
      </div>
      <PanelBody panel={panel} query={query} compact height={28} />
    </div>
  );
}

function ListSkeleton() {
  return (
    <div className="space-y-6" aria-busy="true" aria-label="Loading dashboards" data-testid="dashboards-loading">
      <div className="h-8 w-96 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-32 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none" />
        ))}
      </div>
      <div className="space-y-px overflow-hidden rounded-lg border border-border bg-surface">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-10 animate-pulse bg-ink/5 motion-reduce:animate-none" />
        ))}
      </div>
    </div>
  );
}
