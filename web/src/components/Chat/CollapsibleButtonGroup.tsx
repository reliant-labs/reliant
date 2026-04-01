import { useState, useRef, useEffect, useCallback, type ReactElement } from "react";
import { MoreHorizontal } from "lucide-react";
import { ChatButton } from "./ChatButton";

interface CollapsibleButtonGroupProps {
  children: ReactElement | ReactElement[] | (ReactElement | null)[];
  maxVisibleButtons?: number;
  className?: string;
  compact?: boolean;
}

export function CollapsibleButtonGroup({
  children,
  maxVisibleButtons = 6,
  className = "",
  compact = false,
}: CollapsibleButtonGroupProps) {
  const [isOverflowOpen, setIsOverflowOpen] = useState(false);
  const [shouldCollapse, setShouldCollapse] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const overflowRef = useRef<HTMLDivElement>(null);

  // Filter out null children
  const childArray = Array.isArray(children) ? children : [children];
  const validChildren = childArray.filter(Boolean) as ReactElement[];

  // Debounced check function to avoid layout thrashing
  const checkOverflow = useCallback(() => {
    if (!containerRef.current) return;

    const container = containerRef.current;
    const containerWidth = container.offsetWidth;

    // Aggressive collapse thresholds for better mobile experience:
    // Start collapsing when container gets smaller
    const shouldCollapseConditions = [
      // Always collapse if we have more than 3 buttons and width < 300px
      validChildren.length > 3 && containerWidth < 300,
      // Collapse if we have more than 2 buttons and width < 200px
      validChildren.length > 2 && containerWidth < 200,
      // Collapse if we have more than 1 button and width < 150px
      validChildren.length > 1 && containerWidth < 150,
    ];

    setShouldCollapse(shouldCollapseConditions.some(condition => condition));
  }, [validChildren.length]);

  // Check if we need to collapse based on available space
  // PERFORMANCE: Debounce ResizeObserver callbacks to avoid layout thrashing
  useEffect(() => {
    let rafId: number;
    let debounceTimeout: ReturnType<typeof setTimeout>;

    const debouncedCheckOverflow = () => {
      // Cancel any pending checks
      cancelAnimationFrame(rafId);
      clearTimeout(debounceTimeout);
      
      // Debounce by 50ms and use rAF to batch with browser's layout pass
      debounceTimeout = setTimeout(() => {
        rafId = requestAnimationFrame(checkOverflow);
      }, 50);
    };

    // Use ResizeObserver for more accurate detection
    const resizeObserver = new ResizeObserver(debouncedCheckOverflow);
    if (containerRef.current) {
      resizeObserver.observe(containerRef.current);
    }

    // Initial check (immediate, no debounce needed)
    checkOverflow();

    return () => {
      resizeObserver.disconnect();
      cancelAnimationFrame(rafId);
      clearTimeout(debounceTimeout);
    };
  }, [checkOverflow, maxVisibleButtons]);

  // Close overflow menu when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (overflowRef.current && !overflowRef.current.contains(event.target as Node)) {
        setIsOverflowOpen(false);
      }
    };

    if (isOverflowOpen) {
      document.addEventListener('mousedown', handleClickOutside);
      return () => document.removeEventListener('mousedown', handleClickOutside);
    }
  }, [isOverflowOpen]);

  if (!shouldCollapse) {
    // Show all buttons normally
    return (
      <div ref={containerRef} className={`flex items-center gap-1 ${className}`}>
        {validChildren}
      </div>
    );
  }

  // Calculate how many buttons to show vs hide
  let visibleCount;
  
  if (maxVisibleButtons === 0) {
    // Force all buttons into overflow menu
    visibleCount = 0;
  } else {
    // More responsive calculation: show fewer buttons on very small screens
    const containerWidth = containerRef.current?.offsetWidth || 300;
    
    if (containerWidth < 200) {
      visibleCount = 1; // Ultra-narrow: show only 1 button + overflow
    } else if (containerWidth < 300) {
      visibleCount = 2; // Narrow: show 2 buttons + overflow
    } else if (containerWidth < 400) {
      visibleCount = 3; // Medium-narrow: show 3 buttons + overflow
    } else {
      visibleCount = Math.max(2, Math.floor(maxVisibleButtons * 0.7)); // Wider: show ~70% of max
    }
  }
  
  const visibleButtons = validChildren.slice(0, visibleCount);
  const hiddenButtons = validChildren.slice(visibleCount);

  return (
    <div ref={containerRef} className={`flex items-center gap-1 ${className}`}>
      {/* Always visible buttons */}
      {visibleButtons}

      {/* Overflow menu */}
      {hiddenButtons.length > 0 && (
        <div className="relative" ref={overflowRef}>
          <ChatButton
            onClick={() => setIsOverflowOpen(!isOverflowOpen)}
            tooltip="More actions"
            compact={compact}
            className="bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)] border-[var(--chat-border)]"
          >
            <MoreHorizontal className={compact ? "w-2.5 h-2.5" : "w-3 h-3"} />
          </ChatButton>

          {/* Overflow dropdown */}
          {isOverflowOpen && (
            <div className="absolute bottom-full mb-1 right-0 bg-[var(--chat-dropdown-bg)] border border-border/50 rounded-md elevation-4 z-[1000] min-w-max">
              <div className="flex items-center gap-1 p-1.5">
                {hiddenButtons.map((button, index) => (
                  <div key={index}>
                    {button}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}