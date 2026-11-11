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
 *
 * `leaving`: the timeline has caught up and is usable under it already. The
 * overlay only fades out, and lets every pointer through while it does.
 */
export function TimelineCatchUpOverlay({
  hasContent,
  leaving = false,
}: {
  hasContent: boolean;
  leaving?: boolean;
}) {
  return (
    <div
      data-testid="timeline-catchup-overlay"
      data-leaving={leaving || undefined}
      aria-hidden="true"
      className={cn(
        "absolute inset-0 z-20 flex items-center justify-center",
        leaving ? "timeline-catchup-out pointer-events-none" : "timeline-catchup-in",
        hasContent && "bg-background/40 backdrop-blur-xs",
      )}
      // Under floating chrome the frame runs beneath both bars; center the
      // label in the part of it that is actually visible. 0 where docked.
      style={{ paddingTop: "var(--chat-top-inset, 0px)", paddingBottom: "var(--chat-bottom-inset, 0px)" }}
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
