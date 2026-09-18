// Copyright (c) 2025 Reliant Labs

/**
 * THE OUTGOING REQUEST, and the four refusals that come back.
 *
 * ⛔ MOCKED AT THE CLIENT BOUNDARY — createForgeClient — SO NOTHING REACHES A
 * DAEMON, AND NOTHING CAN REACH A CLUSTER. This is not test hygiene on this
 * surface. StartDeploy applies manifests to a live target; control-plane's prod
 * declares gke_reliant-labs-475814_us-central1_prod, and unlike a promote —
 * which moves a pointer git can restore — a deploy is recoverable from nowhere.
 *
 * What is pinned:
 *   expected_declared_context is derived from the RENDERED PLAN and is always sent
 *   exactly one of the two release claims is sent, never both and never neither
 *   no plan means no token, so no request can be built at all
 *   no skip_preflight / no_digest / force field exists in anything sent
 *   plan and start are separate functions, so nothing writes by defaulting a flag
 *   each refusal reason is read from the structured DETAIL, never the message
 *   an unrecognised job status becomes `unknown`, never `completed`
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  ForgeDeployJobStatus,
  ForgeDeployRefusalReason,
  ForgeDeployRefusalSchema,
} from "@/gen/reliant/v1/forge_pb";

const planDeploy = vi.fn();
const startDeploy = vi.fn();
const getDeployStatus = vi.fn();

vi.mock("../grpc-client", () => ({
  createForgeClient: () => ({ planDeploy, startDeploy, getDeployStatus }),
}));

const okMeta = {
  isForgeProject: true,
  supported: true,
  forgeVersion: "v0.1.15",
  unsupportedReason: "",
  exitCode: 0,
  reachability: 0,
  unreachableReason: "",
};

/** A plan document with the fields the token is derived from. */
function planDoc(overrides: Record<string, unknown> = {}) {
  return {
    env: "prod",
    mode: "dry_run",
    guard: {
      declared_context: "gke_reliant-labs-475814_us-central1_prod",
      current_context: "k3d-control-plane",
      verdict: "allow",
      reason: "context_declared",
    },
    target: {
      kube_context: "gke_reliant-labs-475814_us-central1_prod",
      namespace: "control-plane-prod",
      all_kube_contexts: ["gke_reliant-labs-475814_us-central1_prod"],
    },
    release: "v1.5.15",
    ok: true,
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  planDeploy.mockResolvedValue({ meta: okMeta, reportJson: JSON.stringify(planDoc()) });
  startDeploy.mockResolvedValue({
    meta: okMeta,
    handle: "dep-abc123",
    env: "prod",
    startedAt: "2026-09-10T14:00:00Z",
    jobStatus: ForgeDeployJobStatus.RUNNING,
    reportJson: JSON.stringify(planDoc()),
  });
  getDeployStatus.mockResolvedValue({
    meta: okMeta,
    handle: "dep-abc123",
    env: "prod",
    jobStatus: ForgeDeployJobStatus.RUNNING,
    jobStatusDetail: "",
    startedAt: "2026-09-10T14:00:00Z",
    finishedAt: "",
    reportJson: "",
  });
});

