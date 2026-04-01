import React, { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { createPortal } from "react-dom";

interface TooltipProps {
  content: string;
  children: React.ReactNode;
  delay?: number;
  placement?: "top" | "bottom" | "left" | "right";
  className?: string;
  wrapperClassName?: string;
}

function TooltipInner({
  content,
  children,
  delay = 500,
  placement = "top",
  className = "",
  wrapperClassName = "",
}: TooltipProps) {
  const [isVisible, setIsVisible] = useState(false);
  const [position, setPosition] = useState({ x: 0, y: 0 });
  const [isInteractionActive, setIsInteractionActive] = useState(false);
  const timeoutRef = useRef<NodeJS.Timeout | null>(null);
  const targetRef = useRef<HTMLDivElement>(null);

  const showTooltip = useCallback(() => {
    // Don't show tooltip if user is interacting (dropdown open, etc)
    if (isInteractionActive) return;
    
    // Don't show tooltip if there's an open dropdown inside
    if (targetRef.current) {
      const hasOpenDropdown = targetRef.current.querySelector('[role="dialog"], .dropdown-open, [data-dropdown-open="true"]');
      if (hasOpenDropdown) return;
    }
    
    timeoutRef.current = setTimeout(() => {
      if (targetRef.current && targetRef.current.offsetParent !== null) {
        const rect = targetRef.current.getBoundingClientRect();
        const scrollLeft =
          window.pageXOffset || document.documentElement.scrollLeft;
        const scrollTop =
          window.pageYOffset || document.documentElement.scrollTop;

        let x = rect.left + scrollLeft + rect.width / 2;
        let y = rect.top + scrollTop;

        // Estimate tooltip dimensions (rough approximation)
        const tooltipWidth = content.length * 8; // ~8px per character
        const tooltipHeight = 32; // Rough height estimate

        switch (placement) {
          case "top":
            y = rect.top + scrollTop - 8;
            break;
          case "bottom":
            y = rect.bottom + scrollTop + 8;
            break;
          case "left":
            x = rect.left + scrollLeft - 8;
            y = rect.top + scrollTop + rect.height / 2;
            break;
          case "right":
            x = rect.right + scrollLeft + 8;
            y = rect.top + scrollTop + rect.height / 2;
            break;
        }

        // Boundary detection and adjustment
        const viewportWidth = window.innerWidth;
        const viewportHeight = window.innerHeight;

        // Adjust horizontal position to stay within viewport
        if (placement === "top" || placement === "bottom") {
          // For top/bottom placements, tooltip is centered, so check both sides
          const halfWidth = tooltipWidth / 2;
          if (x - halfWidth < 0) {
            x = halfWidth + 8; // Add some padding
          } else if (x + halfWidth > viewportWidth) {
            x = viewportWidth - halfWidth - 8;
          }
        } else if (placement === "right") {
          // For right placement, check if tooltip extends beyond right edge
          if (x + tooltipWidth > viewportWidth) {
            x = viewportWidth - tooltipWidth - 8;
          }
        } else if (placement === "left") {
          // For left placement, check if tooltip extends beyond left edge
          if (x - tooltipWidth < 0) {
            x = tooltipWidth + 8;
          }
        }

        // Adjust vertical position to stay within viewport
        if (placement === "left" || placement === "right") {
          // For left/right placements, tooltip is vertically centered
          const halfHeight = tooltipHeight / 2;
          if (y - halfHeight < 0) {
            y = halfHeight + 8;
          } else if (y + halfHeight > viewportHeight) {
            y = viewportHeight - halfHeight - 8;
          }
        } else if (placement === "bottom") {
          // For bottom placement, check if tooltip extends beyond bottom edge
          if (y + tooltipHeight > viewportHeight) {
            y = viewportHeight - tooltipHeight - 8;
          }
        } else if (placement === "top") {
          // For top placement, check if tooltip extends beyond top edge
          if (y - tooltipHeight < 0) {
            y = tooltipHeight + 8;
          }
        }

        setPosition({ x, y });
        setIsVisible(true);
      }
    }, delay);
  }, [content, delay, placement, isInteractionActive]);

  const hideTooltip = useCallback(() => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current);
      timeoutRef.current = null;
    }
    setIsVisible(false);
  }, []);

  const handleClick = useCallback(() => {
    // User clicked - mark as interacting and hide tooltip
    setIsInteractionActive(true);
    hideTooltip();
  }, [hideTooltip]);

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      setIsVisible(false);
    };
  }, []);

  // Hide tooltip when a modal opens or when body overflow changes (indicating modal state)
  // Only register observers when tooltip is actually visible
  useEffect(() => {
    if (!isVisible) return;

    const handleModalOpen = () => {
      hideTooltip();
    };

    // Listen for scroll lock (indicates modal opened)
    const observer = new MutationObserver(() => {
      if (document.body.style.overflow === 'hidden') {
        hideTooltip();
      }
    });

    observer.observe(document.body, {
      attributes: true,
      attributeFilter: ['style'],
    });

    // Also listen for custom event if any modal dispatches it
    window.addEventListener('modalOpened', handleModalOpen);

    return () => {
      observer.disconnect();
      window.removeEventListener('modalOpened', handleModalOpen);
    };
  }, [isVisible, hideTooltip]);

  // Hide tooltip on any click anywhere
  // Only register when tooltip is actually visible
  useEffect(() => {
    if (!isVisible) return;

    const handleGlobalClick = () => {
      hideTooltip();
    };

    document.addEventListener('mousedown', handleGlobalClick);
    return () => document.removeEventListener('mousedown', handleGlobalClick);
  }, [isVisible, hideTooltip]);

  // Reset interaction state when clicking anywhere (to allow tooltips again)
  // But with a delay to prevent immediate re-show
  useEffect(() => {
    if (!isInteractionActive) return;

    const resetTimer = setTimeout(() => {
      setIsInteractionActive(false);
    }, 300); // Short delay to prevent tooltip flickering

    return () => clearTimeout(resetTimer);
  }, [isInteractionActive]);

  const tooltipClasses = useMemo(() => `
    absolute z-50 px-2 py-1 text-xs rounded-md pointer-events-none
    shadow-lg
    whitespace-nowrap tooltip-themed
    transition-opacity duration-150
    ${placement === "top" ? "transform -translate-x-1/2 -translate-y-full" : ""}
    ${placement === "bottom" ? "transform -translate-x-1/2" : ""}
    ${
      placement === "left" ? "transform -translate-y-1/2 -translate-x-full" : ""
    }
    ${placement === "right" ? "transform -translate-y-1/2" : ""}
    ${className}
  `, [placement, className]);

  return (
    <>
      <div
        ref={targetRef}
        onMouseEnter={showTooltip}
        onMouseLeave={hideTooltip}
        onFocus={showTooltip}
        onBlur={hideTooltip}
        onClick={handleClick}
        className={wrapperClassName}
      >
        {children}
      </div>
      {isVisible &&
        createPortal(
          <div
            className={tooltipClasses}
            style={{
              left: position.x,
              top: position.y,
            }}
          >
            {content}
          </div>,
          document.body
        )}
    </>
  );
}

export const Tooltip = React.memo(TooltipInner);
