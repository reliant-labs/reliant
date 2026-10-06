// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import { toCloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";
import type { ForgeHostedWorkload } from "@/services/forge/topology";
import { DeploySuspendReason } from "@/gen/controlplane/controlplane/v1/deploy_pb";

import { envHeadline } from "../EnvPage/envHeadline";
import { runStateOf } from "../runStateVocabulary";

const wl = (over: Partial<ForgeHostedWorkload>): ForgeHostedWorkload => ({
  name: "api",
  declared_run_state: "running",
  observed_state: "suspended",
  ...over,
});

describe("runStateOf with a reported suspend reason", () => {
  it("owner → Stopped by you, even with a stale last_error", () => {
    const v = runStateOf(wl({ declared_run_state: "suspended", suspend_reason: "owner", last_error: "x" }));
    expect(v).toMatchObject({ kind: "stopped", label: "Stopped by you" });
  });

  it("no_compute_plan → billing label telling the user to subscribe", () => {
    const v = runStateOf(wl({ suspend_reason: "no_compute_plan" }));
    expect(v.kind).toBe("billing");
    expect(v.label).toBe("Suspended — no compute plan");
    expect(v.detail).toContain("Subscribe to a compute plan");
  });

  it("billing_lapsed → billing label telling the user to settle the invoice, with no error needed", () => {
    const v = runStateOf(wl({ suspend_reason: "billing_lapsed", last_error: "" }));
    expect(v.kind).toBe("billing");
    expect(v.detail).toContain("Settle the invoice in Billing");
    expect(v.detail).not.toContain("Subscribe to a compute plan");
  });

  it("appends the platform's own words when it gave any", () => {
    const v = runStateOf(wl({ suspend_reason: "billing_lapsed", last_error: "grace ended" }));
    expect(v.detail).toContain("Platform says: grace ended");
  });

  it("billing outranks a declared stop (the owner cannot clear it by pressing Start)", () => {
    const v = runStateOf(wl({ declared_run_state: "suspended", suspend_reason: "billing_lapsed" }));
    expect(v.kind).toBe("billing");
  });

  it("an owner reason after the owner already started again reads as starting", () => {
    const v = runStateOf(wl({ declared_run_state: "running", suspend_reason: "owner" }));
    expect(v.kind).toBe("starting");
  });
});

describe("runStateOf falls back to inference when the reason is unspecified", () => {
  for (const reason of [undefined, "unspecified"]) {
    it(`reason=${reason}: declared suspended + observed suspended is the owner's stop`, () => {
      expect(runStateOf(wl({ declared_run_state: "suspended", suspend_reason: reason })).kind).toBe("stopped");
    });
    it(`reason=${reason}: declared running + suspended + error is billing (today's guess)`, () => {
      const v = runStateOf(wl({ suspend_reason: reason, last_error: "no plan" }));
      expect(v.kind).toBe("billing");
      expect(v.label).toBe("Suspended — billing");
    });
    it(`reason=${reason}: declared running + suspended, no error is a start in flight`, () => {
      expect(runStateOf(wl({ suspend_reason: reason })).kind).toBe("starting");
    });
  }
});

describe("envHeadline with a reported suspend reason", () => {
  const live = { observed: { state: "converged" }, holds: [] } as unknown as LiveEnv;
  const status = (w: ForgeHostedWorkload[]): CloudEnvStatus => ({ verdict: "converged", workloads: w, currentPromotion: null });

  it("shows the exact billing fix as a problem", () => {
    const line = envHeadline(live, status([wl({ suspend_reason: "no_compute_plan" })]));
    expect(line).toMatchObject({ tone: "problem", text: "Suspended — no compute plan" });
    expect(line.detail).toContain("Subscribe to a compute plan");
  });

  it("billing lapse beats a whole-environment owner stop", () => {
    const line = envHeadline(live, status([wl({ declared_run_state: "suspended", suspend_reason: "billing_lapsed" })]));
    expect(line).toMatchObject({ tone: "problem", text: "Suspended — billing" });
    expect(line.detail).toContain("Settle the invoice in Billing");
  });

  it("owner reason is Stopped by you", () => {
    const line = envHeadline(live, status([wl({ declared_run_state: "suspended", suspend_reason: "owner" })]));
    expect(line).toMatchObject({ tone: "quiet", text: "Stopped by you" });
  });
});

describe("toCloudEnvStatus maps the wire enum", () => {
  const msg = (r: DeploySuspendReason) =>
    ({
      deployments: [{ deployment: { id: "d", name: "api", observed: { suspendReason: r } } }],
    }) as unknown as Parameters<typeof toCloudEnvStatus>[0];
  const cases: [DeploySuspendReason, string][] = [
    [DeploySuspendReason.OWNER, "owner"],
    [DeploySuspendReason.NO_COMPUTE_PLAN, "no_compute_plan"],
    [DeploySuspendReason.BILLING_LAPSED, "billing_lapsed"],
    [DeploySuspendReason.UNSPECIFIED, "unspecified"],
  ];
  for (const [wire, want] of cases) {
    it(`${DeploySuspendReason[wire]} → ${want}`, () => {
      expect(toCloudEnvStatus(msg(wire)).workloads[0].suspend_reason).toBe(want);
    });
  }
});
