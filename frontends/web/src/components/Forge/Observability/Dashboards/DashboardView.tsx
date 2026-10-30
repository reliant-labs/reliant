// Copyright (c) 2025 Reliant Labs

/**
 * ONE DASHBOARD: its panels over the console's
 * scope, with the controls the mockup draws.
 *
 *   header       title, star, source tag; one meta line (where it came from,
 *                version and editor, panels, the environments it is about);
 *                actions — release: History, Save as copy; workspace:
 *                History, Promote to code, Edit layout, Add panel, More
 *                (save a copy, archive).
 *   variables    env (the console's own scope: changing it here changes it
 *                everywhere), service, time; Compare (previous period or
 *                previous release); auto-refresh; Share (a link carrying all
 *                of it); full screen for a wall display.
 *   grid         four columns; each panel is its forge width in quarters.
 *                Line panels carry release markers (when the dashboard
 *                declares the `releases` annotation) and alert-firing bands;
 *                a series or a row opens the drill-down into Logs, Traces and
 *                the workload.
 *   editing      a workspace dashboard's layout is edited in place — drag a
 *                panel by its handle (or move it with the buttons), set its
 *                width in quarters, remove it — and saved as one new version;
 *                a panel is added or edited in the drawer (PanelDrawer).
 *                Every save is optimistic against the version on screen: a
 *                stale one is refused and says so.
 *
 * Opening a dashboard records the view (Recently viewed, per caller). An
 * older version (`?version=`) or a release dashboard as an earlier release
 * shipped it (`?release=`) opens read-only, with the way back — and, for a
 * workspace version, Restore.
 *
 * The component knows nothing about routing: the console page (and the
 * preview harness) hand it the scope, the URL state and the hrefs.
 */

import { useCallback, useEffect, useMemo, useState, type ComponentProps, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { ArrowLeft, ArrowRight, GripVertical, History, LayoutDashboard, LayoutGrid, Maximize2, MoreHorizontal, Plus, RefreshCw, Share2, Trash2, X } from "lucide-react";

import Card from "@/components/forge-ui/card";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import Tag from "@/components/forge-ui/tag";
import {
  dashboardKeys,
  useDashboard,
  useDashboardMutation,
  useDashboardVersions,
  useStarDashboard,
} from "@/hooks/dashboard-queries";
import { useAlertHistory, useObservabilityOrg, usePanelQuery, useReleases, useServiceOverview } from "@/hooks/observability-queries";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Dashboard, ObsScope, Panel } from "@/services/controlPlane/observability/types";
import { DEFAULT_RANGE, RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { ENV_ALL, PLATFORM_SCOPE_LABEL } from "../../Console/envScope";
import { ObsChartMarkersProvider, type ChartBand, type ChartMarker } from "../charts/UPlotChart";
import { deliveryScopeOf } from "../deliveryScope";
import { formatAgo } from "../format";
import type { ObsEnvironment } from "../obsPlace";
import { ObsAsOf, ObsError } from "../ObsStates";
import { useReleaseMarkers } from "../releaseMarkers";
import { DashboardHeader } from "./DashboardHeader";
import { ViewerControls } from "./ViewerControls";
import { OrganizationCostsView } from "./OrganizationCostsView";
import { useNearViewport } from "./useNearViewport";
import { visiblePanel, viewerQuery } from "./viewerModel";
import {
  alertBands,
  COMPARE_LABELS,
  GRID_SPANS,
  movePanel,
  removePanel,
  resizePanel,
  COMPARE_MODES,
  compareTarget,
  marksReleases,
  rangeMs,
  refreshMs,
  REFRESH_INTERVALS,
  spanOf,
  type CompareMode,
  type CompareTarget,
  type DashboardSearch,
  type RefreshInterval,
} from "./dashboardModel";
import { AlertFromPanelDialog, CREATE_ALERT_LABEL } from "./AlertFromPanelDialog";
import { useAskReliant } from "../AskReliant/AskReliant";
import { PromoteDialog, SaveCopyDialog, envScopeOf, writeErrorMessage } from "./dialogs";
import { useUnmountSafeTimeout } from "@/hooks/useUnmountSafeTimeout";
import { dashboardPivot, useExploreHref } from "../Metrics/pivots";
import { drillSearches, type ExploreHrefs } from "./drill";
import { duplicatePanel, starterPanel, upsertPanel } from "./panelBuilder";
import { PanelBody, type DrillTarget } from "./PanelBody";
import { OrgEnvPicker } from "./OrgEnvPicker";
import { orgDashboardHrefs, orgExploreHref, orgQueryScope, orgScopeLabel, orgSelection } from "./orgScope";
import { PanelDrawer, type DrawerMode } from "./PanelDrawer";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, BUTTON_SMALL, ICON_BUTTON, INK_LINK, Menu, PointMenu, Segmented, SourceTag, StarButton, VariableSelect, type MenuEntry } from "./parts";

export interface DashboardHrefs extends ExploreHrefs {
  list: string;
  release: (version: string) => string;
  trace: (traceId: string) => string;
  /** One browser session (a sessions panel's row). Absent: rows are not links. */
  session?: (sessionId: string) => string;
  dashboard: (ref: DashboardRef, search?: DashboardSearch) => string;
}

export interface DashboardViewProps {
  project: string;
  dashRef: DashboardRef;
  /** The console scope: an environment name or "all". */
  env: string;
  /** The project's recorded environments (id ↔ name). */
  environments: readonly ObsEnvironment[];
  /** Every environment of the org, named `project/env`: what a `scope: "org"` dashboard reads and offers. */
  orgEnvironments?: readonly ObsEnvironment[];
  orgEnvironmentsLoading?: boolean;
  orgEnvironmentsError?: Error | null;
  onRetryOrgEnvironments?: () => void;
  search: DashboardSearch;
  onSearch: (next: Partial<DashboardSearch>) => void;
  onEnvChange: (env: string) => void;
  hrefs: DashboardHrefs;
  navigateTo: (href: string) => void;
}

const NO_MARKERS: readonly ChartMarker[] = [];

