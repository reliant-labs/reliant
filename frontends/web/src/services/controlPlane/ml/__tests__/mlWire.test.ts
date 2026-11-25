// Copyright (c) 2026 Reliant Labs

/**
 * The ML wire mapping (wire.ts) and what a failure means (errors.ts): enums
 * as names (an unknown value is "unknown", never a guess), timestamps as ms
 * (the zero timestamp is unset), int64s as numbers, utilisation points with
 * no time dropped; FEATURE_NOT_ENTITLED by its reason alone and Unimplemented
 * as outcomes, everything else rethrown.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  GetMlMachinesResponseSchema,
  GetMlStackResponseSchema,
  ListMlFindingsResponseSchema,
  ListMlInferenceServicesResponseSchema,
  ListMlModelsResponseSchema,
  ListMlRunsResponseSchema,
  MlComponentState,
  MlFindingSeverity,
  MlNodeHealth,
  MlReleaseVerdict,
  MlRunState,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { GetMyOrgEntitlementsResponseSchema, RequestOrgEntitlementResponseSchema } from "@/gen/controlplane/services/organization/v1/organization_pb";

import { mlErrorKind, outcomeOfError } from "../errors";
import { entitlementsOf, findingsOf, inferenceOf, machinesOf, modelsOf, requestResultOf, runsOf, stackOf } from "../wire";

const T = Date.UTC(2026, 10, 25, 12, 0, 0);

describe("the ML wire mapping", () => {
  it("maps entitlements, unset times as null", () => {
    const res = create(GetMyOrgEntitlementsResponseSchema, {
      features: [{ feature: "ml", enabled: false, requestedAt: timestampFromMs(T) }, { feature: "other", enabled: true, requestedAt: timestampFromMs(0) }],
    });
    expect(entitlementsOf(res)).toEqual([
      { feature: "ml", enabled: false, requestedAtMs: T, grantedAtMs: null },
      { feature: "other", enabled: true, requestedAtMs: null, grantedAtMs: null },
    ]);
  });

  it("maps a request's answer, and fills one in when the server sent no entitlement", () => {
    const answered = create(RequestOrgEntitlementResponseSchema, { entitlement: { feature: "ml", requestedAt: timestampFromMs(T) }, alreadyRequested: true });
    expect(requestResultOf(answered, "ml", 1)).toEqual({ entitlement: { feature: "ml", enabled: false, requestedAtMs: T, grantedAtMs: null }, alreadyRequested: true, alreadyEnabled: false });
    expect(requestResultOf(create(RequestOrgEntitlementResponseSchema, {}), "ml", 42).entitlement.requestedAtMs).toBe(42);
  });

  it("maps machines: health names, flags, and only the utilisation points that have a time", () => {
    const res = create(GetMlMachinesResponseSchema, {
      clusters: [{ clusterId: "c1", name: "train", provider: "gke", location: "us-central1", lastSeenAt: timestampFromMs(T) }],
      pools: [
        {
          clusterId: "c1",
          name: "h100",
          acceleratorProduct: "NVIDIA H100",
          acceleratorsTotal: 8,
          acceleratorsAllocated: 4,
          nodes: [
            { name: "n1", health: MlNodeHealth.BAD, flags: [{ kind: "xid", code: "79", device: "GPU-5" }] },
            { name: "n2", health: 99 as MlNodeHealth },
          ],
          utilization: [{ time: timestampFromMs(T), utilizationPercent: 40, allocatedPercent: 50 }, { utilizationPercent: 1 }],
        },
      ],
      observedAt: timestampFromMs(T),
    });
    const machines = machinesOf(res);
    expect(machines.clusters[0]).toEqual({ id: "c1", name: "train", provider: "gke", location: "us-central1", lastSeenAtMs: T });
    expect(machines.pools[0].nodes.map((n) => n.health)).toEqual(["bad", "unknown"]);
    expect(machines.pools[0].nodes[0].flags[0]).toMatchObject({ kind: "xid", code: "79", device: "GPU-5", firstSeenAtMs: null });
    expect(machines.pools[0].utilization).toEqual([{ atMs: T, utilizationPercent: 40, allocatedPercent: 50 }]);
    expect(machines.observedAtMs).toBe(T);
  });

  it("maps the stack's states", () => {
    const res = create(GetMlStackResponseSchema, {
      components: [
        { name: "dcgm-exporter", state: MlComponentState.ADOPTED },
        { name: "npd", state: MlComponentState.INSTALLED },
        { name: "kueue", state: MlComponentState.MISSING },
        { name: "x", state: MlComponentState.UNSPECIFIED },
      ],
    });
    expect(stackOf(res).map((c) => c.state)).toEqual(["adopted", "installed", "missing", "unknown"]);
  });

  it("maps findings with evidence and a pod-start breakdown", () => {
    const res = create(ListMlFindingsResponseSchema, {
      findings: [
        {
          id: "f1",
          severity: MlFindingSeverity.CRITICAL,
          evidence: [{ label: "Xid", value: "79" }],
          podStart: { workload: "w", sampleCount: 3, stages: [{ name: "image pull", p50Seconds: 9, p95Seconds: 20 }] },
        },
        { id: "f2", severity: MlFindingSeverity.INFO },
      ],
    });
    const [f1, f2] = findingsOf(res);
    expect(f1.severity).toBe("critical");
    expect(f1.evidence).toEqual([{ label: "Xid", value: "79" }]);
    expect(f1.podStart).toEqual({ workload: "w", sampleCount: 3, stages: [{ name: "image pull", p50Seconds: 9, p95Seconds: 20 }] });
    expect(f2.podStart).toBeNull();
  });

  it("maps runs: state, int64 steps, params", () => {
    const res = create(ListMlRunsResponseSchema, {
      runs: [{ id: "r1", state: MlRunState.RUNNING, params: { lr: "1e-4" }, startedAt: timestampFromMs(T), metrics: [{ name: "loss", points: [{ step: 100n, value: 0.5 }] }] }],
    });
    const [run] = runsOf(res);
    expect(run.state).toBe("running");
    expect(run.params).toEqual({ lr: "1e-4" });
    expect(run.metrics[0].points[0]).toEqual({ step: 100, atMs: null, value: 0.5 });
    expect(run.finishedAtMs).toBeNull();
  });

  it("maps model versions: size as a number, metrics and serving lists", () => {
    const res = create(ListMlModelsResponseSchema, {
      models: [{ name: "m", versions: [{ digest: "sha256:abc", sizeBytes: 16_000_000_000n, evalMetrics: { acc: 0.9 }, servingIn: ["shop/prod/assistant"] }] }],
    });
    expect(modelsOf(res)[0].versions[0]).toMatchObject({ digest: "sha256:abc", sizeBytes: 16_000_000_000, evalMetrics: { acc: 0.9 }, servingIn: ["shop/prod/assistant"] });
  });

  it("maps inference verdicts", () => {
    const res = create(ListMlInferenceServicesResponseSchema, {
      services: [
        { name: "a", releaseVerdict: MlReleaseVerdict.PASS },
        { name: "b", releaseVerdict: MlReleaseVerdict.INCONCLUSIVE },
        { name: "c" },
      ],
    });
    expect(inferenceOf(res).map((s) => s.releaseVerdict)).toEqual(["pass", "inconclusive", "unknown"]);
  });
});

describe("what an ML failure means", () => {
  const notEntitled = new ConnectError("refused", Code.PermissionDenied, new Headers({ "x-forge-error-reason": "FEATURE_NOT_ENTITLED" }));

  it("reads FEATURE_NOT_ENTITLED from the reason alone, with no feature header", () => {
    expect(mlErrorKind(notEntitled)).toBe("not-entitled");
    expect(outcomeOfError(notEntitled)).toEqual({ kind: "not-entitled" });
  });

  it("reads Unimplemented as no data yet", () => {
    expect(outcomeOfError(new ConnectError("nope", Code.Unimplemented))).toEqual({ kind: "no-data" });
  });

  it("keeps any other refusal an error", () => {
    const notMember = new ConnectError("refused", Code.PermissionDenied, new Headers({ "x-forge-error-reason": "not_org_member" }));
    expect(mlErrorKind(notMember)).toBe("denied");
    expect(() => outcomeOfError(notMember)).toThrow(notMember);
    expect(mlErrorKind(new ConnectError("down", Code.Unavailable))).toBe("unavailable");
    expect(mlErrorKind(new Error("boom"))).toBe("unavailable");
  });
});
