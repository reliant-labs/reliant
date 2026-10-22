import { PencilRuler } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";

interface DraftStatusBadgeProps {
  /** Number of blocking problems the draft currently has. */
  errorCount?: number;
  className?: string;
}

/**
 * "Draft" marker for a stored workflow that is work in progress: saved as-is,
 * possibly with problems, and never runnable until it is published. Published
 * workflows render no badge — runnable is the default state users expect.
 */
export function DraftStatusBadge({ errorCount = 0, className }: DraftStatusBadgeProps) {
  const tooltip =
    errorCount > 0
      ? `Draft — can't run yet. ${errorCount} problem${errorCount === 1 ? "" : "s"} to fix before it can be published.`
      : "Draft — can't run until it is published.";
  return (
    <Tooltip content={tooltip}>
      <span
        data-testid="workflow-draft-badge"
        className={cn(
          "inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5",
          "text-xs font-medium uppercase text-muted-foreground",
          className,
        )}
      >
        <PencilRuler className="h-2.5 w-2.5" />
        Draft
        {errorCount > 0 && <span className="text-destructive-ink">· {errorCount}</span>}
      </span>
    </Tooltip>
  );
}