describe("the confirmation token", () => {
  it("carries expected_declared_context derived from the RENDERED plan", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    // Derived exactly as the UI does it: from the document, not from a prop.
    const token = deployTokenFor(planDoc());
    expect(token).not.toBeNull();

    await start({ projectId: "p1", env: "prod", token: token! });

    const sent = startDeploy.mock.calls[0][0];
    // THE CLUSTER CLAIM — the most consequential field in the message, and it is
    // the declared context, NOT the ambient current_context.
    expect(sent.expectedDeclaredContext).toBe("gke_reliant-labs-475814_us-central1_prod");
    expect(sent.expectedDeclaredContext).not.toBe("k3d-control-plane");
    expect(sent.expectedCurrentRelease).toBe("v1.5.15");
    expect(sent.expectUnbound).toBe(false);
    expect(sent.env).toBe("prod");
  });

  it("sends expectUnbound alone for an env with no release binding", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    const token = deployTokenFor(planDoc({ release: "" }));
    expect(token).toEqual({
      expectedDeclaredContext: "gke_reliant-labs-475814_us-central1_prod",
      expectUnbound: true,
    });

    await start({ projectId: "p1", env: "prod", token: token! });

    const sent = startDeploy.mock.calls[0][0];
    // Empty, not a guessed release — a non-empty value alongside expectUnbound is
    // rejected by the server as contradictory.
    expect(sent.expectedCurrentRelease).toBe("");
    expect(sent.expectUnbound).toBe(true);
    // The cluster claim is still mandatory and still present.
    expect(sent.expectedDeclaredContext).toBe("gke_reliant-labs-475814_us-central1_prod");
  });

  it("never sends both release claims, and never sends an empty cluster", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    for (const doc of [planDoc(), planDoc({ release: "" })]) {
      startDeploy.mockClear();
      const token = deployTokenFor(doc)!;
      await start({ projectId: "p1", env: "prod", token });
      const sent = startDeploy.mock.calls[0][0];
      const claims = [sent.expectedCurrentRelease !== "", sent.expectUnbound === true];
      expect(claims.filter(Boolean)).toHaveLength(1);
      // There is no legitimate deploy whose target cluster nobody saw.
      expect(sent.expectedDeclaredContext).not.toBe("");
    }
  });

  it("produces NO token — so no request is possible — without a rendered plan", async () => {
    const { deployTokenFor } = await import("@/services/forge/deploy");

    // Nothing at all.
    expect(deployTokenFor(null)).toBeNull();
    expect(deployTokenFor(undefined)).toBeNull();
    expect(deployTokenFor({})).toBeNull();

    // An APPLY report: it describes a deploy that already happened and cannot
    // authorise another.
    expect(deployTokenFor(planDoc({ mode: "apply" }))).toBeNull();

    // Forge's own guard refused. No token overrides it.
    expect(
      deployTokenFor(planDoc({ guard: { declared_context: "gke_x", verdict: "refuse" } }))
    ).toBeNull();

    // No declared cluster: there is nothing to name, and the request has no
    // "unspecified" spelling.
    expect(deployTokenFor(planDoc({ guard: { declared_context: "", verdict: "allow" } }))).toBeNull();

    // An unrecognised verdict is `unknown`, not `allow` — but it also is not
    // `refuse`, so the declared context still governs. What must never happen is
    // a token from a refusal, asserted above.
    expect(startDeploy).not.toHaveBeenCalled();
  });
});

describe("no escape hatches in anything sent", () => {
  it("sends no skip_preflight, no_digest or force field on any deploy request", async () => {
    const forgeGrpcModule = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    await forgeGrpcModule.planDeploy({ projectId: "p1", env: "prod" });
    await forgeGrpcModule.startDeploy({
      projectId: "p1",
      env: "prod",
      token: deployTokenFor(planDoc())!,
    });
    await forgeGrpcModule.getDeployStatus({ projectId: "p1", handle: "dep-abc123" });

    const forbidden = [
      "skipPreflight",
      "skip_preflight",
      "noDigest",
      "no_digest",
      "force",
      "dryRun",
      "dry_run",
    ];

    for (const spy of [planDeploy, startDeploy, getDeployStatus]) {
      const sent = spy.mock.calls[0][0] as Record<string, unknown>;
      for (const field of forbidden) {
        expect(sent[field]).toBeUndefined();
      }
      // And a shape assertion, so a field added under a name nobody predicted is
      // still caught.
      const keys = Object.keys(sent).filter((key) => !key.startsWith("$"));
      for (const key of keys) {
        expect(key.toLowerCase()).not.toMatch(/preflight|digest|force/);
      }
    }
  });

  it("keeps plan, start and status as three distinct functions", async () => {
    const forgeGrpcModule = await import("../forge-grpc");

    await forgeGrpcModule.planDeploy({ projectId: "p1", env: "prod" });
    // The read-only preview did not touch the write path.
    expect(planDeploy).toHaveBeenCalledTimes(1);
    expect(startDeploy).not.toHaveBeenCalled();

    expect(forgeGrpcModule.planDeploy).not.toBe(forgeGrpcModule.startDeploy);
    expect(forgeGrpcModule.startDeploy).not.toBe(forgeGrpcModule.getDeployStatus);

    // And there is no combined one-call deploy on the module — a wrapper that
    // previewed and applied is the shape this whole path makes unavailable.
    const exported = Object.keys(forgeGrpcModule.forgeGrpc);
    expect(exported).toContain("planDeploy");
    expect(exported).toContain("startDeploy");
    expect(exported).toContain("getDeployStatus");
    expect(exported).not.toContain("deploy");
    expect(exported).not.toContain("deployEnv");
    expect(exported).not.toContain("applyDeploy");
  });
});

