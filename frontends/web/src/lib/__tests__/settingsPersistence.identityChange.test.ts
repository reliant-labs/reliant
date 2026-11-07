/**
 * A settings write queued for user A must never be sent with user B's
 * credential: the token wait before a batch (or a delete) goes out can span
 * an identity change, and the transport attaches whichever credential is
 * current. Each case holds the token wait open, moves the identity, then
 * releases it.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  batchUpsertSettings: vi.fn(),
  deleteSetting: vi.fn(),
  getToken: vi.fn(),
}));

vi.mock("../../hooks/settings-queries", () => ({
  batchUpsertSettings: mocks.batchUpsertSettings,
  deleteSetting: mocks.deleteSetting,
}));

vi.mock("../../api/authProvider", () => ({
  getAuthTokenProvider: () => ({ getToken: mocks.getToken }),
}));

vi.mock("../../services/settingsSync", () => ({
  settingsSync: {
    isInitialized: () => true,
    getSettingFromCache: () => null,
    updateCache: vi.fn(),
    removeFromCache: vi.fn(),
  },
}));

import { deleteSettingIfExists, upsertStringSetting } from "../settingsPersistence";
import { resetSessionOwnerForTests, setSessionOwner } from "../auth/sessionOwner";

/** A session access token naming `sub`, shaped like Supabase's. */
function sessionToken(sub: string): string {
  const encode = (value: object) =>
    btoa(JSON.stringify(value)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${encode({ alg: "HS256", typ: "JWT" })}.${encode({ sub })}.signature`;
}

/** Holds getToken open until release(token). */
function holdToken(): { release: (token: string | null) => void; reached: Promise<void> } {
  let release!: (token: string | null) => void;
  let entered!: () => void;
  const reached = new Promise<void>((resolve) => (entered = resolve));
  mocks.getToken.mockImplementationOnce(
    () =>
      new Promise<string | null>((resolve) => {
        release = resolve;
        entered();
      }),
  );
  return { release: (token) => release(token), reached };
}

describe("settingsPersistence across an identity change", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.batchUpsertSettings.mockResolvedValue([]);
    mocks.deleteSetting.mockResolvedValue({});
    resetSessionOwnerForTests();
    setSessionOwner("user-a");
  });

  it("drops A's batch when B is adopted while the token is pending", async () => {
    const token = holdToken();
    const write = upsertStringSetting("tour.completed", "true");
    await token.reached;

    setSessionOwner(null);
    setSessionOwner("user-b");
    token.release(sessionToken("user-b"));
    await write;

    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled();
  });

  it("drops A's batch when the credential moved to B before B was adopted", async () => {
    // The shared cookie already holds B's session; authStore has not seen it.
    const token = holdToken();
    const write = upsertStringSetting("onboarding.checklist.panel", "open");
    await token.reached;

    token.release(sessionToken("user-b"));
    await write;

    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled();
  });

  it("drops A's delete when the credential moved to B", async () => {
    const token = holdToken();
    const removal = deleteSettingIfExists("tour.contexts");
    await token.reached;

    token.release(sessionToken("user-b"));
    await removal;

    expect(mocks.deleteSetting).not.toHaveBeenCalled();
  });

  it("sends A's batch with A's credential", async () => {
    const token = holdToken();
    const write = upsertStringSetting("tour.completed", "true");
    await token.reached;

    token.release(sessionToken("user-a"));
    await write;

    expect(mocks.batchUpsertSettings).toHaveBeenCalledWith([
      { key: "tour.completed", value: "true", valueType: "string" },
    ]);
  });

  it("never mixes two owners' writes in one batch", async () => {
    mocks.getToken.mockResolvedValue(sessionToken("user-b"));
    const a = upsertStringSetting("tour.completed", "true");
    setSessionOwner("user-b");
    const b = upsertStringSetting("tour.completedSteps", "[]");
    await Promise.all([a, b]);

    expect(mocks.batchUpsertSettings).toHaveBeenCalledTimes(1);
    expect(mocks.batchUpsertSettings).toHaveBeenCalledWith([
      { key: "tour.completedSteps", value: "[]", valueType: "string" },
    ]);
  });
});