/** Release comparison on a platform dashboard: no environment, so no release. */
const PLATFORM_NO_RELEASE: CompareTarget = {
  mode: "unavailable",
  reason: "A platform dashboard belongs to no environment, so there is no release to compare with.",
};

const NO_ORG_ENVS: readonly ObsEnvironment[] = [];

export function DashboardView({ project, dashRef, env: consoleEnv, environments: projectEnvironments, orgEnvironments = NO_ORG_ENVS, orgEnvironmentsLoading = false, orgEnvironmentsError = null, onRetryOrgEnvironments, search, onSearch, onEnvChange, hrefs: consoleHrefs, navigateTo }: DashboardViewProps) {
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  const range: RangePreset = search.range ?? DEFAULT_RANGE;
  const compare: CompareMode = search.compare ?? "off";
  const refresh: RefreshInterval = search.refresh ?? "off";
  const consoleExplore = useExploreHref();

  const doc = useDashboard(orgId, dashRef, dashRef.kind === "builtin" ? undefined : search.version, dashRef.kind === "builtin" ? undefined : search.release);
  const spec = doc.data?.spec;
  const summary = doc.data?.summary;

  // A platform dashboard's signals belong to no environment: it reads them
  // platform-wide whatever environment the top bar names.
  const platform = spec?.scope === "platform";
  // An org dashboard reads every project's environments: it names no project,
  // lists the org's environments as project/env, and narrows by its own picker
  // (the top bar's control is the route project's).
  const orgScoped = spec?.scope === "org";
  const orgRosterBlocked = orgScoped && (orgEnvironmentsLoading || !!orgEnvironmentsError);
  const orgPick = useMemo(() => orgSelection(orgEnvironments, search.orgEnvs), [orgEnvironments, search.orgEnvs]);
  const env = orgScoped ? orgPick.env : consoleEnv;
  const environments = orgScoped ? orgEnvironments : projectEnvironments;
  const envId = orgScoped ? (orgPick.ids.length === 1 ? orgPick.ids[0] : null) : env === ENV_ALL ? null : (environments.find((candidate) => candidate.name === env)?.id ?? null);
  const unrecorded = !orgScoped && !platform && env !== ENV_ALL && !envId;
  const envLabel = platform ? PLATFORM_SCOPE_LABEL : orgScoped ? orgScopeLabel(orgPick) : env === ENV_ALL ? "All environments" : env;
  // Its drill-downs and explorers read the same org-wide scope: the org's
  // pages on the picked environments, not the route project's console.
  const hrefs = useMemo(() => (orgScoped ? orgDashboardHrefs(consoleHrefs, orgPick) : consoleHrefs), [orgScoped, consoleHrefs, orgPick]);
  const exploreLink = useMemo(() => (orgScoped ? orgExploreHref(orgPick) : consoleExplore), [orgScoped, orgPick, consoleExplore]);
  const scope = useMemo<ObsScope | null>(() => {
    if (!orgId || unrecorded || orgRosterBlocked || dashRef.kind === "builtin") return null;
    if (platform) return { orgId, envIds: [], filters: [], platformScope: true };
    return orgScoped ? orgQueryScope(orgId, orgPick) : { orgId, projectId: project, envIds: envId ? [envId] : [], filters: [] };
  }, [orgId, project, envId, unrecorded, orgRosterBlocked, platform, orgScoped, orgPick, dashRef.kind]);

  // Recently viewed: once per dashboard opened, and quietly — a view that
  // could not be recorded changes nothing the reader can see.
  const refKey = `${dashRef.kind}/${dashRef.projectId}/${dashRef.key}`;
  useEffect(() => {
    if (!orgId) return;
    void api
      .recordDashboardView(orgId, dashRef)
      .then(() => queryClient.invalidateQueries({ queryKey: [...dashboardKeys.all(orgId), "index"] }))
      .catch(() => undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orgId, refKey]);

  const star = useStarDashboard(orgId);
  const [starredHere, setStarredHere] = useState<boolean | null>(null);
  useEffect(() => setStarredHere(null), [summary?.starred]);
  const starred = starredHere ?? !!summary?.starred;

  // ── Markers, bands, comparison ──
  const marks = !!spec && !platform && marksReleases(spec) && search.releases !== false;
  const releases = useReleaseMarkers(scope, range, environments, marks);
  const delivery = useMemo(() => (scope ? deliveryScopeOf(scope, environments) : null), [scope, environments]);
  // A platform dashboard belongs to no environment: the environment the top
  // bar names (hidden for it) must not lend it that environment's alert bands
  // or its last release to compare with. Previous-period comparison stays.
  const promotions = useReleases(delivery, "days:30", () => ({ startMs: Date.now() - 30 * 86_400_000, endMs: Date.now() }), { enabled: !!spec && !!envId && !platform && !orgRosterBlocked && dashRef.kind !== "builtin" });
  // Both wait for the document: until it loads, a platform dashboard is not yet known to be one.
  const history = useAlertHistory(dashRef.kind === "builtin" || !spec || platform || orgRosterBlocked ? null : orgId, range, envId ?? undefined);
  const scopeEnvIds = useMemo(
    () => (envId ? [envId] : orgScoped && orgPick.ids.length ? orgPick.ids : environments.map((candidate) => candidate.id)),
    [envId, environments, orgScoped, orgPick.ids]
  );
  const bands = useMemo(
    () => platform || search.alerts === false ? [] : alertBands(history.data ?? [], scopeEnvIds, Date.now(), search.service),
    [platform, history.data, scopeEnvIds, search.service, search.alerts]
  );
  const now = Date.now();
  const releaseCompare = platform ? PLATFORM_NO_RELEASE : compareTarget("release", range, promotions.data?.promotions ?? [], envId, now);
  const target = platform && compare === "release" ? PLATFORM_NO_RELEASE : compareTarget(compare, range, promotions.data?.promotions ?? [], envId, now);

  // ── Drill-down ──
  const [drill, setDrill] = useState<{ target: DrillTarget; panel: Panel; at: { x: number; y: number } } | null>(null);
  const closeDrill = useCallback(() => setDrill(null), []);

  // ── Dialogs ──
  const [dialog, setDialog] = useState<"copy" | "promote" | "archive" | null>(null);
  const copy = useDashboardMutation(orgId, (a, org, input: { title: string; scope: "scope" | "all" }) =>
    a.saveDashboardAsCopy({
      orgId: org,
      from: dashRef,
      projectId: project,
      title: input.title,
      envScope: envScopeOf(input.scope, env, (name) => environments.find((candidate) => candidate.name === name)?.id),
    })
  );
  const promote = useDashboardMutation(orgId, (a, org, version: number | undefined) => a.promoteDashboardToCode(org, dashRef, version));
  const archive = useDashboardMutation(orgId, (a, org) => a.archiveDashboard(org, dashRef));
  const restore = useDashboardMutation(orgId, (a, org, input: { version: number; base: number }) => a.restoreDashboardVersion(org, dashRef, input.version, input.base));
  const save = useDashboardMutation(orgId, (a, org, input: { spec: Dashboard; base: number; message: string }) =>
    a.saveDashboard({ orgId: org, projectId: project, slug: dashRef.key, spec: input.spec, baseVersion: input.base, message: input.message })
  );

  // ── Editing ──
  const [layout, setLayout] = useState<Dashboard | null>(null);
  const [drawer, setDrawer] = useState<{ mode: DrawerMode; panel: Panel } | null>(null);
  const [alertPanel, setAlertPanel] = useState<Panel | null>(null);
  const { ask: askReliant, dialog: askReliantDialog } = useAskReliant({ forgeProject: project, envChoices: environments.map((candidate) => candidate.name) });
  const [removing, setRemoving] = useState<Panel | null>(null);
  const [dragging, setDragging] = useState<string | null>(null);

  // ── Full screen ──
  const tv = !!search.tv;
  const exitTv = useCallback(() => onSearch({ tv: undefined }), [onSearch]);
  const tvLayer = useEscapeLayer(tv, exitTv);

  if (doc.isLoading || org.isLoading || (orgScoped && orgEnvironmentsLoading)) return <DashboardSkeleton />;
  if (orgScoped && orgEnvironmentsError) return <ObsError error={orgEnvironmentsError} subject="organization environments" onRetry={onRetryOrgEnvironments} />;
  if (!orgId && org.error) return <ObsError error={org.error} subject={project} onRetry={() => void org.refetch()} />;
  if (doc.error || !spec || !summary) {
    const missing = doc.error instanceof ConnectError && doc.error.code === Code.NotFound;
    return missing ? (
      <div className="space-y-6" data-testid="dashboard-missing">
        <EmptyState
          icon={<LayoutDashboard className="h-6 w-6" aria-hidden="true" />}
          title="This dashboard isn't here"
          description={
            dashRef.kind === "release"
              ? "No release running in this project's environments carries it. It may have been renamed or removed from the repo."
              : "It was archived, or the link names one that never existed."
          }
        />
        <Link href={hrefs.list} className={INK_LINK}>
          All dashboards
        </Link>
      </div>
    ) : (
      <ObsError error={doc.error} subject={project} onRetry={() => void doc.refetch()} retrying={doc.isFetching} />
    );
  }

  if (summary.viewKind === "organization-costs") return <OrganizationCostsView orgId={orgId!} />;

  const currentVersion = summary.workspace?.version ?? 0;
  const viewingOld = !!search.version && search.version !== currentVersion;
  const editable = !summary.readOnly && !!doc.data?.canWrite && !search.version && !search.release;
  const writeVersion = (next: Dashboard, message: string, done?: () => void) =>
    save.mutate({ spec: next, base: currentVersion, message }, { onSuccess: () => done?.() });
  const shown = layout ?? spec;
  const workspaceScope = summary.workspace?.envIds.length
    ? summary.workspace.envIds.map((id) => environments.find((candidate) => candidate.id === id)?.name ?? id).join(", ")
    : "all environments";
  const services = spec.filters.some((filter) => filter.field === "service.name") && search.service ? [search.service] : [];
  const refreshEvery = refreshMs(refresh);
  const processForPanel = (panel: Panel) => viewerQuery(spec, panel, search).services[0];
  const exploreHref = (panel: Panel): string | null =>
    exploreLink?.(dashboardPivot(viewerQuery(spec, panel, search).panel, { service: processForPanel(panel), compare }), { range: search.range ? range : undefined }) ?? null;
  const links = {
    traceHref: hrefs.trace,
    sessionHref: hrefs.session,
    searchHref: (panel: Panel) => {
      const { logs, traces } = drillSearches(viewerQuery(spec, panel, search).panel, {}, { range, service: processForPanel(panel) });
      return panel.signal === "logs" ? hrefs.logs(logs) : hrefs.traces(traces);
    },
  };

  const grid = (
    <ObsChartMarkersProvider markers={marks ? releases.markers : NO_MARKERS}>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4" data-testid="dashboard-grid" data-editing={layout ? "true" : undefined}>
        {shown.panels.filter((panel) => !!layout || visiblePanel(panel, search)).map((panel, index) => (
          <DashboardPanelCard
            key={panel.id}
            panel={panel}
            queryPanel={viewerQuery(spec, panel, search).panel}
            layout={
              layout
                ? {
                    index,
                    count: layout.panels.length,
                    dragging: dragging === panel.id,
                    onMove: (to) => setLayout(movePanel(layout, panel.id, to)),
                    onResize: (span) => setLayout(resizePanel(layout, panel.id, span)),
                    onRemove: () => setLayout(removePanel(layout, panel.id)),
                    onDragStart: () => setDragging(panel.id),
                    onDragEnd: () => setDragging(null),
                    onDropOn: () => {
                      if (dragging && dragging !== panel.id) setLayout(movePanel(layout, dragging, index));
                      setDragging(null);
                    },
                  }
                : undefined
            }
            menu={[
              ...(editable && !layout
                ? [
                    { label: "Edit panel", onSelect: () => setDrawer({ mode: "edit" as const, panel }), testId: `dashboard-panel-edit-${panel.id}` },
                    { label: "Duplicate", onSelect: () => writeVersion(duplicatePanel(spec, panel.id), `Duplicated ${panel.title}`), testId: `dashboard-panel-duplicate-${panel.id}` },
                  ]
                : []),
              ...(!editable ? [{ label: "View spec", onSelect: () => setDrawer({ mode: "view" as const, panel }), testId: `dashboard-panel-view-${panel.id}` }] : []),
              ...(exploreHref(panel)
                ? [{ label: "Explore in Metrics", detail: "this query, editable", href: exploreHref(panel)!, testId: `dashboard-panel-explore-${panel.id}` }]
                : []),
              { label: panel.signal === "logs" ? "Explore in Logs" : "Explore in Traces", detail: "with this panel's service", href: links.searchHref(panel) },
              { label: CREATE_ALERT_LABEL, detail: "as KCL", onSelect: () => setAlertPanel(panel), separated: true, testId: `dashboard-panel-alert-${panel.id}` },
              {
                label: "Ask Reliant",
                detail: "in a new chat",
                onSelect: () => void askReliant({ kind: "panel", panel, dashboard: summary.title, window: range, env: env === ENV_ALL ? null : env }),
                testId: `dashboard-panel-ask-${panel.id}`,
              },
              ...(editable && !layout
                ? [{ label: "Remove panel", onSelect: () => setRemoving(panel), separated: true, testId: `dashboard-panel-remove-${panel.id}` }]
                : []),
            ]}
            scope={scope}
            range={range}
            services={viewerQuery(spec, panel, search).services}
            refreshEvery={refreshEvery}
            target={target}
            bands={bands}
            links={{ ...links, onDrill: (drillTarget, at) => setDrill({ target: drillTarget, panel: viewerQuery(spec, panel, search).panel, at }) }}
          />
        ))}
      </div>
      {!layout && shown.panels.every((panel) => !visiblePanel(panel, search)) && (
        <p className="rounded-lg border border-dashed border-border bg-surface-sunken p-4 text-sm text-ink-muted" role="status">
          All metrics are hidden in this view. Use Metrics to show them again; the saved dashboard is unchanged.
        </p>
      )}
    </ObsChartMarkersProvider>
  );

  const variables = (
    <VariablesBar
      scope={scope}
      env={env}
      environments={environments}
      range={range}
      service={search.service}
      hasProcessFilter={spec.filters.some((filter) => filter.field === "service.name")}
      compare={compare}
      target={target}
      releaseCompare={releaseCompare}
      refresh={refresh}
      onEnvChange={onEnvChange}
      onSearch={onSearch}
      tv={tv}
      orgPicker={orgScoped ? <OrgEnvPicker environments={orgEnvironments} selection={orgPick} onChange={(names) => onSearch({ orgEnvs: names.length ? names : undefined })} /> : undefined}
    />
  );

  if (tv) {
    return (
      <div
        {...tvLayer}
        className="fixed inset-0 z-[55] overflow-y-auto bg-surface-muted px-6 pb-10 pt-4"
        role="region"
        aria-label={`${summary.title}, full screen`}
        data-testid="dashboard-tv"
      >
        <div className="mb-4 flex items-center justify-between gap-4">
          <div className="min-w-0">
            <h1 className="truncate text-xl font-semibold tracking-tight text-ink">{summary.title}</h1>
            <p className="text-sm text-ink-muted">
              {envLabel} · {RANGE_LABELS[range].toLowerCase()}
              {refreshEvery ? ` · refreshes every ${refresh}` : ""}
            </p>
          </div>
          <button type="button" className={BUTTON_SECONDARY} onClick={exitTv} data-testid="dashboard-tv-exit">
            <X className="h-3.5 w-3.5" aria-hidden="true" />
            Exit full screen
          </button>
        </div>
        {grid}
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="dashboard-view" data-kind={dashRef.kind}>
      <DashboardHeader
        testId="dashboard-header"
        title={summary.title}
        titleAccessory={
          <>
            <StarButton
              starred={starred}
              title={summary.title}
              onToggle={() => {
                setStarredHere(!starred);
                star.mutate({ ref: dashRef, starred: !starred }, { onError: () => setStarredHere(null) });
              }}
              testId="dashboard-star"
            />
            <SourceTag source={summary.source} />
            {viewingOld && <Tag tone="warning">v{search.version}</Tag>}
          </>
        }
        meta={<DashboardMeta summary={summary} panels={spec.panels.length} environments={environments} hrefs={hrefs} marks={marks} hasBands={bands.length > 0} />}
        actions={
          <>
            <HistoryMenu orgId={orgId} dashRef={dashRef} summary={summary} hrefs={hrefs} search={search} />
            {summary.readOnly ? (
              <button
                type="button"
                className={BUTTON_PRIMARY}
                onClick={() => setDialog("copy")}
                disabled={!doc.data?.canWrite}
                title={doc.data?.canWrite ? undefined : "Only organization admins can create dashboards."}
                data-testid="dashboard-save-copy"
              >
                Save as copy
              </button>
            ) : (
              <>
                <button
                  type="button"
                  className={BUTTON_SECONDARY}
                  onClick={() => {
                    setDialog("promote");
                    promote.mutate(search.version);
                  }}
                  disabled={!doc.data?.canWrite}
                  title={doc.data?.canWrite ? undefined : "Only organization admins can promote dashboards."}
                  data-testid="dashboard-promote-open"
                >
                  Promote to code
                </button>
                {editable && (
                  <>
                    <button type="button" className={BUTTON_SECONDARY} onClick={() => setLayout(spec)} disabled={!!layout} data-testid="dashboard-edit-layout">
                      <LayoutGrid className="h-3.5 w-3.5" aria-hidden="true" />
                      Edit layout
                    </button>
                    <button
                      type="button"
                      className={BUTTON_PRIMARY}
                      onClick={() => setDrawer({ mode: "add", panel: starterPanel(spec) })}
                      disabled={!!layout}
                      data-testid="dashboard-add-panel"
                    >
                      <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                      Add panel
                    </button>
                  </>
                )}
                <Menu
                  label="More actions"
                  trigger={<MoreHorizontal className="h-4 w-4" aria-hidden="true" />}
                  triggerClassName={cn(ICON_BUTTON, "h-8 w-8 border border-border bg-surface")}
                  testId="dashboard-more"
                  entries={[
                    { label: "Save as copy", onSelect: () => setDialog("copy"), disabled: !doc.data?.canWrite, testId: "dashboard-more-copy" },
                    {
                      label: "Archive dashboard",
                      detail: "versions are kept",
                      onSelect: () => setDialog("archive"),
                      disabled: !doc.data?.canWrite,
                      separated: true,
                      testId: "dashboard-more-archive",
                    },
                  ]}
                />
              </>
            )}
          </>
        }
      />

      {summary.readOnly && (
        <p className="flex items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2 text-sm text-ink-muted" data-testid="dashboard-readonly-note">
          {summary.source === "repo" ? (
            <>
              Declared in your repo
              {summary.release?.filePath && (
                <>
                  {" "}
                  at <Mono value={summary.release.filePath} copy={false} />
                </>
              )}
              : edit it there, or save a copy to change it here.
            </>
          ) : (
            <>Generated by forge from your services on every release: save a copy to change it.</>
          )}
        </p>
      )}

      {viewingOld && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-surface px-3 py-2 text-sm" role="status" data-testid="dashboard-old-version">
          <span className="text-ink-muted">
            You are looking at <span className="font-medium text-ink">v{search.version}</span>. The current version is v{currentVersion}.
          </span>
          {doc.data?.canWrite && (
            <button
              type="button"
              className={BUTTON_SMALL}
              disabled={restore.isPending}
              onClick={() =>
                restore.mutate(
                  { version: search.version!, base: currentVersion },
                  { onSuccess: () => onSearch({ version: undefined }) }
                )
              }
              data-testid="dashboard-restore"
            >
              {restore.isPending ? "Restoring…" : `Restore v${search.version} as v${currentVersion + 1}`}
            </button>
          )}
          <Link href={hrefs.dashboard(dashRef, { ...search, version: undefined })} className={INK_LINK}>
            Back to the current version
          </Link>
          {!!restore.error && <span className="w-full text-xs text-danger-ink">{writeErrorMessage(restore.error)}</span>}
        </div>
      )}

      {search.release && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-surface px-3 py-2 text-sm" role="status" data-testid="dashboard-old-release">
          <span className="text-ink-muted">
            As it shipped in <span className="font-medium text-ink">{search.release}</span>.
          </span>
          <Link href={hrefs.dashboard(dashRef, { ...search, release: undefined })} className={INK_LINK}>
            Back to the current release
          </Link>
        </div>
      )}

      {variables}
      {!layout && <ViewerControls dashboard={spec} scope={scope} range={range} search={search} onSearch={onSearch} />}

      {layout && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-surface px-3 py-2 text-sm" role="status" data-testid="dashboard-layout-bar">
          <LayoutGrid className="h-4 w-4 text-ink-muted" aria-hidden="true" />
          <span className="flex-1 text-ink-muted">
            Editing the layout for {workspaceScope}: drag a panel by its handle (or use its arrows) to move it, and set its width from one to four columns.
          </span>
          {!!save.error && <span className="w-full text-xs text-danger-ink">{writeErrorMessage(save.error)}</span>}
          <button
            type="button"
            className={BUTTON_SECONDARY}
            onClick={() => {
              save.reset();
              setLayout(null);
            }}
          >
            Cancel
          </button>
          <button
            type="button"
            className={BUTTON_PRIMARY}
            disabled={save.isPending || layout.panels.length === 0}
            title={layout.panels.length === 0 ? "A dashboard keeps at least one panel." : undefined}
            onClick={() => writeVersion(layout, "Layout", () => setLayout(null))}
            data-testid="dashboard-layout-save"
          >
            {save.isPending ? "Saving…" : `Save layout v${currentVersion} → v${currentVersion + 1}`}
          </button>
        </div>
      )}
      {!layout && !drawer && !!save.error && (
        <p role="alert" className="rounded-lg border border-border bg-surface px-3 py-2 text-sm text-danger-ink" data-testid="dashboard-save-error">
          {writeErrorMessage(save.error)}
        </p>
      )}

      {unrecorded ? (
        <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="dashboard-unrecorded-env">
          Reliant has no record of <span className="font-medium text-ink">{env}</span>, so no telemetry is filed under it. Pick another environment.
        </p>
      ) : (
        grid
      )}

      {drill && (
        <DrillMenu drill={drill} range={range} service={processForPanel(drill.panel)} hrefs={hrefs} env={env} onClose={closeDrill} />
      )}

      {drawer && (
        <PanelDrawer
          mode={drawer.mode}
          panel={drawer.panel}
          dashboardTitle={summary.title}
          scope={scope}
          range={range}
          services={services}
          environments={environments}
          env={env}
          scopeLabel={`${platform ? PLATFORM_SCOPE_LABEL : orgScoped ? envLabel : env === ENV_ALL ? "all environments" : env}${search.service ? ` · ${search.service}` : ""}`}
          version={currentVersion}
          envScope={workspaceScope}
          saving={save.isPending}
          error={
            save.error ? (
              <p role="alert" className="text-xs text-danger-ink" data-testid="panel-drawer-error">
                {writeErrorMessage(save.error)}
              </p>
            ) : null
          }
          onCancel={() => {
            save.reset();
            setDrawer(null);
          }}
          onSave={(panel) =>
            writeVersion(upsertPanel(spec, panel), `${drawer.mode === "add" ? "Added" : "Edited"} ${panel.title}`, () => setDrawer(null))
          }
        />
      )}
      {askReliantDialog}
      {alertPanel && (
        <AlertFromPanelDialog panel={alertPanel} dashboardTitle={summary.title} environments={environments} env={env} services={services} onClose={() => setAlertPanel(null)} />
      )}
      <ConfirmationDialog
        open={!!removing}
        title={`Remove ${removing?.title ?? "this panel"}?`}
        description={`Saves v${currentVersion + 1} without it. v${currentVersion} keeps it, and History can restore it.`}
        confirmLabel="Remove panel"
        loading={save.isPending}
        onCancel={() => setRemoving(null)}
        onConfirm={() => removing && writeVersion(removePanel(spec, removing.id), `Removed ${removing.title}`, () => setRemoving(null))}
      />

      {dialog === "copy" && (
        <SaveCopyDialog
          sourceTitle={summary.title}
          what={summary.readOnly ? (summary.source === "repo" ? "repo dashboard" : "generated dashboard") : "dashboard"}
          env={env}
          pending={copy.isPending}
          error={copy.error}
          onCancel={() => {
            copy.reset();
            setDialog(null);
          }}
          onSave={(input) =>
            copy.mutate(input, {
              onSuccess: (saved) => {
                setDialog(null);
                navigateTo(hrefs.dashboard(saved.summary.ref, { ...search, version: undefined, release: undefined }));
              },
            })
          }
        />
      )}
      {dialog === "promote" && (
        <PromoteDialog
          title={summary.title}
          result={promote.data}
          pending={promote.isPending}
          error={promote.error}
          onRetry={() => promote.mutate(search.version)}
          onClose={() => {
            promote.reset();
            setDialog(null);
          }}
        />
      )}
      <ConfirmationDialog
        open={dialog === "archive"}
        title={`Archive ${summary.title}?`}
        description="It leaves the Dashboards list and frees its name. Its versions are kept, and a dashboard promoted to code is not affected."
        confirmLabel="Archive dashboard"
        loading={archive.isPending}
        onCancel={() => setDialog(null)}
        onConfirm={() =>
          archive.mutate(undefined, {
            onSuccess: () => {
              setDialog(null);
              navigateTo(hrefs.list);
            },
          })
        }
      />
    </div>
  );
}

