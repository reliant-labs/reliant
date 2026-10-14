// Copyright (c) 2025 Reliant Labs

/**
 * The DeliveryInsightsService wire mapping: the scope → request (only the
 * fields the ledger takes, the window as timestamps, the limit inside the
 * server's bound), and the answer → model, where an unset figure stays null
 * rather than becoming zero.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { DurationSchema, TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { msOf } from "../api";
import { durationMs, getDoraMetrics, listReleases } from "../deliveryApi";
import { deliveryErrorKind } from "../errors";

const client = { listReleases: vi.fn(), getDoraMetrics: vi.fn() };
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
    // Field 9 is reserved for the alert-based MTTR; nothing fills it yet.
    expect(m.alertTimeToRestore).toBeNull();
    expect(durationMs(undefined)).toBeNull();
  });
});

describe("the ledger's two refusals", () => {
  it("tells a non-member from an unavailable environment", () => {
    expect(deliveryErrorKind(new ConnectError("not a member of this organization", Code.PermissionDenied))).toBe("not-member");
    expect(deliveryErrorKind(new ConnectError("one or more environments are not available to this organization", Code.PermissionDenied))).toBe("env-unavailable");
    expect(deliveryErrorKind(new ConnectError("delivery insights are not available to machine credentials", Code.PermissionDenied))).toBe("denied");
    expect(deliveryErrorKind(new ConnectError("boom", Code.Unavailable))).toBe("unavailable");
  });
});
