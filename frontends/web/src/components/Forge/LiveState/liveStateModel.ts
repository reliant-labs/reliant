// Copyright (c) 2026 Reliant Labs

/**
 * AN APP'S LIVE STATE, as one model (U6c, owner 2026-11-20): what every
 * place that shows "how is my stuff running" reads, so the Services tab, the
 * Overview's live-state strip and the built-in Live state dashboard always
 * agree. Pure, so the merge is tested on its own (liveStateModel.test.ts);
 * the reads are useAppLiveState.ts.
 *
 *   environments   the live view (DeployService.GetLiveView): each recorded
 *                  environment and the release bound there. A local one
 *                  (forge env up) runs on a laptop and has nothing to watch.
 *   rows           servicesRows (Services/servicesModel.ts): one per workload
 *                  (ListWorkloads, with GetServiceOverview's request side
 *                  joined by name and environment), per job seen only in its
 *                  runs, per static site and per managed database (GetStatus).
 *   database       the managed database's row carries no live status yet:
 *                  the operator's CR status is not exposed (project-data-map
 *                  §2), so it says so rather than guessing.
 */

import type { ServiceHealth, Workload, WorkloadJob } from "@/services/controlPlane/observability/types";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../Console/envScope";
import { servicesRows, type ServicesRow } from "../Services/servicesModel";

/** One recorded environment of the app, as the live state addresses it. */
export interface LiveStateEnv {
  /** The control plane's environment id. */
  id: string;
  name: string;
  /** The release bound there, "" before the first promotion. */
  release: string;
  preview: boolean;
}

/** What the live view says of one environment (services/forge/live.ts LiveEnv, the fields the merge reads). */
export interface LiveViewEnv {
  id: string;
  name: string;
  kind: string;
  release: string;
}

/** The live view's environments the live state watches: every recorded one but a local one, prod first. */
export function liveStateEnvs(live: readonly LiveViewEnv[]): LiveStateEnv[] {
  return live
    .filter((env) => env.kind !== "local")
    .map((env) => ({ id: env.id, name: env.name, release: env.release, preview: env.kind === "preview" }))
    .sort((a, b) => Number(b.name === "prod") - Number(a.name === "prod") || Number(a.preview) - Number(b.preview) || a.name.localeCompare(b.name));
}

/** The words the database row shows until the platform reports a database's live status. */
export const DATABASE_STATUS_NOTE = "Status coming soon";

export type LiveStateRow = ServicesRow & {
  /** Set when the row's status is not a live reading (the managed database): what to say instead. */
  statusNote?: string;
};

export interface LiveStateInput {
  envs: readonly LiveStateEnv[];
  /** An environment name or ENV_ALL. */
  env: string;
  workloads: readonly Workload[];
  jobs: readonly WorkloadJob[];
  telemetry: readonly ServiceHealth[];
  /** GetStatus per environment name, then deployment name. */
  hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>;
}

export interface LiveStateModel {
  rows: LiveStateRow[];
  /** Rows whose status is a warning or worse. */
  attention: number;
  /** The environment in scope, or null when every environment is (or the named one is not recorded). */
  scopedEnv: LiveStateEnv | null;
}

/** The merge: every read into one row per thing the app runs, in the scope, worst first. */
export function mergeLiveState({ envs, env, workloads, jobs, telemetry, hosted }: LiveStateInput): LiveStateModel {
  const rows: LiveStateRow[] = servicesRows({ workloads, jobs, telemetry, hosted, envs, env }).map((row) =>
    row.type === "database" ? { ...row, tone: "neutral", statusNote: DATABASE_STATUS_NOTE } : row
  );
  return {
    rows,
    attention: rows.filter((row) => row.tone === "danger" || row.tone === "warning").length,
    scopedEnv: env === ENV_ALL ? null : (envs.find((candidate) => candidate.name === env) ?? null),
  };
}
