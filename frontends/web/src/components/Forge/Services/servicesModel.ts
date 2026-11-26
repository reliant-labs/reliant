// Copyright (c) 2026 Reliant Labs

/**
 * THE SERVICES PAGE'S ROWS — pure, so its rules are tested alone (prototype).
 *
 * One row per thing the app runs, in the scope, from reads the console
 * already makes:
 *
 *   workload   ListWorkloads: a Deployment, StatefulSet, DaemonSet or CronJob,
 *              with the server's health verdict, ready/desired, CPU and memory
 *              (each container's worst replica, summed over the containers),
 *              and the request side joined from GetServiceOverview by name.
 *   job        a Job seen only in its runs (ListWorkloads.jobs): its latest run.
 *   site       a static deployment GetStatus reports (tier `static`).
 *   database   a managed database GetStatus reports (tier `database`).
 *
 * The kind is the Kubernetes kind, said as it is: the control plane does not
 * report forge's service/worker distinction, so a worker is a "Deployment"
 * that serves no requests.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type { MetricCatalogEntry } from "@/services/controlPlane/observability/metricsTypes";
import type { Panel, ServiceHealth, Workload, WorkloadContainer, WorkloadJob } from "@/services/controlPlane/observability/types";
import { domainStatusLine, type DomainState, type DomainStatusLine, type ForgeDomain } from "@/services/forge/domains";
import type { ReleasePromotion } from "@/services/forge/releases";
import { workloadCustomDomain, workloadOpenUrl, type ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../Console/envScope";
import { ENV_KEY, SERVICE_KEY } from "../Observability/attributes";
import { defaultSeriesFor } from "../Observability/Metrics/model/explorerState";
import { HEALTH_STATUS, JOB_STATUS, KIND_LABEL, isJobKind, jobRows, jobStatusOf, latestRunsByWorkload } from "../Workloads/workloadModel";

export interface ContainerFigure {
  container: string;
  /** Cores, the worst replica's latest; null when not reported. */
  cpu: number | null;
  /** Bytes (working set), the worst replica's latest; null when not reported. */
  memory: number | null;
}

interface RowBase {
  key: string;
  name: string;
  envName: string;
  /** The word beside the name: "Deployment", "CronJob", "Static site", "Managed Postgres". */
  kindLabel: string;
  tone: StatusTone;
  status: string;
}

export type ServicesRow =
  | (RowBase & {
      type: "workload";
      workload: Workload;
      /** Ready of desired; "—" for a Job or CronJob, which has no replica claim. */
      ready: string;
      short: boolean;
      /** A Job or CronJob's latest run in the window; null for anything else, or with no run in the window. */
      lastRun: WorkloadJob | null;
      cpu: number | null;
      memory: number | null;
      containers: ContainerFigure[];
      health: ServiceHealth | undefined;
      release: string;
    })
  | (RowBase & { type: "job"; latest: WorkloadJob; release: string })
  | (RowBase & { type: "site"; url: string; release: string } & SiteState)
  | (RowBase & { type: "database"; release: string });

export interface ServicesEnv {
  name: string;
  release: string;
  preview?: boolean;
}

export interface ServicesRowsInput {
  workloads: readonly Workload[];
  jobs: readonly WorkloadJob[];
  telemetry: readonly ServiceHealth[];
  /** GetStatus per environment name, then deployment name. */
  hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>;
  envs: readonly ServicesEnv[];
  /** An environment name or ENV_ALL. */
  env: string;
}

/** A sum that is null only when every part is. */
function sumOf(values: readonly (number | null)[]): number | null {
  return values.some((v) => v !== null) ? values.reduce<number>((acc, v) => acc + (v ?? 0), 0) : null;
}

export function containerFigures(containers: readonly WorkloadContainer[]): ContainerFigure[] {
  return containers.map((c) => ({ container: c.container, cpu: c.cpu?.latest ?? null, memory: c.memory?.latest ?? null }));
}

/** The telemetry row for `name` in `envName`: grouped by environment name when the scope is every environment. */
export function telemetryFor(telemetry: readonly ServiceHealth[], name: string, envName: string): ServiceHealth | undefined {
  return telemetry.find((t) => t.name === name && (t.group[ENV_KEY] === undefined || t.group[ENV_KEY] === envName));
}

