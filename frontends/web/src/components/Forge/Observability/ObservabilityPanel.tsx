// Copyright (c) 2025 Reliant Labs

/**
 * The Observability area: Overview · Errors · Logs · Traces · Profiles
 * over one time window for one SCOPE, Alerts (what fires, the rules
 * each environment declares, silences and receivers) and Usage (the org's
 * allowance). Errors is the issue inbox (IssuesService), browser and server;
 * Sessions are real-user browser sessions with their masked replays
 * (RumService).
 *
 * Every time-series chart carries the release markers of its scope (one
 * ListReleases per scope and window, through ObsChartMarkersProvider), and any
 * promotion opens a before/after comparison over the current view — the
 * server's release-health verdict (GetReleaseHealth).
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
 *
 * "Set up, but nothing has arrived yet" is the same kind of fact (about the
 * scope, not a view), so it is asked once too (useNothingArrived, a week of
 * GetServiceSignals) and answered once, in place of the telemetry views. Both
 * lead with what the current view will show (ObsStates' OBS_TOPICS), then how
 * to get data onto it.
 */

import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { activityKeys, deliveryKeys, issueKeys, obsKeys, useObservabilityOrg, useServiceOverview } from "@/hooks/observability-queries";
import { sessionKeys } from "@/hooks/observability-session-queries";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObsScope, ServiceSignal } from "@/services/controlPlane/observability/types";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import { RANGE_LABELS, RANGE_PRESETS, rangeOf } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { AlertNotifications } from "./AlertNotifications";
import { ObsAskContextValue } from "./AskReliant/askReliantContext";
import { AlertsView } from "./AlertsView";
import { ObsChartMarkersProvider } from "./charts/UPlotChart";
import { compareParam, healthWindowOf, parseCompareParam } from "./deliveryScope";
import { ErrorsView } from "./Errors/ErrorsView";
import { LogsView } from "./LogsView";
import { filterParam, filterTokensOf, groupByOf, groupByParam, scopeFiltersOf } from "./obsFilters";
import {
  alertsTabOf,
  GROUPABLE_VIEWS,
  issuePlatformOf,
  issueSortOf,
  issueStatusesOf,
  OBS_VIEW_LABELS,
  OBS_VIEWS,
  severitiesOf,
  viewOf,
  type ObsNavigate,
  type ObsSearch,
  type ObsView,
} from "./obsSearch";
import { envIdsOf, orgPickerEnvironments, orgSelectionIsEmpty, placeEnvironments, type ObsPlace } from "./obsPlace";
import { ObsScopeBar } from "./ObsScopeBar";
import { ObsError, ObsLoadingRows, ObsNoAppEnvironments, ObsNothingYet, ObsNotSetUp } from "./ObsStates";
import { OverviewView } from "./OverviewView";
import { ProfilesView } from "./ProfilesView";
import { ReleaseComparison } from "./ReleaseComparison";
import { useReleaseMarkers } from "./releaseMarkers";
import { ReleaseStrip } from "./ReleaseStrip";
import { signalState, useNothingArrived, useSignalsIndex, type SignalState } from "./serviceSignals";
import { SessionsView } from "./Sessions/SessionsView";
import { TracesView } from "./TracesView";
import { UsageView } from "./UsageView";

export type { ObsEnvironment, ObsPlace } from "./obsPlace";

/** The views with time-series charts, where release markers are drawn and listed. */
const MARKED_VIEWS: readonly ObsView[] = ["overview"];

/** The views that read the scope's telemetry, so "nothing has arrived yet" is true of them (not Alerts or Usage). */
const NOTHING_YET_VIEWS: readonly ObsView[] = ["overview", "errors", "sessions", "logs", "traces", "profiles"];

/** The views that read one signal of one service, and the signal. */
const SIGNAL_VIEWS: Partial<Record<ObsView, ServiceSignal>> = { logs: "logs", traces: "traces", profiles: "profiles" };

