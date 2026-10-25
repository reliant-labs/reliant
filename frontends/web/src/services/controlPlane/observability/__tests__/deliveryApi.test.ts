// Copyright (c) 2025 Reliant Labs

/**
 * The DeliveryInsightsService wire mapping: the scope → request (only the
 * fields the ledger takes, the window as timestamps, the limit inside the
 * server's bound), and the answer → model, where an unset figure stays null
 * rather than becoming zero.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create, fromJson } from "@bufbuild/protobuf";

import { GetReleaseHealthResponseSchema } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { DurationSchema, TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { msOf } from "../api";
import { durationMs, getDoraMetrics, getReleaseHealth, listReleases, mapReleaseHealth } from "../deliveryApi";
import { deliveryErrorKind } from "../errors";

const client = { listReleases: vi.fn(), getDoraMetrics: vi.fn(), getReleaseHealth: vi.fn() };
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const T = Date.UTC(2026, 9, 14, 12);
const ts = (ms: number) => create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });
const dur = (ms: number) => create(DurationSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });

beforeEach(() => vi.clearAllMocks());

describe("ListReleases", () => {
  it("sends the org, project, environments and window, and nothing the ledger does not take", async () => {
    client.listReleases.mockResolvedValue({ releases: [], truncated: false });
    await listReleases({ orgId: "org-1", projectId: "shop", envIds: ["env-prod"], window: { startMs: T - 3_600_000, endMs: T } });
    const req = client.listReleases.mock.calls[0][0];
    expect(req.scope).toEqual({ orgId: "org-1", projectId: "shop", envIds: ["env-prod"], timeRange: expect.anything() });
    expect(msOf(req.scope.timeRange.start)).toBe(T - 3_600_000);
    expect(msOf(req.scope.timeRange.end)).toBe(T);
    expect(req.limit).toBe(500);
  });

  it("leaves the project unset when there is none, and caps the limit at the server's 2000", async () => {
    client.listReleases.mockResolvedValue({ releases: [], truncated: false });
    await listReleases({ orgId: "org-1", envIds: [], window: { startMs: 0, endMs: T }, limit: 10_000 });
    const req = client.listReleases.mock.calls[0][0];
    expect(req.scope.projectId).toBeUndefined();
    expect(req.limit).toBe(2000);
  });

  it("maps each promotion and says when the list was cut", async () => {
    client.listReleases.mockResolvedValue({
      truncated: true,
      releases: [
        {
          promotionId: "p1",
          environmentId: "env-prod",
          environmentName: "prod",
          releaseId: "r1",
          version: "v1.8.3",
          gitCommit: "abc",
          gitTag: "v1.8.3",
          promotedAt: ts(T),
          promotedBy: "Ada Lovelace",
          promotedByUserId: "u1",
          kind: "promote",
        },
      ],
    });
    const out = await listReleases({ orgId: "org-1", envIds: [], window: { startMs: 0, endMs: T } });
    expect(out.truncated).toBe(true);
    expect(out.promotions[0]).toEqual({
      promotionId: "p1",
      envId: "env-prod",
      envName: "prod",
      releaseId: "r1",
      version: "v1.8.3",
      gitCommit: "abc",
      gitTag: "v1.8.3",
      promotedAtMs: T,
      promotedBy: "Ada Lovelace",
      promotedByUserId: "u1",
      kind: "promote",
    });
  });
});

describe("GetDoraMetrics", () => {
  it("sends the window and N, or 0 for the server's defaults", async () => {
    client.getDoraMetrics.mockResolvedValue({});
    await getDoraMetrics({ orgId: "org-1", envIds: [], window: { startMs: T - 86_400_000, endMs: T }, failureWindowHours: 4 });
    expect(client.getDoraMetrics.mock.calls[0][0].failureWindowHours).toBe(4);
    await getDoraMetrics({ orgId: "org-1", envIds: [] });
    expect(client.getDoraMetrics.mock.calls[1][0]).toMatchObject({ failureWindowHours: 0, scope: { timeRange: undefined } });
  });

  it("keeps every unmeasured figure null: lead time unknown is not zero", async () => {
    client.getDoraMetrics.mockResolvedValue({
      windowStart: ts(T - 30 * 86_400_000),
      windowEnd: ts(T),
      failureWindowHours: 24,
      environmentCount: 3,
      deploymentFrequency: { definition: "df", deployments: 13, perDay: 13 / 30, rollbacks: 1, reapplies: 1 },
      leadTime: { definition: "lt", measuredChanges: 0, unknownChanges: 13, unknownReleases: 5 },
      changeFailureRate: { definition: "cfr", changes: 0, failedChanges: 0 },
      timeToRestore: { definition: "ttr", source: "release_ledger", failures: 1, median: dur(3 * 3_600_000), mean: dur(3 * 3_600_000) },
    });
    const m = await getDoraMetrics({ orgId: "org-1", envIds: [] });
    expect(m.leadTime).toEqual({ definition: "lt", measuredChanges: 0, unknownChanges: 13, unknownReleases: 5, medianMs: null, meanMs: null });
    expect(m.changeFailureRate.rate).toBeNull();
    expect(m.timeToRestore).toMatchObject({ source: "release_ledger", medianMs: 3 * 3_600_000 });
    expect(m.deploymentFrequency).toMatchObject({ deployments: 13, rollbacks: 1, reapplies: 1 });
    expect(m).toMatchObject({ windowStartMs: T - 30 * 86_400_000, windowEndMs: T, environmentCount: 3 });
    expect(m.timeToRestore.ongoing).toBe(0);
    expect(m.alertTimeToRestore).toBeNull();
    expect(durationMs(undefined)).toBeNull();
  });

  describe("alert-based time to restore (field 9)", () => {
    const alerts = (t: object) => ({ definition: "alerts", source: "alert_events", ...t });

    it("maps an unset field 9 to null: no figure, not zero incidents", async () => {
      client.getDoraMetrics.mockResolvedValue({ timeToRestore: { definition: "ttr", source: "release_ledger", failures: 1, median: dur(3_600_000) } });
      expect((await getDoraMetrics({ orgId: "org-1", envIds: [] })).alertTimeToRestore).toBeNull();
    });

    it("maps a set field 9 with zero counts to a figure of zero incidents, with no durations", async () => {
      client.getDoraMetrics.mockResolvedValue({ alertTimeToRestore: alerts({ failures: 0, ongoing: 0 }) });
      const m = await getDoraMetrics({ orgId: "org-1", envIds: [] });
      expect(m.alertTimeToRestore).toEqual({ definition: "alerts", source: "alert_events", failures: 0, ongoing: 0, medianMs: null, meanMs: null });
    });

    it("maps resolved incidents with some still ongoing, beside an untouched ledger figure", async () => {
      client.getDoraMetrics.mockResolvedValue({
        timeToRestore: { definition: "ttr", source: "release_ledger", failures: 1, median: dur(3_600_000), mean: dur(3_600_000) },
        alertTimeToRestore: alerts({ failures: 3, ongoing: 2, median: dur(38 * 60_000), mean: dur(52 * 60_000) }),
      });
      const m = await getDoraMetrics({ orgId: "org-1", envIds: [] });
      expect(m.alertTimeToRestore).toEqual({ definition: "alerts", source: "alert_events", failures: 3, ongoing: 2, medianMs: 38 * 60_000, meanMs: 52 * 60_000 });
      expect(m.timeToRestore).toMatchObject({ source: "release_ledger", failures: 1, ongoing: 0, medianMs: 3_600_000 });
    });
  });
});

describe("the ledger's two refusals", () => {
  it("tells a non-member from an unavailable environment", () => {
    expect(deliveryErrorKind(new ConnectError("not a member of this organization", Code.PermissionDenied, new Headers({ "x-forge-error-reason": "not_org_member" })))).toBe("not-member");
    expect(deliveryErrorKind(new ConnectError("one or more environments are not available to this organization", Code.PermissionDenied))).toBe("env-unavailable");
    expect(deliveryErrorKind(new ConnectError("delivery insights are not available to machine credentials", Code.PermissionDenied))).toBe("denied");
    expect(deliveryErrorKind(new ConnectError("boom", Code.Unavailable))).toBe("unavailable");
  });
});

describe("GetReleaseHealth", () => {
  it("asks about one promotion in one environment with the width as a duration, and no window of its own", async () => {
    client.getReleaseHealth.mockResolvedValue(fromJson(GetReleaseHealthResponseSchema, {}));
    await getReleaseHealth({ orgId: "org-1", projectId: "shop", envIds: ["env-prod"], promotionId: "promo-9", windowMs: 30 * 60_000 });
    const req = client.getReleaseHealth.mock.calls[0][0];
    expect(req.scope).toMatchObject({ orgId: "org-1", projectId: "shop", envIds: ["env-prod"] });
    // QueryScope.time_range is required by the validator and ignored by the rpc: a token minute.
    expect(msOf(req.scope.timeRange.end) - msOf(req.scope.timeRange.start)).toBe(60_000);
    expect(req.subject).toEqual({ case: "promotionId", value: "promo-9" });
    expect(durationMs(req.window)).toBe(30 * 60_000);
  });

  it("leaves the subject and the width to the server when unset (the current promotion, 15 minutes)", async () => {
    client.getReleaseHealth.mockResolvedValue(fromJson(GetReleaseHealthResponseSchema, {}));
    await getReleaseHealth({ orgId: "org-1", envIds: ["env-prod"] });
    const req = client.getReleaseHealth.mock.calls[0][0];
    expect(req.subject).toEqual({ case: undefined });
    expect(req.window).toBeUndefined();
  });

  it("maps protojson — int64 strings, enum names, not_evaluated — into the model", () => {
    const h = mapReleaseHealth(
      fromJson(GetReleaseHealthResponseSchema, {
        promotion: { id: "promo-9", releaseVersion: "v1.8.3", gitCommit: "abc", promotedAt: new Date(T).toISOString(), previousReleaseVersion: "v1.8.2" },
        before: { start: new Date(T - 900_000).toISOString(), end: new Date(T).toISOString() },
        after: { start: new Date(T).toISOString(), end: new Date(T + 600_000).toISOString() },
        afterComplete: false,
        services: [{ service: "api", before: { requests: "10350", errorRate: 0.004, p50Ms: 41, p95Ms: 118, p99Ms: 240 }, after: { requests: "10890", errorRate: 0.016, p95Ms: 171 }, versionShare: 0.97 }],
        newIssues: [{ id: "iss-1", status: "ISSUE_STATUS_NEW", events: "38" }],
        regressedIssues: [{ id: "iss-2", status: "ISSUE_STATUS_REGRESSED" }],
        firingAlerts: [{ rule: "HighErrorRate", fingerprint: "f", firingAt: new Date(T + 60_000).toISOString(), labels: { service: "api" } }],
        workloadFindings: [{ workload: "api", restarts: "2", lastTerminatedReason: "OOMKilled" }],
        verdict: "RELEASE_VERDICT_INCONCLUSIVE",
        findings: [{ code: "RELEASE_FINDING_CODE_LATENCY", service: "api", value: 1.45, threshold: 1.25, policy: "warn", message: "slower" }],
        thresholds: [{ name: "max_p95_ratio", value: "1.25", source: "default" }],
        notEvaluated: ["alert_firing", "new_issue", "some_future_code"],
      })
    );
    expect(h.promotion).toEqual({ promotionId: "promo-9", version: "v1.8.3", gitCommit: "abc", promotedAtMs: T, previousVersion: "v1.8.2" });
    expect(h.before).toEqual({ startMs: T - 900_000, endMs: T });
    expect(h.after).toEqual({ startMs: T, endMs: T + 600_000 });
    expect(h.afterComplete).toBe(false);
    expect(h.services[0]).toEqual({
      service: "api",
      before: { requests: 10350, errorRate: 0.004, p50Ms: 41, p95Ms: 118, p99Ms: 240 },
      after: { requests: 10890, errorRate: 0.016, p50Ms: 0, p95Ms: 171, p99Ms: 0 },
      versionShare: 0.97,
    });
    expect(h.newIssues[0]).toMatchObject({ id: "iss-1", status: "new", events: 38 });
    expect(h.regressedIssues[0]).toMatchObject({ id: "iss-2", status: "regressed" });
    expect(h.firingAlerts).toEqual([{ rule: "HighErrorRate", fingerprint: "f", firingAtMs: T + 60_000, labels: { service: "api" } }]);
    expect(h.workloadFindings).toEqual([{ workload: "api", restarts: 2, lastTerminatedReason: "OOMKilled" }]);
    expect(h.verdict).toBe("inconclusive");
    expect(h.findings).toEqual([{ code: "latency", service: "api", value: 1.45, threshold: 1.25, policy: "warn", message: "slower" }]);
    expect(h.thresholds).toEqual([{ name: "max_p95_ratio", value: "1.25", source: "default" }]);
    // Kept raw, unknown codes included: a newer server's code is still something the PASS does not cover.
    expect(h.notEvaluated).toEqual(["alert_firing", "new_issue", "some_future_code"]);
  });

  it("keeps an unset promotion, window and stats as null, and an unspecified verdict as unknown", () => {
    const h = mapReleaseHealth(fromJson(GetReleaseHealthResponseSchema, { services: [{ service: "worker" }] }));
    expect(h).toMatchObject({ promotion: null, before: null, after: null, verdict: "unknown", notEvaluated: [] });
    expect(h.services[0]).toMatchObject({ before: null, after: null });
  });
});
