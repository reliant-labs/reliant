import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UpdateStatus } from "../../types/electron";
import { connectAppUpdates, nextPendingUpdate, useAppUpdateStore } from "../appUpdateStore";

type Listener = (status: UpdateStatus) => void;

function fakeElectron(overrides: Record<string, unknown> = {}) {
  const listeners: { status?: Listener; reveal?: Listener } = {};
  const api = {
    onUpdateStatus: vi.fn((cb: Listener) => {
      listeners.status = cb;
      return () => (listeners.status = undefined);
    }),
    onUpdateReveal: vi.fn((cb: Listener) => {
      listeners.reveal = cb;
      return () => (listeners.reveal = undefined);
    }),
    getUpdateStatus: vi.fn(async () => null),
    checkForUpdates: vi.fn(async () => ({})),
    downloadUpdate: vi.fn(async () => ({ success: true })),
    installUpdate: vi.fn(async () => ({ success: true })),
    ...overrides,
  };
  (window as unknown as { electronAPI: unknown }).electronAPI = api;
  return { api, listeners };
}

const initial = useAppUpdateStore.getState();

beforeEach(() => {
  useAppUpdateStore.setState(initial, true);
});

afterEach(() => {
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

describe("nextPendingUpdate", () => {
  const available = { type: "available", version: "1.14.0" } as const;

  it("keeps a found update through a check and a failed check", () => {
    expect(nextPendingUpdate(available, { type: "checking" })).toBe(available);
    expect(nextPendingUpdate(available, { type: "error", error: "offline" })).toBe(available);
  });

  it("clears it when the app is up to date", () => {
    expect(nextPendingUpdate(available, { type: "not-available" })).toBeNull();
  });

  it("falls back to available when a download fails", () => {
    const downloading = { type: "download-progress", version: "1.14.0" } as const;
    expect(nextPendingUpdate(downloading, { type: "error", error: "stalled" })).toEqual(available);
  });
});

describe("appUpdateStore", () => {
  it("does not interrupt when a background check finds an update", () => {
    const { listeners } = fakeElectron();
    const disconnect = connectAppUpdates();
    listeners.status?.({ type: "checking" });
    listeners.status?.({ type: "available", version: "1.14.0" });

    const state = useAppUpdateStore.getState();
    expect(state.pending).toEqual({ type: "available", version: "1.14.0" });
    expect(state.dialogOpen).toBe(false);
    disconnect();
  });

  it("opens the dialog when the app menu reveals an update", () => {
    const { listeners } = fakeElectron();
    const disconnect = connectAppUpdates();
    listeners.reveal?.({ type: "available", version: "1.14.0" });

    const state = useAppUpdateStore.getState();
    expect(state.dialogOpen).toBe(true);
    expect(state.status).toEqual({ type: "available", version: "1.14.0" });
    disconnect();
  });

  it("picks up an update found before the window mounted", async () => {
    const { api } = fakeElectron({
      getUpdateStatus: vi.fn(async () => ({ type: "downloaded", version: "1.14.0" })),
    });
    const disconnect = connectAppUpdates();
    await vi.waitFor(() => expect(useAppUpdateStore.getState().pending?.type).toBe("downloaded"));
    expect(api.getUpdateStatus).toHaveBeenCalledTimes(1);
    expect(useAppUpdateStore.getState().dialogOpen).toBe(false);
    disconnect();
  });

  it("does not reopen a minimized download on every progress tick, only when it is ready", async () => {
    const { listeners } = fakeElectron();
    const disconnect = connectAppUpdates();
    const store = useAppUpdateStore.getState();
    store.openDialog({ type: "available", version: "1.14.0" });
    await store.downloadUpdate();
    listeners.status?.({ type: "download-progress", version: "1.14.0", progress: { percent: 10, transferred: 1, total: 10, bytesPerSecond: 1 } });
    useAppUpdateStore.getState().closeDialog();
    listeners.status?.({ type: "download-progress", version: "1.14.0", progress: { percent: 50, transferred: 5, total: 10, bytesPerSecond: 1 } });
    expect(useAppUpdateStore.getState().dialogOpen).toBe(false);

    listeners.status?.({ type: "downloaded", version: "1.14.0" });
    expect(useAppUpdateStore.getState().dialogOpen).toBe(true);
    expect(useAppUpdateStore.getState().status?.type).toBe("downloaded");
    disconnect();
  });

  it("opens the dialog when a user's check finds an update, and stays quiet when up to date", async () => {
    const checkForUpdates = vi
      .fn()
      .mockResolvedValueOnce({ success: true, status: { type: "not-available", version: "1.13.0" } })
      .mockResolvedValueOnce({ success: true, status: { type: "available", version: "1.14.0" } });
    fakeElectron({ checkForUpdates });

    await useAppUpdateStore.getState().checkForUpdates();
    expect(useAppUpdateStore.getState().dialogOpen).toBe(false);
    expect(useAppUpdateStore.getState().status?.type).toBe("not-available");

    await useAppUpdateStore.getState().checkForUpdates();
    expect(useAppUpdateStore.getState().dialogOpen).toBe(true);
    expect(useAppUpdateStore.getState().pending?.version).toBe("1.14.0");
  });

  it("shows a failed check", async () => {
    fakeElectron({ checkForUpdates: vi.fn(async () => ({ error: "offline" })) });
    await useAppUpdateStore.getState().checkForUpdates();
    const state = useAppUpdateStore.getState();
    expect(state.dialogOpen).toBe(true);
    expect(state.status).toEqual({ type: "error", error: "offline" });
    expect(state.isChecking).toBe(false);
  });

  it("opens on the waiting update, not an earlier check's error", () => {
    fakeElectron();
    const store = useAppUpdateStore.getState();
    store.receiveStatus({ type: "available", version: "1.14.0" });
    store.receiveStatus({ type: "error", error: "offline" });
    useAppUpdateStore.getState().openDialog();

    const state = useAppUpdateStore.getState();
    expect(state.dialogOpen).toBe(true);
    expect(state.status).toEqual({ type: "available", version: "1.14.0" });
  });

  it("opens nothing when no update is waiting", () => {
    fakeElectron();
    useAppUpdateStore.getState().openDialog();
    expect(useAppUpdateStore.getState().dialogOpen).toBe(false);
  });
});
