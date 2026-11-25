/**
 * The cloud onboarding service's current user.
 *
 * Supabase sign-in reads it from controlplane.v1.UserService.GetCurrentUser.
 * forge sign-in (AUTH_PROVIDER "forge") reads the control-plane account the
 * login broker's /auth/session carries, because the control plane's Connect
 * chain does not read the session cookie yet — and its `created_at` is what
 * onboarding's returning-user heal compares daemons against (an imported
 * user keeps their own creation date, not the import's).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const forge = vi.hoisted(() => ({ on: false }));
const fetchIdentity = vi.hoisted(() => vi.fn());
const getCurrentUserRpc = vi.hoisted(() => vi.fn());
const completeOnboardingRpc = vi.hoisted(() => vi.fn());

vi.mock("@/lib/auth/forge-api", () => ({ isForgeAuth: () => forge.on }));
vi.mock("@/lib/auth/native-login", () => ({ fetchIdentity }));
vi.mock("../../client", () => ({
  getControlPlaneClient: () => ({ getCurrentUser: getCurrentUserRpc, completeOnboarding: completeOnboardingRpc }),
}));
vi.mock("@/hooks/settings-queries", () => ({ syncReliantProvider: vi.fn() }));

import { completeOnboarding, getCurrentUser, resetUserCache } from "../cloud";

const CREATED = "2025-03-04T05:06:07Z";

beforeEach(() => {
  resetUserCache();
  fetchIdentity.mockReset();
  getCurrentUserRpc.mockReset().mockResolvedValue({
    user: { id: "u-rpc", email: "rpc@test", name: "Rpc", onboardingCompleted: true, createdAt: { seconds: 1_700_000_000n } },
  });
  completeOnboardingRpc.mockReset().mockResolvedValue({});
});

afterEach(() => {
  forge.on = false;
});

describe("getCurrentUser", () => {
  it("supabase: reads the rpc", async () => {
    expect(await getCurrentUser()).toEqual({
      onboardingCompleted: true,
      id: "u-rpc",
      email: "rpc@test",
      name: "Rpc",
      createdAtMs: 1_700_000_000_000,
    });
    expect(fetchIdentity).not.toHaveBeenCalled();
  });

  describe("forge sign-in", () => {
    beforeEach(() => {
      forge.on = true;
    });

    it("reads the control-plane account from the broker's session, created_at included", async () => {
      fetchIdentity.mockResolvedValue({
        authenticated: true,
        subject: "sub-1",
        email: "idp@test",
        account: { id: "u-cp", email: "a@test", name: "Alice", created_at: CREATED, onboarding_completed: false },
      });
      expect(await getCurrentUser()).toEqual({
        onboardingCompleted: false,
        id: "u-cp",
        email: "a@test",
        name: "Alice",
        createdAtMs: Date.parse(CREATED),
      });
      expect(getCurrentUserRpc).not.toHaveBeenCalled();
    });

    it("leaves createdAtMs unset when the account names no creation", async () => {
      fetchIdentity.mockResolvedValue({ authenticated: true, account: { id: "u-cp", onboarding_completed: true } });
      expect((await getCurrentUser())?.createdAtMs).toBeUndefined();
    });

    it("answers no user when the broker has no session", async () => {
      fetchIdentity.mockResolvedValue({ authenticated: false });
      expect(await getCurrentUser()).toBeNull();
      expect(getCurrentUserRpc).not.toHaveBeenCalled();
    });

    it("falls back to the rpc when the broker names no account", async () => {
      fetchIdentity.mockResolvedValue({ authenticated: true, subject: "sub-1" });
      expect((await getCurrentUser())?.id).toBe("u-rpc");
    });

    it("shares one read while cached, and reads again after resetUserCache and completeOnboarding", async () => {
      fetchIdentity.mockResolvedValue({ authenticated: true, account: { id: "u-cp", onboarding_completed: false } });
      await Promise.all([getCurrentUser(), getCurrentUser()]);
      await getCurrentUser();
      expect(fetchIdentity).toHaveBeenCalledTimes(1);

      resetUserCache();
      await getCurrentUser();
      expect(fetchIdentity).toHaveBeenCalledTimes(2);

      await completeOnboarding({ compute: "cloud" });
      expect(completeOnboardingRpc).toHaveBeenCalledOnce();
      await getCurrentUser();
      expect(fetchIdentity).toHaveBeenCalledTimes(3);
    });
  });
});
