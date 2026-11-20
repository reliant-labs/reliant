// Copyright (c) 2026 Reliant Labs

/**
 * What an app's console sections need to be drawn: the data half of
 * ConsoleLayout, split out so the ONE left nav (Layout/Sidebar) can draw the
 * open app's sections inline while the console's own layout renders below it
 * (U6: no second nav column).
 *
 * It reads the app, the page and the scope from the URL it is handed, never
 * from the console's context: the nav renders above the route outlet, where
 * that context does not exist. Both callers make the same reads, which React
 * Query shares.
 */

import { useMemo } from "react";

import { useDashboard, useDashboardIndex } from "@/hooks/dashboard-queries";
import { useForgeRoster } from "@/hooks/forge-queries";
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { isQueued, queuedOnLabel } from "@/services/forge/live";

import { displayTitle, hubOf, hubsOf, hubTabLabel } from "../Observability/Dashboards/dashboardHubs";
import { useConsoleReliantProject } from "./consoleHost";
import type { ConsoleNavDashboard, ConsoleNavEnv } from "./ConsoleShell";
import { ENV_ALL, rememberedEnv, resolveEnvScope, scopeOptions } from "./envScope";
import type { GroupedNavInput, ProductAnalyticsHub } from "./groupedNav";
import { consolePageOf, type ConsolePage } from "./paths";
import { useOptionalViews } from "./useOptionalViews";

export interface ConsoleNavModel extends GroupedNavInput {
  /** The Reliant project list has answered (consoleHost.ts). */
  listed: boolean;
  /** The Reliant project declaring the app, once found. */
  projectId: string | null;
  roster: ReturnType<typeof useForgeRoster>;
  /** The roster has answered: `env` is checked against the app's real environments. */
  settled: boolean;
  scope: ReturnType<typeof scopeOptions>;
  /** The open dashboard reads platform telemetry, which belongs to no environment. */
  platformWide: boolean;
}

const OVERVIEW: ConsolePage = { kind: "overview" };

export function useConsoleNavModel(project: string, pathname: string, search: { env?: unknown }): ConsoleNavModel {
  const page = useMemo<ConsolePage>(() => consolePageOf(pathname)?.page ?? OVERVIEW, [pathname]);

  const { listed, projectId } = useConsoleReliantProject(project);
  const roster = useForgeRoster(listed ? projectId : null, project);

  const settled = listed && !roster.isLoading && !roster.topology.isLoading;
  const known = useMemo(() => (settled ? roster.envs.map((env) => env.name) : null), [settled, roster.envs]);
  const env = resolveEnvScope(typeof search.env === "string" ? search.env : undefined, rememberedEnv(project), known);

  const envs: ConsoleNavEnv[] = useMemo(
    () =>
      roster.envs.map((candidate) => ({
        name: candidate.name,
        preview: candidate.live?.kind === "preview",
        queuedOn: candidate.live && isQueued(candidate.live) ? queuedOnLabel(candidate.live.holds) : undefined,
      })),
    [roster.envs],
  );
  const scope = useMemo(() => scopeOptions(roster.envs), [roster.envs]);

  // Firing alerts in scope, for the badge on Alerts. One org-wide read (shared
  // with the Alerts page's cache), narrowed here to this app's envs.
  const org = useObservabilityOrg();
  const alerts = useAlerts(org.orgId);
  const alertsFiring = useMemo(() => {
    const ids = new Set(
      roster.envs
        .filter((candidate) => candidate.live && (env === ENV_ALL || candidate.name === env))
        .map((candidate) => candidate.live!.id),
    );
    return (alerts.data ?? []).filter((alert) => alert.state === "firing" && ids.has(alert.envId)).length;
  }, [alerts.data, roster.envs, env]);

  // The caller's starred dashboards, nested under Dashboards: the same index
  // (and cache entry) the Dashboards page reads for this scope.
  const scopedEnvId = env === ENV_ALL ? undefined : roster.envs.find((candidate) => candidate.name === env)?.live?.id;
  // An unregistered scoped environment must not accidentally probe the whole app.
  const discoveryScope = useMemo(
    () =>
      org.orgId && settled && (env === ENV_ALL || scopedEnvId)
        ? { orgId: org.orgId, projectId: project, envIds: scopedEnvId ? [scopedEnvId] : [], filters: [] }
        : null,
    [org.orgId, settled, env, project, scopedEnvId],
  );
  const optionalViews = useOptionalViews(discoveryScope);
  const dashboards = useDashboardIndex(org.orgId, project, scopedEnvId);
  const starred = useMemo<ConsoleNavDashboard[]>(
    () =>
      (dashboards.data?.dashboards ?? []).flatMap((d) =>
        d.starred && d.ref.kind !== "template" ? [{ title: displayTitle(d.title, project), dashKind: d.ref.kind, key: d.ref.key }] : [],
      ),
    [dashboards.data, project],
  );
  // A hub's tab is named by the hub and the tab ("Product analytics · Users"); anything else by its title, less the app prefix.
  const dashboardTitle = useMemo(() => {
    if (page.kind !== "dashboard" || !dashboards.data) return undefined;
    const ref = { kind: page.dashKind, key: page.key };
    const hub = hubOf(dashboards.data.dashboards, ref, project);
    const tab = hub && hubTabLabel(hub, ref);
    if (hub && tab) return `${hub.title} · ${tab}`;
    const title = dashboards.data.dashboards.find((d) => d.ref.kind === page.dashKind && d.ref.key === page.key)?.title;
    return title === undefined ? undefined : displayTitle(title, project);
  }, [page, dashboards.data, project]);

  // The Product analytics row leads to the app's hub, when it has one.
  const productAnalytics = useMemo<ProductAnalyticsHub | undefined>(() => {
    const hub = hubsOf(dashboards.data?.dashboards ?? [], project).find((candidate) => candidate.id === "product");
    const first = hub?.tabs[0];
    return hub && first && first.ref.kind !== "template"
      ? { open: { kind: "dashboard", dashKind: first.ref.kind, key: first.ref.key }, keys: hub.tabs.map((tab) => tab.ref.key) }
      : undefined;
  }, [dashboards.data, project]);

  // The app's own dashboards — from its repo, or saved by people in the org —
  // not the generated ones, the templates, the built-ins or a hub's tabs.
  const customDashboards = useMemo<ConsoleNavDashboard[]>(() => {
    const hubKeys = new Set(productAnalytics?.keys ?? []);
    return (dashboards.data?.dashboards ?? []).flatMap((d) =>
      (d.source === "repo" || d.source === "workspace") && !hubKeys.has(d.ref.key) && d.ref.kind !== "template" && d.ref.kind !== "builtin"
        ? [{ title: displayTitle(d.title, project), dashKind: d.ref.kind, key: d.ref.key }]
        : [],
    );
  }, [dashboards.data, project, productAnalytics]);

  // The open dashboard's own scope (the same cached read the page makes): a
  // platform dashboard is not per environment, and the control must not imply it is.
  const openDashboard = useDashboard(
    org.orgId,
    page.kind === "dashboard" && page.dashKind !== "builtin" ? { kind: page.dashKind, projectId: project, key: page.key } : null,
  );
  const platformWide = openDashboard.data?.spec.scope === "platform";

  return {
    project,
    page,
    env,
    envs,
    alertsFiring,
    starred,
    customDashboards,
    dashboardTitle,
    optionalViews,
    productAnalytics,
    listed,
    projectId,
    roster,
    settled,
    scope,
    platformWide,
  };
}