// ── Header pieces ───────────────────────────────────────────────────────────

function DashboardMeta({
  summary,
  panels,
  environments,
  hrefs,
  marks,
  hasBands,
}: {
  summary: DashboardSummary;
  panels: number;
  environments: readonly ObsEnvironment[];
  hrefs: DashboardHrefs;
  marks: boolean;
  hasBands: boolean;
}) {
  const nameOf = (id: string) => environments.find((candidate) => candidate.id === id)?.name ?? id;
  const parts: ReactNode[] = [];
  if (summary.release) {
    parts.push(
      <span key="shipped">
        Shipped in{" "}
        {summary.release.releaseVersion ? (
          <Link href={hrefs.release(summary.release.releaseVersion)} className={INK_LINK}>
            {summary.release.releaseVersion}
          </Link>
        ) : (
          "a release"
        )}
      </span>
    );
    if (summary.release.envIds.length) parts.push(<span key="where">running in {summary.release.envIds.map(nameOf).join(", ")}</span>);
  }
  if (summary.workspace) {
    const editor = summary.workspace.updatedBy ?? summary.workspace.createdBy;
    parts.push(
      <span key="version">
        v{summary.workspace.version}
        {summary.workspace.updatedAtMs ? ` · edited ${formatAgo(summary.workspace.updatedAtMs)}` : ""}
        {editor ? ` by ${editor.name || editor.email}` : ""}
      </span>
    );
    parts.push(
      <Tag key="scope" title="The environments this dashboard is about">
        {summary.workspace.envIds.length ? summary.workspace.envIds.map(nameOf).join(", ") : "all environments"}
      </Tag>
    );
  }
  parts.push(<span key="panels">{panels === 1 ? "1 panel" : `${panels} panels`}</span>);
  if (marks || hasBands) {
    parts.push(
      <span key="legend">
        {[marks ? "dashed lines are releases" : "", hasBands ? "red bands are alerts firing" : ""].filter(Boolean).join("; ").replace(/^./, (c) => c.toUpperCase())}
      </span>
    );
  }
  return <>{parts}</>;
}