describe("refusals", () => {
  const reasons = [
    {
      proto: ForgeDeployRefusalReason.STALE_DECLARED_CONTEXT,
      expected: "stale-declared-context",
    },
    { proto: ForgeDeployRefusalReason.STALE_CURRENT_RELEASE, expected: "stale-current-release" },
    { proto: ForgeDeployRefusalReason.GUARD_REFUSED, expected: "guard-refused" },
    { proto: ForgeDeployRefusalReason.ALREADY_RUNNING, expected: "already-running" },
  ] as const;

  it("reads all four reasons from the structured detail, not the message", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");
    const token = deployTokenFor(planDoc())!;

    for (const reason of reasons) {
      const detail = create(ForgeDeployRefusalSchema, {
        reason: reason.proto,
        detail: "the guard found something else",
        expectedDeclaredContext: "gke_reliant-labs-475814_us-central1_prod",
        actualDeclaredContext: "k3d-control-plane",
        expectedCurrentRelease: "v1.5.15",
        actualCurrentRelease: "v1.5.16",
        actualBound: true,
        guardFix: "run gcloud container clusters get-credentials",
        runningHandle: "dep-running-1",
      });

      startDeploy.mockRejectedValue(
        // The message deliberately does NOT name the reason: branching on it would
        // break the moment the wording improves.
        new ConnectError("deploy refused, nothing was applied: see detail", Code.FailedPrecondition, undefined, [
          { desc: ForgeDeployRefusalSchema, value: detail },
        ])
      );

      const result = await start({ projectId: "p1", env: "prod", token });

      // A refusal is DATA, not a thrown error, so no call site can render it as a
      // generic failure by forgetting to catch.
      expect(result.kind).toBe("refused");
      if (result.kind !== "refused") throw new Error("expected a refusal");
      expect(result.refusal.reason).toBe(reason.expected);
      expect(result.refusal.actualDeclaredContext).toBe("k3d-control-plane");
      expect(result.refusal.runningHandle).toBe("dep-running-1");
      expect(result.refusal.guardFix).toContain("get-credentials");
    }
  });

  it("maps an unrecognised reason to `unknown` rather than dropping the refusal", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    const detail = create(ForgeDeployRefusalSchema, {
      reason: ForgeDeployRefusalReason.UNSPECIFIED,
      detail: "refused for a reason this build does not know",
    });
    startDeploy.mockRejectedValue(
      new ConnectError("deploy refused, nothing was applied", Code.FailedPrecondition, undefined, [
        { desc: ForgeDeployRefusalSchema, value: detail },
      ])
    );

    const result = await start({
      projectId: "p1",
      env: "prod",
      token: deployTokenFor(planDoc())!,
    });
    // Still a refusal. A refusal this build cannot classify is a refusal, not a
    // started deploy.
    expect(result.kind).toBe("refused");
    if (result.kind !== "refused") throw new Error("expected a refusal");
    expect(result.refusal.reason).toBe("unknown");
  });

  it("still throws when a FailedPrecondition carries no decodable detail", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    // Fails closed. An empty refusal panel would claim facts about the cluster
    // that never arrived.
    startDeploy.mockRejectedValue(
      new ConnectError("deploy refused, nothing was applied: unexplained", Code.FailedPrecondition)
    );
    await expect(
      start({ projectId: "p1", env: "prod", token: deployTokenFor(planDoc())! })
    ).rejects.toThrow(/deploy refused/);
  });

  it("does not swallow an unrelated error code", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");
    startDeploy.mockRejectedValue(new ConnectError("no", Code.PermissionDenied));
    await expect(
      start({ projectId: "p1", env: "prod", token: deployTokenFor(planDoc())! })
    ).rejects.toThrow();
  });
});

