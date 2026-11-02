// Copyright (c) 2026 Reliant Labs

/**
 * Which group a dashboard sits in on the Dashboards page, and the Forge
 * workload it is about — pure, so the rule is tested on its own.
 *
 * The groups, in the order the page shows them:
 *
 *   overview    the project's generated overview
 *   services    one dashboard per long-running service, worker or operator
 *   jobs        the consolidated dashboard of every job and cron job
 *   product     frontends and product analytics (product-*)
 *   platform    workloads, Reliant's own self-monitoring (selfmon-*), built-ins
 *   releases    release health
 *   custom      everything a team wrote itself
 *
 * The control plane names the group (`DashboardSummary.group`, from the spec's
 * `group` field, defaulted server-side). A server that predates the field
 * sends none, so `groupOf` falls back to what the key and source say; once
 * every server sends it the fallback only ever sees "custom" dashboards.
 */

import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

export const DASHBOARD_GROUPS = ["overview", "services", "jobs", "product", "platform", "releases", "custom"] as const;
export type DashboardGroup = (typeof DASHBOARD_GROUPS)[number];

export const GROUP_LABELS: Record<DashboardGroup, string> = {
  overview: "Overview",
  services: "Services",
  jobs: "Jobs",
  product: "Product",
  platform: "Platform",
  releases: "Releases",
  custom: "Custom",
};

export const GROUP_ABOUT: Record<DashboardGroup, string> = {
  overview: "The whole project at a glance",
  services: "One per long-running service, worker and operator",
  jobs: "Every job and cron job, on one dashboard",
  product: "Frontends and product analytics",
  platform: "Workloads, self-monitoring and organization views",
  releases: "How each release changed errors and latency",
  custom: "Dashboards your team wrote",
};

export function isDashboardGroup(value: unknown): value is DashboardGroup {
  return typeof value === "string" && (DASHBOARD_GROUPS as readonly string[]).includes(value);
}

/**
 * The group a dashboard belongs to. The server's word wins; an unknown one
 * is "custom" (the contract's read rule), and only an absent one — an older
 * control plane — is inferred.
 */
export function groupOf(d: Pick<DashboardSummary, "group" | "ref" | "source">): DashboardGroup {
  if (d.group) return isDashboardGroup(d.group) ? d.group : "custom";
  return inferredGroup(d);
}

/** What an older server's dashboard most likely is, from its key and source. */
export function inferredGroup(d: Pick<DashboardSummary, "ref" | "source">): DashboardGroup {
  const key = d.ref.key.toLowerCase();
  if (d.source === "builtin") return "platform";
  if (key.startsWith("selfmon-")) return "platform";
  if (key.startsWith("product-")) return "product";
  if (d.source !== "generated") return "custom";
  if (key === "overview" || key.endsWith("-overview")) return "overview";
  if (key === "jobs" || key.startsWith("job-") || key.startsWith("cron-") || key.startsWith("cronjob-")) return "jobs";
  if (key === "workloads") return "platform";
  if (key === "releases") return "releases";
  if (key === "frontend" || key.startsWith("frontend-")) return "product";
  if (key === "custom-metrics") return "custom";
  // Every other generated dashboard is forge's per-workload one.
  return "services";
}

/**
 * The Forge workload a generated per-workload dashboard is about: the id is
 * `process-<workload>` (`service-<workload>` before the rename). Null for
 * anything else — a workload is never guessed from a title.
 */
export function workloadOf(d: Pick<DashboardSummary, "ref" | "source">): string | null {
  if (d.source !== "generated") return null;
  const match = /^(?:process|service)-(.+)$/.exec(d.ref.key);
  return match ? match[1]! : null;
}

export interface GroupedDashboards {
  group: DashboardGroup;
  dashboards: DashboardSummary[];
}

/**
 * The index in group order. Inside a group: built-in, then generated, repo
 * and workspace dashboards, each in the order the server lists them. Every
 * group is returned, empty ones included, so a caller decides whether to show
 * them.
 */
export function groupIndex(dashboards: readonly DashboardSummary[]): GroupedDashboards[] {
  const buckets = new Map<DashboardGroup, DashboardSummary[]>(DASHBOARD_GROUPS.map((g) => [g, []]));
  for (const d of dashboards) buckets.get(groupOf(d))!.push(d);
  return DASHBOARD_GROUPS.map((group) => ({
    group,
    // Array.prototype.sort is stable: equal ranks keep the server's order.
    dashboards: [...buckets.get(group)!].sort((a, b) => sourceRank(a) - sourceRank(b)),
  }));
}

const SOURCE_RANK: Record<DashboardSummary["source"], number> = { builtin: 0, generated: 1, repo: 2, workspace: 3, template: 4 };
const sourceRank = (d: DashboardSummary) => SOURCE_RANK[d.source] ?? 5;

/** The counts per group, for the hub's link cards and the section headers. */
export function groupCounts(dashboards: readonly DashboardSummary[]): Record<DashboardGroup, number> {
  const counts = Object.fromEntries(DASHBOARD_GROUPS.map((g) => [g, 0])) as Record<DashboardGroup, number>;
  for (const d of dashboards) counts[groupOf(d)] += 1;
  return counts;
}
