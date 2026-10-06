// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { envHeadline } from "../envHeadline";

const NOW = Date.parse("2026-10-05T12:00:00.000Z");

function env(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "cp-prod",
    name: "prod",
    project: "hounders",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "v12",
    releaseProvenance: null,
    promotedByActor: "ci",
    promotedByUserId: "",
    phase: "unspecified",
    observed: { state: "not-reported" },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "",
    holds: [],
    ...overrides,
  };
}

function status(workloads: Partial<ForgeHostedWorkload>[]): CloudEnvStatus {
  return { verdict: "converged", workloads: workloads as ForgeHostedWorkload[], currentPromotion: null };
}

describe("the header's one status line", () => {
  it("says Running with how recently it was confirmed", () => {
    const line = envHeadline(env({ observed: { state: "converged", observedAt: "2026-10-05T11:57:00.000Z" } }), undefined, NOW);
    expect(line).toMatchObject({ tone: "ok", text: "Running · confirmed 3 minutes ago" });
  });

  it("says rolling out while converging", () => {
    expect(envHeadline(env({ observed: { state: "converging" } }), undefined, NOW)).toMatchObject({
      tone: "progress",
      text: "Rolling out…",
    });
  });

  it("carries the platform's reason for can't-confirm, without calling it running", () => {
    const line = envHeadline(
      env({ observed: { state: "unknown" }, driftDetail: "newest observation is stale" }),
      undefined,
      NOW
    );
    expect(line.tone).toBe("quiet");
    expect(line.text).toBe("Can't confirm what's running");
    expect(line.detail).toBe("newest observation is stale");
  });

  it("never reads absence as running", () => {
    for (const state of ["not-reported", "unknown", "converging", "failed"] as const) {
      expect(envHeadline(env({ observed: { state } }), undefined, NOW).text).not.toMatch(/^Running/);
    }
  });

  it("lets the owner's stop outrank the rollout reading", () => {
    const line = envHeadline(
      env({ observed: { state: "converged", observedAt: "2026-10-05T11:57:00.000Z" } }),
      status([{ name: "web", declared_run_state: "suspended", observed_state: "suspended" }]),
      NOW
    );
    expect(line).toMatchObject({ tone: "quiet", text: "Stopped by you" });
  });

  it("flags a billing suspension as the problem it is", () => {
    const line = envHeadline(
      env(),
      status([{ name: "web", declared_run_state: "running", observed_state: "suspended", last_error: "invoice overdue" }]),
      NOW
    );
    expect(line.tone).toBe("problem");
    expect(line.detail).toMatch(/invoice overdue/);
  });

  it("says nothing is released yet before the first release", () => {
    expect(envHeadline(env({ release: "" }), undefined, NOW).text).toBe("Nothing released yet");
  });

  /**
   * A QUEUED deploy outranks the readings and the run state: both describe the
   * release still running, which converged long ago. "Running · confirmed"
   * above a banner saying the new release is waiting on billing would read as
   * if it had gone out.
   */
  it("says a queued deploy is waiting, in the waiting register, above a converged reading", () => {
    const line = envHeadline(
      env({
        release: "v13",
        phase: "held",
        observed: { state: "queued" },
        holds: [
          {
            kind: "billing",
            promotionId: "promo-2",
            reason: "this runs compute (1 workload) and the organization has no active compute plan",
            fix: "",
            actionUrl: "",
            callerCanResolve: false,
          },
        ],
      }),
      status([{ name: "web", declared_run_state: "suspended", observed_state: "suspended" }]),
      NOW
    );
    expect(line).toMatchObject({ tone: "waiting", text: "Queued · waiting on billing" });
    expect(line.detail).toMatch(/no active compute plan/);
  });
});
