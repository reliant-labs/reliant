import { memo, useState, useEffect, useRef } from "react";
import { ArrowDown } from "lucide-react";
import { cn } from "../../lib/utils";

interface ScrollToBottomButtonProps {
  visible: boolean;
  onClick: () => void;
}

/**
 * Floating scroll-to-bottom pill that sits above the chat input.
 *
 * Rendered as a zero-height flex item so it doesn't affect layout.
 * The button uses negative margin to float upward over the messages area.
 *
 * Starts as a small circle, then expands into a labeled pill after a
 * short delay so users notice it during longer scrolls.
 */
export const ScrollToBottomButton = memo(function ScrollToBottomButton({
  visible,
  onClick,
}: ScrollToBottomButtonProps) {
  const [mounted, setMounted] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const expandTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (visible) {
      setMounted(true);
      setExpanded(false);
      expandTimerRef.current = setTimeout(() => setExpanded(true), 600);
    } else {
      setExpanded(false);
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
    };
  }, [visible]);

  if (!mounted) return null;

  return (
    <div className="flex-shrink-0 flex justify-center relative z-20" style={{ height: 0 }}>
      <button
        onClick={onClick}
        style={{ marginTop: -44 }}
        className={cn(
          "flex items-center justify-center gap-1.5",
          "rounded-full shadow-lg",
          "bg-primary text-primary-foreground",
          "hover:bg-primary/90 active:scale-95",
          "border border-primary-foreground/20",
          "transition-all duration-300 ease-out",
          "cursor-pointer select-none",
          visible
            ? "opacity-100 translate-y-0 scale-100"
            : "opacity-0 translate-y-2 scale-95",
          expanded
            ? "h-8 px-3.5"
            : "h-8 w-8",
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