const VERDICT: Record<string, { tone: StatusTone; label: string }> = {
  converged: { tone: "success", label: "Live" },
  converging: { tone: "neutral", label: "Deploying" },
  diverged: { tone: "warning", label: "Drifted" },
  degraded: { tone: "danger", label: "Degraded" },
};

export function verdictStatus(hosted: ForgeHostedWorkload): { tone: StatusTone; label: string } {
  return VERDICT[hosted.verdict ?? ""] ?? { tone: "neutral", label: "Unknown" };
}

/** Worst first, then workloads before jobs, sites and the database, then by name and environment. */
const TONE_RANK: Record<StatusTone, number> = { danger: 0, warning: 1, neutral: 2, success: 3 };
const TYPE_RANK: Record<ServicesRow["type"], number> = { workload: 0, job: 1, site: 2, database: 3 };

export function servicesRows({ workloads, jobs, telemetry, hosted, envs, env }: ServicesRowsInput): ServicesRow[] {
  const inScope = (envName: string) => (env === ENV_ALL ? !envs.find((e) => e.name === envName)?.preview : envName === env);
  const releaseOf = (envName: string) => envs.find((e) => e.name === envName)?.release ?? "";
  const rows: ServicesRow[] = [];
  const seen = new Set<string>();
  const latestRuns = latestRunsByWorkload(jobs);
  for (const w of workloads) {
    if (!inScope(w.envName)) continue;
    const key = `${w.envName}/${w.workload}`;
    seen.add(key);
    // A Job or CronJob is judged on its runs, never on ready of desired: no
    // pod running after a completed run is its normal state, not a shortfall.
    const runs = isJobKind(w.kind);
    const lastRun = runs ? (latestRuns.get(key) ?? null) : null;
    const status = runs ? jobStatusOf(w, lastRun) : HEALTH_STATUS[w.health];
    const containers = containerFigures(w.containers);
    rows.push({
      type: "workload",
      key: `w:${key}`,
      name: w.workload,
      envName: w.envName,
      kindLabel: KIND_LABEL[w.kind] || "Workload",
      tone: status.tone,
      status: status.label,
      workload: w,
      ready: runs ? "—" : w.desiredReplicas !== null ? `${w.readyReplicas ?? 0}/${w.desiredReplicas}` : w.readyReplicas !== null ? String(w.readyReplicas) : "—",
      short: !runs && w.desiredReplicas !== null && (w.readyReplicas ?? 0) < w.desiredReplicas,
      lastRun,
      cpu: sumOf(containers.map((c) => c.cpu)),
      memory: sumOf(containers.map((c) => c.memory)),
      containers,
      health: telemetryFor(telemetry, w.workload, w.envName),
      release: w.releaseVersion || releaseOf(w.envName),
    });
  }
  for (const job of jobRows(jobs, ENV_ALL)) {
    if (!inScope(job.envName) || seen.has(`${job.envName}/${job.workload}`)) continue;
    const status = JOB_STATUS[job.latest.result];
    rows.push({
      type: "job",
      key: `j:${job.key}`,
      name: job.workload,
      envName: job.envName,
      kindLabel: "Job",
      tone: status.tone,
      status: status.label,
      latest: job.latest,
      release: job.latest.releaseVersion || releaseOf(job.envName),
    });
  }
  for (const [envName, byName] of hosted) {
    if (!inScope(envName)) continue;
    for (const deployment of byName.values()) {
      if (!deployment.name) continue;
      const verdict = verdictStatus(deployment);
      if (deployment.tier === "static") {
        rows.push({
          type: "site",
          key: `s:${envName}/${deployment.name}`,
          name: deployment.name,
          envName,
          kindLabel: "Static site",
          tone: verdict.tone,
          status: verdict.label,
          url: deployment.url || (deployment.hostname ? `https://${deployment.hostname}` : ""),
          release: releaseOf(envName),
          ...siteStateOf(deployment),
        });
      } else if (deployment.tier === "database") {
        rows.push({
          type: "database",
          key: `d:${envName}/${deployment.name}`,
          name: deployment.name,
          envName,
          kindLabel: "Managed Postgres",
          tone: verdict.tone,
          status: verdict.label === "Live" ? "Provisioned" : verdict.label,
          release: releaseOf(envName),
        });
      }
    }
  }
  return rows.sort(
    (a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone] || TYPE_RANK[a.type] - TYPE_RANK[b.type] || a.name.localeCompare(b.name) || a.envName.localeCompare(b.envName)
  );
}

