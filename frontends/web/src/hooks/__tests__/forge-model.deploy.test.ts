// Copyright (c) 2025 Reliant Labs

/**
 * THE OUTGOING REQUEST, and the four refusals that come back.
 *
 * ⛔ PURE MODEL FUNCTIONS (hooks/forge-model.ts), SO NOTHING REACHES A DAEMON,
 * AND NOTHING CAN REACH A CLUSTER. This is not test hygiene on this surface.
 * StartDeploy applies manifests to a live target; control-plane's prod
 * declares gke_reliant-labs-475814_us-central1_prod, and unlike a promote —
 * which moves a pointer git can restore — a deploy is recoverable from nowhere.
 * The hook that sends the request over the transport is pinned in
 * forge-keys.test.tsx.
 *
 * What is pinned:
 *   expected_declared_context is derived from the RENDERED PLAN and is always sent
 *   exactly one of the two release claims is sent, never both and never neither
 *   no plan means no token, so no request can be built at all
 *   no skip_preflight / no_digest / force field exists on any deploy request
 *   plan, start and status are separate rpcs, so nothing writes by defaulting a flag
 *   each refusal reason is read from the structured DETAIL, never the message
 *   an unrecognised job status becomes `unknown`, never `completed`
 */

import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";

import {
  ForgeDeployJobStatus,
  ForgeDeployRefusalReason,
  ForgeDeployRefusalSchema,
  ForgeReportMetaSchema,
  ForgeService,
  GetForgeDeployStatusRequestSchema,
  GetForgeDeployStatusResponseSchema,
  PlanForgeDeployRequestSchema,
  StartForgeDeployRequestSchema,
  StartForgeDeployResponseSchema,
} from "@/gen/reliant/v1/forge_pb";
import { deployTokenFor } from "@/services/forge/deploy";
import { deployRefusalOf, deployStatusOf, startDeployRequest, startDeployResultOf } from "../forge-model";

/**
 * The content approval every start now carries: the digest of the plan the
 * operator read, and the release it was computed for. The target token
 * authorises WHERE; this authorises WHAT.
 */
const APPROVAL = {
  approveDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
  releaseVersion: "20261003.114500-abcdef123456",
  acknowledgedFindings: [] as string[],
};

const okMeta = create(ForgeReportMetaSchema, {
  isForgeProject: true,
  supported: true,
  forgeVersion: "v0.1.15",
});

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

function startResponse(init: MessageInitShape<typeof StartForgeDeployResponseSchema> = {}) {
  return create(StartForgeDeployResponseSchema, {
    meta: okMeta,
    handle: "dep-abc123",
    env: "prod",
    startedAt: "2026-09-10T14:00:00Z",
    jobStatus: ForgeDeployJobStatus.RUNNING,
    reportJson: JSON.stringify(planDoc()),
    ...init,
  });
}

function statusResponse(init: MessageInitShape<typeof GetForgeDeployStatusResponseSchema> = {}) {
  return create(GetForgeDeployStatusResponseSchema, {
    meta: okMeta,
    handle: "dep-abc123",
    env: "prod",
    jobStatus: ForgeDeployJobStatus.RUNNING,
    jobStatusDetail: "",
    startedAt: "2026-09-10T14:00:00Z",
    finishedAt: "",
    reportJson: "",
    ...init,
  });
}

