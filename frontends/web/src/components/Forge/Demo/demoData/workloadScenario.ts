// Copyright (c) 2025 Reliant Labs

/** Reconcile the small observability workload fixtures with the demo's one
 * release ledger. Never change production UI to compensate for fixture drift.
 */
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Workload } from "@/services/controlPlane/observability/types";

import { demoBundleId } from "./configScenario";
import { DEMO_RELEASES, DEMO_WORKLOADS, RUNNING } from "./consoleScenario";
import { DEMO_ENVS, demoEnvId } from "./demoScope";

function currentWorkload(row: Workload, detail: boolean): Workload {
  const envId = demoEnvId(row.envId);
  if (!envId) return row;
  const env = DEMO_ENVS.find((e) => e.id === envId)!;
  const current = RUNNING[envId];
  const release = DEMO_RELEASES.find((r) => r.version === current.version)!;
  const declared = DEMO_WORKLOADS.find((w) => w.name === row.workload);
  return {
    ...row, serviceVersion: current.version, releaseVersion: current.version,
    containers: row.containers.map((container) => ({ ...container, imageTag: current.version })),
    declared: detail && declared ? {
      name: row.workload, kind: row.kind, specRecorded: true, specGap: null, replicas: declared.replicas,
      cpuRequestMillicores: 100, cpuLimitMillicores: 1000,
      memoryRequestBytes: 128 * 1024 * 1024, memoryLimitBytes: 512 * 1024 * 1024,
      ports: row.workload === "api" ? [{ name: "http", port: 8080, protocol: "TCP" }] : [],
      envNames: ["LOG_LEVEL", "ORDER_TIMEOUT_MS", "DATABASE_URL", "PAYMENTS_API_KEY"],
      image: `registry.example.com/shop/${row.workload}:${current.version}`,
      sourcePath: `deploy/kcl/${env.name}/main.k`, sourceRepo: "github.com/acme/shop", sourceCommit: release.commit,
      bundleId: demoBundleId(envId, current.version),
    } : null,
  };
}

export function withDemoWorkloadLedger(base: ObservabilityApi): ObservabilityApi {
  return {
    ...base,
    listWorkloads: async (req) => {
      const result = await base.listWorkloads(req);
      return { ...result, workloads: result.workloads.map((row) => currentWorkload(row, false)) };
    },
    getWorkload: async (req) => {
      const result = await base.getWorkload(req);
      return { ...result, workloads: result.workloads.map((row) => currentWorkload(row, true)) };
    },
  };
}
