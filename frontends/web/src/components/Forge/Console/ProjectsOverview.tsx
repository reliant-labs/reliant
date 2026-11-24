// Copyright (c) 2026 Reliant Labs

/**
 * Home while the shell shows several projects (U6b, owner 2026-11-20), at
 * the top of the Projects page: one compact row per project — its health, errors and p95 in prod over the
 * last 24 hours, its last release — and a way into that project alone.
 */

import { useMemo } from "react";
import { ChevronRight } from "lucide-react";

import Status from "@/components/forge-ui/status";
import { useApps } from "@/hooks/apps-queries";
import { useProjectCost } from "@/hooks/organization-usage-queries";
import { useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import type { HostedVerdict } from "@/services/forge/topology";
import type { ProjectCatalogRow } from "@/services/projects/catalog";

import { verdictBusy, verdictTone } from "../../Apps/appParts";
import { ProjectMark } from "../../Layout/ProjectMark";
import { navigateWithShellScope } from "../../Layout/shellScopeNav";
import { deployedAppOf, sectionsAppOf, useShellLastPick, useShellSeveral } from "../../Layout/sidebar/shellProject";
import { formatAgo, formatCount, formatDuration } from "../Observability/format";
import { redPanel } from "../Observability/Home/homeModel";
import { consolePath } from "./paths";

const VERDICT_LABEL: Partial<Record<HostedVerdict, string>> = {
  converged: "Healthy",
  converging: "Deploying",
  diverged: "Unhealthy",
  degraded: "Degraded",
};

export function ProjectsOverview() {
  const { mode, rows } = useShellSeveral();
  if (!mode) return null;
  return (
    <div className="mx-auto max-w-[var(--page-max)]" data-testid="projects-overview">
      <h1 className="text-section text-ink">Overview</h1>
      <p className="mt-1 text-ui text-ink-muted">
        {mode === "all" ? "Every project" : `${rows.length} projects`}, in prod over the last 24 hours; cost is this month to date.
      </p>
      <div className="mt-5 overflow-hidden rounded-xl border border-border">
        <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.5fr)_minmax(0,1fr)_2rem] items-center gap-4 border-b border-border px-4 py-2 text-meta text-ink-muted">
          <span>Project</span>
          <span>Health</span>
          <span className="text-right">Errors</span>
          <span className="text-right">p95</span>
          <span>Last release</span>
          <span className="text-right">Cost</span>
          <span />
        </div>
        {rows.map((row) => (
          <ProjectRow key={row.key} row={row} />
        ))}
      </div>
    </div>
  );
}

/**
 * An app's health in prod over the last 24 hours: its prod environment (the
 * verdict, the release live there) and its requests, errors and p95. Shared by
 * the Overview of several projects and a project's Home.
 */
export function useAppHealth(app: string, deployed: boolean) {
  const { apps, loading } = useApps();
  const summary = apps.find((candidate) => candidate.name === app);
  const prod = summary?.envs.find((env) => env.name === "prod") ?? summary?.envs[0];
  const org = useObservabilityOrg();
  const scope = useMemo(
    () => (org.orgId && prod ? { orgId: org.orgId, projectId: app, envIds: [prod.id], filters: [] } : null),
    [org.orgId, prod, app],
  );
  const panel = useMemo(() => redPanel("number"), []);
  const totals = usePanelQuery(deployed ? scope : null, "24h", panel, []);
  const value = (label: string) => totals.data?.series.find((series) => series.label === label)?.value ?? null;
  // The org's environments answered and none is this app's: nothing is deployed, whatever the catalog said.
  const noEnvironments = !loading && !prod;
  return { prod, noEnvironments, requests: value("requests"), errors: value("errors"), p95: value("p95") };
}

export const VERDICT_WORDS = VERDICT_LABEL;

function ProjectRow({ row }: { row: ProjectCatalogRow }) {
  const setLastPick = useShellLastPick((state) => state.set);
  const deployed = deployedAppOf(row);
  const app = sectionsAppOf(row) ?? row.name;
  const { prod, errors, p95 } = useAppHealth(app, !!deployed);
  const href = consolePath(app, { kind: "overview" });
  const deployedAt = prod?.deployedAt ? Date.parse(prod.deployedAt) : NaN;
  // GetProjectCost (U6c): hosted compute, database storage and CDN, month to date.
  const org = useObservabilityOrg();
  const cost = useProjectCost(deployed ? org.orgId : null, deployed ? app : null);
  const mtd = cost.data?.monthToDate?.usdNanos;
  const costText = mtd === undefined ? "—" : `$${(Number(mtd) / 1e9).toFixed(2)}`;

  return (
    <a
      href={href}
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
        event.preventDefault();
        // Into this project alone: the shell's one project, on its app's Overview.
        setLastPick(row.key);
        navigateWithShellScope(undefined, { to: href });
      }}
      data-testid={`projects-overview-row-${row.key}`}
      className="no-color grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.5fr)_minmax(0,1fr)_2rem] items-center gap-4 border-b border-border-subtle px-4 py-3 text-ui text-ink transition-colors last:border-b-0 hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      <span className="flex min-w-0 items-center gap-2">
        <ProjectMark name={row.name} />
        <span className="min-w-0 truncate font-medium">{row.name}</span>
        {app !== row.name && <span className="min-w-0 truncate text-meta text-ink-muted">{app}</span>}
      </span>
      <span className="min-w-0">
        {!deployed ? (
          <span className="text-ink-muted">Not deployed</span>
        ) : prod ? (
          <Status tone={verdictTone(prod.verdict)} busy={verdictBusy(prod.verdict)} label={VERDICT_LABEL[prod.verdict] ?? "Unknown"} />
        ) : (
          <span className="text-ink-muted">No data</span>
        )}
      </span>
      <span className="text-right tabular-nums">{errors === null ? "—" : formatCount(errors)}</span>
      <span className="text-right tabular-nums">{p95 === null ? "—" : formatDuration(p95)}</span>
      <span className="min-w-0 truncate">
        {prod?.release ? (
          <>
            <span className="font-mono text-code">{prod.release}</span>
            {Number.isFinite(deployedAt) && <span className="text-meta text-ink-muted"> · {formatAgo(deployedAt)}</span>}
          </>
        ) : (
          <span className="text-ink-muted">None yet</span>
        )}
      </span>
      <span className="text-right tabular-nums">{costText}</span>
      <ChevronRight className="size-4 justify-self-end text-ink-muted" aria-hidden="true" />
    </a>
  );
}
