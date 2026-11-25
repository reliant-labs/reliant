// Copyright (c) 2026 Reliant Labs

/**
 * The live-state merge (liveStateModel.ts): the one model the Services tab,
 * the Overview's strip and Services' charts read, so they agree.
 */

import { describe, expect, it } from "vitest";

import type { ServiceHealth, Workload } from "@/services/controlPlane/observability/types";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../../Console/envScope";
import { ENV_KEY } from "../../Observability/attributes";
import { FIXTURE_JOBS, fixtureWorkloadList } from "../../Workloads/__fixtures__/workloadFixtures";
import { DATABASE_STATUS_NOTE, liveStateEnvs, mergeLiveState, type LiveStateEnv } from "../liveStateModel";

const ENVS: LiveStateEnv[] = [
  { id: "env-prod", name: "prod", release: "v1.4.0", preview: false },
  { id: "env-e2e", name: "e2e", release: "v1.3.0", preview: false },
  { id: "env-pr-981", name: "pr-981", release: "v1.5.0-rc", preview: true },
];

function health(name: string, env: string | undefined, over: Partial<ServiceHealth> = {}): ServiceHealth {
  return {
    name,
    group: env ? { [ENV_KEY]: env } : {},
    requestCount: 1200,
    requestRate: 2,
    errorCount: 12,
    errorRate: 0.01,
    p50Ms: 20,
    p95Ms: 180,
    p99Ms: 400,
    errorLogCount: 3,
    timestamps: [],
    rateSeries: [],
    errorRateSeries: [],
    requestTrend: [],
    ...over,
  } as ServiceHealth;
}

const hosted = (name: string, tier: string, verdict = "converged"): ForgeHostedWorkload => ({ name, tier, verdict }) as ForgeHostedWorkload;

const HOSTED = new Map([
  [
    "prod",
    new Map([
      ["site", hosted("site", "static")],
      ["db", hosted("db", "database")],
    ]),
  ],
]);

describe("liveStateEnvs", () => {
  it("drops a local environment and puts prod first, previews last", () => {
    const envs = liveStateEnvs([
      { id: "p1", name: "pr-12", kind: "preview", release: "" },
      { id: "l", name: "dev", kind: "local", release: "" },
      { id: "s", name: "staging", kind: "persistent", release: "v2" },
      { id: "p", name: "prod", kind: "persistent", release: "v1" },
    ]);
    expect(envs.map((env) => env.name)).toEqual(["prod", "staging", "pr-12"]);
    expect(envs[2]).toMatchObject({ preview: true });
  });
});

