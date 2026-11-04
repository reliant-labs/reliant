// Copyright (c) 2025 Reliant Labs

/**
 * The Environments page's per-environment summary: the model (what each fact
 * says, and that an unread fact never reads as good) and the cards that show
 * it, with links scoped to the environment.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { ReleaseHealth } from "@/services/controlPlane/observability/deliveryTypes";
import type { Workload } from "@/services/controlPlane/observability/types";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { DeployRun } from "@/services/forge/deployAttempts";
import type { LiveEnv } from "@/services/forge/live";

import { EnvironmentList } from "../EnvironmentList";
import { summarizeEnvironment, workloadCount, type EnvSummaryInput, type Read } from "../environmentSummary";

function liveEnv(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "denv_prod",
    name: "prod",
    project: "acme",
    kind: "self_managed",
    declared: true,
    declaredBy: null,
    release: "v1.7.89",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "succeeded",
    provenance: "v1.7.89 · main@abc1234",
    holds: [],
    observed: { state: "not-reported" },
    drift: { state: "in_sync" },
    driftDetail: "",
    ...overrides,
  };
}

function workload(overrides: Partial<Workload>): Workload {
  return {
    envId: "denv_prod",
    envName: "prod",
    workload: "api",
    kind: "deployment",
    health: "healthy",
    readyReplicas: 2,
    desiredReplicas: 2,
    ...overrides,
  } as Workload;
}

function run(overrides: Partial<DeployRun> = {}): DeployRun {
  return {
    id: "run-1",
    env: "prod",
    mode: "apply",
    source: "terminal",
    release: "v1.7.89",
    promotionId: "p1",
    startedByUserId: "",
    startedByActor: "",
    phase: "verify",
    phases: [],
    state: "succeeded",
    failedPhase: "",
    error: "",
    exitCode: 0,
    runUrl: "",
    startedAt: "2026-11-03T17:00:00Z",
    finishedAt: "2026-11-03T17:05:00Z",
    hasLog: false,
    logTruncated: false,
    hasReport: false,
    ...overrides,
  };
}

function health(overrides: Partial<ReleaseHealth> = {}): ReleaseHealth {
  return {
    promotion: null,
    before: null,
    after: null,
    afterComplete: true,
    services: [],
    newIssues: [],
    regressedIssues: [],
    firingAlerts: [],
    workloadFindings: [],
    verdict: "pass",
    findings: [],
    thresholds: [],
    notEvaluated: [],
    ...overrides,
  };
}

const read = <T,>(data: T | undefined, more: Partial<Read<T>> = {}): Read<T> => ({ data, loading: false, failed: false, ...more });

function input(overrides: Partial<EnvSummaryInput> = {}): EnvSummaryInput {
  return {
    env: liveEnv(),
    status: read<CloudEnvStatus>(undefined),
    workloads: read({ workloads: [workload({}), workload({ workload: "worker", readyReplicas: 0, desiredReplicas: 1, health: "crash_looping" })], jobs: [] }),
    runs: read([run({ env: "staging", id: "other" }), run()]),
    check: read(health()),
    declared: [],
    ...overrides,
  };
}

describe("an environment's summary", () => {
  it("reads health and ready/total from the environment's own telemetry, as the Overview's strip does", () => {
    const s = summarizeEnvironment(input());
    expect(s.health).toMatchObject({ tone: "danger", label: "1 failing" });
    expect(s.workloads).toEqual({ ready: 1, total: 2, source: "telemetry" });
  });

  it("takes the last deploy of THIS environment, the newest first", () => {
    expect(summarizeEnvironment(input()).lastDeploy?.id).toBe("run-1");
  });

  it("says what the obs check says, and an open after window is not a pass", () => {
    expect(summarizeEnvironment(input()).check).toMatchObject({ tone: "success", label: "Proven good" });
    expect(summarizeEnvironment(input({ check: read(health({ verdict: "pass", afterComplete: false })) })).check).toMatchObject({ label: "Still watching" });
    expect(summarizeEnvironment(input({ check: read(health({ verdict: "inconclusive" })) })).check).toMatchObject({ tone: "warning", label: "Not proven" });
    expect(summarizeEnvironment(input({ check: read(health({ verdict: "fail", afterComplete: false })) })).check).toMatchObject({ tone: "danger", label: "Regressed" });
  });

  it("never turns an unread fact into a good one", () => {
    const s = summarizeEnvironment(
      input({ workloads: read<NonNullable<EnvSummaryInput["workloads"]["data"]>>(undefined, { failed: true }), check: read<ReleaseHealth>(undefined, { failed: true }), env: liveEnv({ drift: { state: "not-reported" } }) })
    );
    expect(s.health).toMatchObject({ tone: "neutral", label: "Not read" });
    expect(s.workloads).toBeNull();
    expect(s.check).toMatchObject({ tone: "neutral", label: "Not read" });
    expect(s.drift).toMatchObject({ tone: "neutral", label: "Not reported" });
  });

  it("has no obs check for an environment nothing was promoted to", () => {
    expect(summarizeEnvironment(input({ env: liveEnv({ release: "" }), check: null })).check).toBeNull();
  });

  it("calls a declared, silent self-managed environment unobserved, not empty", () => {
    const s = summarizeEnvironment(input({ workloads: read({ workloads: [], jobs: [] }), declared: ["api", "worker"] }));
    expect(s.health).toMatchObject({ tone: "neutral", label: "Unobserved" });
  });

  it("counts a placed environment's backends as the platform observed them when telemetry has none", () => {
    const status: CloudEnvStatus = {
      verdict: "converging",
      currentPromotion: null,
      workloads: [
        { name: "api", tier: "backend", observed_state: "ready" },
        { name: "web", tier: "static", observed_state: "ready" },
        { name: "worker", tier: "backend", observed_state: "progressing" },
      ],
    };
    expect(workloadCount([], status)).toEqual({ ready: 1, total: 2, source: "platform" });
  });

  it("names drift with the server's detail when it does not match", () => {
    const s = summarizeEnvironment(input({ env: liveEnv({ drift: { state: "drifted" }, driftDetail: "api runs sha256:aaa, the release has sha256:bbb" }) }));
    expect(s.drift).toMatchObject({ tone: "warning", detail: "api runs sha256:aaa, the release has sha256:bbb" });
  });
});

describe("the environment cards", () => {
  it("show every fact, and links scoped to the environment", () => {
    const failed = run({ state: "failed", failedPhase: "build", error: "docker build exited 1" });
    const summary = summarizeEnvironment(input({ runs: read([failed]) }));
    render(
      <EnvironmentList
        rows={[
          {
            env: summary.env,
            status: undefined,
            statusLoading: false,
            summary,
            links: { dashboards: "/d?env=prod", errors: "/e?env=prod", alerts: "/a?env=prod" },
          },
        ]}
        onOpen={vi.fn()}
      />
    );
    const card = screen.getByTestId("env-row-prod");
    expect(within(card).getByTestId("telemetry-health-prod")).toHaveTextContent("1 failing");
    expect(within(card).getByTestId("workloads-prod")).toHaveTextContent("1/2 ready");
    expect(within(card).getByTestId("last-deploy-prod")).toHaveTextContent(/Failed.*v1\.7\.89/);
    expect(within(card).getByTestId("last-deploy-prod")).toHaveTextContent("stopped at build: docker build exited 1");
    expect(within(card).getByTestId("obs-check-prod")).toHaveTextContent("Proven good");
    expect(within(card).getByTestId("drift-prod")).toHaveTextContent("Matches what you asked for");
    expect(within(card).getByTestId("env-link-dashboards-prod")).toHaveAttribute("href", "/d?env=prod");
    expect(within(card).getByTestId("env-link-errors-prod")).toHaveAttribute("href", "/e?env=prod");
    expect(within(card).getByTestId("env-link-alerts-prod")).toHaveAttribute("href", "/a?env=prod");
  });
});
