import { useEffect, useCallback } from 'react';
import { useSidebarStore } from '../store/sidebarStore';

interface UsePanelSafetyOptions {
  width: number;
  setWidth: (width: number) => void;
  minWidth: number;
  /** IDs of other elements that consume horizontal space (e.g., opposing sidebar) */
  subtractIds: string[];
  isEnabled?: boolean;
}

const MIN_MAIN_CONTENT_WIDTH = 300;

/**
 * Hook to strictly enforce that a panel does not consume so much space
 * that the main content area (Chat) is hidden or becomes too small.
 * 
 * It reacts to:
 * 1. Window resize events
 * 2. Layout changes (via ResizeObserver on body)
 */
export function usePanelSafety({
  width,
  setWidth,
  minWidth,
  subtractIds,
  isEnabled = true
}: UsePanelSafetyOptions) {
  // Subscribe to the global resizing state to avoid conflict during active drag
  const isResizingGlobal = useSidebarStore((state) => state.isResizing);

  const checkSafety = useCallback(() => {
    if (!isEnabled || isResizingGlobal) return;

    let otherPanelsWidth = 0;
    
    // Sum up the width of all subtracted elements
    for (const id of subtractIds) {
      const el = document.getElementById(id);
      if (el) {
        otherPanelsWidth += el.getBoundingClientRect().width;
      }
    }

    // Calculate maximum allowed width for THIS panel
    // Window - Others - SafeZone = MaxForUs
    const maxSafeWidth = window.innerWidth - otherPanelsWidth - MIN_MAIN_CONTENT_WIDTH;
    
    // If our current width is larger than allowed, shrink it
    if (width > maxSafeWidth) {
      // Ensure we don't go below absolute minWidth (though that might mean overlap, it's better than 0 width chat)
      // Actually, if maxSafeWidth < minWidth, the window is just too small. 
      // We prioritize the chat visibility over the sidebar width if possible, 
      // but we respect the panel's minWidth to prevent breaking its layout.
      const newWidth = Math.max(minWidth, maxSafeWidth);
      
      // Only update if there's a significant difference to avoid rounding jitter
      if (Math.abs(width - newWidth) > 1) {
        setWidth(newWidth);
      }
    }
  }, [width, setWidth, minWidth, subtractIds, isEnabled, isResizingGlobal]);

  useEffect(() => {
    if (!isEnabled) return;

    window.addEventListener('resize', checkSafety);
    
    // Watch for layout changes (e.g. sidebar toggles)
    const observer = new ResizeObserver(() => {
      // Defer slightly to allow React layout updates to settle
      requestAnimationFrame(checkSafety);
    });
    observer.observe(document.body);

    // Initial check
    checkSafety();

    return () => {
      window.removeEventListener('resize', checkSafety);
      observer.disconnect();
    };
  }, [checkSafety, isEnabled]);

  return { checkSafety };
}
