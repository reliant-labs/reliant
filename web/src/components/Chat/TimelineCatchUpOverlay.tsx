import { Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";

const LABEL = "Loading conversation…";

/**
 * Covers the chat timeline while it catches up to the stream
 * (lib/timelineCatchUp). The stale transcript under it is blurred and dimmed;
 * an empty one has nothing to blur, so only the label shows. The overlay
 * itself takes the pointer — clicks and wheel never reach the content being
 * replaced — while the host makes that content inert for the keyboard.
 *
 * The visual label is aria-hidden: the announcement comes from
 * TimelineCatchUpStatus, a live region that stays mounted so assistive tech
 * hears the change.
 */
export function TimelineCatchUpOverlay({ hasContent }: { hasContent: boolean }) {
  return (
    <div
      data-testid="timeline-catchup-overlay"
      aria-hidden="true"
      className={cn(
        "timeline-catchup-in absolute inset-0 z-20 flex items-center justify-center",
        hasContent && "bg-background/40 backdrop-blur-xs",
      )}
    >
      <span className="flex items-center gap-2 rounded-full border border-border bg-card px-3 py-1.5 text-sm text-muted-foreground shadow-sm">
        <Loader2 className="h-3.5 w-3.5 motion-safe:animate-spin" />
        {LABEL}
      </span>
    </div>
  );
}

/** Polite announcement of the overlay; mounted always, filled while shown. */
export function TimelineCatchUpStatus({ visible }: { visible: boolean }) {
  return (
    <div
      role="status"
      aria-live="polite"
      className="sr-only"
      data-testid="timeline-catchup-status"
    >
      {visible ? LABEL : null}
    </div>
  );
}
