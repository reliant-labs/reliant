import { PencilRuler } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";

interface DraftStatusBadgeProps {
  /** Number of blocking validation errors the draft currently has. */
  errorCount?: number;
  className?: string;
}

/**
 * "Draft" marker for a stored workflow that is work in progress: saved as-is,
 * possibly invalid, and never runnable until it is marked complete. Complete
 * workflows render no badge — runnable is the default state users expect.
 */
export function DraftStatusBadge({ errorCount = 0, className }: DraftStatusBadgeProps) {
  const tooltip =
    errorCount > 0
      ? `Draft — not runnable. ${errorCount} validation error${errorCount === 1 ? "" : "s"} to fix before it can be marked complete.`
      : "Draft — not runnable until it is marked complete.";
  return (
    <Tooltip content={tooltip}>
      <span
        data-testid="workflow-draft-badge"
        className={cn(
          "inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5",
          "text-2xs font-medium uppercase text-muted-foreground",
          className,
        )}
      >
        <PencilRuler className="h-2.5 w-2.5" />
        Draft
        {errorCount > 0 && <span className="text-destructive">· {errorCount}</span>}
      </span>
    </Tooltip>
  );
}
