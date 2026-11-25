import { create } from "zustand";
import type { DaemonUpgradeRequest, UpdateStatus } from "../types/electron";
import { logger } from "../lib/logger";

/**
 * The desktop app's update state, shared by everything that shows it: the
 * sidebar's update button, Settings → About, and the one update dialog
 * (GlobalUpdateHandler). The Electron main process owns the updater; this
 * store mirrors its `update-status` events, and asks for the current state when
 * it starts, so a window opened or reloaded after a check still knows an
 * update is waiting.
 *
 * Outside Electron there is no updater and every field stays at rest.
 */

/** An update waiting for the user: found, downloading, or ready to install. */
export type PendingUpdate = UpdateStatus & { type: "available" | "download-progress" | "downloaded" };

export function isPendingUpdate(status: UpdateStatus | null | undefined): status is PendingUpdate {
  return (
    !!status &&
    (status.type === "available" || status.type === "download-progress" || status.type === "downloaded")
  );
}

/**
 * The update waiting after `event`, given the one before it. A check in
 * progress or a failed check changes nothing; a failed download falls back to
 * "available", from where it can be retried. Mirrors nextUpdateState in
 * electron/src/update-check.js, which keeps the same state for new windows.
 */
export function nextPendingUpdate(previous: PendingUpdate | null, event: UpdateStatus): PendingUpdate | null {
  switch (event.type) {
    case "checking":
    case "forced":
      return previous;
    case "not-available":
      return null;
    case "error":
      return previous?.type === "download-progress" ? { type: "available", version: previous.version } : previous;
    default:
      return event as PendingUpdate;
  }
}

/**
 * Whether the daemon's upgrade request is overdue at `nowMs`: a security or
 * emergency request past its deadline. The update dialog says so plainly; the
 * user still chooses when to restart (a person's machine is never blocked).
 * Mirrors isUpgradeOverdue in electron/src/update-check.js.
 */
export function isUpgradeOverdue(request: DaemonUpgradeRequest | null | undefined, nowMs: number): boolean {
  if (!request) return false;
  if (request.urgency !== "security" && request.urgency !== "emergency") return false;
  return request.deadlineUnixMs > 0 && nowMs >= request.deadlineUnixMs;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Unknown error";
}

/** The server requires a newer version (forceUpdate). */
export interface ForcedUpdate {
  minVersion: string;
  /** Where to install from when the updater has nothing newer. */
  upgradeUrl?: string;
}

interface AppUpdateState {
  /** The updater's latest status: what the update dialog shows. */
  status: UpdateStatus | null;
  /**
   * Set once the server requires a newer version, for the rest of the
   * session: the dialog stays open and cannot be dismissed.
   */
  forced: ForcedUpdate | null;
  isForcing: boolean;
  /** The update waiting for the user, if any: what the sidebar button shows. */
  pending: PendingUpdate | null;
  dialogOpen: boolean;
  isChecking: boolean;
  isDownloading: boolean;
  isInstalling: boolean;

  /** Applies a status from the main process. */
  receiveStatus: (status: UpdateStatus) => void;
  /** Shows the update dialog, on `status` when given (the menu's reveal). */
  openDialog: (status?: UpdateStatus | null) => void;
  closeDialog: () => void;
  /** A check the user asked for: the dialog opens when it finds an update or fails. */
  checkForUpdates: () => Promise<void>;
  downloadUpdate: () => Promise<void>;
  installUpdate: () => Promise<void>;
  /**
   * The server requires `minVersion` or newer: open the dialog for good and
   * have the main process check, download and install (preload forceUpdate).
   * Called again, it retries.
   */
  forceUpdate: (minVersion: string, upgradeUrl?: string) => Promise<void>;
}

