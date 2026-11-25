/**
 * The update dialog when the server requires a newer version: it opens by
 * itself, follows the forced update the main process runs, and cannot be
 * dismissed. The ordinary dialog keeps its Later.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { UpdateStatus } from "../../../types/electron";
import { useAppUpdateStore } from "../../../store/appUpdateStore";
import { GlobalUpdateHandler } from "../GlobalUpdateHandler";

const openExternalLink = vi.hoisted(() => vi.fn(async () => {}));
vi.mock("../../../lib/open-link", () => ({ openExternalLink }));

type Listener = (status: UpdateStatus) => void;
type ForceResult = { outcome: "installing" | "up-to-date" | "inactive" | "error"; error?: string };

function fakeElectron(forceUpdate: (min: string) => Promise<ForceResult>) {
  const listeners: { status?: Listener } = {};
  const api = {
    onUpdateStatus: vi.fn((cb: Listener) => {
      listeners.status = cb;
      return () => (listeners.status = undefined);
    }),
    onUpdateReveal: vi.fn(() => () => {}),
    getUpdateStatus: vi.fn(async () => null),
    checkForUpdates: vi.fn(async () => ({})),
    downloadUpdate: vi.fn(async () => ({ success: true })),
    installUpdate: vi.fn(async () => ({ success: true })),
    forceUpdate: vi.fn(forceUpdate),
  };
  (window as unknown as { electronAPI: unknown }).electronAPI = api;
  return { api, listeners };
}

const initial = useAppUpdateStore.getState();

beforeEach(() => {
  useAppUpdateStore.setState(initial, true);
});

afterEach(() => {
  cleanup();
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

describe("GlobalUpdateHandler, forced", () => {
  it("follows the forced update without a way to dismiss it", async () => {
    let finish!: (r: ForceResult) => void;
    const { api, listeners } = fakeElectron(() => new Promise((resolve) => (finish = resolve)));
    render(<GlobalUpdateHandler />);

    let pending!: Promise<void>;
    act(() => {
      pending = useAppUpdateStore.getState().forceUpdate("1.9.0");
    });
    expect(api.forceUpdate).toHaveBeenCalledWith("1.9.0");
    expect(await screen.findByText(/no longer supported/)).toBeInTheDocument();
    expect(screen.getByText(/1\.9\.0 or newer/)).toBeInTheDocument();

    // The main process reports the update it found; no Later beside it.
    act(() => listeners.status?.({ type: "available", version: "1.14.0" }));
    expect(screen.getByText("Update Available")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Later" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Close dialog" })).toBeNull();

    act(() => listeners.status?.({ type: "download-progress", version: "1.14.0", progress: { percent: 50, transferred: 5, total: 10, bytesPerSecond: 1 } }));
    expect(screen.queryByRole("button", { name: "Minimize" })).toBeNull();

    // Escape and the store's own close do nothing.
    fireEvent.keyDown(document, { key: "Escape" });
    act(() => useAppUpdateStore.getState().closeDialog());
    expect(screen.getByText("Downloading Update")).toBeInTheDocument();

    await act(async () => {
      finish({ outcome: "installing" });
      await pending;
    });
    expect(useAppUpdateStore.getState().isInstalling).toBe(true);
  });

  it("offers to try again after a failure", async () => {
    const { api } = fakeElectron(async () => ({ outcome: "error", error: "stalled" }));
    render(<GlobalUpdateHandler />);

    await act(() => useAppUpdateStore.getState().forceUpdate("1.9.0"));
    expect(screen.getByText("Update Error")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Try again" })));
    expect(api.forceUpdate).toHaveBeenCalledTimes(2);
  });

  it("sends the user to the download page when the updater has nothing newer", async () => {
    fakeElectron(async () => ({ outcome: "up-to-date" }));
    render(<GlobalUpdateHandler />);

    await act(() => useAppUpdateStore.getState().forceUpdate("1.9.0", "https://reliantlabs.io/download"));
    expect(screen.getByText("Install the latest version")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(openExternalLink).toHaveBeenCalledWith("https://reliantlabs.io/download");
  });

  it("opens in every window when another window started it", async () => {
    const { listeners } = fakeElectron(async () => ({ outcome: "installing" }));
    render(<GlobalUpdateHandler />);

    act(() => listeners.status?.({ type: "forced", minVersion: "1.9.0" }));
    expect(await screen.findByText(/no longer supported/)).toBeInTheDocument();
    expect(useAppUpdateStore.getState().forced).toEqual({ minVersion: "1.9.0" });
  });
});

describe("GlobalUpdateHandler, not forced", () => {
  it("keeps Later on an ordinary update", async () => {
    fakeElectron(async () => ({ outcome: "installing" }));
    render(<GlobalUpdateHandler />);

    act(() => useAppUpdateStore.getState().openDialog({ type: "available", version: "1.14.0" }));
    expect(screen.getByRole("button", { name: "Later" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(useAppUpdateStore.getState().dialogOpen).toBe(false);
  });
});

// The platform asked this app's daemon to upgrade, and the app downloaded the
// update by itself (electron/src/update-check.js createDaemonUpgradeResponder).
// The ready update opens the dialog, and the restart is still the user's:
// Later stays, nothing installs until they click, and an overdue security
// request only strengthens the wording.
describe("GlobalUpdateHandler, the daemon asked for an upgrade", () => {
  const request = {
    targetVersion: "1.14.0",
    urgency: "security",
    // Long past, whatever the clock says: the test does not read it.
    deadlineUnixMs: 1,
    reason: "a fix for a credential leak",
    rolloutId: "roll-7",
  };

  it("opens the restart prompt when the update is downloaded, and waits for the user", async () => {
    const { api, listeners } = fakeElectron(async () => ({ outcome: "installing" }));
    render(<GlobalUpdateHandler />);

    act(() => listeners.status?.({ type: "downloaded", version: "1.14.0", daemonUpgrade: { ...request, urgency: "normal" } }));
    expect(await screen.findByText("Update Ready")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(api.installUpdate).not.toHaveBeenCalled();
    expect(api.forceUpdate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(useAppUpdateStore.getState().dialogOpen).toBe(false);
    expect(api.installUpdate).not.toHaveBeenCalled();
  });

  it("says plainly that an overdue security update is overdue, and still lets the user wait", async () => {
    const { api, listeners } = fakeElectron(async () => ({ outcome: "installing" }));
    render(<GlobalUpdateHandler />);

    act(() => listeners.status?.({ type: "downloaded", version: "1.14.0", daemonUpgrade: request }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/security issue and is overdue/);
    expect(alert).toHaveTextContent(request.reason);
    expect(screen.getByRole("button", { name: "Later" })).toBeInTheDocument();
    expect(api.installUpdate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /Restart & Update/ }));
    expect(api.installUpdate).toHaveBeenCalledTimes(1);
  });
});
