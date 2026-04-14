/**
 * ApprovalActions - Renders approve/deny buttons for workflow approvals
 */

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
  return (
    <div className="flex items-center gap-2">
      <button
        onClick={onApprove}
        className="flex items-center gap-2 px-3 py-1.5 text-sm bg-success hover:bg-success/90 text-success-foreground rounded transition-colors"
        title="Approve All"
      >
        Approve All
        <span className="px-1.5 py-0.5 rounded text-xs font-mono" style={{
          backgroundColor: 'hsl(var(--success-foreground) / 0.2)',
          color: 'hsl(var(--success-foreground))'
        }}>
          {shortcutKey}+↵
        </span>
      </button>
      
      <button
        onClick={onDeny}
        className="px-3 py-1.5 text-sm bg-destructive hover:bg-destructive/90 text-destructive-foreground rounded transition-colors"
        title="Deny All"
      >
        Deny All
      </button>
    </div>
  );
}