export const useAppUpdateStore = create<AppUpdateState>()((set, get) => ({
  status: null,
  forced: null,
  isForcing: false,
  pending: null,
  dialogOpen: false,
  isChecking: false,
  isDownloading: false,
  isInstalling: false,

  receiveStatus: (event) => {
    const { pending, isDownloading, dialogOpen, isChecking } = get();
    // Another window (or the main process's own state) says the update is
    // forced: this window shows it too, and cannot dismiss it.
    const forced =
      get().forced ??
      (event.type === "forced" || event.forced ? { minVersion: event.minVersion ?? "" } : null);
    if (forced) {
      const shown = event.type === "forced" && isPendingUpdate(get().status) ? get().status! : event;
      set({
        forced,
        status: { ...shown, forced: true, minVersion: forced.minVersion },
        pending: nextPendingUpdate(pending, event),
        isDownloading:
          event.type === "download-progress"
            ? true
            : event.type === "downloaded" || event.type === "error"
              ? false
              : isDownloading,
        dialogOpen: true,
      });
      return;
    }
    set({
      // A background check must not swap an open dialog's content for
      // "checking"; only a check the user started shows that.
      ...(event.type === "checking" && dialogOpen && !isChecking ? {} : { status: event }),
      pending: nextPendingUpdate(pending, event),
      isDownloading:
        event.type === "download-progress"
          ? true
          : event.type === "downloaded" || event.type === "error"
            ? false
            : isDownloading,
      // A download the user started here and then minimized comes back when it
      // is ready to install. So does one the app fetched because the platform
      // asked its daemon to upgrade (daemonUpgrade): downloaded, it waits
      // for the user to restart.
      dialogOpen: dialogOpen || (event.type === "downloaded" && (isDownloading || !!event.daemonUpgrade)),
    });
  },

  openDialog: (status) => {
    if (status) get().receiveStatus(status);
    // Without a status (the sidebar button), show the waiting update: an
    // earlier background check that failed must not hide it behind its error.
    const show = status ?? get().pending;
    if (!show) return;
    set({ status: show, dialogOpen: true });
  },

  closeDialog: () => {
    if (get().forced) return;
    set({ dialogOpen: false });
  },

  checkForUpdates: async () => {
    const api = window.electronAPI;
    if (!api || get().isChecking) return;
    set({ isChecking: true });
    try {
      const result = await api.checkForUpdates();
      if (result?.error) {
        set({ status: { type: "error", error: result.error }, dialogOpen: true });
      } else if (result?.status) {
        get().receiveStatus(result.status);
        if (isPendingUpdate(result.status)) set({ dialogOpen: true });
      }
    } catch (error) {
      logger.error("[appUpdate] update check failed", error);
      set({ status: { type: "error", error: errorText(error) }, dialogOpen: true });
    } finally {
      set({ isChecking: false });
    }
  },

  downloadUpdate: async () => {
    const api = window.electronAPI;
    if (!api) return;
    set({ isDownloading: true });
    // A failure goes through receiveStatus, so a download that had reported
    // progress falls back to a retryable "available" (nextPendingUpdate)
    // instead of leaving the sidebar on a dead "Downloading…".
    try {
      const result = await api.downloadUpdate();
      if (result.error) {
        get().receiveStatus({ type: "error", error: result.error });
      }
    } catch (error) {
      logger.error("[appUpdate] update download failed", error);
      get().receiveStatus({ type: "error", error: errorText(error) });
    }
  },

  forceUpdate: async (minVersion, upgradeUrl) => {
    const api = window.electronAPI;
    if (!api || get().isForcing) return;
    const forced = { minVersion, upgradeUrl: upgradeUrl ?? get().forced?.upgradeUrl };
    const current = get().status;
    set({
      forced,
      isForcing: true,
      dialogOpen: true,
      status: isPendingUpdate(current)
        ? { ...current, forced: true, minVersion }
        : { type: "forced", forced: true, minVersion },
    });
    try {
      if (!api.forceUpdate) {
        // A desktop build without the forced IPC: the ordinary check, with
        // the dialog held open by `forced`.
        set({ isForcing: false });
        await get().checkForUpdates();
        return;
      }
      const result = await api.forceUpdate(minVersion);
      if (result.outcome === "installing") {
        set({ isInstalling: true });
      } else if (result.outcome === "error") {
        set({ status: { type: "error", error: result.error, forced: true, minVersion } });
      } else {
        // The updater has nothing newer (or this build cannot update itself):
        // the dialog says where to install from.
        set({ status: { type: "not-available", forced: true, minVersion } });
      }
    } catch (error) {
      logger.error("[appUpdate] forced update failed", error);
      set({ status: { type: "error", error: errorText(error), forced: true, minVersion } });
    } finally {
      set({ isForcing: false });
    }
  },

  installUpdate: async () => {
    const api = window.electronAPI;
    if (!api) return;
    set({ isInstalling: true });
    try {
      const result = await api.installUpdate();
      // On success the app quits to install, so isInstalling stays set.
      if (result.error) {
        set({ status: { type: "error", error: result.error }, isInstalling: false });
      }
    } catch (error) {
      logger.error("[appUpdate] update install failed", error);
      set({ status: { type: "error", error: errorText(error) }, isInstalling: false });
    }
  },
}));

/**
 * Connects the store to the main process: the status stream, the menu's
 * reveal, and the state as of now. Returns the disconnect. Mounted once, by
 * GlobalUpdateHandler at the app root.
 */
export function connectAppUpdates(): () => void {
  const api = window.electronAPI;
  if (!api) return () => {};
  const store = useAppUpdateStore.getState();

  const offStatus = api.onUpdateStatus((status) => store.receiveStatus(status));
  const offReveal = api.onUpdateReveal?.((status) => store.openDialog(status));

  let disconnected = false;
  api
    .getUpdateStatus?.()
    .then((status) => {
      // Only fills in a state this window has not heard yet; a live event
      // that arrived first is newer.
      if (!disconnected && status && !useAppUpdateStore.getState().status) {
        store.receiveStatus(status);
      }
    })
    .catch((error: unknown) => logger.warn("[appUpdate] could not read the update status", error));

  return () => {
    disconnected = true;
    offStatus();
    offReveal?.();
  };
}
