// Copyright (c) 2025 Reliant Labs

/**
 * The control plane's view of a forge project's environments: the request is
 * keyed by project, the response is filtered by project AGAIN (an old server
 * ignores the filter), and the wire shapes convert to the console's
 * vocabulary without inventing anything.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

const listEnvironments = vi.fn();
const getStatus = vi.fn();
const listPromotions = vi.fn();

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));
vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({ listEnvironments, getStatus, listPromotions }),
}));

import {
  DeployEnvironmentKind,
  DeployEnvironmentSchema,
  DeploymentSchema,
  DeployObservedState,
  DeployObservedStateDetailSchema,
  DeployPromotionKind,
  DeployPromotionSchema,
  DeployTier,
  DeployVerdict,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  DeploymentStatusSchema,
  GetDeploymentStatusResponseSchema,
} from "@/gen/controlplane/services/deploy/v1/deploy_pb";

import {
  cloudAvailabilityFromError,
  getEnvironmentStatus,
  listProjectEnvironments,
} from "../cloudEnvs";

beforeEach(() => {
  listEnvironments.mockReset();
  getStatus.mockReset();
  listPromotions.mockReset();
});

describe("listProjectEnvironments", () => {
  it("asks for ONE project's environments", async () => {
    listEnvironments.mockResolvedValue({ environments: [] });
    await listProjectEnvironments("barksocial");
    expect(listEnvironments).toHaveBeenCalledWith({ project: "barksocial" });
  });

  it("drops rows for another project — an old control plane ignores the filter", async () => {
    listEnvironments.mockResolvedValue({
      environments: [
        create(DeployEnvironmentSchema, { id: "a", name: "prod", project: "barksocial", kind: DeployEnvironmentKind.PERSISTENT }),
        create(DeployEnvironmentSchema, { id: "b", name: "prod", project: "control-plane", kind: DeployEnvironmentKind.PERSISTENT }),
        // A server predating the field sends no project at all.
        create(DeployEnvironmentSchema, { id: "c", name: "staging", kind: DeployEnvironmentKind.PERSISTENT }),
        create(DeployEnvironmentSchema, { id: "d", name: "dev", project: "barksocial", kind: DeployEnvironmentKind.LOCAL }),
      ],
    });
    const envs = await listProjectEnvironments("barksocial");
    expect(envs.map((e) => [e.id, e.name, e.kind])).toEqual([
      ["d", "dev", "local"],
      ["a", "prod", "persistent"],
    ]);
  });
});

describe("getEnvironmentStatus", () => {
  it("converts deployments into the hosted-workload shape, verdicts in forge's vocabulary", async () => {
    getStatus.mockResolvedValue(
      create(GetDeploymentStatusResponseSchema, {
        environmentVerdict: DeployVerdict.DEGRADED,
        deployments: [
          create(DeploymentStatusSchema, {
            deployment: create(DeploymentSchema, {
              name: "api",
              tier: DeployTier.BACKEND,
              observed: create(DeployObservedStateDetailSchema, {
                state: DeployObservedState.DEGRADED,
                imageDigest: "sha256:bbb",
                lastError: "CrashLoopBackOff",
                url: "https://wild-mongoose.reliantapps.dev",
              }),
            }),
            verdict: DeployVerdict.DEGRADED,
            verdictReason: "not ready",
            desiredDigest: "sha256:aaa",
            drifted: true,
            observedAt: timestampFromDate(new Date("2026-09-26T12:00:00Z")),
          }),
        ],
        currentPromotion: create(DeployPromotionSchema, {
          id: "p",
          releaseVersion: "v2",
          kind: DeployPromotionKind.PROMOTE,
          resolvedArtifacts: { api: "sha256:aaa" },
          createdAt: timestampFromDate(new Date("2026-09-25T00:00:00Z")),
        }),
      })
    );

    const status = await getEnvironmentStatus("env-1");
    expect(getStatus).toHaveBeenCalledWith({ environmentId: "env-1" });
    expect(status.verdict).toBe("degraded");
    expect(status.workloads).toEqual([
      {
        name: "api",
        tier: "backend",
        url: "https://wild-mongoose.reliantapps.dev",
        verdict: "degraded",
        verdict_reason: "not ready",
        observed_state: "degraded",
        observed_digest: "sha256:bbb",
        desired_digest: "sha256:aaa",
        drifted: true,
        last_error: "CrashLoopBackOff",
        deployment_id: "",
        declared_run_state: "unspecified",
        suspend_reason: "unspecified",
      },
    ]);
    expect(status.currentPromotion).toMatchObject({
      releaseVersion: "v2",
      kind: "promote",
      artifacts: [{ name: "api", digest: "sha256:aaa" }],
    });
    expect(status.observedAt).toBe("2026-09-26T12:00:00.000Z");
  });

  it("reports an empty environment as unknown — never healthy", async () => {
    getStatus.mockResolvedValue(create(GetDeploymentStatusResponseSchema, {}));
    const status = await getEnvironmentStatus("env-1");
    expect(status.verdict).toBe("unknown");
    expect(status.workloads).toEqual([]);
    expect(status.currentPromotion).toBeNull();
  });
});

describe("cloudAvailabilityFromError", () => {
  it("describes a role without access and an unconfigured control plane rather than erroring", () => {
    expect(cloudAvailabilityFromError(new ConnectError("no", Code.PermissionDenied))).toBe("no-access");
    expect(cloudAvailabilityFromError(new ConnectError("no", Code.Unimplemented))).toBe("not-configured");
    expect(cloudAvailabilityFromError(new ConnectError("down", Code.Unavailable))).toBe("unreachable");
  });

  it("never reads FailedPrecondition as a per-org deploy entitlement — there is none", () => {
    // The control plane removed the org_features 'deploy' gate: every org may
    // deploy. A FailedPrecondition on a read is a real failure to surface, not
    // a quiet "not enabled for your organization" that hides it.
    expect(cloudAvailabilityFromError(new ConnectError("no", Code.FailedPrecondition))).toBe("unreachable");
  });
});
