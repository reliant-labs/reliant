// Copyright (c) 2025 Reliant Labs

/**
 * The Observability home, the Dashboards index and one dashboard as console
 * routes:
 *
 *   /forge/p/:project/observability
 *   /forge/p/:project/observability/dashboards
 *   /forge/p/:project/observability/dashboards/:kind/:key
 *
 * This file only adapts the console to the pages — the project and scope
 * from the console context, the recorded environments from its roster, the
 * URL's search state, and every href built by the console's own paths — so
 * ObservabilityHome, DashboardsList and DashboardView know nothing about
 * routing, which is what lets the preview harness and the tests mount them on
 * fixtures.
 */

import { useCallback, useMemo, type ReactNode } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Activity } from "lucide-react";

import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import { DEFAULT_RANGE } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../../Console/consoleContext";
import { CloudNotice } from "../../SourceNotices";
import type { ObsEnvironment } from "../obsPlace";
import type { ObsSearch } from "../obsSearch";
import { ObservabilityHome, type ObservabilityHomeHrefs } from "../Home/ObservabilityHome";
import { compareParam, isDashboardPageKind, rangeParam, refreshParam, type DashboardSearch } from "./dashboardModel";
import { DashboardsList, type DashboardsSearch } from "./DashboardsList";
import { DashboardView, type DashboardHrefs } from "./DashboardView";

/** The roster's recorded environments, as the observability pages address them (control-plane id ↔ name). */
function useRecordedEnvs(): ObsEnvironment[] {
  const { roster } = useForgeConsole();
  return useMemo(
    () => roster.envs.flatMap((candidate) => (candidate.live ? [{ id: candidate.live.id, name: candidate.name, declared: candidate.live.declaredShape }] : [])),
    [roster.envs]
  );
}

/** Merge `next` into the current search without a history entry. */
function useSearchUpdater<T extends object>() {
  const navigate = useNavigate();
  return useCallback(
    (next: Partial<T>) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace: true,
      }),
    [navigate]
  );
}

/** Every href the observability pages link to, from the console's own paths (and carrying the scope). */
function useObservabilityHrefs(): DashboardHrefs & ObservabilityHomeHrefs {
  const { href, env } = useForgeConsole();
  return useMemo(
    () => ({
      logs: (search: ObsSearch = {}) => href({ kind: "obs-view", view: "logs" }, { ...search }),
      traces: (search: ObsSearch = {}) => href({ kind: "obs-view", view: "traces" }, { ...search }),
      profiles: (search: ObsSearch = {}) => href({ kind: "obs-view", view: "profiles" }, { ...search }),
      alerts: href({ kind: "obs-view", view: "alerts" }),
      dashboards: href({ kind: "obs-view", view: "dashboards" }),
      list: href({ kind: "obs-view", view: "dashboards" }),
      releases: href({ kind: "releases" }),
      release: (version: string) => href({ kind: "release", version }),
      trace: (traceId: string) => href({ kind: "obs-view", view: "traces" }, { trace: traceId, range: "7d" }),
      workload: (name: string, envName?: string | null) => href({ kind: "overview" }, { workload: name, env: envName ?? env }),
      dashboard: (ref: DashboardRef, search: DashboardSearch = {}) =>
        ref.kind === "template" ? href({ kind: "obs-view", view: "dashboards" }) : href({ kind: "dashboard", dashKind: ref.kind, key: ref.key }, { ...search }),
    }),
    [href, env]
  );
}

/**
 * What every one of these pages shows before its own body: the roster still
 * loading, or a project with no environment Reliant has recorded — there is
 * no telemetry to show and no environment to scope a dashboard to.
 */