function HistoryMenu({
  orgId,
  dashRef,
  summary,
  hrefs,
  search,
}: {
  orgId: string | null;
  dashRef: DashboardRef;
  summary: DashboardSummary;
  hrefs: DashboardHrefs;
  search: DashboardSearch;
}) {
  const versions = useDashboardVersions(orgId, dashRef, true);
  const release = dashRef.kind === "release";
  const entries: MenuEntry[] = versions.isLoading
    ? [{ label: "Loading…", disabled: true }]
    : versions.error
      ? [{ label: "History could not be read", disabled: true }]
      : (versions.data ?? []).slice(0, 20).map((v) => {
          const who = v.author ? v.author.name || v.author.email : "";
          const when = v.createdAtMs ? formatAgo(v.createdAtMs) : "";
          const detail = [who, when].filter(Boolean).join(" · ");
          return release
            ? {
                label: v.releaseVersion || "a release",
                detail,
                checked: (search.release ?? "") === (v.current ? "" : v.releaseVersion),
                href: hrefs.dashboard(dashRef, { ...search, release: v.current ? undefined : v.releaseVersion }),
              }
            : {
                label: `v${v.version}${v.current ? " · current" : ""}${v.message ? ` — ${v.message}` : ""}`,
                detail,
                checked: (search.version ?? summary.workspace?.version) === v.version,
                href: hrefs.dashboard(dashRef, { ...search, version: v.current ? undefined : v.version }),
              };
        });
  if (!versions.isLoading && !versions.error && entries.length === 0) entries.push({ label: "No history yet", disabled: true });
  return (
    <Menu
      label="History"
      heading={release ? "Changes come from releases" : "Versions"}
      trigger={
        <>
          <History className="h-3.5 w-3.5" aria-hidden="true" />
          History
        </>
      }
      entries={entries}
      testId="dashboard-history"
    />
  );
}

