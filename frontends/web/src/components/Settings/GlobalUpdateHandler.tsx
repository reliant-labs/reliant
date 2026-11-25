import { useEffect } from "react";
import { UpdateModal } from "./UpdateModal";
import { connectAppUpdates, useAppUpdateStore } from "../../store/appUpdateStore";

/**
 * Connects the desktop updater to the app and renders the one update dialog.
 * Mounted at the app root so the dialog opens on every route. What opens it:
 * the sidebar's update button, Settings → About, the app menu's "Check for
 * Updates…", a download the user started finishing, and an update the app
 * downloaded because the platform asked its daemon to upgrade (the status's
 * `daemonUpgrade`; it waits for the user to restart, overdue or not). A
 * background check that finds an update only lights up the sidebar button;
 * it does not interrupt.
 *
 * The exception is a forced update (the server requires a newer version,
 * AppOutOfDateModal → appUpdateStore.forceUpdate): the dialog opens by
 * itself, follows the check, download and restart the main process runs, and
 * cannot be dismissed. A failure offers to try again.
 */
export function GlobalUpdateHandler() {
  const status = useAppUpdateStore((s) => s.status);
  const dialogOpen = useAppUpdateStore((s) => s.dialogOpen);
  const isDownloading = useAppUpdateStore((s) => s.isDownloading);
  const isInstalling = useAppUpdateStore((s) => s.isInstalling);
  const closeDialog = useAppUpdateStore((s) => s.closeDialog);
  const downloadUpdate = useAppUpdateStore((s) => s.downloadUpdate);
  const installUpdate = useAppUpdateStore((s) => s.installUpdate);
  const forced = useAppUpdateStore((s) => s.forced);
  const isForcing = useAppUpdateStore((s) => s.isForcing);
  const forceUpdate = useAppUpdateStore((s) => s.forceUpdate);

  useEffect(() => connectAppUpdates(), []);

  if (!window.electronAPI || !status) {
    return null;
  }

  return (
    <UpdateModal
      isOpen={dialogOpen}
      onClose={closeDialog}
      updateStatus={status}
      onDownload={() => void downloadUpdate()}
      onInstall={() => void installUpdate()}
      isDownloading={isDownloading}
      isInstalling={isInstalling}
      forced={forced}
      onRetryForced={forced && !isForcing ? () => void forceUpdate(forced.minVersion) : undefined}
    />
  );
}
