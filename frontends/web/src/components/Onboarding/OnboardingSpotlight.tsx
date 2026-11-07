/**
 * Onboarding Spotlight
 *
 * An overlay component that highlights UI elements during the onboarding tour.
 * Uses SVG masks to create a "cutout" effect around the target element.
 */

import { useEffect, useState, useCallback, useRef } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/utils";
import { useUnmountSafeTimeout } from "../../hooks/useUnmountSafeTimeout";
import { BELOW_FIRST, BESIDE_FIRST, placeCallout, type CalloutSide } from "./calloutPlacement";
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
  /**
   * Enter / → advance and ← goes back. Off while a step waits on a required
   * action: the user is typing into the app, and Enter must reach it.
   */
  keyboardNav?: boolean;
  /**
   * Block pointer clicks outside the target (a required action is pending).
   * Only the dimmed area blocks: the target stays clickable, the nav bar sits
   * above, and keyboard focus and screen readers are untouched. Nothing is
   * blocked while the target can't be found, so a missing anchor never traps
   * the user.
   */
  blockOutside?: boolean;
  /**
   * The compact presentation (Journey.controls "callout"): the step's own
   * controls inside the card (`footer`), the pending action's instruction
   * (`hint`), no bottom bar to keep clear of, and the card beside the target
   * rather than below it.
   */
  callout?: { footer: React.ReactNode; hint?: React.ReactNode };
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

interface ElementSize {
  width: number;
  height: number;
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

const DEFAULT_TOOLTIP_SIZE: ElementSize = { width: 320, height: 200 };
const VIEWPORT_PADDING = 16;
// Height reserved for the OnboardingNavBar at the bottom of the screen
const NAV_BAR_CLEARANCE = 176;

/** Where the card goes: beside the target, never over it (calloutPlacement.ts). */
function calculateTooltipPosition(
  rect: TargetRect,
  position: "top" | "bottom" | "left" | "right" | "auto",
  tooltipSize: ElementSize = DEFAULT_TOOLTIP_SIZE,
  targetGap: number = 16,
  bottomReserve: number = NAV_BAR_CLEARANCE,
  order: readonly CalloutSide[] = BELOW_FIRST
): Position & { actualPosition: CalloutSide } {
  const placed = placeCallout(
    rect,
    tooltipSize,
    { width: window.innerWidth, height: window.innerHeight },
    { preferred: position, order, gap: targetGap, margin: VIEWPORT_PADDING, bottomReserve }
  );
  return { top: placed.top, left: placed.left, actualPosition: placed.side };
}

/** Below this width a callout is a bottom sheet (tailwind `sm`). */
export const SHEET_MAX_WIDTH = 640;
/** The sheet's tallest, as a share of the viewport: the target keeps the rest. */
const SHEET_MAX_VH = 45;

export function isSheetViewport(): boolean {
  return typeof window !== "undefined" && window.innerWidth < SHEET_MAX_WIDTH;
}

/** Scroll `target` into the part of the screen the bottom sheet leaves free. */
function scrollAboveSheet(target: Element): void {
  const rect = target.getBoundingClientRect();
  const free = window.innerHeight * (1 - SHEET_MAX_VH / 100);
  if (rect.top >= 8 && rect.bottom <= free) return;
  try {
    target.scrollIntoView({ block: "start", behavior: "auto" });
  } catch {
    // jsdom and old engines: the sheet still shows.
  }
}

export function OnboardingSpotlight({
  targetSelector,
  title,
  description,
  stepNumber: _stepNumber,
  totalSteps: _totalSteps,
  onNext,
  onBack,
  onSkipAll: _onSkipAll,
  tooltipPosition = "auto",
  autoSkipIfMissing = false,
  tooltipPadding = 16,
  spotlightConfig,
  keyboardNav = true,
  blockOutside = false,
  callout,
}: OnboardingSpotlightProps) {
  const compact = !!callout;
  // A phone: the card is a bottom sheet, and the target is scrolled into the space above it.
  const [sheet, setSheet] = useState(() => compact && isSheetViewport());
  useEffect(() => {
    if (!compact) return;
    const onResize = () => setSheet(isSheetViewport());
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [compact]);
  // stepNumber/totalSteps are accepted for interface compatibility — the
  // OnboardingNavBar renders progress. onSkipAll is accepted but unused:
  // ESC no longer ends the tour; skipping happens via the nav bar button.
  void _stepNumber;
  void _totalSteps;
  void _onSkipAll;
  const [spotlightRect, setSpotlightRect] = useState<SpotlightRect | null>(null);
  const tooltipRef = useRef<HTMLDivElement | null>(null);
  const [tooltipSize, setTooltipSize] = useState<ElementSize>(DEFAULT_TOOLTIP_SIZE);
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
  // A blocked click pulses the highlight, pointing at what to click instead.
  const [nudge, setNudge] = useState(0);
  // updatePosition also runs from resize/scroll listeners, so its highlight
  // fade-in timer is not owned by any one effect: tie it to the component.
  const scheduleTimeout = useUnmountSafeTimeout();

  // Extract spotlight config with defaults
  const spotlightPadding = spotlightConfig?.padding ?? 8;
  const shouldDetectBorderRadius = spotlightConfig?.detectBorderRadius !== false;

  // Save position on unmount for next step's smooth transition
  useEffect(() => {
    return () => {
      lastTooltipPosition = { top: tooltipPos.top, left: tooltipPos.left };
    };
  }, [tooltipPos]);

  const measureTooltip = useCallback(() => {
    const tooltip = tooltipRef.current;
    if (!tooltip) return;

    const rect = tooltip.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;

    const nextSize = {
      width: Math.ceil(rect.width),
      height: Math.ceil(rect.height),
    };

    setTooltipSize((currentSize) => {
      if (
        Math.abs(currentSize.width - nextSize.width) < 1 &&
        Math.abs(currentSize.height - nextSize.height) < 1
      ) {
        return currentSize;
      }
      return nextSize;
    });
  }, []);

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
    // Fade in the highlight after a brief delay
    scheduleTimeout(() => setHighlightVisible(true), 50);
  }, [targetSelector, spotlightConfig, shouldDetectBorderRadius, scheduleTimeout]);

