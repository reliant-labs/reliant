import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const env = vi.hoisted(() => ({ electron: false }));
vi.mock("@/lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/constants")>()),
  isElectron: () => env.electron,
}));

import type { AuthIdentity } from "@/lib/auth/identity";
import { changesStore, setChangesStoreForTests, startChanges, userChangeContext } from "@/lib/changesStore";

const snapshot = { schema: "forge.changes/v1", revision: 9, changes: {} };

beforeEach(() => {
  env.electron = false;
  setChangesStoreForTests(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
  setChangesStoreForTests(null);
});

describe("startChanges — the page's one snapshot load", () => {
  it("fetches /_forge/changes.json once and keeps the store", async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify(snapshot), { status: 200 }));
    vi.stubGlobal("fetch", fetch);

    expect(changesStore()).toBeNull();
    const [first, second] = await Promise.all([startChanges(), startChanges()]);

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith("/_forge/changes.json", expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(first).toBe(second);
    expect(changesStore()?.revision).toBe(9);
  });

  it("settles on an empty store when there is no snapshot (a dev server, a self-hosted build)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 404 })));

    await startChanges();

    expect(changesStore()?.revision).toBe(0);
    expect(changesStore()?.evaluate("pending-provider-copy", { targetingKey: "u_1" }).reason).toBe("unknown");
  });

  it("never fetches in the desktop app, which has no snapshot origin", async () => {
    env.electron = true;
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);

    expect(await startChanges()).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
    expect(changesStore()).toBeNull();
  });
});

describe("userChangeContext — unit `user`", () => {
  const person: AuthIdentity = {
    id: "u_1",
    email: "u@example.com",
    name: null,
    avatarUrl: null,
    isAnonymous: false,
    emailVerified: true,
    createdAtMs: null,
    isApiKey: false,
    provider: "supabase",
  };

  it("is the signed-in person's id", () => {
    expect(userChangeContext(person)).toEqual({ targetingKey: "u_1" });
  });

  it("is no unit for no one, a guest or an API-key session", () => {
    expect(userChangeContext(null)).toEqual({});
    expect(userChangeContext({ ...person, isAnonymous: true })).toEqual({});
    expect(userChangeContext({ ...person, isApiKey: true })).toEqual({});
  });
});
