import { memo, useState, useEffect, useRef, useCallback } from "react";
import { ArrowDown } from "lucide-react";
import { cn } from "../../lib/utils";
import { useCoarsePointer } from "./useCoarsePointer";

interface ScrollToBottomButtonProps {
  visible: boolean;
  onClick: () => void;
}

const IDLE_FADE_MS = 2000;

/**
 * Floating scroll-to-bottom pill over the bottom edge of the transcript.
 *
 * Render it inside the transcript's positioned frame: it anchors to that
 * frame's bottom edge. It used to hang off the composer's top edge by a fixed
 * offset, which put it on whatever sat between the two — the background-work
 * pill, the queued-message strip, the permissions panel — and that band
 * changes height with what is running, so no fixed offset can clear it.
 * Anchored to the transcript, the band can only push it up with the frame.
 *
 * Where the composer floats over the transcript (the mobile chat screen), the
 * frame's bottom edge is under the composer, so the anchor adds the
 * `--chat-bottom-inset` the floating band publishes.
 *
 * Starts as a small circle, then expands into a labeled pill after a
 * short delay so users notice it during longer scrolls. On a coarse pointer it
 * stays a 44px circle: on a phone the expanding label covers the very text the
 * reader is looking at, and the ↓ alone is the platform idiom (ChatGPT,
 * Claude) — see `dev-docs/design/mobile-chat-redesign.md` §7.3.
 *
 * Dims after a couple seconds of inactivity so it obstructs less of the text
 * beneath it, but stays plainly visible — it is the only way back to the live
 * end of the conversation. Hovering brings it back to full opacity.
 */
export const ScrollToBottomButton = memo(function ScrollToBottomButton({
  visible,
  onClick,
}: ScrollToBottomButtonProps) {
  const [mounted, setMounted] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [idleFaded, setIdleFaded] = useState(false);
  const [hovered, setHovered] = useState(false);
  const coarsePointer = useCoarsePointer();
  const expandTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const idleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const startIdleTimer = useCallback(() => {
    if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
    idleTimerRef.current = setTimeout(() => setIdleFaded(true), IDLE_FADE_MS);
  }, []);

  const clearIdleTimer = useCallback(() => {
    if (idleTimerRef.current) {
      clearTimeout(idleTimerRef.current);
      idleTimerRef.current = null;
    }
  }, []);

  useEffect(() => {
    if (visible) {
      setMounted(true);
      setExpanded(false);
      setIdleFaded(false);
      if (!coarsePointer) {
        expandTimerRef.current = setTimeout(() => setExpanded(true), 600);
      }
      startIdleTimer();
    } else {
      setExpanded(false);
      setIdleFaded(false);
      setHovered(false);
      clearIdleTimer();
      const exitTimer = setTimeout(() => setMounted(false), 200);
      if (expandTimerRef.current) {
        clearTimeout(expandTimerRef.current);
        expandTimerRef.current = null;
      }
      return () => clearTimeout(exitTimer);
    }
    return () => {
      if (expandTimerRef.current) {
        clearTimeout(expandTimerRef.current);
        expandTimerRef.current = null;
      }
      clearIdleTimer();
    };
  }, [visible, coarsePointer, startIdleTimer, clearIdleTimer]);

  const handleMouseEnter = useCallback(() => {
    setHovered(true);
    clearIdleTimer();
  }, [clearIdleTimer]);

  const handleMouseLeave = useCallback(() => {
    setHovered(false);
    startIdleTimer();
  }, [startIdleTimer]);

  if (!mounted) return null;

  return (
    // Non-interactive full-width strip, so only the button itself takes clicks
    // — the transcript on either side of it stays selectable and clickable.
    // Lifted by the floating composer band's height where the chrome floats
    // over the transcript (floatingChrome.tsx); the inset is unset, so 0,
    // where it docks.
    <div
      className="pointer-events-none absolute inset-x-0 z-30 flex justify-center"
      style={{ bottom: "calc(var(--chat-bottom-inset, 0px) + 0.75rem)" }}
      data-testid="scroll-to-bottom-anchor"
    >
      <button
        onClick={onClick}
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
        // The visible label is transient (desktop) or absent (touch); the
        // accessible name is neither.
        aria-label="Scroll to bottom"
        className={cn(
          "pointer-events-auto flex items-center justify-center",
          "rounded-full shadow-lg",
          "bg-primary text-primary-foreground",
          "hover:bg-primary/90 active:scale-95",
          "border border-primary-foreground/20",
          "transition-all duration-300 ease-out",
          "cursor-pointer select-none",
          visible
            ? idleFaded && !hovered
              ? "opacity-60 translate-y-0 scale-100"
              : "opacity-100 translate-y-0 scale-100"
            : "opacity-0 translate-y-2 scale-95 pointer-events-none",
          coarsePointer
            ? "h-[44px] w-[44px]"
            : expanded
              ? "h-8 px-3.5 gap-1.5"
              : "h-8 w-8 gap-0",
        )}
      >
        <ArrowDown aria-hidden="true" className={cn("flex-shrink-0", coarsePointer ? "h-5 w-5" : "h-3.5 w-3.5")} />
        {!coarsePointer && (
          <span
            aria-hidden="true"
            className={cn(
              "text-xs font-medium whitespace-nowrap overflow-hidden transition-all duration-300",
              expanded
                ? "max-w-[120px] opacity-100"
                : "max-w-0 opacity-0",
            )}
          >
            Scroll to bottom
          </span>
        )}
      </button>
    </div>
  );
});