export interface ObservabilityPanelProps {
  place: ObsPlace;
  search: ObsSearch;
  navigate: ObsNavigate;
  /**
   * "tabs" (default): the panel carries its own view tabs and environment
   * picker. "console": it is one page of the console (Console/
   * ObservabilityHost) — the nav picks the view and the top bar the
   * environment scope, so it draws neither (decision D9).
   */
  chrome?: "tabs" | "console";
}

export default function ObservabilityPanel({ place, search, navigate, chrome = "tabs" }: ObservabilityPanelProps) {
  const consoleChrome = chrome === "console";
  const view = viewOf(search.view);
  const range = rangeOf(search.range);
  const queryClient = useQueryClient();
  const org = useObservabilityOrg();

  const filterTokens = useMemo(() => filterTokensOf(search.f), [search.f]);
  const groupBy = useMemo(() => groupByOf(search.groupBy), [search.groupBy]);
  const envIdsKey = envIdsOf(place, search.envs, search.apps).join(",");
  // Apps picked that own no environment: an empty id list would read the whole org, so nothing is queried.
  const noEnvironments = orgSelectionIsEmpty(place, search.envs, search.apps);
  const selectedApps = useMemo(() => (place.kind === "org" ? (search.apps ?? []) : []), [place.kind, search.apps]);
  const projectId =
    place.kind === "project" ? place.project : place.kind === "org" && selectedApps.length === 1 && selectedApps[0] ? selectedApps[0] : undefined;
  const scope = useMemo<ObsScope | null>(
    () =>
      org.orgId
        ? { orgId: org.orgId, projectId, envIds: envIdsKey ? envIdsKey.split(",") : [], filters: scopeFiltersOf(filterTokens) }
        : null,
    [org.orgId, projectId, envIdsKey, filterTokens]
  );
  const subject =
    place.kind === "env"
      ? place.env.name
      : place.kind === "project"
        ? place.project
        : selectedApps.length
          ? selectedApps.join(", ")
          : "All apps";
  const queryScope = noEnvironments ? null : scope;
  const overview = useServiceOverview(queryScope, range);
  const notSetUp = !!overview.error && obsErrorKind(overview.error) === "not-set-up";
  const groupable = (GROUPABLE_VIEWS as readonly ObsView[]).includes(view);
  const environments = useMemo(() => placeEnvironments(place), [place]);
  const comparing = parseCompareParam(search.compare);
  const marked = !!queryScope && !notSetUp && !comparing && MARKED_VIEWS.includes(view);
  const releases = useReleaseMarkers(queryScope, range, environments, marked);
  /** A comparison opens over the view (Back returns to it); a push, so the browser's Back closes it too. */
  const openCompare = (promotion: Promotion) => navigate({ compare: compareParam(promotion) });
  const closeCompare = () => navigate({ compare: undefined, cmpWindow: undefined });

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
      live: undefined,
      errors: undefined,
      minDuration: undefined,
      alerts: undefined,
      rule: undefined,
      compare: undefined,
      cmpWindow: undefined,
      issue: undefined,
      istatus: undefined,
      platform: undefined,
      isort: undefined,
      assignee: undefined,
      session: undefined,
      t: undefined,
      replay: undefined,
      user: undefined,
      cursor: undefined,
    });
  };
  const envName = (id: string) => placeEnvironments(place).find((env) => env.id === id)?.name;
  const askContext = useMemo(() => ({ environments, forgeProject: place.kind === "project" ? place.project : undefined }), [environments, place]);

  // Narrowed to one service, the signal views it neither sends nor declares
  // are not offered and the idle ones are muted (serviceSignals.ts). The view
  // in the URL is always offered: a link never lands on a missing tab. The
  // console draws no tabs (its nav is the project's, every view kept).
  const signals = useSignalsIndex(!consoleChrome && search.service && !notSetUp ? scope : null, range);

  // Set up, but nothing in scope has sent anything this week: the views say
  // how to get data here, once, instead of each one "nothing in this window".
  // Asked only when the window's overview came back empty: a service in it
  // already answers "something has arrived", so a scope with data never pays
  // for the week's probe. A deep link (an issue, a session, a trace) still
  // opens: issues outlive the week, and the link is what the reader came for.
  const deepLink = !!(search.issue || search.session || search.trace);
  const windowEmpty = !notSetUp && overview.data?.services.length === 0;
  const nothingYet = useNothingArrived(scope && windowEmpty ? scope : null) && !deepLink && NOTHING_YET_VIEWS.includes(view);
  /** The one environment in scope, so a quoted command names it; none when it is all of them. */
  const envHint = place.kind === "env" ? place.env.name : search.envs?.length === 1 ? search.envs[0] : undefined;
  const viewState = (id: ObsView): SignalState => {
    const signal = SIGNAL_VIEWS[id];
    return signal ? signalState(signals, search.service, signal) : { kind: "available" };
  };
  const tabViews = OBS_VIEWS.filter((id) => id === view || viewState(id).kind !== "absent");
  const selectableViews = tabViews.filter((id) => id === view || viewState(id).kind === "available");

  return (
    <ObsAskContextValue.Provider value={askContext}>
    <div className="space-y-4" data-testid="obs-panel" data-view={view} data-place={place.kind}>
      <div className={cn("flex flex-wrap items-center gap-3", consoleChrome ? "justify-end" : "justify-between")}>
        {!consoleChrome && (
          <div
            role="tablist"
            aria-label="Observability views"
            className="inline-flex flex-wrap rounded-lg border border-border bg-background p-0.5"
            onKeyDown={(event) => {
              if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
              const index = selectableViews.indexOf(view);
              const next = selectableViews[(index + (event.key === "ArrowRight" ? 1 : -1) + selectableViews.length) % selectableViews.length];
              event.preventDefault();
              selectView(next);
              document.getElementById(`obs-view-${next}`)?.focus();
            }}
          >
            {tabViews.map((id) => {
              const state = viewState(id);
              const idle = state.kind === "idle" && id !== view ? state : null;
              return (
                <button
                  key={id}
                  type="button"
                  role="tab"
                  id={`obs-view-${id}`}
                  aria-selected={id === view}
                  aria-controls="obs-view-panel"
                  tabIndex={id === view ? 0 : -1}
                  disabled={!!idle}
                  title={idle?.hint}
                  onClick={() => selectView(id)}
                  data-testid={`obs-view-${id}`}
                  data-signal-state={SIGNAL_VIEWS[id] && search.service ? state.kind : undefined}
                  className={cn(
                    "rounded-md px-3 py-1 text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    idle
                      ? "cursor-not-allowed text-muted-foreground/60"
                      : id === view
                        ? "bg-card font-medium text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  {OBS_VIEW_LABELS[id]}
                  {idle && <span className="ml-1.5 text-2xs">{idle.badge}</span>}
                </button>
              );
            })}
          </div>
        )}
        <div className="flex items-center gap-2">
          {org.orgId && <AlertNotifications orgId={org.orgId} envName={envName} onOpenAlerts={() => selectView("alerts")} />}
          {/* A comparison has its own windows (around the promotion). */}
          {!comparing && (
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
              void queryClient.invalidateQueries({ queryKey: issueKeys.root() });
              void queryClient.invalidateQueries({ queryKey: activityKeys.root() });
              void queryClient.invalidateQueries({ queryKey: sessionKeys.root() });
            }}
            leftIcon={<RefreshCw className="h-3.5 w-3.5" />}
            data-testid="obs-refresh"
          >
            Refresh
          </Button>
        </div>
      </div>

      {/* Usage is the org's allowance, not a query over the scope: nothing here would narrow it. */}
      {/* Alerts keeps the environment picker; attribute filters and group-by mean nothing there. */}
      {/* Nor does a filter mean anything over a scope that has sent nothing yet. */}
      {scope && (!notSetUp || view === "alerts") && !nothingYet && view !== "usage" && (
        <ObsScopeBar
          scope={scope}
          range={range}
          environments={
            place.kind === "org"
              ? orgPickerEnvironments(place, selectedApps)
              : place.kind === "project" && !consoleChrome
                ? place.environments
                : undefined
          }
          apps={place.kind === "org" ? place.apps : undefined}
          selectedApps={selectedApps}
          onAppsChange={(names) => {
            // Picked environments that no longer belong to a selected app are dropped with it.
            const inApps = new Set(orgPickerEnvironments(place, names).map((env) => env.name));
            const envs = (search.envs ?? []).filter((name) => inApps.has(name));
            navigate({ apps: names.length ? names : undefined, envs: envs.length ? envs : undefined }, { replace: true });
          }}
          selectedEnvs={search.envs ?? []}
          onEnvsChange={(names) => navigate({ envs: names.length ? names : undefined }, { replace: true })}
          pinnedEnv={place.kind === "env" ? place.env.name : undefined}
          onWiden={place.kind === "env" && place.onWiden ? () => place.onWiden!({ ...search, envs: [place.env.name] }) : undefined}
          filters={filterTokens}
          onFiltersChange={(tokens) => navigate({ f: filterParam(tokens) }, { replace: true })}
          groupBy={groupBy}
          onGroupByChange={(keys) => navigate({ groupBy: groupByParam(keys) }, { replace: true })}
          groupable={groupable && !noEnvironments}
          filterable={view !== "alerts" && view !== "errors" && view !== "sessions" && !noEnvironments}
        />
      )}

      {marked && (
        <ReleaseStrip
          key={view}
          releases={releases}
          rangeLabel={RANGE_LABELS[range].toLowerCase()}
          onCompare={openCompare}
        />
      )}

      <div id="obs-view-panel" {...(consoleChrome ? {} : { role: "tabpanel", "aria-labelledby": `obs-view-${view}` })}>
        <ObsChartMarkersProvider markers={releases.markers}>
        {!scope ? (
          org.isLoading ? (
            <ObsLoadingRows testId="obs-org-loading" />
          ) : org.error ? (
            <ObsError error={org.error} subject={subject} onRetry={() => void org.refetch()} />
          ) : (
            // Signed in, but no organization resolved yet (mid-provisioning):
            // there is no tenant to read, which is the not-set-up answer.
            <ObsNotSetUp subject={subject} topic={view} env={envHint} />
          )
        ) : view === "usage" ? (
          // Usage is a Postgres read, not a tenant query: it stays available
          // before the store exists, which is when the allowance is worth seeing.
          <UsageView orgId={scope.orgId} subject={subject} />
        ) : noEnvironments ? (
          <ObsNoAppEnvironments apps={selectedApps} />
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
            envName={envName(comparing.envId)}
            width={healthWindowOf(search.cmpWindow)}
            back={{ label: OBS_VIEW_LABELS[view], onBack: closeCompare }}
            navigate={navigate}
          />
        ) : notSetUp ? (
          <ObsNotSetUp subject={subject} topic={view} env={envHint} />
        ) : nothingYet ? (
          <ObsNothingYet subject={subject} topic={view} env={envHint} />
        ) : view === "errors" ? (
          <ErrorsView
            scope={scope}
            subject={subject}
            range={range}
            text={search.q ?? ""}
            service={search.service}
            statuses={issueStatusesOf(search.istatus)}
            platform={issuePlatformOf(search.platform)}
            sort={issueSortOf(search.isort)}
            assignee={search.assignee}
            issueId={search.issue}
            navigate={navigate}
          />
        ) : view === "sessions" ? (
          <SessionsView
            scope={scope}
            subject={subject}
            range={range}
            service={search.service}
            text={search.q ?? ""}
            hasError={!!search.errors}
            hasReplay={!!search.replay}
            user={search.user ?? ""}
            cursor={search.cursor ?? []}
            sessionId={search.session}
            atMs={search.t}
            navigate={navigate}
          />
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
    </ObsAskContextValue.Provider>
  );
}