// ── Variables bar ───────────────────────────────────────────────────────────

function VariablesBar({
  scope,
  env,
  environments,
  range,
  service,
  hasProcessFilter,
  compare,
  target,
  releaseCompare,
  refresh,
  onEnvChange,
  onSearch,
  tv,
  orgPicker,
}: {
  scope: ObsScope | null;
  env: string;
  environments: readonly ObsEnvironment[];
  range: RangePreset;
  service: string | undefined;
  hasProcessFilter: boolean;
  compare: CompareMode;
  target: CompareTarget;
  releaseCompare: CompareTarget;
  refresh: RefreshInterval;
  onEnvChange: (env: string) => void;
  onSearch: (next: Partial<DashboardSearch>) => void;
  tv: boolean;
  /** An org dashboard's own environment picker; it replaces the console's one. */
  orgPicker?: ReactNode;
}) {
  const overview = useServiceOverview(scope, range);
  const names = useMemo(() => {
    const known = (overview.data?.services ?? []).filter((s) => !s.operatorData).map((s) => s.name);
    if (service && !known.includes(service)) known.unshift(service);
    return [...new Set(known)];
  }, [overview.data, service]);
  const [copied, setCopied] = useState(false);
  const later = useUnmountSafeTimeout();

  const share = () => {
    const url = new URL(window.location.href);
    url.searchParams.delete("tv");
    void navigator.clipboard?.writeText(url.toString()).then(() => {
      setCopied(true);
      later(() => setCopied(false), 2000);
    });
  };

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-surface p-2" data-testid="dashboard-variables">
      {/* The console's top bar is the one environment selector. Full screen
          hides the top bar, so only there does the dashboard offer its own. */}
      {orgPicker}
      {tv && !orgPicker && (
        <VariableSelect
          id="dashboard-var-env"
          label="env"
          value={env}
          options={[...environments.map((candidate) => ({ value: candidate.name, label: candidate.name })), { value: ENV_ALL, label: "All" }]}
          onChange={onEnvChange}
        />
      )}
      {hasProcessFilter && <VariableSelect
        id="dashboard-var-service"
        label="process"
        value={service ?? ""}
        options={[{ value: "", label: "all" }, ...names.map((name) => ({ value: name, label: name }))]}
        onChange={(value) => onSearch({ service: value || undefined })}
      />}
      <VariableSelect
        id="dashboard-var-range"
        label="time"
        value={range}
        options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
        onChange={(value) => onSearch({ range: value === DEFAULT_RANGE ? undefined : (value as RangePreset) })}
      />
      <Segmented
        label="Compare"
        value={compare}
        options={COMPARE_MODES.map((mode) => ({ value: mode, label: COMPARE_LABELS[mode] }))}
        onChange={(mode) => onSearch({ compare: mode === "off" ? undefined : mode })}
        disabledReason={releaseCompare.mode === "unavailable" ? { release: releaseCompare.reason } : undefined}
        testId="dashboard-compare"
      />
      {target.mode === "unavailable" && (
        <span className="text-xs text-ink-muted" role="status" data-testid="dashboard-compare-unavailable">
          {target.reason}
        </span>
      )}
      {target.mode === "release" && (
        <span className="text-xs text-ink-muted" data-testid="dashboard-compare-release">
          dashed: the {formatSpan(rangeMs(range))} before {target.promotion.version || "the release"} reached {target.promotion.envName}
        </span>
      )}
      <span className="flex-1" />
      <Menu
        label="Auto-refresh"
        trigger={
          <>
            <RefreshCw className="h-3 w-3" aria-hidden="true" />
            {refresh === "off" ? "Off" : refresh}
          </>
        }
        triggerClassName={BUTTON_SMALL}
        heading="Auto-refresh"
        entries={REFRESH_INTERVALS.map((interval) => ({
          label: interval === "off" ? "Off" : `Every ${interval}`,
          checked: refresh === interval,
          onSelect: () => onSearch({ refresh: interval === "off" ? undefined : interval }),
        }))}
        testId="dashboard-refresh"
      />
      {!tv && (
        <>
          <button type="button" className={BUTTON_SMALL} onClick={share} title="Copy a link with the env, service, time, compare and refresh" data-testid="dashboard-share">
            <Share2 className="h-3 w-3" aria-hidden="true" />
            {copied ? "Link copied" : "Share"}
          </button>
          <button
            type="button"
            className={BUTTON_SMALL}
            onClick={() => onSearch({ tv: true })}
            aria-label="Full screen"
            title="Full screen, for a wall display"
            data-testid="dashboard-tv-open"
          >
            <Maximize2 className="h-3 w-3" aria-hidden="true" />
          </button>
        </>
      )}
    </div>
  );
}

