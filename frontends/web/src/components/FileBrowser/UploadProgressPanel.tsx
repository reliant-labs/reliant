import { AlertCircle, Check, Loader2, RotateCw, X } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { cn } from "../../lib/utils";
import { formatBytes } from "../../lib/formatBytes";
import { useFileUploadStore, type UploadItem, type UploadStatus } from "../../store/fileUploadStore";
import { uploadFolderLabel } from "./UploadConflictDialog";

const STATUS_LABEL: Record<UploadStatus, string> = {
  queued: "Queued",
  uploading: "Uploading",
  done: "Uploaded",
  failed: "Failed",
  cancelled: "Cancelled",
};

function percentOf(item: UploadItem): number {
  if (item.status === "done") return 100;
  if (item.size === 0) return 0;
  return Math.min(100, Math.floor((item.bytesReceived / item.size) * 100));
}

function RowAction({ label, icon, onClick }: { label: string; icon: React.ReactNode; onClick: () => void }) {
  return (
    <Tooltip content={label} side="top" delay={300}>
      <button
        type="button"
        onClick={onClick}
        aria-label={label}
        className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground"
      >
        {icon}
      </button>
    </Tooltip>
  );
}

function UploadRow({ item }: { item: UploadItem }) {
  const cancel = useFileUploadStore((state) => state.cancel);
  const retry = useFileUploadStore((state) => state.retry);
  const dismiss = useFileUploadStore((state) => state.dismiss);
  const active = item.status === "queued" || item.status === "uploading";
  const percent = percentOf(item);

  return (
    <li className="px-3 py-2" data-testid="upload-row" data-status={item.status}>
      <div className="flex items-center gap-2">
        <span className="flex h-4 w-4 shrink-0 items-center justify-center" aria-hidden="true">
          {item.status === "uploading" && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
          {item.status === "done" && <Check className="h-3.5 w-3.5 text-success-ink" />}
          {item.status === "failed" && <AlertCircle className="h-3.5 w-3.5 text-destructive-ink" />}
        </span>
        <span className="min-w-0 flex-1 truncate text-ui text-foreground" title={item.path ?? item.name}>
          {item.name}
        </span>
        <div className="flex shrink-0 items-center">
          {active && (
            <RowAction label={`Cancel upload of ${item.name}`} icon={<X className="h-3.5 w-3.5" />} onClick={() => cancel(item.id)} />
          )}
          {(item.status === "failed" || item.status === "cancelled") && (
            <RowAction label={`Retry ${item.name}`} icon={<RotateCw className="h-3.5 w-3.5" />} onClick={() => retry(item.id)} />
          )}
          {!active && (
            <RowAction label={`Dismiss ${item.name}`} icon={<X className="h-3.5 w-3.5" />} onClick={() => dismiss(item.id)} />
          )}
        </div>
      </div>
      <div className="ml-6 mt-1 flex items-center gap-2 text-meta text-muted-foreground">
        <span className="min-w-0 flex-1 truncate">
          {STATUS_LABEL[item.status]} · to {uploadFolderLabel(item.directory)}
        </span>
        {item.status !== "failed" && item.status !== "cancelled" && (
          <span className="shrink-0 tabular-nums">
            {percent}% · {formatBytes(item.status === "done" ? item.size : item.bytesReceived)} of {formatBytes(item.size)}
          </span>
        )}
      </div>
      {active && (
        <div
          role="progressbar"
          aria-label={`Uploading ${item.name}`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          className="ml-6 mt-1.5 h-1 overflow-hidden rounded-full bg-border"
        >
          <div
            className={cn("h-full rounded-full bg-primary transition-[width] duration-300", item.status === "queued" && "opacity-0")}
            // Runtime geometry: the fill's width is the upload's progress.
            style={{ width: `${percent}%` }}
          />
        </div>
      )}
      {item.status === "failed" && item.error && (
        <p className="ml-6 mt-1 text-meta text-destructive-ink">{item.error}</p>
      )}
    </li>
  );
}

/**
 * The Files tab's upload list: one row per queued, running or finished
 * upload, under the tree. Hidden while there is nothing to show.
 */
export function UploadProgressPanel() {
  const items = useFileUploadStore((state) => state.items);
  const clearFinished = useFileUploadStore((state) => state.clearFinished);
  if (items.length === 0) return null;

  const remaining = items.filter((item) => item.status === "queued" || item.status === "uploading").length;
  const finished = items.some((item) => item.status === "done" || item.status === "cancelled");

  return (
    <section aria-label="Uploads" className="shrink-0 border-t border-border bg-background" data-testid="upload-panel">
      <div className="flex items-center gap-2 px-3 pt-2">
        <h3 className="flex-1 text-ui font-medium text-muted-foreground">
          {remaining > 0 ? `Uploading ${remaining} ${remaining === 1 ? "file" : "files"}` : "Uploads"}
        </h3>
        {finished && (
          <button
            type="button"
            onClick={clearFinished}
            className="rounded-md px-1.5 py-0.5 text-meta text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground"
          >
            Clear finished
          </button>
        )}
      </div>
      <ul className="max-h-48 overflow-y-auto">
        {items.map((item) => (
          <UploadRow key={item.id} item={item} />
        ))}
      </ul>
    </section>
  );
}
