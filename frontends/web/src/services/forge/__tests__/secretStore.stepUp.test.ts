// Copyright (c) 2025 Reliant Labs

/**
 * The managed store's protected-environment surface (design D6) at the
 * transport boundary: what ListSecrets now carries, how a refusal is
 * recognised, the two step-up RPCs, how their failures map onto the dialog's
 * vocabulary, and who set each version.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { SecretAuthorKind } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";

const rpc = {
  listSecrets: vi.fn(),
  getSecretVersions: vi.fn(),
  beginStepUp: vi.fn(),
  completeStepUp: vi.fn(),
};

vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => rpc }));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "http://127.0.0.1:8090" }));

import {
  beginStepUp,
  completeStepUp,
  getSecretVersions,
  isStepUpRequired,
  listSecrets,
  sendFailureOf,
  verifyFailureOf,
} from "../secretStore";
import { authorLabel, modeExplanation, protectionOf } from "../secretSurface";

const UNTIL = new Date("2026-10-22T12:15:00Z");

function refused(reason?: string, code = Code.FailedPrecondition): ConnectError {
  const metadata = new Headers();
  if (reason) metadata.set("x-forge-error-reason", reason);
  return new ConnectError("this environment is protected", code, metadata);
}

beforeEach(() => {
  for (const fn of Object.values(rpc)) fn.mockReset();
});

describe("listSecrets carries protection", () => {
  it("reports protected and the caller's grant", async () => {
    rpc.listSecrets.mockResolvedValue({
      secrets: [{ name: "B" }, { name: "A" }],
      protected: true,
      stepUpValidUntil: timestampFromDate(UNTIL),
    });
    const listing = await listSecrets("denv_1");
    expect(listing.protected).toBe(true);
    expect(listing.stepUpValidUntil).toBe(UNTIL.toISOString());
    expect(listing.secrets.map((s) => s.name)).toEqual(["A", "B"]);
  });

  it("an unprotected environment carries no grant", async () => {
    rpc.listSecrets.mockResolvedValue({ secrets: [], protected: false });
    expect(await listSecrets("denv_1")).toEqual({ secrets: [], protected: false });
  });
});

describe("recognising a step-up refusal", () => {
  it("is FailedPrecondition WITH the step_up_required reason", () => {
    expect(isStepUpRequired(refused("step_up_required"))).toBe(true);
  });

  it("is not a bare FailedPrecondition — that is an older control plane's CAS conflict", () => {
    expect(isStepUpRequired(refused())).toBe(false);
    expect(isStepUpRequired(refused("something_else"))).toBe(false);
  });

  it("is not the reason on another code, nor a plain Error", () => {
    expect(isStepUpRequired(refused("step_up_required", Code.PermissionDenied))).toBe(false);
    expect(isStepUpRequired(new Error("step_up_required"))).toBe(false);
  });
});

describe("the step-up RPCs", () => {
  it("BeginStepUp is keyed on the environment and answers where the code went", async () => {
    rpc.beginStepUp.mockResolvedValue({ sentTo: "a***@acme.dev", expiresAt: timestampFromDate(UNTIL) });
    expect(await beginStepUp("denv_1")).toEqual({ sentTo: "a***@acme.dev", expiresAt: UNTIL.toISOString() });
    expect(rpc.beginStepUp).toHaveBeenCalledWith({ environmentId: "denv_1" });
  });

  it("CompleteStepUp sends the code inbound and answers until when the grant holds", async () => {
    rpc.completeStepUp.mockResolvedValue({ validUntil: timestampFromDate(UNTIL) });
    expect(await completeStepUp("denv_1", "ABCDE-FGHJK")).toEqual({ validUntil: UNTIL.toISOString() });
    expect(rpc.completeStepUp).toHaveBeenCalledWith({ environmentId: "denv_1", code: "ABCDE-FGHJK" });
  });

  it.each([
    [Code.ResourceExhausted, "rate-limited"],
    [Code.PermissionDenied, "forbidden"],
    [Code.Unavailable, "unavailable"],
    [Code.Internal, "other"],
  ] as const)("a BeginStepUp %s is %s", (code, want) => {
    expect(sendFailureOf(new ConnectError("x", code))).toBe(want);
  });

  it.each([
    [Code.PermissionDenied, "invalid"],
    [Code.Unavailable, "unavailable"],
    [Code.Unknown, "network"],
  ] as const)("a CompleteStepUp %s is %s", (code, want) => {
    expect(verifyFailureOf(new ConnectError("x", code))).toBe(want);
  });

  it("a non-Connect failure is a network failure, never an invalid code", () => {
    expect(verifyFailureOf(new TypeError("Failed to fetch"))).toBe("network");
    expect(sendFailureOf(new TypeError("Failed to fetch"))).toBe("other");
  });
});

describe("who set each version", () => {
  it("maps every author kind, and an absent one to not recorded", async () => {
    rpc.getSecretVersions.mockResolvedValue({
      versions: [
        { version: 1, destroyed: false, setBy: { kind: SecretAuthorKind.USER, email: "alice@acme.dev" } },
        { version: 2, destroyed: false, setBy: { kind: SecretAuthorKind.TOKEN, email: "" } },
        { version: 3, destroyed: false, setBy: { kind: SecretAuthorKind.PLATFORM, email: "" } },
        { version: 4, destroyed: false },
        { version: 5, destroyed: false, setBy: { kind: SecretAuthorKind.UNSPECIFIED, email: "" } },
        { version: 6, destroyed: false, setBy: { kind: SecretAuthorKind.USER, email: "" } },
      ],
    });
    const { versions } = await getSecretVersions("denv_1", "KEY");
    const byVersion = Object.fromEntries(versions.map((v) => [v.version, v.setBy]));
    expect(byVersion).toEqual({
      1: { kind: "user", email: "alice@acme.dev" },
      2: { kind: "token" },
      3: { kind: "platform" },
      4: null,
      5: null,
      6: { kind: "user" },
    });
    expect(versions.map((v) => authorLabel(v.setBy))).toEqual([
      "a signed-in user",
      "author not recorded",
      "author not recorded",
      "the platform (generated)",
      "an automation token",
      "alice@acme.dev",
    ]);
  });
});

describe("protectionOf", () => {
  const now = Date.parse("2026-10-22T12:00:00Z");

  it("is unprotected without a listing or with protected false", () => {
    expect(protectionOf(null, null, now)).toEqual({ protected: false, grantUntil: null });
    expect(protectionOf({ protected: false, stepUpValidUntil: UNTIL.toISOString() }, null, now)).toEqual({
      protected: false,
      grantUntil: null,
    });
  });

  it("takes the later of the listing's grant and the dialog's own", () => {
    const local = UNTIL.getTime() + 60_000;
    expect(protectionOf({ protected: true, stepUpValidUntil: UNTIL.toISOString() }, local, now).grantUntil).toBe(local);
    expect(protectionOf({ protected: true, stepUpValidUntil: UNTIL.toISOString() }, null, now).grantUntil).toBe(
      UNTIL.getTime()
    );
  });

  it("an expired grant is no grant", () => {
    expect(protectionOf({ protected: true, stepUpValidUntil: UNTIL.toISOString() }, null, UNTIL.getTime())).toEqual({
      protected: true,
      grantUntil: null,
    });
  });
});

describe("the store sentence for a self-managed environment", () => {
  it("says External Secrets delivers, not a deploy", () => {
    expect(modeExplanation("managed", "self_managed")).toMatch(/External Secrets/);
    expect(modeExplanation("managed", "persistent")).toMatch(/fetched at deploy time/);
  });
});
