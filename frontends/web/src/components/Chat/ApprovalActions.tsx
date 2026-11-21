/**
 * ApprovalActions - Renders approve/deny buttons for workflow approvals.
 *
 * Props only: the caller decides what approving means. The chat's approval
 * panel (PermissionsPanel) approves one approval, or every routine one at
 * once; the Inbox approves one approval of any chat.
 *
 * Approve is the one filled button, the brand's primary; Deny is a quiet
 * ghost beside it. Green is reserved for success. Name the buttons after their subject
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
  const size = isNarrow ? "min-h-[44px] flex-1 px-4 text-ui" : compact ? "h-8 px-3 text-ui" : "h-9 px-4 text-ui";
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
          "flex items-center justify-center gap-2 rounded-lg font-medium bg-primary text-primary-foreground transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60",
          size,
        )}
      >
        {approveLabel}
        {showShortcut && (
          <span className="text-meta font-normal opacity-70" aria-hidden="true">
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
          "rounded-lg font-medium text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60",
          size,
        )}
      >
        {denyLabel}
      </button>
    </div>
  );
}
