import { Download, RefreshCw, CheckCircle2, AlertCircle } from "lucide-react";
import type { UpdateStatus } from "../../types/electron";
import { useAppUpdateStore, type PendingUpdate } from "../../store/appUpdateStore";

function statusConfig(status: UpdateStatus | null) {
  if (!status) return null;

  switch (status.type) {
    case 'checking':
      return {
        icon: RefreshCw,
        message: 'Checking for updates...',
        iconClass: 'animate-spin text-primary-ink',
      };
    case 'available':
      return {
        icon: Download,
        message: `v${status.version} available`,
        iconClass: 'text-primary-ink',
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
        message: `v${status.version} ready to install`,
        iconClass: 'text-success',
      };
    case 'download-progress':
      return {
        icon: Download,
        message: `Downloading ${Math.round(status.progress?.percent || 0)}%`,
        iconClass: 'text-primary-ink animate-pulse',
      };
    case 'error':
      return {
        icon: AlertCircle,
        message: 'Update failed',
        iconClass: 'text-destructive-ink',
      };
    default:
      return null;
  }
}

function pendingActionLabel(pending: PendingUpdate): string {
  switch (pending.type) {
    case 'downloaded':
      return 'Install';
    case 'download-progress':
      return 'View';
    default:
      return 'Update';
  }
}

/**
 * Settings → About's update row. The state and the dialog are the app-wide
 * ones (appUpdateStore, GlobalUpdateHandler), so this row, the sidebar button
 * and the app menu always agree.
 */
export function UpdateSection() {
  const status = useAppUpdateStore((s) => s.status);
  const pending = useAppUpdateStore((s) => s.pending);
  const isChecking = useAppUpdateStore((s) => s.isChecking);
  const isDownloading = useAppUpdateStore((s) => s.isDownloading);
  const checkForUpdates = useAppUpdateStore((s) => s.checkForUpdates);
  const openDialog = useAppUpdateStore((s) => s.openDialog);

  if (!window.electronAPI) {
    return null;
  }

  // A failed or in-progress check says so; otherwise the waiting update does.
  const shown = isChecking ? { type: 'checking' as const } : (status?.type === 'error' ? status : pending ?? status);
  const config = statusConfig(shown);
  const progress = pending?.type === 'download-progress' ? pending.progress : undefined;

  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span className="text-sm font-medium text-foreground">Updates</span>
          {config && (
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <config.icon className={`w-3.5 h-3.5 ${config.iconClass}`} />
              {config.message}
            </span>
          )}
        </div>

        {pending ? (
          <button
            type="button"
            onClick={() => openDialog()}
            className="text-xs font-medium text-primary-ink hover:text-primary-ink/80 transition-colors"
          >
            {pendingActionLabel(pending)}
          </button>
        ) : (
          <button
            type="button"
            onClick={() => void checkForUpdates()}
            disabled={isChecking || isDownloading}
            className="text-xs font-medium text-primary-ink hover:text-primary-ink/80 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {isChecking ? 'Checking...' : 'Check'}
          </button>
        )}
      </div>

      {/* Progress bar for downloads */}
      {progress && (
        <div className="mt-3">
          <div className="h-1 bg-foreground/10 rounded-full overflow-hidden">
            <div
              className="h-full bg-primary transition-all duration-300"
              style={{ width: `${progress.percent}%` }}
            />
          </div>
        </div>
      )}
    </div>
  );
}
