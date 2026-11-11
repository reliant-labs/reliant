/**
 * A settings write queued for user A is checked against A's credential before
 * it is sent, but the transport's auth stage resolves the credential a second
 * time when the request leaves. These cases run the real settings client
 * through the app's real interceptor chain (buildInterceptors) to an
 * in-memory SettingsService: the preliminary check sees A, the transport's
 * own token wait is held open while the credential moves to B, and the
 * server must never receive the write with B's credential.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRouterTransport } from "@connectrpc/connect";

const mocks = vi.hoisted(() => ({
  getToken: vi.fn(),
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

vi.mock("../../api/authProvider", () => ({
  getAuthTokenProvider: () => ({
    getToken: mocks.getToken,
    hasSession: async () => true,
    refresh: async () => null,
  }),
}));

vi.mock("../logger", () => ({ logger: mocks.logger }));

vi.mock("../../services/settingsSync", () => ({
  settingsSync: {
    isInitialized: () => true,
    getSettingFromCache: () => null,
    updateCache: vi.fn(),
    removeFromCache: vi.fn(),
  },
}));

import { SettingsService } from "../../gen/reliant/v1/settings_pb";
import { buildInterceptors } from "../../api/transport";
import { setTransport } from "../connect";
import { deleteSettingIfExists, upsertStringSetting } from "../settingsPersistence";
import { resetSessionOwnerForTests, setSessionOwner } from "../auth/sessionOwner";

/** A session access token naming `sub`, shaped like Supabase's. */
function sessionToken(sub: string): string {
  const encode = (value: object) =>
    btoa(JSON.stringify(value)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${encode({ alg: "HS256", typ: "JWT" })}.${encode({ sub })}.signature`;
}

/** Holds the next getToken open until release(token). */
function holdToken(): { release: (token: string) => void; reached: Promise<void> } {
  let release!: (token: string) => void;
  let entered!: () => void;
  const reached = new Promise<void>((resolve) => (entered = resolve));
  mocks.getToken.mockImplementationOnce(
    () =>
      new Promise<string>((resolve) => {
        release = resolve;
        entered();
      }),
  );
  return { release: (token) => release(token), reached };
}

/** What the server received: the method and the Authorization header. */
let received: Array<{ method: string; authorization: string | null }> = [];
let restoreTransport: () => void = () => {};

describe("settings writes through the real transport across an identity change", () => {
  beforeEach(() => {
    mocks.getToken.mockReset();
    received = [];
    resetSessionOwnerForTests();
    setSessionOwner("user-a");
    restoreTransport = setTransport(
      createRouterTransport(
        (router) => {
          router.service(SettingsService, {
            batchUpsertSettings: (_req, ctx) => {
              received.push({
                method: "BatchUpsertSettings",
                authorization: ctx.requestHeader.get("Authorization"),
              });
              return { settings: [] };
            },
            deleteSetting: (_req, ctx) => {
              received.push({
                method: "DeleteSetting",
                authorization: ctx.requestHeader.get("Authorization"),
              });
              return {};
            },
          });
        },
        { transport: { interceptors: buildInterceptors({ withAuth: true }) } },
      ),
    );
  });

  afterEach(() => {
    restoreTransport();
  });

  it("never sends A's batch with B's credential", async () => {
    // The preliminary check (settingsPersistence.mayWriteFor) sees A.
    mocks.getToken.mockResolvedValueOnce(sessionToken("user-a"));
    // The transport's own token wait is held, and ends holding B's session.
    const outbound = holdToken();
    const write = upsertStringSetting("tour.completed", "true");
    await outbound.reached;

    outbound.release(sessionToken("user-b"));
    await write;

    expect(received).toEqual([]);
  });

  it("never sends A's delete with B's credential", async () => {
    mocks.getToken.mockResolvedValueOnce(sessionToken("user-a"));
    const outbound = holdToken();
    const removal = deleteSettingIfExists("tour.contexts");
    await outbound.reached;

    outbound.release(sessionToken("user-b"));
    await removal;

    expect(received).toEqual([]);
  });

  it("never sends A's batch after B is adopted during the transport's token wait", async () => {
    mocks.getToken.mockResolvedValueOnce(sessionToken("user-a"));
    const outbound = holdToken();
    const write = upsertStringSetting("tour.completed", "true");
    await outbound.reached;

    setSessionOwner(null);
    setSessionOwner("user-b");
    outbound.release(sessionToken("user-b"));
    await write;

    expect(received).toEqual([]);
  });

  it("sends A's batch and delete with A's credential", async () => {
    mocks.getToken.mockResolvedValue(sessionToken("user-a"));

    await upsertStringSetting("tour.completed", "true");
    await deleteSettingIfExists("tour.contexts");

    expect(received).toEqual([
      { method: "BatchUpsertSettings", authorization: `Bearer ${sessionToken("user-a")}` },
      { method: "DeleteSetting", authorization: `Bearer ${sessionToken("user-a")}` },
    ]);
  });
});
