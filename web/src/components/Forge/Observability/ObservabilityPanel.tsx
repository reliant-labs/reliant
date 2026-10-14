// Copyright (c) 2025 Reliant Labs

/**
 * The Observability area: Overview · Logs · Traces · Dashboards · Workloads ·
 * Profiles over one time window for one SCOPE, Alerts (what fires, the rules
 * each environment declares, silences and receivers), Delivery (releases and
 * DORA, from the release ledger) and Usage (the org's allowance).
 *
 * Every time-series chart carries the release markers of its scope (one
 * ListReleases per scope and window, through ObsChartMarkersProvider), and any
 * promotion opens a before/after comparison over the current view.
 *
 * ── ENVIRONMENT IS A FILTER, NOT A PLACE (owner decision 1) ─────────────────
 *
 * The same panel renders in two places:
 *
 *   project area   /forge/observability — every environment of the project
 *                  the caller can see; `envs` narrows it, `groupBy` compares.
 *   env page tab   /forge/env/<env>?tab=observability — the same views with
 *                  the scope pinned to that one environment: a pre-filtered
 *                  deep link, with a way out to the project area.
 *
 * There is one component and one URL shape (obsSearch.ts), so a link can move
 * between the two and nothing is forked.
 *
 * It is the default export of its own chunk, loaded only when a page shows it,
 * so uPlot, TanStack Table and every view here cost the rest of the app nothing.
 *
 * ── ONE "NOT SET UP" ANSWER, SAID ONCE ──────────────────────────────────────
 *
 * Until control-plane provisions the org's telemetry store, every RPC answers
 * FailedPrecondition. That is a fact about the organization, not about a view,
 * so the panel asks once (the overview, a cache entry every view shares) and,
 * when that is the answer, shows the explanation in place of all the views
 * rather than one copy each.
 */

import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { deliveryKeys, obsKeys, useObservabilityOrg, useServiceOverview } from "@/hooks/observability-queries";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import { RANGE_LABELS, RANGE_PRESETS, rangeOf } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { AlertNotifications } from "./AlertNotifications";
import { AlertsView } from "./AlertsView";
import { ObsChartMarkersProvider } from "./charts/UPlotChart";
import { DashboardsView } from "./DashboardsView";
import { compareParam, compareWindowOf, doraWindowOf, failureHoursOf, parseCompareParam } from "./deliveryScope";
import { DeliveryView } from "./DeliveryView";
import { LogsView } from "./LogsView";
import { filterParam, filterTokensOf, groupByOf, groupByParam, scopeFiltersOf } from "./obsFilters";
import {
  alertsTabOf,
  GROUPABLE_VIEWS,
  OBS_VIEW_LABELS,
  OBS_VIEWS,
  severitiesOf,
  viewOf,
  type ObsNavigate,
  type ObsSearch,
  type ObsView,
} from "./obsSearch";
import { envIdsOf, placeEnvironments, type ObsPlace } from "./obsPlace";
import { ObsScopeBar } from "./ObsScopeBar";
import { ObsError, ObsLoadingRows, ObsNotSetUp } from "./ObsStates";
import { OverviewView } from "./OverviewView";
import { ProfilesView } from "./ProfilesView";
import { ReleaseComparison } from "./ReleaseComparison";
import { useReleaseMarkers } from "./releaseMarkers";
import { ReleaseStrip } from "./ReleaseStrip";
import { TracesView } from "./TracesView";
import { UsageView } from "./UsageView";
import { WorkloadsView } from "./WorkloadsView";

export type { ObsEnvironment, ObsPlace } from "./obsPlace";

/** The views with time-series charts, where release markers are drawn and listed. */
const MARKED_VIEWS: readonly ObsView[] = ["overview", "dashboards", "workloads"];
const ENV_FILTER_KEYS = ["deployment.environment.name", "reliant.env.id"];

export interface ObservabilityPanelProps {
  place: ObsPlace;
  search: ObsSearch;
  navigate: ObsNavigate;
}

