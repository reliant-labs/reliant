import { X } from "lucide-react";
import { useExternalWorktreeHint } from "./useExternalWorktrees";
import { hintLabel } from "./discoverWorktreesUtils";

interface Props {
  projectId: string;
  onReview: () => void;
}

export function WorktreesDiscoverHint({ projectId, onReview }: Props) {
  const { count, visible, dismiss } = useExternalWorktreeHint(projectId);
  if (!visible) return null;
  return (
    <div className="flex items-center gap-1.5 text-xs text-muted-foreground" data-testid="worktrees-discover-hint">
      <span>{hintLabel(count)} ·</span>
      <button type="button" onClick={onReview} className="font-medium text-primary hover:underline">
        Review
      </button>
      <button
        type="button"
        onClick={dismiss}
        aria-label="Dismiss"
        className="ml-auto rounded p-0.5 hover:text-foreground"
      >
        <X className="h-3 w-3" aria-hidden="true" />
      </button>
    </div>
  );
}
