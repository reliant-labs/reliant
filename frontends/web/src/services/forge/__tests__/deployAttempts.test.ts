// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { DeployAttemptSchema, DeployAttemptState } from "@/gen/controlplane/controlplane/v1/deploy_pb";

import { deployHeadline, deploysNeedingAttention, toDeployRun, type DeployRun } from "../deployAttempts";

const NOW = Date.parse("2026-10-26T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

function run(over: Partial<DeployRun>): DeployRun {
  return {
    id: "a",
    env: "prod",
    mode: "apply",
    source: "terminal",
    release: "",
    promotionId: "",
    startedByUserId: "",
    startedByActor: "",
    phase: "build",
    phases: [],
    state: "succeeded",
    failedPhase: "",
    error: "",
    exitCode: 0,
    runUrl: "",
    startedAt: ago(60_000),
    hasLog: false,
    logTruncated: false,
    hasReport: false,
    ...over,
  };
}

describe("a deploy attempt from the control plane", () => {
  it("maps its state, and reads an unknown one as unknown, never a success", () => {
    const msg = create(DeployAttemptSchema, {
      id: "att-1",
      environmentName: "prod",
      state: DeployAttemptState.FAILED,
      failedPhase: "build",
      startedAt: timestampFromDate(new Date(NOW)),
    });
    expect(toDeployRun(msg)).toMatchObject({ id: "att-1", env: "prod", state: "failed", failedPhase: "build", exitCode: null });
    expect(toDeployRun(create(DeployAttemptSchema, { state: 99 as DeployAttemptState })).state).toBe("unknown");
  });
});

describe("the deploys the Overview interrupts for", () => {
  it("are each environment's newest, when it runs or failed within a day", () => {
    const shown = deploysNeedingAttention(
      [
        run({ id: "e2e-running", env: "e2e", state: "running" }),
        run({ id: "prod-failed", env: "prod", state: "failed", finishedAt: ago(2 * 3600_000) }),
        run({ id: "prod-older", env: "prod", state: "failed", finishedAt: ago(3 * 3600_000) }),
        run({ id: "dev-fine", env: "dev", state: "succeeded" }),
        run({ id: "staging-old", env: "staging", state: "failed", finishedAt: ago(3 * 86_400_000) }),
      ],
      NOW
    );
    expect(shown.map((r) => r.id)).toEqual(["e2e-running", "prod-failed"]);
  });

  it("drop a failure once a later deploy to that environment succeeded", () => {
    const shown = deploysNeedingAttention([run({ id: "fixed", state: "succeeded" }), run({ id: "broke", state: "failed", finishedAt: ago(60_000) })], NOW);
    expect(shown).toEqual([]);
  });
});

describe("a deploy in one line", () => {
  it("says where it failed, and what it is doing while it runs", () => {
    expect(deployHeadline(run({ state: "failed", failedPhase: "build" }))).toBe("Deploy to prod failed while building");
    expect(deployHeadline(run({ state: "running", phase: "rollout", release: "v2" }))).toBe("Deploy v2 to prod: rolling out");
    expect(deployHeadline(run({ mode: "plan", release: "v2" }))).toBe("Planned v2 for prod");
    expect(deployHeadline(run({ release: "v2" }))).toBe("Deployed v2 to prod");
  });
});