describe("mergeLiveState", () => {
  const { workloads } = fixtureWorkloadList();

  it("joins each workload's request side by name and environment, in one environment", () => {
    const model = mergeLiveState({
      envs: ENVS,
      env: "prod",
      workloads,
      jobs: [],
      telemetry: [health("admin-server", undefined, { requestRate: 7, p95Ms: 95 })],
      hosted: new Map(),
    });
    const admin = model.rows.find((row) => row.name === "admin-server");
    expect(admin?.type).toBe("workload");
    expect(admin?.envName).toBe("prod");
    expect(admin?.type === "workload" && admin.health?.requestRate).toBe(7);
    expect(admin?.type === "workload" && admin.ready).toBe("2/2");
    // Every row is prod's.
    expect(model.rows.every((row) => row.envName === "prod")).toBe(true);
    expect(model.scopedEnv?.id).toBe("env-prod");
  });

  it("across every environment, matches telemetry to its own environment and leaves previews out", () => {
    const model = mergeLiveState({
      envs: ENVS,
      env: ENV_ALL,
      workloads,
      jobs: [],
      telemetry: [health("admin-server", "prod", { requestRate: 7 }), health("admin-server", "e2e", { requestRate: 1 })],
      hosted: new Map(),
    });
    const rate = (env: string) => {
      const row = model.rows.find((candidate) => candidate.name === "admin-server" && candidate.envName === env);
      return row?.type === "workload" ? row.health?.requestRate : undefined;
    };
    expect(rate("prod")).toBe(7);
    expect(rate("e2e")).toBe(1);
    expect(model.rows.some((row) => row.envName === "pr-981")).toBe(false);
    expect(model.scopedEnv).toBeNull();
  });

  it("puts the worst first and counts what needs attention", () => {
    const model = mergeLiveState({ envs: ENVS, env: "prod", workloads, jobs: [], telemetry: [], hosted: new Map() });
    expect(model.rows[0]?.tone).toBe("danger");
    expect(model.attention).toBe(model.rows.filter((row) => row.tone === "danger" || row.tone === "warning").length);
    expect(model.attention).toBeGreaterThan(0);
  });

  it("adds the site and the database, the database saying its status is not reported yet", () => {
    const model = mergeLiveState({ envs: ENVS, env: "prod", workloads: [], jobs: [], telemetry: [], hosted: HOSTED });
    const db = model.rows.find((row) => row.type === "database");
    expect(db).toMatchObject({ name: "db", statusNote: DATABASE_STATUS_NOTE, tone: "neutral", release: "v1.4.0" });
    const site = model.rows.find((row) => row.type === "site");
    expect(site?.name).toBe("site");
    expect(site?.statusNote).toBeUndefined();
    // A database never counts as needing attention: there is no reading to judge.
    expect(model.attention).toBe(0);
  });

  describe("a Job reads its runs, never its replicas (owner, 2026-11-24: a completed job read yellow)", () => {
    // The owner's case: a hosted migrate job declared at one copy, its run completed, no pod running.
    const migrate = (over: Partial<Workload> = {}): Workload => ({
      ...workloads[0]!,
      workload: "control-plane-migrate",
      kind: "job",
      health: "healthy",
      healthReason: "",
      readyReplicas: 0,
      desiredReplicas: 1,
      replicasObserved: 0,
      containers: [],
      ...over,
    });
    const rowOf = (model: ReturnType<typeof mergeLiveState>) => model.rows.find((row) => row.name === "control-plane-migrate")!;

    it("a completed job with nothing running is green, says when it ran, and shows no ready count", () => {
      // Even against a server that still judged it on replicas (degraded, "0 of 1 ready"), the run decides.
      for (const health of ["healthy", "degraded"] as const) {
        const model = mergeLiveState({ envs: ENVS, env: "prod", workloads: [migrate({ health })], jobs: FIXTURE_JOBS, telemetry: [], hosted: new Map() });
        const row = rowOf(model);
        expect(row).toMatchObject({ type: "workload", tone: "success", status: "Succeeded", ready: "—", short: false });
        expect(row.type === "workload" && row.lastRun?.name).toBe("control-plane-migrate-b9e1b9d6de");
        expect(model.attention).toBe(0);
        // Listed once: as the workload, not again as a runs-only job.
        expect(model.rows.filter((r) => r.name === "control-plane-migrate")).toHaveLength(1);
      }
    });

    it("a failed latest run is red, a running one neutral", () => {
      const failed = mergeLiveState({ envs: ENVS, env: "e2e", workloads: [migrate({ envId: "env-e2e", envName: "e2e" })], jobs: FIXTURE_JOBS, telemetry: [], hosted: new Map() });
      expect(rowOf(failed)).toMatchObject({ tone: "danger", status: "Failed" });
      const running = mergeLiveState({
        envs: ENVS,
        env: "prod",
        workloads: [migrate({ health: "pending" })],
        jobs: [{ ...FIXTURE_JOBS[1]!, result: "running", finishedAtMs: null }],
        telemetry: [],
        hosted: new Map(),
      });
      expect(rowOf(running)).toMatchObject({ tone: "neutral", status: "Running" });
    });

    it("with no run in the window, the server's verdict in a run's words", () => {
      const quiet = (health: Workload["health"]) => rowOf(mergeLiveState({ envs: ENVS, env: "prod", workloads: [migrate({ health })], jobs: [], telemetry: [], hosted: new Map() }));
      expect(quiet("healthy")).toMatchObject({ tone: "success", status: "Succeeded", ready: "—" });
      expect(quiet("degraded")).toMatchObject({ tone: "danger", status: "Failed" });
      expect(quiet("unknown")).toMatchObject({ tone: "neutral", status: "Unknown" });
    });

    it("leaves a service judged on its replicas", () => {
      const model = mergeLiveState({ envs: ENVS, env: "prod", workloads, jobs: FIXTURE_JOBS, telemetry: [], hosted: new Map() });
      const litellm = model.rows.find((row) => row.name === "litellm");
      expect(litellm).toMatchObject({ type: "workload", tone: "warning", status: "Degraded", ready: "1/2", short: true, lastRun: null });
    });
  });

  it("lists a job seen only in its runs, once", () => {
    const model = mergeLiveState({ envs: ENVS, env: ENV_ALL, workloads: [], jobs: FIXTURE_JOBS, telemetry: [], hosted: new Map() });
    const jobs = model.rows.filter((row) => row.type === "job");
    expect(jobs.length).toBeGreaterThan(0);
    expect(new Set(jobs.map((row) => row.key)).size).toBe(jobs.length);
  });
});
