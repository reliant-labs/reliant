import { useEffect, useState } from "react";
import { Download, RefreshCw, CheckCircle2, AlertCircle } from "lucide-react";
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

export function UpdateSection() {
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [isChecking, setIsChecking] = useState(false);
  const [isDownloading, setIsDownloading] = useState(false);
  const [isInstalling, setIsInstalling] = useState(false);
  const [isElectron, setIsElectron] = useState(false);
  const [showUpdateModal, setShowUpdateModal] = useState(false);

  useEffect(() => {
    const checkElectron = async () => {
      try {
        if (window.electronAPI) {
          setIsElectron(true);
          window.electronAPI.onUpdateStatus((status: UpdateStatus) => {
            setUpdateStatus(status);
            
            if (status.type === 'checking') {
              setIsChecking(true);
            } else {
              setIsChecking(false);
            }

            if (status.type === 'available' || status.type === 'download-progress' || status.type === 'downloaded') {
              setShowUpdateModal(true);
            }

            if (status.type === 'download-progress') {
              setIsDownloading(true);
            } else if (status.type === 'downloaded' || status.type === 'error') {
              setIsDownloading(false);
            }
          });
        }
      } catch (error) {
        console.error('Error checking Electron status:', error);
      }
    };

    checkElectron();
  }, []);

  const handleCheckForUpdates = async () => {
    if (!window.electronAPI) return;

    setIsChecking(true);
    try {
      const result = await window.electronAPI.checkForUpdates();
      if (result.error) {
        setUpdateStatus({ type: 'error', error: result.error });
        setShowUpdateModal(true);
      }
    } catch (error) {
      setUpdateStatus({
        type: 'error',
        error: error instanceof Error ? error.message : 'Unknown error'
      });
      setShowUpdateModal(true);
    } finally {
      setIsChecking(false);
    }
  };

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
    } catch (error) {
      console.error('Error installing update:', error);
      setUpdateStatus({
        type: 'error',
        error: error instanceof Error ? error.message : 'Unknown error'
      });
      setIsInstalling(false);
    }
  };

  if (!isElectron) {
    return null;
  }

  const getStatusConfig = () => {
    if (!updateStatus) return null;

    switch (updateStatus.type) {
      case 'checking':
        return {
          icon: RefreshCw,
          message: 'Checking for updates...',
          iconClass: 'animate-spin text-primary',
        };
      case 'available':
        return {
          icon: Download,
          message: `v${updateStatus.version} available`,
          iconClass: 'text-success',
        };
      case 'not-available':
        return {
          icon: CheckCircle2,
          message: 'Up to date',
          iconClass: 'text-success',
        };
      case 'downloaded':
        return {
          icon: CheckCircle2,
          message: `v${updateStatus.version} ready to install`,
          iconClass: 'text-success',
        };
      case 'download-progress':
        return {
          icon: Download,
          message: `Downloading ${Math.round(updateStatus.progress?.percent || 0)}%`,
          iconClass: 'text-primary animate-pulse',
        };
      case 'error':
        return {
          icon: AlertCircle,
          message: 'Update check failed',
          iconClass: 'text-destructive',
        };
      default:
        return null;
    }
  };

  const statusConfig = getStatusConfig();

  return (
    <>
      <div className="bg-card/50 backdrop-blur-sm rounded-xl border border-border/50 p-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <span className="text-sm font-medium text-foreground">Updates</span>
            {statusConfig && (
              <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <statusConfig.icon className={`w-3.5 h-3.5 ${statusConfig.iconClass}`} />
                {statusConfig.message}
              </span>
            )}
          </div>
          
          {updateStatus?.type === 'downloaded' ? (
            <button
              onClick={() => setShowUpdateModal(true)}
              className="text-xs font-medium text-success hover:text-success/80 transition-colors"
            >
              Install
            </button>
          ) : (
            <button
              onClick={handleCheckForUpdates}
              disabled={isChecking || isDownloading}
              className="text-xs font-medium text-primary hover:text-primary/80 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {isChecking ? 'Checking...' : 'Check'}
            </button>
          )}
        </div>

        {/* Progress bar for downloads */}
        {updateStatus?.type === 'download-progress' && updateStatus.progress && (
          <div className="mt-3">
            <div className="h-1 bg-muted rounded-full overflow-hidden">
              <div
                className="h-full bg-primary transition-all duration-300"
                style={{ width: `${updateStatus.progress.percent}%` }}
              />
            </div>
          </div>
        )}
      </div>

      {updateStatus && (
        <UpdateModal
          isOpen={showUpdateModal}
          onClose={() => setShowUpdateModal(false)}
          updateStatus={updateStatus}
          onDownload={handleDownloadUpdate}
          onInstall={handleInstallUpdate}
          isDownloading={isDownloading}
          isInstalling={isInstalling}
        />
      )}
    </>
  );
}
