// Copyright (c) 2025 Reliant Labs

/**
 * The Dashboards index and one dashboard as console routes:
 *
 *   /forge/p/:project/observability/dashboards
 *   /forge/p/:project/observability/dashboards/:kind/:key
 *
 * This file only adapts the console to the pages — the project and scope
 * from the console context, the recorded environments from its roster, the
 * URL's search state, and every href built by the console's own paths — so
 * DashboardsList and DashboardView know nothing about
 * routing, which is what lets the preview harness and the tests mount them on
 * fixtures.
 */

import { useCallback, useMemo, type ReactNode } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { useForgeConsole } from "../../Console/consoleContext";
import { ENV_ALL } from "../../Console/envScope";
import { CloudNotice } from "../../SourceNotices";
import type { ObsEnvironment } from "../obsPlace";
import type { ObsSearch } from "../obsSearch";
import { ObsNoEnvironments, type ObsTopic } from "../ObsStates";
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

/** Merge viewer state into the URL; dashboard controls keep browser history. */
function useSearchUpdater<T extends object>(replace = true) {
  const navigate = useNavigate();
  return useCallback(
    (next: Partial<T>) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace,
      }),
    [navigate, replace]
  );
}

type ConsoleHref = ReturnType<typeof useForgeConsole>["href"];

/**
 * A workload is one per environment: with every environment in scope and
 * none named, the Overview filtered to it lets the reader pick one.
 */
function workloadPath(href: ConsoleHref, where: string, name: string): string {
  return where === ENV_ALL ? href({ kind: "overview" }, { q: name }) : href({ kind: "workload", env: where, name });
}

/** Every href the observability pages link to, from the console's own paths (and carrying the scope). */
function useObservabilityHrefs(): DashboardHrefs {
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
      session: (sessionId: string) => href({ kind: "obs-view", view: "sessions" }, { session: sessionId }),
      workload: (name: string, envName?: string | null) => workloadPath(href, envName ?? env, name),
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
function RosterGate({ testId, topic, children }: { testId: string; topic: ObsTopic; children: ReactNode }) {
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
          <ObsNoEnvironments topic={topic} />
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

export function DashboardsPage() {
  const { forgeProject, env } = useForgeConsole();
  const search = useSearch({ strict: false }) as DashboardsSearch;
  const onSearch = useSearchUpdater<DashboardsSearch>();
  const environments = useRecordedEnvs();
  const hrefs = useObservabilityHrefs();
  const navigateTo = useNavigateTo();
  const { href } = useForgeConsole();
  const listHrefs = useMemo(
    () => ({
      dashboard: hrefs.dashboard,
      hub: {
        alerts: href({ kind: "obs-view", view: "alerts" }),
        releases: href({ kind: "releases" }),
        errors: href({ kind: "obs-view", view: "errors" }),
        release: hrefs.release,
        workload: (name: string, where: string | null) => workloadPath(href, where ?? env, name),
        logs: (service: string) => hrefs.logs({ service, severity: "error,fatal" }),
      },
    }),
    [hrefs, href, env]
  );
  return (
    // Not behind RosterGate: organization costs are a builtin dashboard that
    // needs no deployed environment, so the list stays reachable and the
    // getting-started guidance sits above it instead of replacing it.
    <div className="space-y-4" data-testid="console-dashboards">
      {environments.length === 0 && (
        <div data-testid="observability-no-envs">
          <ObsNoEnvironments topic="dashboards" />
        </div>
      )}
      <DashboardsList
        project={forgeProject}
        env={env}
        environments={environments}
        search={{ view: search.view, q: search.q, group: search.group, source: search.source, owner: search.owner }}
        onSearch={onSearch}
        hrefs={listHrefs}
        navigateTo={navigateTo}
      />
    </div>
  );
}

export function DashboardPage() {
  const { forgeProject, env, setEnv } = useForgeConsole();
  const { kind, key = "" } = useParams({ strict: false }) as { kind?: string; key?: string };
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const onSearch = useSearchUpdater<DashboardSearch>(false);
  const environments = useRecordedEnvs();
  const hrefs = useObservabilityHrefs();
  const navigateTo = useNavigateTo();
  const dashRef = useMemo<DashboardRef | null>(
    () => (isDashboardPageKind(kind) ? { kind, projectId: kind === "builtin" ? "" : forgeProject, key } : null),
    [kind, forgeProject, key]
  );
  const search: DashboardSearch = {
    range: rangeParam(raw.range),
    service: typeof raw.service === "string" ? raw.service : undefined,
    rpcService: typeof raw.rpcService === "string" ? raw.rpcService : undefined,
    rpcMethod: typeof raw.rpcMethod === "string" ? raw.rpcMethod : undefined,
    rpcProcedure: typeof raw.rpcProcedure === "string" ? raw.rpcProcedure : undefined,
    variables: raw.variables && typeof raw.variables === "object" && !Array.isArray(raw.variables) ? raw.variables as Record<string, string> : undefined,
    breakdown: typeof raw.breakdown === "string" ? raw.breakdown : undefined,
    hiddenSections: Array.isArray(raw.hiddenSections) ? raw.hiddenSections.filter((id): id is string => typeof id === "string") : undefined,
    hiddenPanels: Array.isArray(raw.hiddenPanels) ? raw.hiddenPanels.filter((id): id is string => typeof id === "string") : undefined,
    releases: typeof raw.releases === "boolean" ? raw.releases : undefined,
    alerts: typeof raw.alerts === "boolean" ? raw.alerts : undefined,
    compare: raw.compare === undefined ? undefined : compareParam(raw.compare),
    refresh: raw.refresh === undefined ? undefined : refreshParam(raw.refresh),
    tv: raw.tv === true || undefined,
    version: typeof raw.version === "number" ? raw.version : undefined,
    release: typeof raw.release === "string" ? raw.release : undefined,
  };
  if (!dashRef) return null;
  // Organization accounting is discoverable without a deployed environment.
  if (dashRef.kind === "builtin") return <DashboardView key={`builtin/${key}`} project={forgeProject} dashRef={dashRef} env={env} environments={environments} search={search} onSearch={onSearch} onEnvChange={setEnv} hrefs={hrefs} navigateTo={navigateTo} />;
  return (
    <RosterGate testId="console-dashboard" topic="dashboards">
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