describe("the confirmation token", () => {
  it("carries expected_declared_context derived from the RENDERED plan", () => {
    // Derived exactly as the UI does it: from the document, not from a prop.
    const token = deployTokenFor(planDoc());
    expect(token).not.toBeNull();

    const sent = startDeployRequest({ projectId: "p1", env: "prod", token: token!, approval: APPROVAL });

    // THE CLUSTER CLAIM — the most consequential field in the message, and it is
    // the declared context, NOT the ambient current_context.
    expect(sent.expectedDeclaredContext).toBe("gke_reliant-labs-475814_us-central1_prod");
    expect(sent.expectedDeclaredContext).not.toBe("k3d-control-plane");
    expect(sent.expectedCurrentRelease).toBe("v1.5.15");
    expect(sent.expectUnbound).toBe(false);
    expect(sent.env).toBe("prod");
    // THE CONTENT CLAIM rides alongside.
    expect(sent.approveDigest).toBe(APPROVAL.approveDigest);
    expect(sent.releaseVersion).toBe(APPROVAL.releaseVersion);
  });

  it("sends expectUnbound alone for an env with no release binding", () => {
    const token = deployTokenFor(planDoc({ release: "" }));
    expect(token).toEqual({
      expectedDeclaredContext: "gke_reliant-labs-475814_us-central1_prod",
      expectUnbound: true,
    });

    const sent = startDeployRequest({ projectId: "p1", env: "prod", token: token!, approval: APPROVAL });

    // Empty, not a guessed release — a non-empty value alongside expectUnbound is
    // rejected by the server as contradictory.
    expect(sent.expectedCurrentRelease).toBe("");
    expect(sent.expectUnbound).toBe(true);
    // The cluster claim is still mandatory and still present.
    expect(sent.expectedDeclaredContext).toBe("gke_reliant-labs-475814_us-central1_prod");
  });

  it("never sends both release claims, and never sends an empty cluster", () => {
    for (const doc of [planDoc(), planDoc({ release: "" })]) {
      const token = deployTokenFor(doc)!;
      const sent = startDeployRequest({ projectId: "p1", env: "prod", token, approval: APPROVAL });
      const claims = [sent.expectedCurrentRelease !== "", sent.expectUnbound === true];
      expect(claims.filter(Boolean)).toHaveLength(1);
      // There is no legitimate deploy whose target cluster nobody saw.
      expect(sent.expectedDeclaredContext).not.toBe("");
    }
  });

  it("produces NO token — so no request is possible — without a rendered plan", () => {
    // Nothing at all.
    expect(deployTokenFor(null)).toBeNull();
    expect(deployTokenFor(undefined)).toBeNull();
    expect(deployTokenFor({})).toBeNull();

    // An APPLY report: it describes a deploy that already happened and cannot
    // authorise another.
    expect(deployTokenFor(planDoc({ mode: "apply" }))).toBeNull();

    // Forge's own guard refused. No token overrides it.
    expect(deployTokenFor(planDoc({ guard: { declared_context: "gke_x", verdict: "refuse" } }))).toBeNull();

    // No declared cluster: there is nothing to name, and the request has no
    // "unspecified" spelling.
    expect(deployTokenFor(planDoc({ guard: { declared_context: "", verdict: "allow" } }))).toBeNull();
  });
});