describe("the job lifecycle", () => {
  it("returns a started deploy with its handle and a RUNNING disposition", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    const result = await start({
      projectId: "p1",
      env: "prod",
      token: deployTokenFor(planDoc())!,
    });
    expect(result.kind).toBe("started");
    if (result.kind !== "started") throw new Error("expected a start");
    expect(result.handle).toBe("dep-abc123");
    expect(result.jobStatus).toBe("running");
    // The carried document is the GUARD PLAN — mode dry_run, not an apply report.
    expect(result.guardPlan.kind).toBe("report");
    if (result.guardPlan.kind === "report") {
      expect(result.guardPlan.report.mode).toBe("dry_run");
    }
  });

  it("reports a handle-less reply as not-started rather than something to poll", async () => {
    const { startDeploy: start } = await import("../forge-grpc");
    const { deployTokenFor } = await import("@/services/forge/deploy");

    startDeploy.mockResolvedValue({
      meta: { ...okMeta, supported: false, unsupportedReason: "forge too old" },
      handle: "",
      env: "prod",
      jobStatus: ForgeDeployJobStatus.UNSPECIFIED,
      reportJson: "",
    });

    const result = await start({
      projectId: "p1",
      env: "prod",
      token: deployTokenFor(planDoc())!,
    });
    // Not a started deploy with an empty handle a client would poll forever.
    expect(result.kind).toBe("not-started");
  });

  it("maps an unrecognised job status to `unknown`, NEVER to completed", async () => {
    const { getDeployStatus: status } = await import("../forge-grpc");

    // A value from a newer server this build has never seen.
    getDeployStatus.mockResolvedValue({
      meta: okMeta,
      handle: "dep-abc123",
      env: "prod",
      jobStatus: 99 as ForgeDeployJobStatus,
      jobStatusDetail: "",
      startedAt: "",
      finishedAt: "",
      reportJson: "",
    });

    const result = await status({ projectId: "p1", handle: "dep-abc123" });
    expect(result.jobStatus).toBe("unknown");
  });

  it("maps UNSPECIFIED to `unknown` too — the server said something unclassifiable", async () => {
    const { getDeployStatus: status } = await import("../forge-grpc");
    getDeployStatus.mockResolvedValue({
      meta: okMeta,
      handle: "dep-abc123",
      env: "prod",
      jobStatus: ForgeDeployJobStatus.UNSPECIFIED,
      jobStatusDetail: "",
      startedAt: "",
      finishedAt: "",
      reportJson: "",
    });
    expect((await status({ projectId: "p1", handle: "dep-abc123" })).jobStatus).toBe("unknown");
  });

  it("treats an empty report body while running as absent, not malformed", async () => {
    const { getDeployStatus: status } = await import("../forge-grpc");
    const result = await status({ projectId: "p1", handle: "dep-abc123" });
    // Null rather than an outcome claiming forge said something it did not.
    expect(result.report).toBeNull();
    expect(result.jobStatus).toBe("running");
  });

  it("classifies a finished report once one arrives", async () => {
    const { getDeployStatus: status } = await import("../forge-grpc");
    getDeployStatus.mockResolvedValue({
      meta: okMeta,
      handle: "dep-abc123",
      env: "prod",
      jobStatus: ForgeDeployJobStatus.COMPLETED,
      jobStatusDetail: "",
      startedAt: "2026-09-10T14:00:00Z",
      finishedAt: "2026-09-10T14:04:00Z",
      reportJson: JSON.stringify(planDoc({ mode: "apply" })),
    });

    const result = await status({ projectId: "p1", handle: "dep-abc123" });
    expect(result.jobStatus).toBe("completed");
    expect(result.report?.kind).toBe("report");
  });
});
