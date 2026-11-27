// Copyright (c) 2026 Reliant Labs

/**
 * The entitlement mapping (wire.ts) and what a failure means (errors.ts):
 * timestamps as ms (the zero timestamp is unset); FEATURE_NOT_ENTITLED by its
 * reason alone and Unimplemented as outcomes, everything else rethrown.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { GetMyOrgEntitlementsResponseSchema, RequestOrgEntitlementResponseSchema } from "@/gen/controlplane/services/organization/v1/organization_pb";

import { mlErrorKind, outcomeOfError } from "../errors";
import { entitlementsOf, requestResultOf } from "../wire";

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