export default function ObservabilityPanel({ place, search, navigate }: ObservabilityPanelProps) {
  const view = viewOf(search.view);
  const range = rangeOf(search.range);
  const queryClient = useQueryClient();
  const org = useObservabilityOrg();

  const filterTokens = useMemo(() => filterTokensOf(search.f), [search.f]);
  const groupBy = useMemo(() => groupByOf(search.groupBy), [search.groupBy]);
  const envIdsKey = envIdsOf(place, search.envs).join(",");
  const projectId = place.kind === "project" ? place.project : undefined;
  const scope = useMemo<ObsScope | null>(
    () =>
      org.orgId
        ? { orgId: org.orgId, projectId, envIds: envIdsKey ? envIdsKey.split(",") : [], filters: scopeFiltersOf(filterTokens) }
        : null,
    [org.orgId, projectId, envIdsKey, filterTokens]
  );
  const subject = place.kind === "env" ? place.env.name : place.project;
  const overview = useServiceOverview(scope, range);
  const notSetUp = !!overview.error && obsErrorKind(overview.error) === "not-set-up";
  const groupable = (GROUPABLE_VIEWS as readonly ObsView[]).includes(view);
  const environments = useMemo(() => placeEnvironments(place), [place]);
  const comparing = parseCompareParam(search.compare);
  const marked = !!scope && !notSetUp && !comparing && MARKED_VIEWS.includes(view);
  const releases = useReleaseMarkers(scope, range, environments, marked);
  /** A comparison opens over the view (Back returns to it); a push, so the browser's Back closes it too. */
  const openCompare = (promotion: Promotion) => navigate({ compare: compareParam(promotion) });
  const closeCompare = () => navigate({ compare: undefined, cmpWindow: undefined });
  /** The project area's way out of "an environment in view isn't available": every environment, no environment filter. */
  const showEveryEnvironment =
    place.kind === "project"
      ? () =>
          navigate(
            { envs: undefined, f: filterParam(filterTokens.filter((token) => !ENV_FILTER_KEYS.includes(token.key))) },
            { replace: true }
          )
      : undefined;

  /**
   * A view switch keeps what a reader carries between views — the window, the
   * service, the environments, the filters and the group-by — and drops the
   * rest, which belonged to the view they left (a log query means nothing to
   * the trace list).
   */
  const selectView = (next: ObsView) => {
    if (next === view) return;
    navigate({
      view: next === "overview" ? undefined : next,
      q: undefined,
      severity: undefined,
      trace: undefined,
      span: undefined,
      dashboard: undefined,
      live: undefined,
      errors: undefined,
      minDuration: undefined,
      workload: undefined,
      container: undefined,
      alerts: undefined,
      rule: undefined,
      compare: undefined,
      cmpWindow: undefined,
      doraWindow: undefined,
      failureHours: undefined,
    });
  };
  const envName = (id: string) => placeEnvironments(place).find((env) => env.id === id)?.name;

  return (
    <div className="space-y-4" data-testid="obs-panel" data-view={view} data-place={place.kind}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div
          role="tablist"
          aria-label="Observability views"
          className="inline-flex flex-wrap rounded-lg border border-border bg-background p-0.5"
          onKeyDown={(event) => {
            if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
            const index = OBS_VIEWS.indexOf(view);
            const next = OBS_VIEWS[(index + (event.key === "ArrowRight" ? 1 : -1) + OBS_VIEWS.length) % OBS_VIEWS.length];
            event.preventDefault();
            selectView(next);
            document.getElementById(`obs-view-${next}`)?.focus();
          }}
        >
          {OBS_VIEWS.map((id) => (
            <button
              key={id}
              type="button"
              role="tab"
              id={`obs-view-${id}`}
              aria-selected={id === view}
              aria-controls="obs-view-panel"
              tabIndex={id === view ? 0 : -1}
              onClick={() => selectView(id)}
              data-testid={`obs-view-${id}`}
              className={cn(
                "rounded-md px-3 py-1 text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                id === view ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"
              )}
            >
              {OBS_VIEW_LABELS[id]}
            </button>
          ))}
        </div>
        <div className="flex items-center gap-2">
          {org.orgId && <AlertNotifications orgId={org.orgId} envName={envName} onOpenAlerts={() => selectView("alerts")} />}
          {/* Delivery has its own windows (days); a comparison its own (around the promotion). */}
          {view !== "delivery" && !comparing && (
            <>
              <label htmlFor="obs-range" className="sr-only">
                Time range
              </label>
              <select
                id="obs-range"
                value={range}
                onChange={(event) => navigate({ range: rangeOf(event.target.value) }, { replace: true })}
                className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                data-testid="obs-range"
              >
                {RANGE_PRESETS.map((preset) => (
                  <option key={preset} value={preset}>
                    {RANGE_LABELS[preset]}
                  </option>
                ))}
              </select>
            </>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              void queryClient.invalidateQueries({ queryKey: obsKeys.all(scope) });
              void queryClient.invalidateQueries({ queryKey: deliveryKeys.root() });
            }}
            leftIcon={<RefreshCw className="h-3.5 w-3.5" />}
            data-testid="obs-refresh"
          >
            Refresh
          </Button>
        </div>
      </div>

      {/* Usage is the org's allowance, not a query over the scope: nothing here would narrow it. */}
      {/* Alerts and Delivery keep the environment picker; attribute filters and group-by mean nothing there. */}
      {scope && (!notSetUp || view === "alerts" || view === "delivery") && view !== "usage" && (
        <ObsScopeBar
          scope={scope}
          range={range}
          environments={place.kind === "project" ? place.environments : undefined}
          selectedEnvs={search.envs ?? []}
          onEnvsChange={(names) => navigate({ envs: names.length ? names : undefined }, { replace: true })}
          pinnedEnv={place.kind === "env" ? place.env.name : undefined}
          onWiden={place.kind === "env" && place.onWiden ? () => place.onWiden!({ ...search, envs: [place.env.name] }) : undefined}
          filters={filterTokens}
          onFiltersChange={(tokens) => navigate({ f: filterParam(tokens) }, { replace: true })}
          groupBy={groupBy}
          onGroupByChange={(keys) => navigate({ groupBy: groupByParam(keys) }, { replace: true })}
          groupable={groupable}
          filterable={view !== "alerts" && view !== "delivery"}
        />
      )}

      {marked && (
        <ReleaseStrip
          key={search.dashboard ?? view}
          releases={releases}
          rangeLabel={RANGE_LABELS[range].toLowerCase()}
          defaultOpen={view === "dashboards" && /(^|\/)releases$/.test(search.dashboard ?? "")}
          onCompare={openCompare}
        />
      )}

      <div id="obs-view-panel" role="tabpanel" aria-labelledby={`obs-view-${view}`}>
        <ObsChartMarkersProvider markers={releases.markers}>
        {!scope ? (
          org.isLoading ? (
            <ObsLoadingRows testId="obs-org-loading" />
          ) : org.error ? (
            <ObsError error={org.error} subject={subject} onRetry={() => void org.refetch()} />
          ) : (
            // Signed in, but no organization resolved yet (mid-provisioning):
            // there is no tenant to read, which is the not-set-up answer.
            <ObsNotSetUp subject={subject} />
          )
        ) : view === "usage" ? (
          // Usage is a Postgres read, not a tenant query: it stays available
          // before the store exists, which is when the allowance is worth seeing.
          <UsageView orgId={scope.orgId} subject={subject} />
        ) : view === "alerts" ? (
          // Alerting answers for itself (AlertingService, its own "not
          // configured"), and the declared rules come from the release shape,
          // so it stays available before the telemetry store exists.
          <AlertsView
            orgId={scope.orgId}
            place={place}
            envIds={scope.envIds}
            range={range}
            tab={alertsTabOf(search.alerts)}
            rule={search.rule}
            navigate={navigate}
          />
        ) : comparing ? (
          <ReleaseComparison
            scope={scope}
            subject={subject}
            target={comparing}
            width={compareWindowOf(search.cmpWindow)}
            backLabel={OBS_VIEW_LABELS[view]}
            onBack={closeCompare}
            navigate={navigate}
          />
        ) : view === "delivery" ? (
          // The release ledger is Postgres, not the telemetry store: Delivery
          // answers before observability is set up, like Usage and Alerts.
          <DeliveryView
            scope={scope}
            environments={environments}
            subject={subject}
            window={doraWindowOf(search.doraWindow)}
            failureHours={failureHoursOf(search.failureHours)}
            onShowEveryEnvironment={showEveryEnvironment}
            onCompare={openCompare}
            navigate={navigate}
          />
        ) : notSetUp ? (
          <ObsNotSetUp subject={subject} />
        ) : view === "overview" ? (
          <OverviewView scope={scope} subject={subject} range={range} service={search.service} groupBy={groupBy} navigate={navigate} />
        ) : view === "logs" ? (
          <LogsView
            scope={scope}
            subject={subject}
            range={range}
            query={search.q ?? ""}
            service={search.service}
            severities={severitiesOf(search.severity)}
            traceId={search.trace}
            spanId={search.span}
            live={!!search.live}
            navigate={navigate}
          />
        ) : view === "traces" ? (
          <TracesView
            scope={scope}
            subject={subject}
            range={range}
            rootSpanName={search.q ?? ""}
            service={search.service}
            errorsOnly={!!search.errors}
            minDuration={search.minDuration}
            traceId={search.trace}
            spanId={search.span}
            groupBy={groupBy}
            filters={filterTokens}
            navigate={navigate}
          />
        ) : view === "dashboards" ? (
          <DashboardsView
            scope={scope}
            subject={subject}
            range={range}
            dashboardId={search.dashboard}
            service={search.service}
            groupBy={groupBy}
            filterTokens={filterTokens}
            edits={search.edit}
            navigate={navigate}
          />
        ) : view === "workloads" ? (
          <WorkloadsView
            scope={scope}
            subject={subject}
            range={range}
            service={search.service}
            workload={search.workload}
            container={search.container}
            showEnv={place.kind === "project"}
            navigate={navigate}
          />
        ) : (
          <ProfilesView
            scope={scope}
            subject={subject}
            range={range}
            service={search.service}
            profileType={search.profileType}
            traceId={search.trace}
            navigate={navigate}
          />
        )}
        </ObsChartMarkersProvider>
      </div>
    </div>
  );
}
