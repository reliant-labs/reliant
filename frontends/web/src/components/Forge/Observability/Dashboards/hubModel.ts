// Copyright (c) 2026 Reliant Labs

/**
 * The pure parts of the Dashboards Overview hub: which job runs are failing,
 * which releases are recent, and how an issue count reads. No React, so each
 * rule is tested on its own.
 */

import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { WorkloadJob } from "@/services/controlPlane/observability/types";

import { ENV_ALL } from "../../Console/envScope";

/** The window every hub figure is over: the project Overview's, so the two pages agree. */
export const HUB_RANGE = "24h" as const;

/** How far back the hub looks for releases. */
export const HUB_RELEASE_DAYS = 30;

/** How many rows a hub card lists before "view all". */
export const HUB_ROWS = 5;

/**
 * The jobs whose LATEST run failed, newest first — one row per job (and
 * environment). A job that failed and then succeeded is not failing. `jobs`
 * is the server's list, newest first.
 */
export function failingJobs(jobs: readonly WorkloadJob[], env: string): WorkloadJob[] {
  const latest = new Map<string, WorkloadJob>();
  for (const job of jobs) {
    if (env !== ENV_ALL && job.envName !== env) continue;
    const key = `${job.envName}/${job.workload}`;
    if (!latest.has(key)) latest.set(key, job);
  }
  return [...latest.values()].filter((job) => job.result === "failed");
}

/** The newest promotions first (the ledger answers oldest first), at most `limit`. */
export function recentPromotions(promotions: readonly Promotion[], limit = HUB_ROWS): Promotion[] {
  return [...promotions].sort((a, b) => b.promotedAtMs - a.promotedAtMs).slice(0, limit);
}

/** An issue count from the first page: "50+" when the server has more. */
export function issueCountLabel(count: number, more: boolean): string {
  return more ? `${count}+` : String(count);
}
