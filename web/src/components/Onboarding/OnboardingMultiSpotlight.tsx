/**
 * Onboarding Multi-Spotlight
 *
 * An overlay component that highlights multiple UI elements simultaneously
 * during the onboarding tour. Each target gets its own SVG mask cutout,
 * highlight border, and compact label tooltip.
 */

import { useEffect, useState, useCallback, useRef } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/utils";
import type { SpotlightConfig, SpotlightTarget } from "./types";

interface OnboardingMultiSpotlightProps {
  targets: SpotlightTarget[];
  stepNumber: number;
  totalSteps: number;
  onNext: () => void;
  onBack?: () => void;
  onSkipAll: () => void;
}

interface TargetRect {
  top: number;
  left: number;
  width: number;
  height: number;
}

interface CutoutRect {
  x: number;
  y: number;
  width: number;
  height: number;
  rx: number;
}

interface ElementSize {
  width: number;
  height: number;
}

interface ResolvedTarget {
  target: SpotlightTarget;
  elementRect: TargetRect;
  cutout: CutoutRect;
}

const DEFAULT_LABEL_SIZE: ElementSize = { width: 192, height: 56 };
const LABEL_GAP = 14;
const VIEWPORT_PADDING = 16;
const NAV_BAR_CLEARANCE = 176;

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(value, Math.max(min, max)));
}

/**
 * Detect the border-radius of an element from its computed styles.
 */
function detectBorderRadius(element: Element): number {
  const style = window.getComputedStyle(element);
  const radii = [
    parseFloat(style.borderTopLeftRadius) || 0,
    parseFloat(style.borderTopRightRadius) || 0,
    parseFloat(style.borderBottomRightRadius) || 0,
    parseFloat(style.borderBottomLeftRadius) || 0,
  ];
  const nonZero = radii.filter((r) => r > 0);
  if (nonZero.length === 0) return 0;
  return Math.round(nonZero.reduce((a, b) => a + b, 0) / nonZero.length);
}

/**
 * Compute cutout rect for a target element, handling viewport edge detection.
 */
function computeCutout(
  rect: TargetRect,
  borderRadius: number,
  spotlightConfig?: SpotlightConfig
): CutoutRect {
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const spotlightPadding = spotlightConfig?.padding ?? 8;

  const touchesLeft = rect.left <= 10;
  const touchesTop = rect.top <= 10;
  const touchesRight = rect.left + rect.width >= vw - 10;
  const touchesBottom = rect.top + rect.height >= vh - 10;

  const edgeCount = [touchesLeft, touchesTop, touchesRight, touchesBottom].filter(Boolean).length;
  const effectivePadding = edgeCount > 0 ? 0 : spotlightPadding;
  const borderInset = edgeCount > 0 ? 4 : 0;

  const x = Math.max(borderInset, rect.left - effectivePadding);
  const y = Math.max(borderInset, rect.top - effectivePadding);
  const width = Math.min(rect.width + effectivePadding * 2, vw - x - borderInset);
  const height = Math.min(rect.height + effectivePadding * 2, vh - y - borderInset);
  const rx =
    edgeCount > 0
      ? 0
      : borderRadius + (effectivePadding > 0 ? Math.min(effectivePadding, 4) : 0);

  return { x, y, width, height, rx };
}

/**
 * Determine which side to place a label based on available space around the target.
 */
function determineLabelPosition(
  cutout: CutoutRect,
  labelSize: ElementSize = DEFAULT_LABEL_SIZE
): "right" | "left" | "above" | "below" {
  const viewportWidth = window.innerWidth;
  const effectiveViewportBottom = Math.max(
    VIEWPORT_PADDING,
    window.innerHeight - NAV_BAR_CLEARANCE
  );
  const spaces = {
    right: viewportWidth - (cutout.x + cutout.width) - VIEWPORT_PADDING,
    left: cutout.x - VIEWPORT_PADDING,
    above: cutout.y - VIEWPORT_PADDING,
    below: effectiveViewportBottom - (cutout.y + cutout.height),
  };

  if (spaces.right >= labelSize.width + LABEL_GAP) return "right";
  if (spaces.left >= labelSize.width + LABEL_GAP) return "left";
  if (spaces.above >= labelSize.height + LABEL_GAP) return "above";
  if (spaces.below >= labelSize.height + LABEL_GAP) return "below";

  return Object.entries(spaces).reduce((best, [position, space]) =>
    space > best.space ? { position: position as keyof typeof spaces, space } : best
  , { position: "above" as keyof typeof spaces, space: -Infinity }).position;
}

/**
 * Calculate absolute CSS position for a label relative to its cutout.
 */