  useEffect(() => {
    if (!spotlightRect) return;
    setTooltipPos(
      compact
        ? calculateTooltipPosition(spotlightRect, tooltipPosition, tooltipSize, tooltipPadding, 0, BESIDE_FIRST)
        : calculateTooltipPosition(spotlightRect, tooltipPosition, tooltipSize, tooltipPadding)
    );
  }, [spotlightRect, tooltipPosition, tooltipSize, tooltipPadding, compact]);

  useEffect(() => {
    const frame = requestAnimationFrame(measureTooltip);
    return () => cancelAnimationFrame(frame);
  });

  useEffect(() => {
    // Initial position update with retry logic for elements that appear after navigation
    const attemptUpdate = () => {
      const target = document.querySelector(targetSelector);
      if (target) {
        if (sheet) scrollAboveSheet(target);
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
  }, [updatePosition, targetSelector, autoSkipIfMissing, onNext, sheet]);

  // Handle keyboard navigation.
  // ESC intentionally does nothing — it must not end the tour. The user can
  // skip explicitly via the "Skip tour" button in OnboardingNavBar.
  useEffect(() => {
    if (!keyboardNav) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Enter" || e.key === "ArrowRight") {
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
  }, [onNext, onBack, keyboardNav]);

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
        "fixed inset-0 z-[100] transition-opacity duration-300 overflow-hidden pointer-events-none",
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

      {/* Visual dim layer — does NOT block clicks. The tour state lives in the
       * URL (?tour=<step-id>); if the user clicks a non-spotlighted element
       * during the tour, the wizard simply re-renders against the new page.
       * Blocking clicks here was the source of dead-button bugs (e.g. Back
       * to app on /workflow). */}
      <div className="absolute inset-0 pointer-events-none" />

      {/* Click blockers — only while a required action is pending (see
       * blockOutside), and only around a target that was found. Four panes
       * around the cutout, so the target itself stays clickable. */}
      {blockOutside && cutoutRect && (
        <>
          {[
            { top: 0, left: 0, width: "100%", height: cutoutRect.y },
            { top: cutoutRect.y + cutoutRect.height, left: 0, width: "100%", bottom: 0 },
            { top: cutoutRect.y, left: 0, width: cutoutRect.x, height: cutoutRect.height },
            { top: cutoutRect.y, left: cutoutRect.x + cutoutRect.width, right: 0, height: cutoutRect.height },
          ].map((style, i) => (
            <div
              key={i}
              aria-hidden="true"
              data-testid="onboarding-click-blocker"
              className="absolute pointer-events-auto"
              style={style}
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                setNudge((n) => n + 1);
              }}
            />
          ))}
        </>
      )}

      {/* Highlight border around target - uses inset box-shadow to avoid overflow issues */}
      {cutoutRect && (
        <div
          key={nudge}
          className={cn(
            "absolute pointer-events-none transition-opacity duration-300",
            highlightVisible ? "opacity-100" : "opacity-0",
            nudge > 0 && "animate-pulse"
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
        ref={tooltipRef}
        role="status"
        aria-live="polite"
        className={cn(
          "absolute rounded-xl border border-border bg-popover text-popover-foreground shadow-2xl",
          "transition-all duration-500 ease-out motion-reduce:transition-none",
          isVisible ? "opacity-100 scale-100" : "opacity-0 scale-95",
          compact && "pointer-events-auto"
        )}
        data-testid="tour-callout"
        data-side={sheet ? "sheet" : tooltipPos.actualPosition}
        style={
          sheet
            ? { left: 0, right: 0, bottom: 0, top: "auto", width: "100%", maxHeight: `${SHEET_MAX_VH}vh`, overflowY: "auto", borderBottomLeftRadius: 0, borderBottomRightRadius: 0 }
            : { top: tooltipPos.top, left: tooltipPos.left, width: "min(20rem, calc(100vw - 2rem))" }
        }
      >
        <div className={sheet ? "p-4 pb-[max(1rem,env(safe-area-inset-bottom))]" : "p-5"}>
          {/* Title & Description */}
          <h3 className="text-lg font-semibold text-foreground mb-2.5 tracking-tight">{title}</h3>
          <div className="text-sm text-muted-foreground leading-relaxed">{description}</div>
          {callout?.hint}
          {callout?.footer}
        </div>
      </div>
    </div>
  );

  return createPortal(spotlightContent, document.body);
}