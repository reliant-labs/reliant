import { memo, useState, useEffect, useRef, useCallback } from "react";
import { ArrowDown } from "lucide-react";
import { cn } from "../../lib/utils";

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
 * Starts as a small circle, then expands into a labeled pill after a
 * short delay so users notice it during longer scrolls.
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
      expandTimerRef.current = setTimeout(() => setExpanded(true), 600);
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
  }, [visible, startIdleTimer, clearIdleTimer]);

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
    <div className="pointer-events-none absolute inset-x-0 bottom-3 z-30 flex justify-center">
      <button
        onClick={onClick}
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
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
          expanded
            ? "h-8 px-3.5 gap-1.5"
            : "h-8 w-8 gap-0",
        )}
      >
        <ArrowDown className="w-3.5 h-3.5 flex-shrink-0" />
        <span
          className={cn(
            "text-xs font-medium whitespace-nowrap overflow-hidden transition-all duration-300",
            expanded
              ? "max-w-[120px] opacity-100"
              : "max-w-0 opacity-0",
          )}
        >
          Scroll to bottom
        </span>
      </button>
    </div>
  );
});