function calculateLabelStyle(
  cutout: CutoutRect,
  position: "right" | "left" | "above" | "below",
  labelSize: ElementSize = DEFAULT_LABEL_SIZE
): React.CSSProperties {
  const viewportWidth = window.innerWidth;
  const effectiveViewportBottom = Math.max(
    VIEWPORT_PADDING,
    window.innerHeight - NAV_BAR_CLEARANCE
  );
  const labelWidth = labelSize.width;
  const labelHeight = labelSize.height;
  let top = 0;
  let left = 0;

  switch (position) {
    case "right":
      top = cutout.y + cutout.height / 2 - labelHeight / 2;
      left = cutout.x + cutout.width + LABEL_GAP;
      break;
    case "left":
      top = cutout.y + cutout.height / 2 - labelHeight / 2;
      left = cutout.x - labelWidth - LABEL_GAP;
      break;
    case "above":
      top = cutout.y - labelHeight - LABEL_GAP;
      left = cutout.x + cutout.width / 2 - labelWidth / 2;
      break;
    case "below":
      top = cutout.y + cutout.height + LABEL_GAP;
      left = cutout.x + cutout.width / 2 - labelWidth / 2;
      break;
  }

  return {
    top: clamp(top, VIEWPORT_PADDING, effectiveViewportBottom - labelHeight - VIEWPORT_PADDING),
    left: clamp(left, VIEWPORT_PADDING, viewportWidth - labelWidth - VIEWPORT_PADDING),
  };
}

