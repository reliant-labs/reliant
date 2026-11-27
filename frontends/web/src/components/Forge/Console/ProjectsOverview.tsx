// Copyright (c) 2026 Reliant Labs

/**
 * The Overview of several projects (U6b, owner 2026-11-20): one compact row
 * per project — its health, errors and p95 in prod over the last 24 hours,
 * its last release, its cost this month — and a way into that project alone.
 * It is the console's aggregate page for the Overview (aggregate/registry.ts)
 * and the Home at the top of the Projects page (ProjectsOverview).
 */

import { useMemo } from "react";

import Status from "@/components/forge-ui/status";
import { useApps } from "@/hooks/apps-queries";
import { useProjectCost } from "@/hooks/organization-usage-queries";
import { useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import { useTestPortfolio } from "@/hooks/test-queries";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { HostedVerdict } from "@/services/forge/topology";

import { verdictBusy, verdictTone } from "../../Apps/appParts";
import { formatAgo, formatCount, formatDuration } from "../Observability/format";
import { redPanel } from "../Observability/Home/homeModel";
import { coverageTotal, passRateTotal, portfolioProjects } from "../Tests/portfolioModel";
import { AggregatePage, ProjectBreakdown, type AggregateTotal, type BreakdownColumn } from "./aggregate/AggregatePage";
import { DrillDownHint } from "./aggregate/DrillDownHint";
import type { AggregatePageProps } from "./aggregate/registry";
import { useSeveralApps, type SeveralApp } from "./severalProjects";

const VERDICT_LABEL: Partial<Record<HostedVerdict, string>> = {
  converged: "Healthy",
  converging: "Deploying",
  diverged: "Unhealthy",
  degraded: "Degraded",
};

const OVERVIEW_PAGE = { kind: "overview" } as const;

/** The Overview of the projects the shell shows, outside the console (the Projects page's Home). */
export function ProjectsOverview() {
  const several = useSeveralApps();
  if (!several) return null;
  return <ProjectsOverviewPage page={OVERVIEW_PAGE} apps={several.apps} mode={several.mode} />;
}

const COLUMNS: readonly BreakdownColumn[] = [
  { header: "Health" },
  { header: "Errors", align: "end" },
  { header: "p95", align: "end" },
  { header: "Last release", track: "minmax(0,1.5fr)" },
  { header: "Cost", align: "end" },
];

/** QueryScope takes at most this many environments, and project_ids at most this many projects. */
const SCOPE_MAX = 50;

/**
 * The environment a project's row reads its health in: prod, else its first.
 * The Overview's totals read the same environments, so they cover exactly the
 * figures the rows show.
 */
export function healthEnvOf<E extends { name: string }>(summary: { envs: readonly E[] } | undefined): E | undefined {
  return summary?.envs.find((env) => env.name === "prod") ?? summary?.envs[0];
}

/**
 * The scope of the Overview's requests, errors and p95 across the projects
 * in scope: ONE read, the same panel the rows run (homeModel redPanel), over
 * the union of the environments the rows read. A chosen several names its
 * projects (QueryScope.project_ids, control-plane#3957); every project reads
 * the org, narrowed by those environments alone. Null until the org and the
 * environments are known, when no project is deployed, or when the scope is
 * larger than one read may name.
 */
export function useOverviewRedScope(apps: readonly SeveralApp[], mode: "many" | "all"): ObsScope | null {
  const { apps: summaries } = useApps();
  const org = useObservabilityOrg();
  return useMemo(() => {
    if (!org.orgId) return null;
    const projects = new Set<string>();
    const envIds = new Set<string>();
    for (const project of apps) {
      if (!project.deployed) continue;
      const env = healthEnvOf(summaries.find((summary) => summary.name === project.app));
      if (!env) continue;
      projects.add(project.app);
      envIds.add(env.id);
    }
    if (envIds.size === 0 || envIds.size > SCOPE_MAX || projects.size > SCOPE_MAX) return null;
    return {
      orgId: org.orgId,
      ...(mode === "many" ? { projectIds: [...projects].sort() } : {}),
      envIds: [...envIds].sort(),
      filters: [],
    };
  }, [org.orgId, apps, summaries, mode]);
}

/**
 * The totals strip's figures over every project in scope, each the
 * backend's: requests' errors and p95 from one observability read across
 * them (useOverviewRedScope), the tests' pass rate and coverage from
 * GetTestPortfolio. Never summed from the rows here. A figure whose read has
 * not answered, or cannot be made, is left out; none at all shows no strip.
 * Cost is not totalled: the rows read each project's own cost, and the org's
 * by-project usage is not read on this page.
 */
function useOverviewTotals(apps: readonly SeveralApp[], mode: "many" | "all"): readonly AggregateTotal[] | undefined {
  const scope = useOverviewRedScope(apps, mode);
  const panel = useMemo(() => redPanel("number"), []);
  const red = usePanelQuery(scope, "24h", panel, []);
  const portfolio = useTestPortfolio(portfolioProjects(apps, mode));
  return useMemo(() => {
    const totals: AggregateTotal[] = [];
    const value = (label: string) => red.data?.series.find((series) => series.label === label)?.value ?? null;
    if (scope && red.data) {
      const errors = value("errors");
      const p95 = value("p95");
      totals.push({ label: "Errors", value: errors === null ? "—" : formatCount(errors) });
      totals.push({ label: "p95", value: p95 === null ? "—" : formatDuration(p95) });
    }
    const tests = portfolio.data?.totals;
    if (tests && tests.projectsWithRuns > 0) {
      totals.push(passRateTotal(tests, "Test pass rate"));
      for (const coverage of tests.coverage) {
        const total = coverageTotal(coverage);
        totals.push({ ...total, label: `Coverage · ${total.label}` });
      }
    }
    return totals.length > 0 ? totals : undefined;
  }, [scope, red.data, portfolio.data]);
}

/** The Overview's aggregate page: a row per project in scope. */
export function ProjectsOverviewPage({ page, apps, mode }: AggregatePageProps) {
  const totals = useOverviewTotals(apps, mode);
  return (
    <AggregatePage
      title="Overview"
      mode={mode}
      count={apps.length}
      coverage="in prod over the last 24 hours; tests on each default branch; cost is this month to date"
      totals={totals}
      hint={<DrillDownHint purpose="see its workloads, what needs attention and its recent releases" projects={apps} page={page} />}
      testId="projects-overview"
    >
      <ProjectBreakdown projects={apps} page={page} columns={COLUMNS} Cells={OverviewCells} rowTestId={(project) => `projects-overview-row-${project.key}`} />
    </AggregatePage>
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
  const prod = healthEnvOf(summary);
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

/** A project's cells: health, errors, p95, last release, cost. */
function OverviewCells({ project }: { project: SeveralApp }) {
  const deployed = !!project.deployed;
  const app = project.app;
  const { prod, errors, p95 } = useAppHealth(app, deployed);
  const deployedAt = prod?.deployedAt ? Date.parse(prod.deployedAt) : NaN;
  // GetProjectCost (U6c): hosted compute, database storage and CDN, month to date.
  const org = useObservabilityOrg();
  const cost = useProjectCost(deployed ? org.orgId : null, deployed ? app : null);
  const mtd = cost.data?.monthToDate?.usdNanos;
  const costText = mtd === undefined ? "—" : `$${(Number(mtd) / 1e9).toFixed(2)}`;

  return (
    <>
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
    </>
  );
}