describe("no escape hatches on any deploy request", () => {
  it("declares no skip_preflight, no_digest or force field", () => {
    const forbidden = ["skipPreflight", "noDigest", "force", "dryRun"];

    // The SCHEMAS, not one request built from them: a field nobody sets here is
    // still a field some caller could.
    for (const schema of [PlanForgeDeployRequestSchema, StartForgeDeployRequestSchema, GetForgeDeployStatusRequestSchema]) {
      const keys = schema.fields.map((field) => field.localName);
      for (const field of forbidden) {
        expect(keys).not.toContain(field);
      }
      // And a shape assertion, so a field added under a name nobody predicted
      // is still caught.
      //
      // `digest` ALONE IS NOT FORBIDDEN, and the distinction is the point. The
      // hazard was ever only the SUPPRESSION of digest pinning — a `noDigest`
      // that let a re-tagged layer ship in place of the bytes that were built.
      // `approveDigest` is the opposite: it names the plan a human approved,
      // and it is what makes the deploy refusable. So the pattern matches a
      // negated digest field rather than the word, which keeps the guard sharp
      // instead of forcing it to be deleted the first time a legitimate digest
      // field appears.
      for (const key of keys) {
        expect(key.toLowerCase()).not.toMatch(/preflight|force/);
        expect(key.toLowerCase()).not.toMatch(/^(no|skip|without|disable).*digest/);
      }
    }
  });

  it("keeps plan, start and status as three distinct rpcs, and no combined deploy", () => {
    const methods = ForgeService.methods.map((method) => method.localName);
    expect(methods).toContain("planDeploy");
    expect(methods).toContain("startDeploy");
    expect(methods).toContain("getDeployStatus");
    // There is no combined one-call deploy on the service — a call that
    // previewed and applied is the shape this whole path makes unavailable.
    expect(methods).not.toContain("deploy");
    expect(methods).not.toContain("deployEnv");
    expect(methods).not.toContain("applyDeploy");

    // The preview is declared free of side effects (so forge generates a
    // query for it); the write never is.
    expect(ForgeService.method.planDeploy.idempotency).toBe(MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS);
    expect(ForgeService.method.startDeploy.idempotency).not.toBe(MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS);
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

  it("reads all four reasons from the structured detail, not the message", () => {
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

      const refusal = deployRefusalOf(
        // The message deliberately does NOT name the reason: branching on it would
        // break the moment the wording improves.
        new ConnectError("deploy refused, nothing was applied: see detail", Code.FailedPrecondition, undefined, [
          { desc: ForgeDeployRefusalSchema, value: detail },
        ])
      );

      // A refusal is DATA — the deploy hooks resolve it as `{ kind: "refused" }`
      // — so no call site can render it as a generic failure by forgetting to
      // catch.
      expect(refusal).not.toBeNull();
      expect(refusal!.reason).toBe(reason.expected);
      expect(refusal!.actualDeclaredContext).toBe("k3d-control-plane");
      expect(refusal!.runningHandle).toBe("dep-running-1");
      expect(refusal!.guardFix).toContain("get-credentials");
    }
  });

  it("maps an unrecognised reason to `unknown` rather than dropping the refusal", () => {
    const detail = create(ForgeDeployRefusalSchema, {
      reason: ForgeDeployRefusalReason.UNSPECIFIED,
      detail: "refused for a reason this build does not know",
    });
    const refusal = deployRefusalOf(
      new ConnectError("deploy refused, nothing was applied", Code.FailedPrecondition, undefined, [
        { desc: ForgeDeployRefusalSchema, value: detail },
      ])
    );
    // Still a refusal. A refusal this build cannot classify is a refusal, not a
    // started deploy.
    expect(refusal?.reason).toBe("unknown");
  });

  it("is not one when a FailedPrecondition carries no decodable detail", () => {
    // Fails closed: the hook rethrows. An empty refusal panel would claim facts
    // about the cluster that never arrived.
    expect(
      deployRefusalOf(new ConnectError("deploy refused, nothing was applied: unexplained", Code.FailedPrecondition))
    ).toBeNull();
  });

  it("does not swallow an unrelated error code", () => {
    expect(deployRefusalOf(new ConnectError("no", Code.PermissionDenied))).toBeNull();
  });
});

describe("the job lifecycle", () => {
  it("reads a started deploy with its handle and a RUNNING disposition", () => {
    const result = startDeployResultOf(startResponse());
    expect(result.kind).toBe("started");
    if (result.kind !== "started") throw new Error("expected a start");
    expect(result.handle).toBe("dep-abc123");
    expect(result.jobStatus).toBe("running");
    // The carried document is the GUARD PLAN — mode dry_run, not an apply report.
    expect(result.guardPlan?.kind).toBe("report");
    if (result.guardPlan?.kind === "report") {
      expect(result.guardPlan.report.mode).toBe("dry_run");
    }
  });

  it("reads a handle-less reply as not-started rather than something to poll", () => {
    const result = startDeployResultOf(
      startResponse({
        meta: { ...okMeta, supported: false, unsupportedReason: "forge too old" },
        handle: "",
        jobStatus: ForgeDeployJobStatus.UNSPECIFIED,
        reportJson: "",
      })
    );
    // Not a started deploy with an empty handle a client would poll forever.
    expect(result.kind).toBe("not-started");
  });

  it("maps an unrecognised job status to `unknown`, NEVER to completed", () => {
    // A value from a newer server this build has never seen.
    expect(deployStatusOf(statusResponse({ jobStatus: 99 as ForgeDeployJobStatus })).jobStatus).toBe("unknown");
  });

  it("maps UNSPECIFIED to `unknown` too — the server said something unclassifiable", () => {
    expect(deployStatusOf(statusResponse({ jobStatus: ForgeDeployJobStatus.UNSPECIFIED })).jobStatus).toBe("unknown");
  });

  it("treats an empty report body while running as absent, not malformed", () => {
    const result = deployStatusOf(statusResponse());
    // Null rather than an outcome claiming forge said something it did not.
    expect(result.report).toBeNull();
    expect(result.jobStatus).toBe("running");
  });

  it("classifies a finished report once one arrives", () => {
    const result = deployStatusOf(
      statusResponse({
        jobStatus: ForgeDeployJobStatus.COMPLETED,
        finishedAt: "2026-09-10T14:04:00Z",
        reportJson: JSON.stringify(planDoc({ mode: "apply" })),
      })
    );
    expect(result.jobStatus).toBe("completed");
    expect(result.report?.kind).toBe("report");
  });
});