export function OnboardingMultiSpotlight({
  targets,
  stepNumber: _stepNumber,
  totalSteps: _totalSteps,
  onNext,
  onBack,
  onSkipAll,
}: OnboardingMultiSpotlightProps) {
  void _stepNumber;
  void _totalSteps;

  const [resolved, setResolved] = useState<ResolvedTarget[]>([]);
  const [isVisible, setIsVisible] = useState(false);
  const [highlightVisible, setHighlightVisible] = useState(false);
  const [labelSizes, setLabelSizes] = useState<Record<number, ElementSize>>({});
  const labelRefs = useRef<Map<number, HTMLDivElement>>(new Map());

  // Unique mask ID to avoid collisions if multiple instances exist
  const maskId = useRef(`multi-spotlight-mask-${Math.random().toString(36).slice(2, 8)}`);

  const updatePositions = useCallback(() => {
    const results: ResolvedTarget[] = [];

    for (const target of targets) {
      const el = document.querySelector(target.selector);
      if (!el) continue;

      const domRect = el.getBoundingClientRect();
      const elementRect: TargetRect = {
        top: domRect.top,
        left: domRect.left,
        width: domRect.width,
        height: domRect.height,
      };

      const shouldDetect = target.spotlightConfig?.detectBorderRadius !== false;
      let borderRadius = 0;
      if (target.spotlightConfig?.borderRadius === "none") {
        borderRadius = 0;
      } else if (typeof target.spotlightConfig?.borderRadius === "number") {
        borderRadius = target.spotlightConfig.borderRadius;
      } else if (shouldDetect) {
        borderRadius = detectBorderRadius(el);
      }

      const cutout = computeCutout(elementRect, borderRadius, target.spotlightConfig);

      results.push({ target, elementRect, cutout });
    }

    setResolved(results);
  }, [targets]);

  // Retry logic for elements that appear after navigation
  useEffect(() => {
    let retryInterval: ReturnType<typeof setInterval> | null = null;
    let settleTimeout: ReturnType<typeof setTimeout> | null = null;

    setHighlightVisible(false);
    setIsVisible(false);

    const attemptUpdate = (): boolean => {
      // Check if at least one target is found
      const anyFound = targets.some((t) => document.querySelector(t.selector));
      if (anyFound) {
        updatePositions();
        return true;
      }
      return false;
    };

    if (!attemptUpdate()) {
      let attempts = 0;
      const maxAttempts = 20;
      retryInterval = setInterval(() => {
        attempts++;
        if (attemptUpdate() || attempts >= maxAttempts) {
          if (retryInterval) clearInterval(retryInterval);
          retryInterval = null;
          setIsVisible(true);
          setTimeout(() => setHighlightVisible(true), 50);
          if (attempts >= maxAttempts) {
            console.warn(
              `[OnboardingMultiSpotlight] Some targets not found after ${maxAttempts} attempts`
            );
          }
        }
      }, 150);
    } else {
      // Found immediately — allow CSS transitions to settle
      settleTimeout = setTimeout(() => {
        updatePositions();
        setIsVisible(true);
        setTimeout(() => setHighlightVisible(true), 50);
      }, 300);
    }

    window.addEventListener("resize", updatePositions);
    window.addEventListener("scroll", updatePositions, true);

    return () => {
      if (retryInterval) clearInterval(retryInterval);
      if (settleTimeout) clearTimeout(settleTimeout);
      window.removeEventListener("resize", updatePositions);
      window.removeEventListener("scroll", updatePositions, true);
    };
  }, [updatePositions, targets]);

  // Recalculate label positions after first render so we have real dimensions.
  useEffect(() => {
    if (resolved.length === 0 || !isVisible) return;

    const frame = requestAnimationFrame(() => {
      const measuredSizes: Record<number, ElementSize> = {};

      labelRefs.current.forEach((label, index) => {
        const rect = label.getBoundingClientRect();
        if (rect.width <= 0 || rect.height <= 0) return;

        measuredSizes[index] = {
          width: Math.ceil(rect.width),
          height: Math.ceil(rect.height),
        };
      });

      setLabelSizes((currentSizes) => {
        let changed = false;
        for (const [index, measuredSize] of Object.entries(measuredSizes)) {
          const currentSize = currentSizes[Number(index)];
          if (
            !currentSize ||
            Math.abs(currentSize.width - measuredSize.width) >= 1 ||
            Math.abs(currentSize.height - measuredSize.height) >= 1
          ) {
            changed = true;
            break;
          }
        }

        return changed ? measuredSizes : currentSizes;
      });
    });

    return () => cancelAnimationFrame(frame);
  }, [resolved.length, isVisible]);

  // Keyboard navigation
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

    document.addEventListener("keydown", handleKeyDown, true);
    return () => document.removeEventListener("keydown", handleKeyDown, true);
  }, [onNext, onBack, onSkipAll]);

  const content = (
    <div
      className={cn(
        "fixed inset-0 z-[100] transition-opacity duration-300 overflow-hidden",
        isVisible ? "opacity-100" : "opacity-0"
      )}
    >
      {/* SVG overlay with multiple cutouts */}
      <svg className="absolute inset-0 w-full h-full pointer-events-none">
        <defs>
          <mask id={maskId.current}>
            <rect x="0" y="0" width="100%" height="100%" fill="white" />
            {resolved.map((r, i) => (
              <rect
                key={i}
                x={r.cutout.x}
                y={r.cutout.y}
                width={r.cutout.width}
                height={r.cutout.height}
                rx={r.cutout.rx}
                fill="black"
              />
            ))}
          </mask>
        </defs>
        <rect
          x="0"
          y="0"
          width="100%"
          height="100%"
          fill="rgba(0, 0, 0, 0.75)"
          mask={`url(#${maskId.current})`}
        />
      </svg>

      {/* Overlay to block interaction */}
      <div className="absolute inset-0" />

      {/* Highlight borders — one per target */}
      {resolved.map((r, i) => (
        <div
          key={`highlight-${i}`}
          className={cn(
            "absolute pointer-events-none transition-opacity duration-300",
            highlightVisible ? "opacity-100" : "opacity-0"
          )}
          style={{
            top: r.cutout.y,
            left: r.cutout.x,
            width: r.cutout.width,
            height: r.cutout.height,
            borderRadius: r.cutout.rx,
            boxShadow: `inset 0 0 0 2px hsl(var(--primary)), 0 0 0 4px hsl(var(--primary) / 0.25), 0 0 30px hsl(var(--primary) / 0.2)`,
          }}
        />
      ))}

      {/* Label tooltips — one per target */}
      {resolved.map((r, i) => {
        const labelPosition = determineLabelPosition(r.cutout, labelSizes[i]);

        return (
          <div
            key={`label-${i}`}
            ref={(el) => {
              if (el) {
                labelRefs.current.set(i, el);
              } else {
                labelRefs.current.delete(i);
              }
            }}
            className={cn(
              "absolute max-w-48 pointer-events-none rounded-lg border border-border bg-popover px-3.5 py-2.5 text-popover-foreground shadow-xl transition-opacity duration-300",
              highlightVisible ? "opacity-100" : "opacity-0"
            )}
            style={{
              ...calculateLabelStyle(r.cutout, labelPosition, labelSizes[i]),
              maxWidth: "min(12rem, calc(100vw - 2rem))",
            }}
          >
            <p className="text-sm font-semibold text-foreground">{r.target.label}</p>
            {r.target.description && (
              <p className="text-xs text-muted-foreground mt-0.5">{r.target.description}</p>
            )}
          </div>
        );
      })}
    </div>
  );

  return createPortal(content, document.body);
}