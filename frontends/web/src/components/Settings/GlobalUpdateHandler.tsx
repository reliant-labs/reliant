import { useEffect } from "react";
import { UpdateModal } from "./UpdateModal";
import { connectAppUpdates, useAppUpdateStore } from "../../store/appUpdateStore";

/**
 * Connects the desktop updater to the app and renders the one update dialog.
 * Mounted at the app root so the dialog opens on every route. What opens it:
 * the sidebar's update button, Settings → About, the app menu's "Check for
 * Updates…", and a download the user started finishing. A background check
 * that finds an update only lights up the sidebar button; it does not
 * interrupt.
 */
export function GlobalUpdateHandler() {
  const status = useAppUpdateStore((s) => s.status);
  const dialogOpen = useAppUpdateStore((s) => s.dialogOpen);
  const isDownloading = useAppUpdateStore((s) => s.isDownloading);
  const isInstalling = useAppUpdateStore((s) => s.isInstalling);
  const closeDialog = useAppUpdateStore((s) => s.closeDialog);
  const downloadUpdate = useAppUpdateStore((s) => s.downloadUpdate);
  const installUpdate = useAppUpdateStore((s) => s.installUpdate);

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
    />
  );
}
