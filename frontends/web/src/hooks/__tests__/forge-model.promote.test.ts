// Copyright (c) 2025 Reliant Labs

/**
 * THE OUTGOING REQUEST, and the refusal that comes back.
 *
 * Pure model functions (hooks/forge-model.ts), so nothing reaches a daemon.
 * That is not just test hygiene here: an earlier agent wrote to
 * control-plane's live release ledger and had to restore it from git, and a
 * promote overwrites a binding that keeps no history. The hook that sends this
 * request over the transport is pinned in forge-keys.test.tsx.
 *
 * What is pinned:
 *   the apply request carries EXACTLY ONE of the two token fields
 *   it carries the release the RENDERED PLAN targeted
 *   a refusal is read from the structured DETAIL, never from the message string
 *   plan and apply are separate rpcs, so nothing can write by defaulting a flag
 */

import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";

import {
  ForgePromoteRefusalReason,
  ForgePromoteRefusalSchema,
  ForgeService,
  PlanForgePromoteRequestSchema,
} from "@/gen/reliant/v1/forge_pb";
import { confirmationTokenFor } from "@/services/forge/promote";
import { applyPromoteRequest, promoteRefusalOf } from "../forge-model";

describe("the ApplyPromote request", () => {
  it("sends expectedCurrentRelease alone for a bound env", () => {
    // The token is derived from a plan, exactly as the UI does it.
    const plan = { env: "staging", current: { bound: true, release: "v1.3.0" }, target: { release: "v1.5.15" } };
    const token = confirmationTokenFor(plan);
    expect(token).not.toBeNull();

    const sent = applyPromoteRequest({ projectId: "p1", env: "staging", release: "v1.5.15", token: token! });

    expect(sent.expectedCurrentRelease).toBe("v1.3.0");
    // EXACTLY ONE claim: expectUnbound must be false, or the server rejects the
    // pair as contradictory.
    expect(sent.expectUnbound).toBe(false);
    // And the release is the one the plan targeted.
    expect(sent.release).toBe("v1.5.15");
    expect(sent.env).toBe("staging");
  });

  it("sends expectUnbound alone for a first promote", () => {
    const token = confirmationTokenFor({ env: "preprod", current: { bound: false } });
    expect(token).toEqual({ expectUnbound: true });

    const sent = applyPromoteRequest({ projectId: "p1", env: "preprod", release: "v1.5.15", token: token! });

    expect(sent.expectUnbound).toBe(true);
    // Empty, not a guessed release. A non-empty value here alongside
    // expectUnbound is rejected as contradictory.
    expect(sent.expectedCurrentRelease).toBe("");
  });

  it("never sends both, for either token shape", () => {
    for (const plan of [{ current: { bound: true, release: "v1.3.0" } }, { current: { bound: false } }]) {
      const token = confirmationTokenFor(plan)!;
      const sent = applyPromoteRequest({ projectId: "p1", env: "e", release: "v1.5.15", token });
      // Exactly one of the two is a real claim.
      const claims = [sent.expectedCurrentRelease !== "", sent.expectUnbound === true];
      expect(claims.filter(Boolean)).toHaveLength(1);
    }
  });
});

describe("PlanPromote is read-only and separate", () => {
  it("has no dry-run style field to default", () => {
    // The request carries no field that could flip this into a write.
    const fields = PlanForgePromoteRequestSchema.fields.map((field) => field.localName);
    expect(fields).not.toContain("dryRun");
    expect(fields).not.toContain("plan");
    expect(fields).not.toContain("apply");
  });

  it("is its own rpc, declared free of side effects, beside the write", () => {
    expect(ForgeService.method.planPromote).not.toBe(ForgeService.method.applyPromote);
    // NO_SIDE_EFFECTS is what makes forge generate a query for the preview —
    // and TestForgeService_NoSideEffectsRPCsOnlyRead what holds the handler
    // to it. The write is never one.
    expect(ForgeService.method.planPromote.idempotency).toBe(MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS);
    expect(ForgeService.method.applyPromote.idempotency).not.toBe(MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS);
  });
});

describe("refusal", () => {
  it("is read from the structured detail, not the message", () => {
    const detail = create(ForgePromoteRefusalSchema, {
      reason: ForgePromoteRefusalReason.STALE_CURRENT_RELEASE,
      expectedCurrentRelease: "v1.3.0",
      expectedUnbound: false,
      actualBound: true,
      actualCurrentRelease: "v1.4.2",
      actualPromotedAt: "2026-09-10T13:59:00Z",
      detail: "staging is bound to v1.4.2, not v1.3.0",
    });

    const refusal = promoteRefusalOf(
      new ConnectError("promote refused, nothing was written: staging is bound to v1.4.2, not v1.3.0", Code.FailedPrecondition, undefined, [
        { desc: ForgePromoteRefusalSchema, value: detail },
      ])
    );

    // A refusal is DATA — useApplyForgePromote resolves it as `{ kind:
    // "refused" }` — so no call site can render it as a generic failure by
    // forgetting to catch.
    expect(refusal).not.toBeNull();
    expect(refusal!.reason).toBe("stale-current-release");
    expect(refusal!.actualCurrentRelease).toBe("v1.4.2");
    expect(refusal!.actualBound).toBe(true);
    expect(refusal!.actualPromotedAt).toBe("2026-09-10T13:59:00Z");
    expect(refusal!.expectedCurrentRelease).toBe("v1.3.0");
  });

  it("is not one when a FailedPrecondition carries no decodable detail", () => {
    // Fails closed: the hook rethrows. An empty refusal panel would claim
    // facts about the binding that never arrived.
    expect(
      promoteRefusalOf(new ConnectError("promote refused, nothing was written: unexplained", Code.FailedPrecondition))
    ).toBeNull();
  });

  it("does not mistake another FailedPrecondition for a refusal", () => {
    expect(promoteRefusalOf(new ConnectError("project directory does not exist", Code.FailedPrecondition))).toBeNull();
  });

  it("does not swallow an unrelated error code", () => {
    expect(promoteRefusalOf(new ConnectError("no", Code.PermissionDenied))).toBeNull();
    expect(promoteRefusalOf(new Error("not a connect error"))).toBeNull();
  });
});
