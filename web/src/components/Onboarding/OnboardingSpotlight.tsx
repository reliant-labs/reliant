/**
 * Onboarding Spotlight
 *
 * An overlay component that highlights UI elements during the onboarding tour.
 * Uses SVG masks to create a "cutout" effect around the target element.
 */

import { useEffect, useState, useCallback } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/utils";
import type { SpotlightConfig } from "./types";

// Store last known positions for smooth transitions between steps
let lastTooltipPosition: { top: number; left: number } | null = null;

interface OnboardingSpotlightProps {
  targetSelector: string;
  title: string;
  description: React.ReactNode;
  stepNumber: number;
  totalSteps: number;
  onNext: () => void;
  onBack?: () => void;
  onSkipAll: () => void;
  tooltipPosition?: "top" | "bottom" | "left" | "right" | "auto";
  /** If true, automatically skip to next step when target element is not found */
  autoSkipIfMissing?: boolean;
  /** Custom padding between tooltip and target element */
  tooltipPadding?: number;
  /** Spotlight configuration for padding and border-radius */
  spotlightConfig?: SpotlightConfig;
}

interface Position {
  top: number;
  left: number;
}

interface TargetRect {
  top: number;
  left: number;
  width: number;
  height: number;
}

interface SpotlightRect extends TargetRect {
  borderRadius: number;
}

/**
 * Detect the border-radius of an element from its computed styles
 */
function detectBorderRadius(element: Element): number {
  const style = window.getComputedStyle(element);
  // Get all corner radii
  const radii = [
    parseFloat(style.borderTopLeftRadius) || 0,
    parseFloat(style.borderTopRightRadius) || 0,
    parseFloat(style.borderBottomRightRadius) || 0,
    parseFloat(style.borderBottomLeftRadius) || 0,
  ];
  // Use the average of non-zero values, or 0 if all are 0
  const nonZero = radii.filter(r => r > 0);
  if (nonZero.length === 0) return 0;
  return Math.round(nonZero.reduce((a, b) => a + b, 0) / nonZero.length);
}

// Height reserved for the OnboardingNavBar at the bottom of the screen
const NAV_BAR_CLEARANCE = 80;

function calculateTooltipPosition(
  rect: TargetRect,
  position: "top" | "bottom" | "left" | "right" | "auto",
  tooltipWidth: number = 320,
  tooltipHeight: number = 200,
  padding: number = 16
): Position & { actualPosition: "top" | "bottom" | "left" | "right" } {
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  // Effective viewport height accounting for nav bar
  const effectiveViewportHeight = viewportHeight - NAV_BAR_CLEARANCE;

  // Auto-detect best position
  let actualPosition: "top" | "bottom" | "left" | "right" = position === "auto" ? "bottom" : position;
  if (position === "auto") {
    const spaceAbove = rect.top;
    const spaceBelow = effectiveViewportHeight - (rect.top + rect.height);
    const spaceRight = viewportWidth - (rect.left + rect.width);

    // Prefer below, then above, then right, then left
    if (spaceBelow >= tooltipHeight + padding) {
      actualPosition = "bottom";
    } else if (spaceAbove >= tooltipHeight + padding) {
      actualPosition = "top";
    } else if (spaceRight >= tooltipWidth + padding) {
      actualPosition = "right";
    } else {
      actualPosition = "left";
    }
  }

  let top = 0;
  let left = 0;

  switch (actualPosition) {
    case "top":
      top = rect.top - tooltipHeight - padding;
      left = rect.left + rect.width / 2 - tooltipWidth / 2;
      break;
    case "bottom":
      top = rect.top + rect.height + padding;
      left = rect.left + rect.width / 2 - tooltipWidth / 2;
      break;
    case "left":
      top = rect.top + rect.height / 2 - tooltipHeight / 2;
      left = rect.left - tooltipWidth - padding;
      break;
    case "right":
      top = rect.top + rect.height / 2 - tooltipHeight / 2;
      left = rect.left + rect.width + padding;
      break;
  }

  // Clamp to viewport, reserving space at bottom for nav bar
  left = Math.max(padding, Math.min(left, viewportWidth - tooltipWidth - padding));
  top = Math.max(padding, Math.min(top, effectiveViewportHeight - tooltipHeight - padding));

  return { top, left, actualPosition };
}

