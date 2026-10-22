// Copyright (c) 2025 Reliant Labs

/**
 * THE OUTGOING REQUEST, and the refusal that comes back.
 *
 * Mocked at the CLIENT BOUNDARY — createForgeClient — so nothing reaches a
 * daemon. That is not just test hygiene here: an earlier agent wrote to
 * control-plane's live release ledger and had to restore it from git, and a
 * promote overwrites a binding that keeps no history.
 *
 * What is pinned:
 *   the apply request carries EXACTLY ONE of the two token fields
 *   it carries the release the RENDERED PLAN targeted
 *   a refusal is read from the structured DETAIL, never from the message string
 *   plan and apply are separate functions, so nothing can write by defaulting a
 *     flag
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import { ForgePromoteRefusalReason, ForgePromoteRefusalSchema } from "@/gen/reliant/v1/forge_pb";

const planPromote = vi.fn();
const applyPromote = vi.fn();

vi.mock("../grpc-client", () => ({
  createForgeClient: () => ({ planPromote, applyPromote }),
}));

const okMeta = {
  isForgeProject: true,
  supported: true,
  forgeVersion: "v0.9.1",
  unsupportedReason: "",
  exitCode: 0,
  reachability: 0,
  unreachableReason: "",
};

beforeEach(() => {
  vi.clearAllMocks();
  applyPromote.mockResolvedValue({ meta: okMeta, reportJson: JSON.stringify({ applied: true }) });
  planPromote.mockResolvedValue({ meta: okMeta, reportJson: JSON.stringify({ dry_run: true }) });
});

describe("applyPromote request", () => {
  it("sends expectedCurrentRelease alone for a bound env", async () => {
    const { applyPromote: apply } = await import("../forge-grpc");
    const { confirmationTokenFor } = await import("@/services/forge/promote");

    // The token is derived from a plan, exactly as the UI does it.
    const plan = { env: "staging", current: { bound: true, release: "v1.3.0" }, target: { release: "v1.5.15" } };
    const token = confirmationTokenFor(plan);
    expect(token).not.toBeNull();

    await apply({ projectId: "p1", env: "staging", release: "v1.5.15", token: token! });

    const sent = applyPromote.mock.calls[0][0];
    expect(sent.expectedCurrentRelease).toBe("v1.3.0");
    // EXACTLY ONE claim: expectUnbound must be false, or the server rejects the
    // pair as contradictory.
    expect(sent.expectUnbound).toBe(false);
    // And the release is the one the plan targeted.
    expect(sent.release).toBe("v1.5.15");
    expect(sent.env).toBe("staging");
  });

  it("sends expectUnbound alone for a first promote", async () => {
    const { applyPromote: apply } = await import("../forge-grpc");
    const { confirmationTokenFor } = await import("@/services/forge/promote");

    const token = confirmationTokenFor({ env: "preprod", current: { bound: false } });
    expect(token).toEqual({ expectUnbound: true });

    await apply({ projectId: "p1", env: "preprod", release: "v1.5.15", token: token! });

    const sent = applyPromote.mock.calls[0][0];
    expect(sent.expectUnbound).toBe(true);
    // Empty, not a guessed release. A non-empty value here alongside
    // expectUnbound is rejected as contradictory.
    expect(sent.expectedCurrentRelease).toBe("");
  });

  it("never sends both, for either token shape", async () => {
    const { applyPromote: apply } = await import("../forge-grpc");
    const { confirmationTokenFor } = await import("@/services/forge/promote");

    for (const plan of [
      { current: { bound: true, release: "v1.3.0" } },
      { current: { bound: false } },
    ]) {
      applyPromote.mockClear();
      const token = confirmationTokenFor(plan)!;
      await apply({ projectId: "p1", env: "e", release: "v1.5.15", token });
      const sent = applyPromote.mock.calls[0][0];
      // Exactly one of the two is a real claim.
      const claims = [sent.expectedCurrentRelease !== "", sent.expectUnbound === true];
      expect(claims.filter(Boolean)).toHaveLength(1);
    }
  });
});

describe("planPromote is read-only and separate", () => {
  it("has no dry-run style flag to default, and writes nothing", async () => {
    const forgeGrpcModule = await import("../forge-grpc");

    await forgeGrpcModule.planPromote({ projectId: "p1", env: "staging", release: "v1.5.15" });

    // The plan call went to planPromote, and the write method was never touched.
    expect(planPromote).toHaveBeenCalledTimes(1);
    expect(applyPromote).not.toHaveBeenCalled();

    // The request carries no flag that could flip this into a write.
    const sent = planPromote.mock.calls[0][0];
    expect(sent.dryRun).toBeUndefined();
    expect(sent.plan).toBeUndefined();
    expect(sent.apply).toBeUndefined();

    // And the two remain distinct exported functions.
    expect(forgeGrpcModule.planPromote).not.toBe(forgeGrpcModule.applyPromote);
  });
});

describe("refusal", () => {
  it("is read from the structured detail, not the message", async () => {
    const detail = create(ForgePromoteRefusalSchema, {
      reason: ForgePromoteRefusalReason.STALE_CURRENT_RELEASE,
      expectedCurrentRelease: "v1.3.0",
      expectedUnbound: false,
      actualBound: true,
      actualCurrentRelease: "v1.4.2",
      actualPromotedAt: "2026-09-10T13:59:00Z",
      detail: "staging is bound to v1.4.2, not v1.3.0",
    });

    applyPromote.mockRejectedValue(
      new ConnectError("promote refused, nothing was written: staging is bound to v1.4.2, not v1.3.0", Code.FailedPrecondition, undefined, [
        { desc: ForgePromoteRefusalSchema, value: detail },
      ])
    );

    const { applyPromote: apply } = await import("../forge-grpc");
    const result = await apply({
      projectId: "p1",
      env: "staging",
      release: "v1.5.15",
      token: { expectedCurrentRelease: "v1.3.0" },
    });

    // A refusal is DATA, not a thrown error — so no call site can render it as a
    // generic failure by forgetting to catch.
    expect(result.kind).toBe("refused");
    if (result.kind !== "refused") throw new Error("expected a refusal");
    expect(result.refusal.reason).toBe("stale-current-release");
    expect(result.refusal.actualCurrentRelease).toBe("v1.4.2");
    expect(result.refusal.actualBound).toBe(true);
    expect(result.refusal.actualPromotedAt).toBe("2026-09-10T13:59:00Z");
    expect(result.refusal.expectedCurrentRelease).toBe("v1.3.0");
  });

  it("still throws when a FailedPrecondition carries no decodable detail", async () => {
    // Fails closed. An empty refusal panel would claim facts about the binding
    // that never arrived.
    applyPromote.mockRejectedValue(
      new ConnectError("promote refused, nothing was written: unexplained", Code.FailedPrecondition)
    );

    const { applyPromote: apply } = await import("../forge-grpc");
    await expect(
      apply({ projectId: "p1", env: "staging", release: "v1.5.15", token: { expectUnbound: true } })
    ).rejects.toThrow(/promote refused/);
  });

  it("does not mistake another FailedPrecondition for a refusal", async () => {
    applyPromote.mockRejectedValue(
      new ConnectError("project directory does not exist", Code.FailedPrecondition)
    );
    const { applyPromote: apply } = await import("../forge-grpc");
    await expect(
      apply({ projectId: "p1", env: "staging", release: "v1.5.15", token: { expectUnbound: true } })
    ).rejects.toThrow(/project directory/);
  });

  it("does not swallow an unrelated error code", async () => {
    applyPromote.mockRejectedValue(new ConnectError("no", Code.PermissionDenied));
    const { applyPromote: apply } = await import("../forge-grpc");
    await expect(
      apply({ projectId: "p1", env: "staging", release: "v1.5.15", token: { expectUnbound: true } })
    ).rejects.toThrow();
  });
});
