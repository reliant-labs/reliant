/**
 * ApprovalActions - Renders approve/deny buttons for workflow approvals
 */

import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";

interface ApprovalActionsProps {
  onApprove: () => void;
  onDeny: () => void;
  shortcutKey: string; // "⌘" on Mac, "Ctrl" on Windows
}

export function ApprovalActions({
  onApprove,
  onDeny,
  shortcutKey,
}: ApprovalActionsProps) {
  // Narrow surfaces have no physical keyboard, so the shortcut badge is dead
  // weight competing for space with the buttons it's meant to be a shortcut
  // for — drop it and give the buttons a real touch target instead.
  const surface = useSurface();
  const isNarrow = surface !== "desktop";

  return (
    <div className={cn("flex items-center gap-2", isNarrow && "flex-1 flex-wrap")}>
      <button
        onClick={onApprove}
        className={cn(
          "flex items-center justify-center gap-2 rounded font-medium bg-success hover:bg-success/90 text-success-foreground transition-colors",
          isNarrow ? "min-h-[44px] flex-1 px-3 text-sm" : "px-3 py-1.5 text-sm"
        )}
        title="Approve All"
      >
        Approve All
        {!isNarrow && (
          <span className="px-1.5 py-0.5 rounded text-xs font-mono" style={{
            backgroundColor: 'hsl(var(--success-foreground) / 0.2)',
            color: 'hsl(var(--success-foreground))'
          }}>
            {shortcutKey}+↵
          </span>
        )}
      </button>

      <button
        onClick={onDeny}
        className={cn(
          "rounded font-medium bg-destructive hover:bg-destructive/90 text-destructive-foreground transition-colors",
          isNarrow ? "min-h-[44px] flex-1 px-3 text-sm" : "px-3 py-1.5 text-sm"
        )}
        title="Deny All"
      >
        Deny All
      </button>
    </div>
  );
}
