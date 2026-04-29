import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { Download, CheckCircle2, AlertCircle } from "lucide-react";

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

interface UpdateModalProps {
  isOpen: boolean;
  onClose: () => void;
  updateStatus: UpdateStatus;
  onDownload: () => void;
  onInstall: () => void;
  isDownloading: boolean;
  isInstalling: boolean;
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
}

function formatSpeed(bytesPerSecond: number): string {
  return formatBytes(bytesPerSecond) + '/s';
}

export function UpdateModal({
  isOpen,
  onClose,
  updateStatus,
  onDownload,
  onInstall,
  isDownloading,
  isInstalling,
}: UpdateModalProps) {
  const renderContent = () => {
    switch (updateStatus.type) {
      case 'available':
        return (
          <div className="space-y-6">
            <div className="flex items-start gap-4">
              <div className="p-3 rounded-full bg-primary/10">
                <Download className="w-6 h-6 text-primary" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  Update Available
                </h3>
                <p className="text-sm text-muted-foreground mb-4">
                  Version {updateStatus.version} is ready to download. This update includes new features, improvements, and bug fixes.
                </p>
                {updateStatus.releaseNotes && (
                  <div className="mt-4 p-4 bg-muted/50 rounded-lg border border-border/50">
                    <p className="text-xs font-medium text-foreground mb-2">Release Notes:</p>
                    <div className="text-xs text-muted-foreground whitespace-pre-wrap max-h-40 overflow-y-auto">
                      {updateStatus.releaseNotes}
                    </div>
                  </div>
                )}
              </div>
            </div>

            <div className="flex gap-3 justify-end">
              <Button
                variant="outline"
                onClick={onClose}
                className="min-w-[100px]"
              >
                Later
              </Button>
              <Button
                onClick={onDownload}
                disabled={isDownloading}
                className="min-w-[100px] bg-primary hover:bg-primary/90"
              >
                Download
              </Button>
            </div>
          </div>
        );

      case 'download-progress':
        const progress = updateStatus.progress;
        return (
          <div className="space-y-6">
            <div className="flex items-start gap-4">
              <div className="p-3 rounded-full bg-primary/10 animate-pulse">
                <Download className="w-6 h-6 text-primary" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  Downloading Update
                </h3>
                <p className="text-sm text-muted-foreground mb-4">
                  Downloading version {updateStatus.version}...
                </p>

                <div className="space-y-3">
                  {/* Progress bar */}
                  <div className="relative w-full bg-muted/50 rounded-full h-4 overflow-hidden border border-border/50">
                    <div
                      className="absolute inset-y-0 left-0 bg-gradient-to-r from-primary/80 via-primary to-primary rounded-full transition-all duration-300 ease-out shadow-sm"
                      style={{ width: `${Math.round(progress?.percent || 0)}%` }}
                    >
                      <div className="absolute inset-0 bg-white/20 animate-pulse" />
                    </div>
                    {/* Percentage text on bar */}
                    {progress && progress.percent > 5 && (
                      <div className="absolute inset-0 flex items-center justify-center">
                        <span className="text-xs font-semibold text-white drop-shadow-sm">
                          {Math.round(progress.percent)}%
                        </span>
                      </div>
                    )}
                  </div>

                  {/* Download stats */}
                  <div className="flex justify-between items-center text-xs">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-foreground">
                        {Math.round(progress?.percent || 0)}%
                      </span>
                      <span className="text-muted-foreground">
                        {formatBytes(progress?.transferred || 0)} / {formatBytes(progress?.total || 0)}
                      </span>
                    </div>
                    <div className="flex items-center gap-3">
                      {progress && progress.bytesPerSecond > 0 && progress.percent < 100 && (
                        (() => {
                          const remaining = progress.total - progress.transferred;
                          const secondsRemaining = Math.round(remaining / progress.bytesPerSecond);
                          const minutes = Math.floor(secondsRemaining / 60);
                          const seconds = secondsRemaining % 60;
                          return (
                            <span className="text-muted-foreground">
                              {minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`} remaining
                            </span>
                          );
                        })()
                      )}
                      <span className="text-primary font-medium">
                        {formatSpeed(progress?.bytesPerSecond || 0)}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div className="flex gap-3 justify-end">
              <Button
                variant="outline"
                onClick={onClose}
                className="min-w-[100px]"
              >
                Minimize
              </Button>
            </div>
          </div>
        );

      case 'downloaded':
        return (
          <div className="space-y-6">
            <div className="flex items-start gap-4">
              <div className="p-3 rounded-full bg-success/10">
                <CheckCircle2 className="w-6 h-6 text-success" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  Update Ready
                </h3>
                <p className="text-sm text-muted-foreground mb-4">
                  Version {updateStatus.version} has been downloaded and is ready to install. The app will restart to complete the update.
                </p>
                <div className="p-4 bg-primary/5 rounded-lg border border-primary/20">
                  <p className="text-xs text-muted-foreground">
                    <span className="font-medium text-foreground">Note:</span> Any unsaved work will be preserved. You can install now or continue working and install later.
                  </p>
                </div>
              </div>
            </div>

            <div className="flex gap-3 justify-end">
              <Button
                variant="outline"
                onClick={onClose}
                className="min-w-[100px]"
              >
                Later
              </Button>
              <Button
                onClick={onInstall}
                disabled={isInstalling}
                className="min-w-[140px] bg-success hover:bg-success/90"
              >
                {isInstalling ? (
                  <>
                    Restarting...
                  </>
                ) : (
                  <>
                    Restart & Update
                  </>
                )}
              </Button>
            </div>
          </div>
        );

      case 'error':
        return (
          <div className="space-y-6">
            <div className="flex items-start gap-4">
              <div className="p-3 rounded-full bg-destructive/10">
                <AlertCircle className="w-6 h-6 text-destructive" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  Update Error
                </h3>
                <p className="text-sm text-muted-foreground mb-4">
                  An error occurred while checking for updates.
                </p>
                <div className="p-4 bg-destructive/5 rounded-lg border border-destructive/20">
                  <p className="text-xs font-mono text-destructive">
                    {updateStatus.error}
                  </p>
                </div>
              </div>
            </div>

            <div className="flex gap-3 justify-end">
              <Button
                variant="outline"
                onClick={onClose}
                className="min-w-[100px]"
              >
                Close
              </Button>
            </div>
          </div>
        );

      default:
        return null;
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      size="md"
      hideCloseButton={updateStatus.type === 'download-progress'}
    >
      {renderContent()}
    </Modal>
  );
}