/** A Job or CronJob row's latest run in the window (a workload's own, or a job's seen only in its runs); null for anything else. */
export function runOfRow(row: ServicesRow): WorkloadJob | null {
  if (row.type === "job") return row.latest;
  return row.type === "workload" ? row.lastRun : null;
}

/** Whether a row is a Job or CronJob: what has no replica claim and is judged on its runs. */
export function runsToCompletion(row: ServicesRow): boolean {
  return row.type === "job" || (row.type === "workload" && isJobKind(row.workload.kind));
}

/** The database deployments GetStatus reports, by environment: what the Services page's database row and page read. */
export function databasesOf(hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>, name?: string): { envName: string; deployment: ForgeHostedWorkload }[] {
  const out: { envName: string; deployment: ForgeHostedWorkload }[] = [];
  for (const [envName, byName] of hosted) {
    for (const deployment of byName.values()) {
      if (deployment.tier === "database" && (!name || deployment.name === name)) out.push({ envName, deployment });
    }
  }
  return out;
}

/** The static deployments named `name`, by environment. */
export function sitesOf(hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>, name: string): { envName: string; deployment: ForgeHostedWorkload }[] {
  const out: { envName: string; deployment: ForgeHostedWorkload }[] = [];
  for (const [envName, byName] of hosted) {
    const deployment = byName.get(name);
    if (deployment?.tier === "static") out.push({ envName, deployment });
  }
  return out;
}

// ── A static site ───────────────────────────────────────────────────────────

/** One custom domain a site answers on, as GetStatus reports it. */
export interface SiteDomain {
  hostname: string;
  state: DomainState;
  /** Where its DNS points: edge | ingress | elsewhere | unknown. */
  servingPath: string;
}

/** What GetStatus says about one static site in one environment beyond its verdict. */
export interface SiteState {
  /** The platform observed files other than the ones the published release asks for. */
  drifted: boolean;
  /** The control plane's last error, as reported (history too: callers decide when it still matters). */
  lastError: string;
  /** Why it is suspended (owner | no_compute_plan | billing_lapsed | claimable_expired); "" when not, or not reported. */
  suspendReason: string;
  domains: SiteDomain[];
}

const DOMAIN_STATES: readonly DomainState[] = ["pending-dns", "verifying", "issuing", "live", "failed", "conflict", "unknown"];

export function siteStateOf(deployment: ForgeHostedWorkload): SiteState {
  const reason = (deployment.suspend_reason ?? "").trim();
  return {
    drifted: deployment.drifted === true,
    lastError: (deployment.last_error ?? "").trim(),
    suspendReason: reason === "unspecified" ? "" : reason,
    domains: (deployment.domains ?? [])
      .filter((d) => (d.hostname ?? "").trim() !== "")
      .map((d) => ({
        hostname: d.hostname.trim().toLowerCase(),
        state: (DOMAIN_STATES as readonly string[]).includes(d.state) ? (d.state as DomainState) : "unknown",
        servingPath: d.serving_path || "unknown",
      })),
  };
}

/** One environment of a static site: what its page shows for it. */
export interface SiteEnv extends SiteState {
  envName: string;
  /** The environment's control-plane id; "" when the console's environments do not list it. */
  envId: string;
  /** The release live there; "" when nothing was promoted. */
  release: string;
  tone: StatusTone;
  status: string;
  /** The address to open it at: its live custom domain, else the platform's URL; "" with neither. */
  url: string;
  /** Its live custom domain (workloadCustomDomain); "" when none serves. */
  customDomain: string;
  deployment: ForgeHostedWorkload;
}

/** A site's environments, in the console's order (prod leads); environments it lists not, last. */
export function siteEnvs(entries: readonly { envName: string; deployment: ForgeHostedWorkload }[], envs: readonly { id: string; name: string; release: string }[]): SiteEnv[] {
  const rank = (envName: string) => {
    const index = envs.findIndex((e) => e.name === envName);
    return index < 0 ? envs.length : index;
  };
  return [...entries]
    .sort((a, b) => rank(a.envName) - rank(b.envName))
    .map(({ envName, deployment }) => {
      const env = envs.find((e) => e.name === envName);
      const verdict = verdictStatus(deployment);
      return {
        envName,
        envId: env?.id ?? "",
        release: env?.release ?? "",
        tone: verdict.tone,
        status: verdict.label,
        url: workloadOpenUrl(deployment),
        customDomain: workloadCustomDomain(deployment),
        deployment,
        ...siteStateOf(deployment),
      };
    });
}