function RosterGate({ testId, children }: { testId: string; children: ReactNode }) {
  const { roster } = useForgeConsole();
  const environments = useRecordedEnvs();
  if (roster.isLoading || roster.resolvingName) {
    return (
      <Card padding="none" aria-label="Loading" aria-busy="true" data-testid={`${testId}-loading`}>
        <SkeletonLoader variant="table-row" count={4} />
      </Card>
    );
  }
  return (
    <div className="space-y-4" data-testid={testId}>
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      {environments.length === 0 ? (
        <div data-testid="observability-no-envs">
          <EmptyState
            icon={<Activity className="h-6 w-6" aria-hidden="true" />}
            title="No environments to observe yet"
            description="Telemetry and dashboards are filed under the environments Reliant records. Deploy one through Reliant (forge env deploy <env>) and its logs, traces, metrics and dashboards appear here."
          />
        </div>
      ) : (
        children
      )}
    </div>
  );
}

function useNavigateTo() {
  const navigate = useNavigate();
  return useCallback((target: string) => void navigate({ href: target }), [navigate]);
}

export function ObservabilityHomePage() {
  const { forgeProject, env } = useForgeConsole();
  const search = useSearch({ strict: false }) as { range?: string };
  const onSearch = useSearchUpdater<{ range?: string }>();
  const environments = useRecordedEnvs();
  const hrefs = useObservabilityHrefs();
  const navigateTo = useNavigateTo();
  return (
    <RosterGate testId="console-observability">
      <ObservabilityHome
        project={forgeProject}
        env={env}
        environments={environments}
        range={rangeParam(search.range) ?? DEFAULT_RANGE}
        onRange={(range) => onSearch({ range: range === DEFAULT_RANGE ? undefined : range })}
        hrefs={hrefs}
        navigateTo={navigateTo}
      />
    </RosterGate>
  );
}

export function DashboardsPage() {
  const { forgeProject, env } = useForgeConsole();
  const search = useSearch({ strict: false }) as DashboardsSearch;
  const onSearch = useSearchUpdater<DashboardsSearch>();
  const environments = useRecordedEnvs();
  const hrefs = useObservabilityHrefs();
  const navigateTo = useNavigateTo();
  return (
    <RosterGate testId="console-dashboards">
      <DashboardsList
        project={forgeProject}
        env={env}
        environments={environments}
        search={{ q: search.q, source: search.source, owner: search.owner }}
        onSearch={onSearch}
        hrefs={hrefs}
        navigateTo={navigateTo}
      />
    </RosterGate>
  );
}

export function DashboardPage() {
  const { forgeProject, env, setEnv } = useForgeConsole();
  const { kind, key = "" } = useParams({ strict: false }) as { kind?: string; key?: string };
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const onSearch = useSearchUpdater<DashboardSearch>();
  const environments = useRecordedEnvs();
  const hrefs = useObservabilityHrefs();
  const navigateTo = useNavigateTo();
  const dashRef = useMemo<DashboardRef | null>(
    () => (isDashboardPageKind(kind) ? { kind, projectId: forgeProject, key } : null),
    [kind, forgeProject, key]
  );
  const search: DashboardSearch = {
    range: rangeParam(raw.range),
    service: typeof raw.service === "string" ? raw.service : undefined,
    compare: raw.compare === undefined ? undefined : compareParam(raw.compare),
    refresh: raw.refresh === undefined ? undefined : refreshParam(raw.refresh),
    tv: raw.tv === true || undefined,
    version: typeof raw.version === "number" ? raw.version : undefined,
    release: typeof raw.release === "string" ? raw.release : undefined,
  };
  if (!dashRef) return null;
  return (
    <RosterGate testId="console-dashboard">
      <DashboardView
        // Another dashboard is another page: nothing (an open dialog, a drill menu) carries over.
        key={`${dashRef.kind}/${dashRef.key}`}
        project={forgeProject}
        dashRef={dashRef}
        env={env}
        environments={environments}
        search={search}
        onSearch={onSearch}
        onEnvChange={setEnv}
        hrefs={hrefs}
        navigateTo={navigateTo}
      />
    </RosterGate>
  );
}
