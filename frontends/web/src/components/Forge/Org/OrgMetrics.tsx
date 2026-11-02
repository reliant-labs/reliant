// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Metrics, org-wide: the app console's metrics explorer over
 * every app's telemetry, narrowed by the shared `apps` and `envs` URL keys (the
 * same pickers, and the same narrowing, as Logs and Traces here). The query
 * scope names no project unless exactly one app is picked, which is how the
 * org's other views read too (ObservabilityPanel).
 *
 * Reading and linking only: a dashboard, an alert rule and a CLI command each
 * belong to one app, so those actions stay in that app's console.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { ENV_ALL } from "../Console/envScope";
import { orgPath, withSearch } from "../Console/paths";
import { MetricsExplorer, type MetricsExplorerHrefs, type MetricsExplorerOrg } from "../Observability/Metrics/MetricsExplorerPage";
import { envIdsOf, orgPickerEnvironments, orgSelectionIsEmpty, type ObsEnvironment, type ObsPlace } from "../Observability/obsPlace";
import { orgDashboardHref } from "./OrgDashboards";

const strings = (value: unknown): string[] => (Array.isArray(value) ? value.map(String) : typeof value === "string" ? [value] : []);

export default function OrgMetrics({ environments, apps }: { environments: ObsEnvironment[]; apps: string[] }) {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const onSearch = useCallback(
    (next: Record<string, unknown>) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace: true,
      }),
    [navigate]
  );

  const selectedApps = useMemo(() => strings(search.apps), [search.apps]);
  const selectedEnvs = useMemo(() => strings(search.envs), [search.envs]);
  const place = useMemo<ObsPlace>(() => ({ kind: "org", environments, apps }), [environments, apps]);
  const pickerEnvironments = useMemo(() => orgPickerEnvironments(place, selectedApps), [place, selectedApps]);

  const org = useMemo<MetricsExplorerOrg>(
    () => ({
      apps,
      selectedApps,
      onAppsChange: (names) => {
        // Picked environments that no longer belong to a selected app are dropped with it.
        const inApps = new Set(orgPickerEnvironments(place, names).map((env) => env.name));
        const envs = selectedEnvs.filter((name) => inApps.has(name));
        onSearch({ apps: names.length ? names : undefined, envs: envs.length ? envs : undefined });
      },
      pickerEnvironments,
      selectedEnvs,
      onEnvsChange: (names) => onSearch({ envs: names.length ? names : undefined }),
      envIds: envIdsOf(place, selectedEnvs, selectedApps),
      empty: orgSelectionIsEmpty(place, selectedEnvs, selectedApps),
    }),
    [apps, selectedApps, selectedEnvs, pickerEnvironments, place, onSearch]
  );

  // One picked environment drives the single-environment features (compare to a release).
  const picked = pickerEnvironments.filter((env) => selectedEnvs.includes(env.name));
  const env = picked.length === 1 ? picked[0].name : ENV_ALL;
  const project = selectedApps.length === 1 && selectedApps[0] ? selectedApps[0] : undefined;

  const hrefs = useMemo<MetricsExplorerHrefs>(
    () => ({
      trace: (traceId) => withSearch(orgPath({ kind: "obs-view", view: "traces" }), { trace: traceId, range: "7d", envs: selectedEnvs.length ? selectedEnvs : undefined }),
      dashboard: (ref) => orgDashboardHref(ref),
    }),
    [selectedEnvs]
  );

  return (
    <div data-testid="org-metrics">
      <MetricsExplorer
        project={project}
        org={org}
        env={env}
        environments={pickerEnvironments}
        search={search}
        onSearch={onSearch}
        hrefs={hrefs}
      />
    </div>
  );
}
