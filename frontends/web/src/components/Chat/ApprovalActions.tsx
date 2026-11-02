/**
 * ApprovalActions - Renders approve/deny buttons for workflow approvals.
 *
 * Props only: the caller decides what approving means. The chat's approval
 * panel (PermissionsPanel) approves one approval, or every routine one at
 * once; the Inbox approves one approval of any chat.
 *
 * Approve is the brand's primary fill and Deny an outline with danger text:
 * approving should follow reading, not be the loudest thing on the row, and
 * green is reserved for success. Name the buttons after their subject
 * (`approveName`), so a list of approvals is not a list of identical
 * "Approve" buttons to a screen reader.
 */

import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";

interface ApprovalActionsProps {
  onApprove: () => void;
  onDeny: () => void;
  /** "⌘" on Mac, "Ctrl" on Windows. Omit when no shortcut applies. */
  shortcutKey?: string;
  approveLabel?: string;
  denyLabel?: string;
  /** Accessible names, when the visible label does not say what is approved. */
  approveName?: string;
  denyName?: string;
  disabled?: boolean;
  /** Row-height buttons, for a dense list (the Inbox) on a desktop surface. */
  compact?: boolean;
}

export function ApprovalActions({
  onApprove,
  onDeny,
  shortcutKey,
  approveLabel = "Approve",
  denyLabel = "Deny",
  approveName,
  denyName,
  disabled,
  compact,
}: ApprovalActionsProps) {
  // Narrow surfaces have no physical keyboard, so the shortcut badge is dead
  // weight competing for space with the buttons it's meant to be a shortcut
  // for — drop it and give the buttons a real touch target instead.
  const surface = useSurface();
  const isNarrow = surface !== "desktop";
  const size = isNarrow ? "min-h-[44px] flex-1 px-3 text-sm" : compact ? "h-7 px-2.5 text-xs" : "px-3 py-1.5 text-sm";
  const showShortcut = !isNarrow && !!shortcutKey;

  return (
    <div className={cn("flex items-center gap-2", isNarrow && "flex-1 flex-wrap")}>
      <button
        type="button"
        onClick={onApprove}
        disabled={disabled}
        aria-label={approveName}
        aria-keyshortcuts={showShortcut ? `${shortcutKey === "⌘" ? "Meta" : "Control"}+Enter` : undefined}
        className={cn(
          "flex items-center justify-center gap-2 rounded font-medium bg-primary text-primary-foreground transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60",
          size,
        )}
      >
        {approveLabel}
        {showShortcut && (
          <span className="rounded bg-primary-foreground/20 px-1.5 py-0.5 font-mono text-xs" aria-hidden="true">
            {shortcutKey}+↵
          </span>
        )}
      </button>
      <button
        type="button"
        onClick={onDeny}
        disabled={disabled}
        aria-label={denyName}
        className={cn(
          "rounded border border-border bg-background font-medium text-destructive-ink transition-colors hover:bg-destructive/10 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60",
          size,
        )}
      >
        {denyLabel}
      </button>
    </div>
  );
}
