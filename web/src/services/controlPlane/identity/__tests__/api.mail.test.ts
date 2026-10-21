// Copyright (c) 2025 Reliant Labs

/**
 * The wire mapping of mail delivery and returned password-set codes: the
 * enum reaches the tab as a word (UNSPECIFIED, from a server that predates
 * it, as "unknown"), return_code is sent only when asked for, and an absent
 * or empty code maps to null rather than to a blank credential.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

import { IdentityMailDelivery } from "@/gen/controlplane/controlplane/v1/identity_pb";

const rpc = vi.hoisted(() => ({
  getIdentityStatus: vi.fn(),
  inviteUser: vi.fn(),
  sendPasswordReset: vi.fn(),
}));
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => rpc }));
vi.mock("@/services/controlPlane/reliantAI", () => ({ getWalletOverview: vi.fn() }));

import { getIdentityStatus, inviteIdentityUser, sendIdentityPasswordReset } from "../api";

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
