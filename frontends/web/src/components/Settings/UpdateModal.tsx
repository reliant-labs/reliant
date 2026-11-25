import Modal from "../forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { Download, CheckCircle2, AlertCircle, RefreshCw } from "lucide-react";
import type { DaemonUpgradeRequest, UpdateStatus } from "../../types/electron";
import { isUpgradeOverdue, type ForcedUpdate } from "../../store/appUpdateStore";
import { openExternalLink } from "../../lib/open-link";

interface UpdateModalProps {
  isOpen: boolean;
  onClose: () => void;
  updateStatus: UpdateStatus;
  onDownload: () => void;
  onInstall: () => void;
  isDownloading: boolean;
  isInstalling: boolean;
  /**
   * The server requires a newer version: no Later, Minimize or Close, and
   * neither Escape nor the backdrop closes the dialog.
   */
  forced?: ForcedUpdate | null;
  /** Runs the forced update again, after it failed or found nothing. */
  onRetryForced?: () => void;
}

/** Where to install from when the server names no page of its own. */
const DEFAULT_DOWNLOAD_URL = "https://reliantlabs.io";

const noDismiss = () => {};

/** The daemon's upgrade request when it is overdue now, else null. */
function overdueUpgrade(request: DaemonUpgradeRequest | undefined): DaemonUpgradeRequest | null {
  return request && isUpgradeOverdue(request, Date.now()) ? request : null;
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
  forced = null,
  onRetryForced,
}: UpdateModalProps) {
  const isForced = !!forced;
  // The platform asked the daemon to upgrade, for a security or emergency
  // fix, and the deadline has passed: say so plainly. The user still chooses
  // when to restart; a person's machine is never blocked.
  const overdue = isForced ? null : overdueUpgrade(updateStatus.daemonUpgrade);

  // A forced update's only way out is the update itself.
  const dismissButton = (label: string) =>
    isForced ? null : (
      <Button variant="outline" onClick={onClose} className="min-w-[100px]">
        {label}
      </Button>
    );

  const retryButton = onRetryForced ? (
    <Button onClick={onRetryForced} className="min-w-[100px]">
      Try again
    </Button>
  ) : null;

  const renderContent = () => {
    switch (updateStatus.type) {
      case 'available':
        return (
          <div className="space-y-6">
            <div className="flex items-start gap-4">
              <div className="p-3 rounded-full bg-primary/10">
                <Download className="w-6 h-6 text-primary-ink" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  Update Available
                </h3>
                <p className="text-sm text-muted-foreground mb-4">
                  Version {updateStatus.version} is ready to download. This update includes new features, improvements, and bug fixes.
                </p>
                {updateStatus.releaseNotes && (
                  <div className="mt-4 p-4 rounded-lg border border-border/60 bg-background">
                    <p className="text-xs font-medium text-foreground mb-2">Release Notes:</p>
                    <div className="text-xs text-muted-foreground whitespace-pre-wrap max-h-40 overflow-y-auto">
                      {updateStatus.releaseNotes}
                    </div>
                  </div>
                )}
              </div>
            </div>

            <div className="flex gap-3 justify-end">
              {dismissButton("Later")}
              <Button
                onClick={onDownload}
                disabled={isDownloading}
                className="min-w-[100px]"
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
                <Download className="w-6 h-6 text-primary-ink" />
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
                      <span className="text-primary-ink font-medium">
                        {formatSpeed(progress?.bytesPerSecond || 0)}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            {!isForced && (
              <div className="flex gap-3 justify-end">{dismissButton("Minimize")}</div>
            )}
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
              {dismissButton("Later")}
              <Button
                onClick={onInstall}
                disabled={isInstalling}
                className="min-w-[140px]"
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
                <AlertCircle className="w-6 h-6 text-destructive-ink" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  Update Error
                </h3>
                <p className="text-sm text-muted-foreground mb-4">
                  Reliant couldn't finish updating. Try again in a moment.
                </p>
                <div className="p-4 bg-destructive/5 rounded-lg border border-destructive/20">
                  <p className="text-xs font-mono text-destructive-ink">
                    {updateStatus.error}
                  </p>
                </div>
              </div>
            </div>

            <div className="flex gap-3 justify-end">
              {dismissButton("Close")}
              {isForced && retryButton}
            </div>
          </div>
        );

      case 'forced':
      case 'checking':
        return (
          <div className="flex items-start gap-4">
            <div className="p-3 rounded-full bg-primary/10">
              <RefreshCw className="w-6 h-6 text-primary-ink animate-spin" />
            </div>
            <div className="flex-1">
              <h3 className="text-lg font-semibold text-foreground mb-2">
                Checking for Updates
              </h3>
              <p className="text-sm text-muted-foreground">
                Looking for a newer version of Reliant...
              </p>
            </div>
          </div>
        );

      case 'not-available':
        if (isForced) {
          return (
            <div className="space-y-6">
              <div className="flex items-start gap-4">
                <div className="p-3 rounded-full bg-primary/10">
                  <Download className="w-6 h-6 text-primary-ink" />
                </div>
                <div className="flex-1">
                  <h3 className="text-lg font-semibold text-foreground mb-2">
                    Install the latest version
                  </h3>
                  <p className="text-sm text-muted-foreground">
                    The built-in updater found no newer version. Download the latest version of
                    Reliant and install it to keep working.
                  </p>
                </div>
              </div>

              <div className="flex gap-3 justify-end">
                {onRetryForced && (
                  <Button variant="outline" onClick={onRetryForced} className="min-w-[100px]">
                    Try again
                  </Button>
                )}
                <Button
                  onClick={() => void openExternalLink(forced?.upgradeUrl || DEFAULT_DOWNLOAD_URL)}
                  className="min-w-[100px]"
                >
                  Download
                </Button>
              </div>
            </div>
          );
        }
        return (
          <div className="space-y-6">
            <div className="flex items-start gap-4">
              <div className="p-3 rounded-full bg-success/10">
                <CheckCircle2 className="w-6 h-6 text-success" />
              </div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-foreground mb-2">
                  You're Up to Date
                </h3>
                <p className="text-sm text-muted-foreground">
                  {updateStatus.version
                    ? `Reliant ${updateStatus.version} is the latest version.`
                    : 'You have the latest version of Reliant.'}
                </p>
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
      open={isOpen}
      onClose={isForced ? noDismiss : onClose}
      closeOnOverlay={!isForced}
      size="md"
      hideCloseButton={isForced || updateStatus.type === 'download-progress'}
    >
      {isForced && (
        <p className="mb-4 text-sm text-foreground">
          This version of Reliant is no longer supported. Reliant is upgrading to the latest
          version{forced?.minVersion ? ` (${forced.minVersion} or newer)` : ""} and will restart
          when it is ready.
        </p>
      )}
      {overdue && (
        <div
          role="alert"
          className="mb-4 p-4 rounded-lg border border-destructive/20 bg-destructive/5 text-sm text-foreground"
        >
          {overdue.urgency === "emergency"
            ? "This update fixes an urgent problem and is overdue."
            : "This update fixes a security issue and is overdue."}{" "}
          Restart Reliant to install it as soon as you can. Your chats pick up where they left off.
          {overdue.reason && <span className="block mt-1 text-xs text-muted-foreground">{overdue.reason}</span>}
        </div>
      )}
      {renderContent()}
    </Modal>
  );
}