/** The environment a site's page opens on: the scope's when the site runs there, else the first with a live custom domain, else the first. */
export function defaultSiteEnv(sites: readonly SiteEnv[], scopeEnv: string): SiteEnv | null {
  return sites.find((s) => s.envName === scopeEnv) ?? sites.find((s) => s.customDomain !== "") ?? sites[0] ?? null;
}

/** One row of a site's Domains section. */
export interface SiteDomainRow extends SiteDomain {
  /** Where it is in setup, or why it is stuck (DomainService's record); null when the badge says everything. */
  statusLine: DomainStatusLine | null;
}

/**
 * The custom domains a site answers on in one environment: GetStatus's (with
 * where their DNS points), joined by hostname with the org's DomainService
 * records bound to this site there. The org's state wins when both have one:
 * it is re-read while a domain converges. Live first, then apex before
 * subdomains, then by name.
 */
export function siteDomainRows(site: Pick<SiteEnv, "domains" | "envId">, name: string, orgDomains: readonly ForgeDomain[]): SiteDomainRow[] {
  const rows = new Map<string, SiteDomainRow>();
  for (const d of site.domains) rows.set(d.hostname, { ...d, statusLine: null });
  for (const d of orgDomains) {
    const hostname = d.hostname.trim().toLowerCase();
    const bound = d.binding?.target === name && !!site.envId && d.binding.environmentId === site.envId;
    const known = rows.get(hostname);
    if (!bound && !known) continue;
    rows.set(hostname, { hostname, state: d.state, servingPath: known?.servingPath ?? "unknown", statusLine: domainStatusLine(d) });
  }
  return [...rows.values()].sort(
    (a, b) => Number(b.state === "live") - Number(a.state === "live") || a.hostname.split(".").length - b.hostname.split(".").length || a.hostname.localeCompare(b.hostname)
  );
}

/** The artifact prefix a static site's release artifacts carry (`sites/<name>/<version>`). */
export function siteArtifactPrefix(name: string): string {
  return `sites/${name}/`;
}

/**
 * Whether a promotion moved `name`: a scoped deploy names its targets, and
 * (with `artifactPrefix`) a promotion that froze artifacts must have frozen
 * one of it, by its name or under the prefix. A promotion that froze none (an
 * older control plane) moved everything.
 */
export function promotionMoves(promotion: Pick<ReleasePromotion, "targets" | "resolvedArtifacts">, name: string, artifactPrefix?: string): boolean {
  if (promotion.targets.length > 0 && !promotion.targets.includes(name)) return false;
  if (!artifactPrefix) return true;
  const artifacts = Object.keys(promotion.resolvedArtifacts ?? {});
  return artifacts.length === 0 || artifacts.some((artifact) => artifact === name || artifact.startsWith(artifactPrefix));
}

/** Sum per-container series sharing timestamps into one (the workload's), by bucket. */
export function sumSeries(series: readonly { timestamps: number[]; values: (number | null)[] }[]): { timestamps: number[]; values: (number | null)[] } | null {
  if (series.length === 0) return null;
  const longest = [...series].sort((a, b) => b.timestamps.length - a.timestamps.length)[0]!;
  const index = series.map((s) => new Map(s.timestamps.map((t, i) => [t, s.values[i] ?? null])));
  return { timestamps: longest.timestamps, values: longest.timestamps.map((t) => sumOf(index.map((m) => m.get(t) ?? null))) };
}

/** The app's own metrics: declared in its schema, or sent with no forge or infrastructure prefix. */
export function customMetrics(metrics: readonly MetricCatalogEntry[], service?: string): MetricCatalogEntry[] {
  return metrics.filter((m) => (m.group === "declared" || m.group === "other") && (!service || m.services.length === 0 || m.services.includes(service)));
}

export function metricPanel(entry: MetricCatalogEntry, service?: string): Panel {
  return {
    id: `custom-metric-${entry.name}`,
    title: entry.name,
    kind: "line",
    signal: "metrics",
    series: [defaultSeriesFor(entry, "A")],
    where: service ? [{ field: SERVICE_KEY, op: "eq", value: service }] : undefined,
  };
}