function formatSpan(ms: number): string {
  const minutes = ms / 60_000;
  if (minutes < 60) return `${minutes} minutes`;
  if (minutes < 1440) return minutes === 60 ? "hour" : `${minutes / 60} hours`;
  return minutes === 1440 ? "day" : `${minutes / 1440} days`;
}

// ── Panels ──────────────────────────────────────────────────────────────────

interface PanelLayoutControls {
  index: number;
  count: number;
  dragging: boolean;
  onMove: (to: number) => void;
  onResize: (span: number) => void;
  onRemove: () => void;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDropOn: () => void;
}

function DashboardPanelCard({
  panel,
  queryPanel,
  scope,
  range,
  services,
  refreshEvery,
  target,
  bands,
  links,
  menu,
  layout,
}: {
  panel: Panel;
  queryPanel: Panel;
  scope: ObsScope | null;
  range: RangePreset;
  services: string[];
  refreshEvery: number | false;
  target: CompareTarget;
  bands: readonly ChartBand[];
  links: ComponentProps<typeof PanelBody>["links"];
  menu: MenuEntry[];
  /** Set while the layout is edited: the panel's handle, arrows, width and remove. */
  layout?: PanelLayoutControls;
}) {
  // A panel reads only once it is near the viewport: twenty panels do not all queue at mount.
  const [bodyRef, near] = useNearViewport();
  const rawQuery = usePanelQuery(scope, range, queryPanel, services, [], { refetchInterval: refreshEvery, enabled: near });
  // Not yet near is "not loaded yet", not "no data".
  const query = near || queryPanel.kind === "search" ? rawQuery : { ...rawQuery, isLoading: true };
  const comparable = (panel.kind === "line" || panel.kind === "number") && (target.mode === "period" || target.mode === "release");
  const offsetKey = target.mode === "release" ? `release:${target.promotion.promotionId}` : target.mode === "period" ? `period:${range}` : "";
  const compareQuery = usePanelQuery(scope, range, queryPanel, services, [], {
    offsetKey,
    offsetMs:
      target.mode === "release" ? () => Math.max(0, Date.now() - target.promotion.promotedAtMs) : target.mode === "period" ? () => rangeMs(range) : undefined,
    refetchInterval: refreshEvery,
    enabled: comparable && near,
  });
  const span = spanOf(panel);
  const offsetSec = target.mode === "release" || target.mode === "period" ? Math.round(target.offsetMs / 1000) : 0;
  return (
    <Card
      padding="none"
      className={cn(
        "relative min-w-0 md:[grid-column:span_var(--span-md)/span_var(--span-md)] xl:[grid-column:span_var(--span)/span_var(--span)]",
        layout && "border-dashed",
        layout?.dragging && "opacity-50"
      )}
      style={{ ["--span" as string]: String(span), ["--span-md" as string]: String(Math.min(span, 2)) }}
      data-testid={`dashboard-panel-${panel.id}`}
      data-kind={panel.kind}
      draggable={!!layout}
      onDragStart={layout ? (event) => {
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", panel.id);
        layout.onDragStart();
      } : undefined}
      onDragEnd={layout?.onDragEnd}
      onDragOver={layout ? (event) => event.preventDefault() : undefined}
      onDrop={layout ? (event) => {
        event.preventDefault();
        layout.onDropOn();
      } : undefined}
    >
      <div className="flex items-start justify-between gap-2 px-4 pt-3">
        <div className="flex min-w-0 items-start gap-1.5">
          {layout && (
            <span className="mt-0.5 cursor-grab text-ink-muted active:cursor-grabbing" title="Drag to move" aria-hidden="true">
              <GripVertical className="h-4 w-4" />
            </span>
          )}
          <div className="min-w-0">
            <h2 className="truncate text-sm font-medium text-ink">{panel.title || panel.id}</h2>
            {query.data && <ObsAsOf updatedAt={query.dataUpdatedAt} className="mt-0.5" />}
            {panel.description && <p className="truncate text-xs text-ink-muted" title={panel.description}>{panel.description}</p>}
          </div>
        </div>
        {layout ? (
          <div className="flex shrink-0 items-center gap-0.5" data-testid={`dashboard-panel-layout-${panel.id}`}>
            <button type="button" className={ICON_BUTTON} aria-label={`Move ${panel.title} earlier`} disabled={layout.index === 0} onClick={() => layout.onMove(layout.index - 1)}>
              <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
            <button type="button" className={ICON_BUTTON} aria-label={`Move ${panel.title} later`} disabled={layout.index === layout.count - 1} onClick={() => layout.onMove(layout.index + 1)}>
              <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
            <button
              type="button"
              className={cn(BUTTON_SMALL, "h-7 px-2 tabular-nums")}
              aria-label={`Width of ${panel.title}: ${span} of ${GRID_SPANS} columns. Change it.`}
              title="Width: one to four columns"
              onClick={() => layout.onResize((span % GRID_SPANS) + 1)}
              data-testid={`dashboard-panel-resize-${panel.id}`}
            >
              {span}/{GRID_SPANS}
            </button>
            <button type="button" className={ICON_BUTTON} aria-label={`Remove ${panel.title}`} onClick={layout.onRemove} data-testid={`dashboard-panel-layout-remove-${panel.id}`}>
              <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          </div>
        ) : (
          <Menu
            label={`Panel actions for ${panel.title || panel.id}`}
            trigger={<MoreHorizontal className="h-4 w-4" aria-hidden="true" />}
            triggerClassName={ICON_BUTTON}
            entries={menu}
            testId={`dashboard-panel-menu-${panel.id}`}
          />
        )}
      </div>
      <div ref={bodyRef} className="px-4 pb-4 pt-2">
        <PanelBody
          panel={queryPanel}
          query={query}
          compare={comparable && offsetSec ? { query: compareQuery, label: target.mode === "release" ? target.label : "previous period", offsetSec } : undefined}
          bands={panel.kind === "line" ? bands : undefined}
          links={links}
        />
      </div>
    </Card>
  );
}