export function OnboardingSpotlight({
  targetSelector,
  title,
  description,
  stepNumber: _stepNumber,
  totalSteps: _totalSteps,
  onNext,
  onBack,
  onSkipAll,
  tooltipPosition = "auto",
  autoSkipIfMissing = false,
  tooltipPadding = 16,
  spotlightConfig,
}: OnboardingSpotlightProps) {
  // Note: stepNumber and totalSteps are passed for interface compatibility
  // but navigation is handled by the floating OnboardingNavBar
  void _stepNumber;
  void _totalSteps;
  const [spotlightRect, setSpotlightRect] = useState<SpotlightRect | null>(null);
  // Initialize from last position for smooth tooltip transition between steps
  const [tooltipPos, setTooltipPos] = useState<Position & { actualPosition: string }>(() => ({
    top: lastTooltipPosition?.top ?? window.innerHeight / 2,
    left: lastTooltipPosition?.left ?? window.innerWidth / 2,
    actualPosition: "bottom",
  }));
  // Start visible so tooltip can animate from last position
  const [isVisible, setIsVisible] = useState(true);
  // Highlight fades in separately from tooltip
  const [highlightVisible, setHighlightVisible] = useState(false);

  // Extract spotlight config with defaults
  const spotlightPadding = spotlightConfig?.padding ?? 8;
  const shouldDetectBorderRadius = spotlightConfig?.detectBorderRadius !== false;

  // Save position on unmount for next step's smooth transition
  useEffect(() => {
    return () => {
      lastTooltipPosition = { top: tooltipPos.top, left: tooltipPos.left };
    };
  }, [tooltipPos]);

  const updatePosition = useCallback(() => {
    const target = document.querySelector(targetSelector);
    if (!target) {
      console.warn(`[OnboardingSpotlight] Target not found: ${targetSelector}`);
      setSpotlightRect(null);
      setHighlightVisible(false);
      return;
    }

    const rect = target.getBoundingClientRect();
    
    // Determine border radius
    let borderRadius = 0;
    if (spotlightConfig?.borderRadius === 'none') {
      borderRadius = 0;
    } else if (typeof spotlightConfig?.borderRadius === 'number') {
      borderRadius = spotlightConfig.borderRadius;
    } else if (shouldDetectBorderRadius) {
      borderRadius = detectBorderRadius(target);
    }

    const newRect: SpotlightRect = {
      top: rect.top,
      left: rect.left,
      width: rect.width,
      height: rect.height,
      borderRadius,
    };
    setSpotlightRect(newRect);
    setTooltipPos(calculateTooltipPosition(newRect, tooltipPosition, 320, 200, tooltipPadding));
    // Fade in the highlight after a brief delay
    setTimeout(() => setHighlightVisible(true), 50);
  }, [targetSelector, tooltipPosition, tooltipPadding, spotlightConfig, shouldDetectBorderRadius]);

  useEffect(() => {
    // Initial position update with retry logic for elements that appear after navigation
    const attemptUpdate = () => {
      const target = document.querySelector(targetSelector);
      if (target) {
        updatePosition();
        setIsVisible(true);
        return true;
      }
      return false;
    };

    let retryInterval: ReturnType<typeof setInterval> | null = null;
    let settleTimeout: ReturnType<typeof setTimeout> | null = null;

    // Reset highlight visibility for new target
    setHighlightVisible(false);

    // Try immediately
    if (!attemptUpdate()) {
      // If not found, retry a few times with delays (for navigation transitions)
      let attempts = 0;
      const maxAttempts = 20; // More attempts for slower transitions
      retryInterval = setInterval(() => {
        attempts++;
        if (attemptUpdate() || attempts >= maxAttempts) {
          if (retryInterval) clearInterval(retryInterval);
          retryInterval = null;
          if (attempts >= maxAttempts) {
            console.warn(`[OnboardingSpotlight] Target not found after ${maxAttempts} attempts: ${targetSelector}`);
            if (autoSkipIfMissing) {
              // Auto-skip to next step if target not found
              console.info(`[OnboardingSpotlight] Auto-skipping step due to missing target`);
              onNext();
            } else {
              setIsVisible(true); // Show overlay so user can use keyboard to skip
            }
          }
        }
      }, 150); // Faster retries
    } else {
      // Element found immediately - but it might still be animating/settling
      // Re-calculate position after CSS transitions complete (typically 200-300ms)
      settleTimeout = setTimeout(() => {
        updatePosition();
        setIsVisible(true);
      }, 300);
    }

    // Reposition on scroll/resize
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);

    return () => {
      if (retryInterval) clearInterval(retryInterval);
      if (settleTimeout) clearTimeout(settleTimeout);
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  }, [updatePosition, targetSelector, autoSkipIfMissing, onNext]);

  // Handle keyboard navigation
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        e.preventDefault();
        onSkipAll();
      } else if (e.key === "Enter" || e.key === "ArrowRight") {
        e.stopPropagation();
        e.preventDefault();
        onNext();
      } else if (e.key === "ArrowLeft" && onBack) {
        e.stopPropagation();
        e.preventDefault();
        onBack();
      }
    };

    // Use capture phase to intercept before other handlers
    document.addEventListener("keydown", handleKeyDown, true);
    return () => document.removeEventListener("keydown", handleKeyDown, true);
  }, [onNext, onBack, onSkipAll]);

  // Calculate the spotlight cutout dimensions (element + padding)
  // For elements at viewport edges, use exact bounds with no padding
  const cutoutRect = spotlightRect ? (() => {
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    
    // Check if element touches viewport edges (within 10px tolerance)
    const touchesLeft = spotlightRect.left <= 10;
    const touchesTop = spotlightRect.top <= 10;
    const touchesRight = spotlightRect.left + spotlightRect.width >= vw - 10;
    const touchesBottom = spotlightRect.top + spotlightRect.height >= vh - 10;
    
    // Count how many edges are touched
    const edgeCount = [touchesLeft, touchesTop, touchesRight, touchesBottom].filter(Boolean).length;
    
    // For edge elements, use exact bounds; otherwise add padding
    const effectivePadding = edgeCount > 0 ? 0 : spotlightPadding;
    
    // Calculate bounds - for edge elements, inset slightly so border is visible
    const borderInset = edgeCount > 0 ? 4 : 0;
    const x = Math.max(borderInset, spotlightRect.left - effectivePadding);
    const y = Math.max(borderInset, spotlightRect.top - effectivePadding);
    const width = Math.min(spotlightRect.width + effectivePadding * 2, vw - x - borderInset);
    const height = Math.min(spotlightRect.height + effectivePadding * 2, vh - y - borderInset);
    
    // No border radius for edge elements
    const rx = edgeCount > 0 ? 0 : spotlightRect.borderRadius + (effectivePadding > 0 ? Math.min(effectivePadding, 4) : 0);
    
    // Flag to indicate if this is a full-viewport element (touches 3+ edges)
    const isFullViewport = edgeCount >= 3;
    
    return { x, y, width, height, rx, isFullViewport };
  })() : null;

  const spotlightContent = (
    <div
      className={cn(
        "fixed inset-0 z-[100] transition-opacity duration-300 overflow-hidden",
        isVisible ? "opacity-100" : "opacity-0"
      )}
    >
      {/* Semi-transparent overlay with cutout */}
      <svg className="absolute inset-0 w-full h-full pointer-events-none">
        <defs>
          <mask id="spotlight-mask">
            <rect x="0" y="0" width="100%" height="100%" fill="white" />
            {cutoutRect && (
              <rect
                x={cutoutRect.x}
                y={cutoutRect.y}
                width={cutoutRect.width}
                height={cutoutRect.height}
                rx={cutoutRect.rx}
                fill="black"
              />
            )}
          </mask>
        </defs>
        <rect
          x="0"
          y="0"
          width="100%"
          height="100%"
          fill="rgba(0, 0, 0, 0.75)"
          mask="url(#spotlight-mask)"
        />
      </svg>

      {/* Overlay area (blocks interaction with app during spotlight) */}
      <div className="absolute inset-0" />

      {/* Highlight border around target - uses inset box-shadow to avoid overflow issues */}
      {cutoutRect && (
        <div
          className={cn(
            "absolute pointer-events-none transition-opacity duration-300",
            highlightVisible ? "opacity-100" : "opacity-0"
          )}
          style={{
            top: cutoutRect.y,
            left: cutoutRect.x,
            width: cutoutRect.width,
            height: cutoutRect.height,
            borderRadius: cutoutRect.rx,
            // Use inset box-shadow for the border to avoid overflow
            boxShadow: `inset 0 0 0 2px hsl(var(--primary)), 0 0 0 4px hsl(var(--primary) / 0.25), 0 0 30px hsl(var(--primary) / 0.2)`,
          }}
        />
      )}

      {/* Tooltip content - shows and animates from last position */}
      <div
        className={cn(
          "absolute bg-zinc-900 border border-zinc-700/60 rounded-xl shadow-[0_8px_32px_rgba(0,0,0,0.5)]",
          "w-80 transition-all duration-500 ease-out",
          isVisible ? "opacity-100 scale-100" : "opacity-0 scale-95"
        )}
        style={{ top: tooltipPos.top, left: tooltipPos.left }}
      >
        <div className="p-5">
          {/* Title & Description */}
          <h3 className="text-lg font-semibold text-white mb-2.5 tracking-tight">{title}</h3>
          <div className="text-sm text-zinc-300 leading-relaxed">{description}</div>
        </div>
      </div>
    </div>
  );

  return createPortal(spotlightContent, document.body);
}
