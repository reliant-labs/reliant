import { useState, useEffect, useRef } from "react";

/**
 * Hook to track container width and provide responsive breakpoints
 * Uses ResizeObserver for accurate container-based responsiveness
 */
export function useContainerWidth() {
  const [width, setWidth] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const updateWidth = () => {
      if (containerRef.current) {
        setWidth(containerRef.current.offsetWidth);
      }
    };

    // Initial width
    updateWidth();

    // Use ResizeObserver for accurate container width tracking
    const resizeObserver = new ResizeObserver(updateWidth);
    if (containerRef.current) {
      resizeObserver.observe(containerRef.current);
    }

    // Fallback to window resize
    window.addEventListener("resize", updateWidth);

    return () => {
      resizeObserver.disconnect();
      window.removeEventListener("resize", updateWidth);
    };
  }, []);

  // Define responsive breakpoints based on available space
  const breakpoints = {
    // Extra small - very cramped
    xs: width < 400,
    // Small - compact mode needed
    sm: width >= 400 && width < 600,
    // Medium - some compression
    md: width >= 600 && width < 800,
    // Large - comfortable
    lg: width >= 800,
    // Specific use cases
    compact: width < 600,  // Use compact/icon-only mode
    comfortable: width >= 800, // Full labels and spacing
  };

  return { width, breakpoints, containerRef };
}