function DrillMenu({
  drill,
  range,
  service,
  hrefs,
  env,
  onClose,
}: {
  drill: { target: DrillTarget; panel: Panel; at: { x: number; y: number } };
  range: RangePreset;
  service: string | undefined;
  hrefs: DashboardHrefs;
  env: string;
  onClose: () => void;
}) {
  const searches = drillSearches(drill.panel, drill.target.group, { range, service });
  const entries: MenuEntry[] = [
    { label: "View logs", detail: "filtered to this series", href: hrefs.logs(searches.logs), testId: "drill-logs" },
    { label: "View traces", detail: "filtered to this series", href: hrefs.traces(searches.traces), testId: "drill-traces" },
  ];
  // "" when no single workload is meant (an org dashboard over several environments).
  const workload = searches.service ? hrefs.workload(searches.service) : "";
  if (searches.service && workload) entries.push({ label: "Open workload", detail: searches.service, href: workload, testId: "drill-workload" });
  return (
    <PointMenu
      at={drill.at}
      heading={
        <span className="block truncate">
          {drill.target.label} · {env === ENV_ALL ? "all environments" : env}
        </span>
      }
      entries={entries}
      onClose={onClose}
      testId="dashboard-drill"
    />
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-4" aria-busy="true" aria-label="Loading the dashboard" data-testid="dashboard-loading">
      <div className="space-y-2">
        <div className="h-7 w-64 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        <div className="h-4 w-96 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
      </div>
      <div className="h-11 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none" />
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {[1, 1, 1, 1, 3, 1, 2, 2].map((span, index) => (
          <div
            key={index}
            className="h-36 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none xl:[grid-column:span_var(--span)/span_var(--span)]"
            style={{ ["--span" as string]: String(span) }}
          />
        ))}
      </div>
    </div>
  );
}
