import { useEffect, useState } from "react";
import { UpdateModal } from "./UpdateModal";

interface UpdateStatus {
  type: 'checking' | 'available' | 'not-available' | 'downloaded' | 'download-progress' | 'error';
  version?: string;
  progress?: {
    percent: number;
    transferred: number;
    total: number;
    bytesPerSecond: number;
  };
  error?: string;
  releaseNotes?: string;
}

/**
 * Global component that listens for update events and shows the update modal
 * This ensures the modal appears even when the user isn't on the Settings page
 */
export function GlobalUpdateHandler() {
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [showUpdateModal, setShowUpdateModal] = useState(false);
  const [isDownloading, setIsDownloading] = useState(false);
  const [isInstalling, setIsInstalling] = useState(false);

  useEffect(() => {
    // Only setup in Electron
    if (!window.electronAPI) {
      return;
    }

    // Listen for update status events from main process
    const cleanup = window.electronAPI.onUpdateStatus((status: UpdateStatus) => {
      setUpdateStatus(status);

      // Automatically show modal when update is available, downloading, or downloaded
      if (status.type === 'available' || status.type === 'download-progress' || status.type === 'downloaded') {
        setShowUpdateModal(true);
      }

      // Track download state
      if (status.type === 'download-progress') {
        setIsDownloading(true);
      } else if (status.type === 'downloaded' || status.type === 'error') {
        setIsDownloading(false);
      }
    });

    return cleanup;
  }, []);

  const handleDownloadUpdate = async () => {
    if (!window.electronAPI) return;

    setIsDownloading(true);
    try {
      const result = await window.electronAPI.downloadUpdate();
      if (result.error) {
        setUpdateStatus({ type: 'error', error: result.error });
        setIsDownloading(false);
      }
    } catch (error) {
      console.error('Error downloading update:', error);
      setUpdateStatus({
        type: 'error',
        error: error instanceof Error ? error.message : 'Unknown error'
      });
      setIsDownloading(false);
    }
  };

  const handleInstallUpdate = async () => {
    if (!window.electronAPI) return;

    setIsInstalling(true);
    try {
      const result = await window.electronAPI.installUpdate();
      if (result.error) {
        setUpdateStatus({ type: 'error', error: result.error });
        setIsInstalling(false);
      }
      // If successful, the app will restart so we don't need to reset isInstalling
    } catch (error) {
      console.error('Error installing update:', error);
      setUpdateStatus({
        type: 'error',
        error: error instanceof Error ? error.message : 'Unknown error'
      });
      setIsInstalling(false);
    }
  };

  // Don't render anything if no update status or not in Electron
  if (!window.electronAPI || !updateStatus) {
    return null;
  }

  return (
    <UpdateModal
      isOpen={showUpdateModal}
      onClose={() => setShowUpdateModal(false)}
      updateStatus={updateStatus}
      onDownload={handleDownloadUpdate}
      onInstall={handleInstallUpdate}
      isDownloading={isDownloading}
      isInstalling={isInstalling}
    />
  );
}
