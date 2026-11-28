// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * The wire mapping of mail delivery and returned password-set codes: the
 * enum reaches the tab as a word (UNSPECIFIED as "unknown"), and an absent
 * or empty code maps to null rather than to a blank credential.
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { IdentityMailDelivery, IdentityNotEnabledReason, IdentityUserState } from "@/gen/controlplane/controlplane/v1/identity_pb";
import {
  GetIdentityStatusResponseSchema,
  ListIdentityUsersResponseSchema,
  RequestIdentityExportResponseSchema,
} from "@/gen/controlplane/services/identity/v1/identity_pb";

import { listIdentityUsersRequest, toIdentityExportURL, toIdentityPasswordCode, toIdentityStatus, toIdentityUsersPage } from "../api";

const getIdentityStatus = (wire: MessageInitShape<typeof GetIdentityStatusResponseSchema>) => toIdentityStatus(create(GetIdentityStatusResponseSchema, wire));
const listIdentityUsers = (wire: MessageInitShape<typeof ListIdentityUsersResponseSchema>) => toIdentityUsersPage(create(ListIdentityUsersResponseSchema, wire));

const wireStatus = (mailDelivery: IdentityMailDelivery) => ({
  hasInstance: true,
  state: 0,
  issuer: "",
  domain: "",
  public: false,
  adopted: false,
  apps: [],
  deliveries: [],
  ownerEnvironmentId: "",
  sharerEnvironmentIds: [],
  lastError: "",
  failures: 0,
  exportUrlAvailable: false,
  refused: "",
  callerCanManage: true,
  exportEnabled: false,
  mailDelivery,
  notEnabledReason: 0,
  notEnabledMessage: "",
  sharerEnvironments: [],
});

describe("identity api: mail delivery", () => {
  it.each([
    [IdentityMailDelivery.NONE, "none"],
    [IdentityMailDelivery.SMTP, "smtp"],
    [IdentityMailDelivery.UNSPECIFIED, "unknown"],
  ])("maps %s to %s", (wire, want) => {
    expect(getIdentityStatus(wireStatus(wire)).mailDelivery).toBe(want);
  });
});

describe("identity api: returned codes", () => {
  it("maps a returned code", () => {
    const got = toIdentityPasswordCode({
      $typeName: "controlplane.v1.IdentityPasswordCode",
      code: "ABC123",
      expiresAt: { $typeName: "google.protobuf.Timestamp", seconds: 1_800_000_000n, nanos: 0 },
      resetUrl: "https://app.test/auth/reset?code=ABC123",
    });
    expect(got).toEqual({ code: "ABC123", expiresAtMs: 1_800_000_000_000, resetUrl: "https://app.test/auth/reset?code=ABC123" });
    expect(toIdentityPasswordCode(undefined)).toBeNull();
  });

  it("maps an empty code (an account that already had a password) to null", () => {
    expect(toIdentityPasswordCode({ $typeName: "controlplane.v1.IdentityPasswordCode", code: "", resetUrl: "" })).toBeNull();
  });
});

describe("identity api: why there is no instance, and named environments", () => {
  it.each([
    [IdentityNotEnabledReason.NOT_DECLARED, "", "not-declared"],
    [IdentityNotEnabledReason.PENDING, "", "pending"],
    [IdentityNotEnabledReason.NO_SHAPE, "", "no-shape"],
    [IdentityNotEnabledReason.REFUSED, "share_with names nobody", "refused"],
    [IdentityNotEnabledReason.UNSPECIFIED, "", "unknown"],
  ])("maps reason %s (refused %j) to %s", (wire, refused, want) => {
    expect(getIdentityStatus({ ...wireStatus(IdentityMailDelivery.NONE), hasInstance: false, notEnabledReason: wire, refused }).notEnabledReason).toBe(want);
  });

  it("maps the owner and sharers with their names", () => {
    const got = getIdentityStatus({
      ...wireStatus(IdentityMailDelivery.SMTP),
      ownerEnvironment: { id: "e1", name: "prod", project: "acme" },
      sharerEnvironments: [{ id: "e2", name: "", project: "" }],
    });
    expect(got.ownerEnvironment).toEqual({ id: "e1", name: "prod", project: "acme" });
    expect(got.sharerEnvironments).toEqual([{ id: "e2", name: "", project: "" }]);
  });
});

describe("identity api: users", () => {
  const wireUser = { userId: "u", email: "a@b.test", displayName: "", state: 2, roles: [] };
  it("maps the last sign-in and the total, and says the server reports sign-ins", () => {
    const got = listIdentityUsers({
      users: [{ ...wireUser, lastSignInAt: { seconds: 1_800_000_000n, nanos: 0 } }],
      nextPageToken: "",
      roles: [],
      totalSize: 3,
    });
    expect(got.users[0].lastSignInAtMs).toBe(1_800_000_000_000);
    expect(got.totalSize).toBe(3);
    expect(got.reportsSignIns).toBe(true);
  });

  it("claims no count, and no 'never signed in', from a server that does not count", () => {
    const got = listIdentityUsers({ users: [wireUser], nextPageToken: "", roles: [], totalSize: 0 });
    expect(got.totalSize).toBeNull();
    expect(got.reportsSignIns).toBe(false);
    expect(got.users[0].lastSignInAtMs).toBeNull();
  });
});

describe("identity api: users request", () => {
  it("sends a trimmed query and the state only when filtering, one page at a time", () => {
    expect(listIdentityUsersRequest("o", "e", {})).toEqual({ orgId: "o", environmentId: "e", query: undefined, state: undefined, pageSize: 50 });
    expect(listIdentityUsersRequest("o", "e", { query: "  bob ", state: "locked" })).toEqual({
      orgId: "o",
      environmentId: "e",
      query: "bob",
      state: IdentityUserState.LOCKED,
      pageSize: 50,
    });
  });
});

describe("identity api: on-demand export", () => {
  it("maps the signed link", () => {
    const wire = create(RequestIdentityExportResponseSchema, { exportUrl: "https://signed.example/x", expiresAt: { seconds: 1_800_000_000n, nanos: 0 } });
    expect(toIdentityExportURL(wire)).toEqual({ url: "https://signed.example/x", expiresAtMs: 1_800_000_000_000 });
  });
});
