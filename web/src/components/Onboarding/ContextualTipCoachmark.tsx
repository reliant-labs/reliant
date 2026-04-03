import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/utils";

/** Delay before the coachmark fades in, giving transient state changes time to settle. */
const SHOW_DELAY_MS = 300;

interface ContextualTipCoachmarkProps {
  targetSelector: string;
  title: string;
  body: string;
  onDismiss: () => void;
  onDisableAll: () => void;
  /** Called once the coachmark has become visible to the user. */
  onConfirmShown: () => void;
  /** Called when the target element cannot be found or has zero dimensions. */
  onTargetMissing: () => void;
}

interface Rect {
  top: number;
  left: number;
  width: number;
  height: number;
  borderRadius: number;
}

function detectBorderRadius(element: Element): number {
  const style = window.getComputedStyle(element);
  const values = [
    parseFloat(style.borderTopLeftRadius) || 0,
    parseFloat(style.borderTopRightRadius) || 0,
    parseFloat(style.borderBottomRightRadius) || 0,
    parseFloat(style.borderBottomLeftRadius) || 0,
  ].filter((value) => value > 0);
  if (values.length === 0) return 0;
  return Math.round(values.reduce((sum, value) => sum + value, 0) / values.length);
}

export function ContextualTipCoachmark({
  targetSelector,
  title,
  body,
  onDismiss,
  onDisableAll,
  onConfirmShown,
  onTargetMissing,
}: ContextualTipCoachmarkProps) {
  const [targetRect, setTargetRect] = useState<Rect | null>(null);
  const [tooltipPosition, setTooltipPosition] = useState({ top: 24, left: 24 });
  const [isVisible, setIsVisible] = useState(false);
  const maskIdRef = useRef(`contextual-tip-mask-${Math.random().toString(36).slice(2, 8)}`);
  const showDelayRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hasConfirmedRef = useRef(false);
  // Keep stable references to callbacks to avoid re-triggering effects.
  const onConfirmShownRef = useRef(onConfirmShown);
  onConfirmShownRef.current = onConfirmShown;
  const onTargetMissingRef = useRef(onTargetMissing);
  onTargetMissingRef.current = onTargetMissing;

  const updatePosition = useCallback(() => {
    const target = document.querySelector(targetSelector);
    if (!target) {
      setTargetRect(null);
      setIsVisible(false);
      if (showDelayRef.current) {
        clearTimeout(showDelayRef.current);
        showDelayRef.current = null;
      }
      onTargetMissingRef.current();
      return;
    }

    const rect = target.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) {
      setTargetRect(null);
      setIsVisible(false);
      if (showDelayRef.current) {
        clearTimeout(showDelayRef.current);
        showDelayRef.current = null;
      }
      onTargetMissingRef.current();
      return;
    }

    const nextRect: Rect = {
      top: rect.top,
      left: rect.left,
      width: rect.width,
      height: rect.height,
      borderRadius: detectBorderRadius(target),
    };
    setTargetRect(nextRect);

    const tooltipWidth = 340;
    const tooltipHeight = 190;
    const padding = 16;
    const availableBelow = window.innerHeight - (rect.bottom + padding);
    const showAbove = availableBelow < tooltipHeight && rect.top > tooltipHeight + padding;
    const top = showAbove ? rect.top - tooltipHeight - padding : rect.bottom + padding;
    const left = Math.max(
      padding,
      Math.min(
        rect.left + rect.width / 2 - tooltipWidth / 2,
        window.innerWidth - tooltipWidth - padding,
      ),
    );
    setTooltipPosition({ top, left });
    // Position is ready — the show-delay timer (started in the effect)
    // will flip isVisible once enough time has passed.
  }, [targetSelector]);

  useEffect(() => {
    updatePosition();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    const settleTimeout = window.setTimeout(updatePosition, 250);

    // Delay making the coachmark visible so that transient activeTipId
    // changes (from reevaluate running against intermediate store state)
    // resolve before the user ever sees the overlay.
    showDelayRef.current = setTimeout(() => {
      showDelayRef.current = null;
      // Re-check the target is still present before showing.
      const target = document.querySelector(targetSelector);
      if (!target) {
        onTargetMissingRef.current();
        return;
      }
      const rect = target.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) {
        onTargetMissingRef.current();
        return;
      }
      setIsVisible(true);
      if (!hasConfirmedRef.current) {
        hasConfirmedRef.current = true;
        onConfirmShownRef.current();
      }
    }, SHOW_DELAY_MS);

    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
      window.clearTimeout(settleTimeout);
      if (showDelayRef.current) {
        clearTimeout(showDelayRef.current);
        showDelayRef.current = null;
      }
    };
  }, [updatePosition, targetSelector]);

  const cutoutRect = useMemo(() => {
    if (!targetRect) return null;
    const padding = 8;
    const x = Math.max(4, targetRect.left - padding);
    const y = Math.max(4, targetRect.top - padding);
    return {
      x,
      y,
      width: Math.min(targetRect.width + padding * 2, window.innerWidth - x - 4),
      height: Math.min(targetRect.height + padding * 2, window.innerHeight - y - 4),
      rx: targetRect.borderRadius + 4,
    };
  }, [targetRect]);

  if (!targetRect || !cutoutRect || !isVisible) {
    return null;
  }

  return createPortal(
    <div className="fixed inset-0 z-[110]">
      <svg className="absolute inset-0 h-full w-full pointer-events-none">
        <defs>
          <mask id={maskIdRef.current}>
            <rect x="0" y="0" width="100%" height="100%" fill="white" />
            <rect
              x={cutoutRect.x}
              y={cutoutRect.y}
              width={cutoutRect.width}
              height={cutoutRect.height}
              rx={cutoutRect.rx}
              fill="black"
            />
          </mask>
        </defs>
        <rect
          x="0"
          y="0"
          width="100%"
          height="100%"
          fill="rgba(0, 0, 0, 0.68)"
          mask={`url(#${maskIdRef.current})`}
        />
      </svg>

      <div
        className="absolute pointer-events-none"
        style={{
          top: cutoutRect.y,
          left: cutoutRect.x,
          width: cutoutRect.width,
          height: cutoutRect.height,
          borderRadius: cutoutRect.rx,
          boxShadow:
            "inset 0 0 0 2px hsl(var(--primary)), 0 0 0 4px hsl(var(--primary) / 0.2), 0 0 24px hsl(var(--primary) / 0.2)",
        }}
      />

      <div
        className={cn(
          "absolute w-[340px] rounded-xl border border-border bg-popover shadow-2xl transition-opacity duration-200",
          isVisible ? "opacity-100" : "opacity-0",
        )}
        style={{ top: tooltipPosition.top, left: tooltipPosition.left }}
      >
        <div className="p-4 space-y-3">
          <div className="space-y-1">
            <h3 className="text-sm font-semibold text-foreground">{title}</h3>
            <p className="text-sm text-muted-foreground leading-relaxed">{body}</p>
          </div>
          <div className="flex items-center justify-between gap-2 pt-1">
            <button
              type="button"
              onClick={onDisableAll}
              className="text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              Stop showing tips
            </button>
            <button
              type="button"
              onClick={onDismiss}
              className="inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:opacity-90 transition-opacity"
            >
              Dismiss
            </button>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}