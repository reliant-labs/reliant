// Copyright (c) 2025 Reliant Labs

/**
 * The managed store's protected-environment surface (design D6), as its
 * answers are read: what ListSecrets now carries, how a refusal is
 * recognised, what the two step-up RPCs answer, how their failures map onto
 * the dialog's vocabulary, and who set each version. The requests themselves
 * are the generated hooks' (hooks/forge-queries.ts, the step-up gate), pinned
 * by those callers' tests.
 */

import { describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  BeginStepUpResponseSchema,
  CompleteStepUpResponseSchema,
  GetSecretVersionsResponseSchema,
  ListSecretsResponseSchema,
  SecretAuthorKind,
} from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "http://127.0.0.1:8090" }));

import {
  historyOf,
  isStepUpRequired,
  listingOf,
  sendFailureOf,
  stepUpChallengeOf,
  stepUpGrantOf,
  verifyFailureOf,
} from "../secretStore";
import { authorLabel, modeExplanation, protectionOf } from "../secretSurface";

const UNTIL = new Date("2026-10-22T12:15:00Z");

function refused(reason?: string, code = Code.FailedPrecondition): ConnectError {
  const metadata = new Headers();
  if (reason) metadata.set("x-forge-error-reason", reason);
  return new ConnectError("this environment is protected", code, metadata);
}

describe("ListSecrets carries protection", () => {
  it("reports protected and the caller's grant", () => {
    const listing = listingOf(create(ListSecretsResponseSchema, {
      secrets: [{ name: "B" }, { name: "A" }],
      protected: true,
      stepUpValidUntil: timestampFromDate(UNTIL),
    }));
    expect(listing.protected).toBe(true);
    expect(listing.stepUpValidUntil).toBe(UNTIL.toISOString());
    expect(listing.secrets.map((s) => s.name)).toEqual(["A", "B"]);
  });

  it("an unprotected environment carries no grant", () => {
    expect(listingOf(create(ListSecretsResponseSchema, { secrets: [], protected: false }))).toEqual({ secrets: [], protected: false });
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
  it("BeginStepUp answers where the code went", () => {
    const res = create(BeginStepUpResponseSchema, { sentTo: "a***@acme.dev", expiresAt: timestampFromDate(UNTIL) });
    expect(stepUpChallengeOf(res)).toEqual({ sentTo: "a***@acme.dev", expiresAt: UNTIL.toISOString() });
  });

  it("CompleteStepUp answers until when the grant holds, and nothing without one", () => {
    expect(stepUpGrantOf(create(CompleteStepUpResponseSchema, { validUntil: timestampFromDate(UNTIL) }))).toEqual({
      validUntil: UNTIL.toISOString(),
    });
    expect(stepUpGrantOf(create(CompleteStepUpResponseSchema, {}))).toEqual({});
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
  it("maps every author kind, and an absent one to not recorded", () => {
    const { versions } = historyOf(create(GetSecretVersionsResponseSchema, {
      versions: [
        { version: 1, destroyed: false, setBy: { kind: SecretAuthorKind.USER, email: "alice@acme.dev" } },
        { version: 2, destroyed: false, setBy: { kind: SecretAuthorKind.TOKEN, email: "" } },
        { version: 3, destroyed: false, setBy: { kind: SecretAuthorKind.PLATFORM, email: "" } },
        { version: 4, destroyed: false },
        { version: 5, destroyed: false, setBy: { kind: SecretAuthorKind.UNSPECIFIED, email: "" } },
        { version: 6, destroyed: false, setBy: { kind: SecretAuthorKind.USER, email: "" } },
      ],
    }));
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
