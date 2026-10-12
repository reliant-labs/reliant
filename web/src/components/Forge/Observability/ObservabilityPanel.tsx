// Copyright (c) 2025 Reliant Labs

/**
 * The Observability tab of an environment page: Overview · Logs · Traces ·
 * Dashboards · Profiles over one time window, for THIS environment.
 *
 * It is the default export of its own chunk, loaded only when the tab opens
 * (EnvTabPanels.ObservabilityTab), so uPlot, TanStack Table and every view
 * here cost the rest of the app nothing.
 *
 * ── ONE "NOT SET UP" ANSWER, SAID ONCE ──────────────────────────────────────
 *
 * Until control-plane provisions the org's telemetry store, every RPC answers
 * FailedPrecondition. That is a fact about the organization, not about a view,
 * so the panel asks once (the overview, a cache entry every view shares) and,
 * when that is the answer, shows the explanation in place of all five views
 * rather than five copies of it.
 */

import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { obsKeys, useObservabilityOrg, useServiceOverview } from "@/hooks/observability-queries";
import type { EnvScope } from "@/services/controlPlane/observability/types";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import { RANGE_LABELS, RANGE_PRESETS, rangeOf } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { DashboardsView } from "./DashboardsView";
import { LogsView } from "./LogsView";
import { OBS_VIEW_LABELS, OBS_VIEWS, severitiesOf, viewOf, type ObsNavigate, type ObsSearch, type ObsView } from "./obsSearch";
import { ObsError, ObsLoadingRows, ObsNotSetUp } from "./ObsStates";
import { OverviewView } from "./OverviewView";
import { ProfilesView } from "./ProfilesView";
import { TracesView } from "./TracesView";

export interface ObservabilityPanelProps {
  environmentId: string;
  envName: string;
  search: ObsSearch;
  navigate: ObsNavigate;
}

export default function ObservabilityPanel({ environmentId, envName, search, navigate }: ObservabilityPanelProps) {
  const view = viewOf(search.view);
  const range = rangeOf(search.range);
  const queryClient = useQueryClient();
  const org = useObservabilityOrg();
  const scope = useMemo<EnvScope | null>(
    () => (org.orgId ? { orgId: org.orgId, environmentId } : null),
    [org.orgId, environmentId]
  );
  const overview = useServiceOverview(scope, range);
  const notSetUp = !!overview.error && obsErrorKind(overview.error) === "not-set-up";

  /**
   * A view switch keeps the window and the service — the two things a reader
   * carries between views — and drops the rest, which belonged to the view
   * they left (a log query means nothing to the trace list).
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
    });
  };

  return (
    <div className="space-y-4" data-testid="obs-panel" data-view={view}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div
          role="tablist"
          aria-label="Observability views"
          className="inline-flex rounded-lg border border-border bg-background p-0.5"
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
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void queryClient.invalidateQueries({ queryKey: obsKeys.all(scope) })}
            leftIcon={<RefreshCw className="h-3.5 w-3.5" />}
            data-testid="obs-refresh"
          >
            Refresh
          </Button>
        </div>
      </div>

      <div id="obs-view-panel" role="tabpanel" aria-labelledby={`obs-view-${view}`}>
        {!scope ? (
          org.isLoading ? (
            <ObsLoadingRows testId="obs-org-loading" />
          ) : org.error ? (
            <ObsError error={org.error} envName={envName} onRetry={() => void org.refetch()} />
          ) : (
            // Signed in, but no organization resolved yet (mid-provisioning):
            // there is no tenant to read, which is the not-set-up answer.
            <ObsNotSetUp envName={envName} />
          )
        ) : notSetUp ? (
          <ObsNotSetUp envName={envName} />
        ) : view === "overview" ? (
          <OverviewView scope={scope} envName={envName} range={range} service={search.service} navigate={navigate} />
        ) : view === "logs" ? (
          <LogsView
            scope={scope}
            envName={envName}
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
            envName={envName}
            range={range}
            rootSpanName={search.q ?? ""}
            service={search.service}
            errorsOnly={!!search.errors}
            minDuration={search.minDuration}
            traceId={search.trace}
            spanId={search.span}
            navigate={navigate}
          />
        ) : view === "dashboards" ? (
          <DashboardsView
            scope={scope}
            envName={envName}
            range={range}
            dashboardId={search.dashboard}
            service={search.service}
            navigate={navigate}
          />
        ) : (
          <ProfilesView
            scope={scope}
            envName={envName}
            range={range}
            service={search.service}
            profileType={search.profileType}
            traceId={search.trace}
            navigate={navigate}
          />
        )}
      </div>
    </div>
  );
}
