/**
 * ApprovalActions - Renders approve/deny buttons for workflow approvals.
 *
 * Props only: the caller decides what approving means. The chat's batch bar
 * (PermissionsPanel) approves every pending approval of the open chat; the
 * Inbox approves one approval of any chat, so it passes its own labels and no
 * shortcut.
 */

import { Tooltip } from "../ui/Tooltip";
import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";

interface ApprovalActionsProps {
  onApprove: () => void;
  onDeny: () => void;
  /** "⌘" on Mac, "Ctrl" on Windows. Omit when no shortcut applies. */
  shortcutKey?: string;
  approveLabel?: string;
  denyLabel?: string;
  disabled?: boolean;
  /** Row-height buttons, for a dense list (the Inbox) on a desktop surface. */
  compact?: boolean;
}

export function ApprovalActions({
  onApprove,
  onDeny,
  shortcutKey,
  approveLabel = "Approve All",
  denyLabel = "Deny All",
  disabled,
  compact,
}: ApprovalActionsProps) {
  // Narrow surfaces have no physical keyboard, so the shortcut badge is dead
  // weight competing for space with the buttons it's meant to be a shortcut
  // for — drop it and give the buttons a real touch target instead.
  const surface = useSurface();
  const isNarrow = surface !== "desktop";
  const size = isNarrow ? "min-h-[44px] flex-1 px-3 text-sm" : compact ? "h-7 px-2.5 text-xs" : "px-3 py-1.5 text-sm";

  return (
    <div className={cn("flex items-center gap-2", isNarrow && "flex-1 flex-wrap")}>
      <Tooltip content={approveLabel} placement="bottom" delay={300} wrapperClassName={isNarrow ? "flex flex-1" : "inline-flex"}>
<button
        type="button"
        onClick={onApprove}
        disabled={disabled}
        className={cn(
          "flex items-center justify-center gap-2 rounded font-medium bg-success hover:bg-success/90 text-success-foreground transition-colors disabled:opacity-60",
          size
        )}
      >
        {approveLabel}
        {!isNarrow && shortcutKey && (
          <span className="px-1.5 py-0.5 rounded text-xs font-mono" style={{
            backgroundColor: 'hsl(var(--success-foreground) / 0.2)',
            color: 'hsl(var(--success-foreground))'
          }}>
            {shortcutKey}+↵
          </span>
        )}
      </button>
</Tooltip>

      <Tooltip content={denyLabel} placement="bottom" delay={300} wrapperClassName={isNarrow ? "flex flex-1" : "inline-flex"}>
<button
        type="button"
        onClick={onDeny}
        disabled={disabled}
        className={cn(
          "rounded font-medium bg-destructive hover:bg-destructive/90 text-destructive-foreground transition-colors disabled:opacity-60",
          size
        )} aria-label={denyLabel}>
        {denyLabel}
      </button>
</Tooltip>
    </div>
  );
}
