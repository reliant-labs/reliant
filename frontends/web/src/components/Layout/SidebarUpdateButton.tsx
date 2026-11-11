import { CircleArrowDown, Download, RefreshCw } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { cn } from "../../lib/utils";
import { useAppUpdateStore, type PendingUpdate } from "../../store/appUpdateStore";

export function updateButtonLabel(pending: PendingUpdate): string {
  const version = pending.version ? ` ${pending.version}` : "";
  switch (pending.type) {
    case "downloaded":
      return `Restart to update to Reliant${version}`;
    case "download-progress": {
      const percent = Math.round(pending.progress?.percent ?? 0);
      return `Downloading Reliant${version}… ${percent}%`;
    }
    default:
      return `Update available: Reliant${version}`;
  }
}

/**
 * The sidebar footer's update button, beside Settings. It appears only while
 * the desktop app has an update waiting (found, downloading, or ready to
 * install) and opens the update dialog, so an update is one click away
 * without a dialog interrupting work the moment it is found.
 */
export function SidebarUpdateButton() {
  const pending = useAppUpdateStore((s) => s.pending);
  const openDialog = useAppUpdateStore((s) => s.openDialog);

  if (!pending) return null;

  const label = updateButtonLabel(pending);
  const Icon =
    pending.type === "downloaded" ? RefreshCw : pending.type === "download-progress" ? Download : CircleArrowDown;

  return (
    <Tooltip content={label} side="top" delay={300}>
      <button
        type="button"
        onClick={() => openDialog()}
        aria-label={label}
        data-testid="sidebar-update-button"
        data-update-state={pending.type}
        className={cn(
          "flex h-8 w-8 shrink-0 items-center justify-center rounded-md transition-colors",
          "text-primary-ink hover:bg-foreground/5",
          "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        )}
      >
        <Icon className={cn("h-4 w-4", pending.type === "download-progress" && "animate-pulse")} />
      </button>
    </Tooltip>
  );
}
