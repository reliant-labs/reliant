/**
 * useSidebarOverlay - Manages sidebar overlay state for collapsed sidebar hover behavior
 *
 * When the sidebar is collapsed, this hook provides overlay functionality
 * that shows the sidebar temporarily when the user hovers near the left edge.
 *
 * @module hooks/useSidebarOverlay
 */

import { useState, useRef, useCallback, useEffect } from "react";

export interface UseSidebarOverlayOptions {
  /** Whether the sidebar is currently expanded (not collapsed) */
  isSidebarExpanded: boolean;
  /** Delay in ms before hiding overlay on mouse leave (default: 300) */
  hideDelay?: number;
  /** Delay in ms before setting hover state for animation (default: 10) */
  animationDelay?: number;
}

export interface UseSidebarOverlayReturn {
  /** Whether the overlay sidebar is currently visible (mounted in DOM) */
  showOverlaySidebar: boolean;
  /** Whether the sidebar area is being hovered (for CSS animation) */
  isSidebarHovered: boolean;
  /** Handler for mouse entering the sidebar hover zone */
  handleSidebarMouseEnter: () => void;
  /** Handler for mouse leaving the sidebar hover zone */
  handleSidebarMouseLeave: () => void;
  /** Handler for mouse entering the overlay sidebar itself */
  handleOverlayMouseEnter: () => void;
  /** Handler for mouse leaving the overlay sidebar */
  handleOverlayMouseLeave: () => void;
}

/**
 * Hook that manages sidebar overlay state for hover-to-reveal functionality.
 *
 * The overlay works in two phases:
 * 1. `showOverlaySidebar` - Controls whether the overlay is mounted in the DOM
 * 2. `isSidebarHovered` - Controls the CSS animation state (slide in/out)
 *
 * This two-phase approach allows for smooth enter/exit animations.
 *
 * Features:
 * - Shows overlay sidebar when hovering near collapsed sidebar edge
 * - Smooth slide-in animation via isSidebarHovered state
 * - Delayed hide to allow moving mouse to overlay without it closing
 * - Properly cleans up timeouts on unmount
 * - Only active when sidebar is collapsed
 *
 * @example
 * ```tsx
 * const {
 *   showOverlaySidebar,
 *   isSidebarHovered,
 *   handleSidebarMouseEnter,
 *   handleSidebarMouseLeave,
 *   handleOverlayMouseEnter,
 *   handleOverlayMouseLeave,
 * } = useSidebarOverlay({ isSidebarExpanded: showChatSidebar });
 *
 * return (
 *   <>
 *     <div
 *       className="hover-zone"
 *       onMouseEnter={handleSidebarMouseEnter}
 *       onMouseLeave={handleSidebarMouseLeave}
 *     />
 *     {showOverlaySidebar && (
 *       <div
 *         className={isSidebarHovered ? "visible" : "hidden"}
 *         onMouseEnter={handleOverlayMouseEnter}
 *         onMouseLeave={handleOverlayMouseLeave}
 *       >
 *         <Sidebar />
 *       </div>
 *     )}
 *   </>
 * );
 * ```
 */
export function useSidebarOverlay(
  options: UseSidebarOverlayOptions
): UseSidebarOverlayReturn {
  const { isSidebarExpanded, hideDelay = 300, animationDelay = 10 } = options;

  const [isSidebarHovered, setIsSidebarHovered] = useState(false);
  const [showOverlaySidebar, setShowOverlaySidebar] = useState(false);
  const sidebarTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  // Clear any pending timeout
  const clearSidebarTimeout = useCallback(() => {
    if (sidebarTimeoutRef.current) {
      clearTimeout(sidebarTimeoutRef.current);
      sidebarTimeoutRef.current = null;
    }
  }, []);

  // Handle mouse entering the sidebar hover zone
  // Shows the overlay immediately, then triggers animation after a small delay
  const handleSidebarMouseEnter = useCallback(() => {
    // Only trigger overlay behavior when sidebar is collapsed
    if (isSidebarExpanded) return;

    clearSidebarTimeout();
    
    // Mount the overlay in DOM immediately
    setShowOverlaySidebar(true);
    
    // Small delay before setting hover state for smooth slide-in animation
    sidebarTimeoutRef.current = setTimeout(() => {
      setIsSidebarHovered(true);
    }, animationDelay);
  }, [isSidebarExpanded, animationDelay, clearSidebarTimeout]);

  // Handle mouse leaving the sidebar hover zone
  // Delays hiding to allow user to move to the overlay
  const handleSidebarMouseLeave = useCallback(() => {
    if (isSidebarExpanded) return;

    clearSidebarTimeout();

    // Hide overlay after delay (allows moving to overlay without it closing)
    sidebarTimeoutRef.current = setTimeout(() => {
      setIsSidebarHovered(false);
      setShowOverlaySidebar(false);
    }, hideDelay);
  }, [isSidebarExpanded, hideDelay, clearSidebarTimeout]);

  // Handle mouse entering the overlay sidebar itself
  // Cancels any pending hide and ensures overlay stays visible
  const handleOverlayMouseEnter = useCallback(() => {
    clearSidebarTimeout();
    setIsSidebarHovered(true);
    setShowOverlaySidebar(true);
  }, [clearSidebarTimeout]);

  // Handle mouse leaving the overlay sidebar
  // Triggers delayed hide with slide-out animation
  const handleOverlayMouseLeave = useCallback(() => {
    clearSidebarTimeout();

    // First trigger the slide-out animation
    setIsSidebarHovered(false);
    
    // Then unmount after animation completes
    sidebarTimeoutRef.current = setTimeout(() => {
      setShowOverlaySidebar(false);
    }, hideDelay);
  }, [hideDelay, clearSidebarTimeout]);

  // Reset overlay state when sidebar expands
  useEffect(() => {
    if (isSidebarExpanded) {
      clearSidebarTimeout();
      setShowOverlaySidebar(false);
      setIsSidebarHovered(false);
    }
  }, [isSidebarExpanded, clearSidebarTimeout]);

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      clearSidebarTimeout();
    };
  }, [clearSidebarTimeout]);

  return {
    showOverlaySidebar,
    isSidebarHovered,
    handleSidebarMouseEnter,
    handleSidebarMouseLeave,
    handleOverlayMouseEnter,
    handleOverlayMouseLeave,
  };
}
