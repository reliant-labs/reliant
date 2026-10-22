// Copyright (c) 2025 Reliant Labs

/**
 * The wire mapping of mail delivery and returned password-set codes: the
 * enum reaches the tab as a word (UNSPECIFIED, from a server that predates
 * it, as "unknown"), return_code is sent only when asked for, and an absent
 * or empty code maps to null rather than to a blank credential.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

import { IdentityMailDelivery, IdentityNotEnabledReason } from "@/gen/controlplane/controlplane/v1/identity_pb";

const rpc = vi.hoisted(() => ({
  getIdentityStatus: vi.fn(),
  inviteUser: vi.fn(),
  sendPasswordReset: vi.fn(),
  listUsers: vi.fn(),
  requestExport: vi.fn(),
}));
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => rpc }));
vi.mock("@/services/controlPlane/reliantAI", () => ({ getWalletOverview: vi.fn() }));

import { getIdentityStatus, inviteIdentityUser, listIdentityUsers, requestIdentityExport, sendIdentityPasswordReset } from "../api";

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

beforeEach(() => {
  for (const fn of Object.values(rpc)) fn.mockReset();
});

describe("identity api: mail delivery", () => {
  it.each([
    [IdentityMailDelivery.NONE, "none"],
    [IdentityMailDelivery.SMTP, "smtp"],
    [IdentityMailDelivery.UNSPECIFIED, "unknown"],
  ])("maps %s to %s", async (wire, want) => {
    rpc.getIdentityStatus.mockResolvedValue(wireStatus(wire));
    expect((await getIdentityStatus("o", "e")).mailDelivery).toBe(want);
  });
});

describe("identity api: returned codes", () => {
  it("asks for the code only when told to, and maps it", async () => {
    rpc.sendPasswordReset.mockResolvedValue({
      code: { code: "ABC123", expiresAt: { seconds: 1_800_000_000n, nanos: 0 }, resetUrl: "https://app.test/auth/reset?code=ABC123" },
    });
    const got = await sendIdentityPasswordReset("o", "e", "u", { returnCode: true });
    expect(rpc.sendPasswordReset).toHaveBeenCalledWith({ orgId: "o", environmentId: "e", userId: "u", returnCode: true });
    expect(got).toEqual({ code: "ABC123", expiresAtMs: 1_800_000_000_000, resetUrl: "https://app.test/auth/reset?code=ABC123" });

    rpc.sendPasswordReset.mockResolvedValue({});
    expect(await sendIdentityPasswordReset("o", "e", "u")).toBeNull();
    expect(rpc.sendPasswordReset).toHaveBeenLastCalledWith({ orgId: "o", environmentId: "e", userId: "u", returnCode: false });
  });

  it("maps an invite with no code (an account that already had a password) to null", async () => {
    rpc.inviteUser.mockResolvedValue({ userId: "u", created: false, code: { code: "", resetUrl: "" } });
    const got = await inviteIdentityUser("o", "e", { email: " a@b.test ", roles: [], returnCode: true });
    expect(rpc.inviteUser).toHaveBeenCalledWith({ orgId: "o", environmentId: "e", email: "a@b.test", roles: [], returnCode: true });
    expect(got).toEqual({ userId: "u", created: false, code: null });
  });
});

describe("identity api: why there is no instance, and named environments", () => {
  it.each([
    [IdentityNotEnabledReason.NOT_DECLARED, "", "not-declared"],
    [IdentityNotEnabledReason.PENDING, "", "pending"],
    [IdentityNotEnabledReason.NO_SHAPE, "", "no-shape"],
    [IdentityNotEnabledReason.REFUSED, "share_with names nobody", "refused"],
    // A server that predates the field says only refused.
    [IdentityNotEnabledReason.UNSPECIFIED, "share_with names nobody", "refused"],
    [IdentityNotEnabledReason.UNSPECIFIED, "", "unknown"],
  ])("maps reason %s (refused %j) to %s", async (wire, refused, want) => {
    rpc.getIdentityStatus.mockResolvedValue({ ...wireStatus(IdentityMailDelivery.NONE), hasInstance: false, notEnabledReason: wire, refused });
    expect((await getIdentityStatus("o", "e")).notEnabledReason).toBe(want);
  });

  it("maps the owner and sharers with their names", async () => {
    rpc.getIdentityStatus.mockResolvedValue({
      ...wireStatus(IdentityMailDelivery.SMTP),
      ownerEnvironment: { id: "e1", name: "prod", project: "acme" },
      sharerEnvironments: [{ id: "e2", name: "", project: "" }],
    });
    const got = await getIdentityStatus("o", "e");
    expect(got.ownerEnvironment).toEqual({ id: "e1", name: "prod", project: "acme" });
    expect(got.sharerEnvironments).toEqual([{ id: "e2", name: "", project: "" }]);
  });
});

describe("identity api: users", () => {
  const wireUser = { userId: "u", email: "a@b.test", displayName: "", state: 2, roles: [] };
  it("maps the last sign-in and the total, and says the server reports sign-ins", async () => {
    rpc.listUsers.mockResolvedValue({
      users: [{ ...wireUser, lastSignInAt: { seconds: 1_800_000_000n, nanos: 0 } }],
      nextPageToken: "",
      roles: [],
      totalSize: 3,
    });
    const got = await listIdentityUsers("o", "e", {}, "");
    expect(got.users[0].lastSignInAtMs).toBe(1_800_000_000_000);
    expect(got.totalSize).toBe(3);
    expect(got.reportsSignIns).toBe(true);
  });

  it("claims no count, and no 'never signed in', from a server that does not count", async () => {
    rpc.listUsers.mockResolvedValue({ users: [wireUser], nextPageToken: "", roles: [], totalSize: 0 });
    const got = await listIdentityUsers("o", "e", {}, "");
    expect(got.totalSize).toBeNull();
    expect(got.reportsSignIns).toBe(false);
    expect(got.users[0].lastSignInAtMs).toBeNull();
  });
});

describe("identity api: on-demand export", () => {
  it("requests an export for the org and environment and maps the signed link", async () => {
    rpc.requestExport.mockResolvedValue({ exportUrl: "https://signed.example/x", expiresAt: { seconds: 1_800_000_000n, nanos: 0 } });
    expect(await requestIdentityExport("o", "e")).toEqual({ url: "https://signed.example/x", expiresAtMs: 1_800_000_000_000 });
    expect(rpc.requestExport).toHaveBeenCalledWith({ orgId: "o", environmentId: "e" });
  });
});
