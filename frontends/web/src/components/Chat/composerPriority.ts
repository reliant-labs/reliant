import { useLayoutEffect, useState, type RefObject } from "react";

/**
 * How many of the composer's inline controls fit, in priority order
 * (design doc §5.5, "priority+").
 *
 * Strictly a prefix: once one control does not fit, every control after it
 * folds too, so the row never shows a low-priority control while hiding a
 * higher one. Zero-width items (a control that renders nothing right now,
 * like the account chip under 80%) take no space and no gap.
 */
export function foldCount(available: number, widths: number[], gap: number): number {
  let used = 0;
  let placed = 0;
  for (let i = 0; i < widths.length; i++) {
    const w = widths[i];
    if (w === 0) continue;
    const next = used + (placed > 0 ? gap : 0) + w;
    if (next > available) return i;
    used = next;
    placed++;
  }
  return widths.length;
}

export interface PriorityFold {
  /** How many leading items fit inline. */
  visible: number;
  /** Whether the row is laid out at all (the compact layout hides it). */
  shown: boolean;
}

/**
 * Measures the row at `containerRef` and its `[data-fold-item]` children, and
 * returns how many leading items fit (see `foldCount`).
 *
 * Which LAYOUT the composer uses is a container query in CSS; this answers a
 * different question no query can: whether these particular controls, with
 * their current labels, fit in the space that layout leaves them. Folded items
 * stay mounted (invisible and out of flow, see chat-composer.css) so their
 * state survives and they can still be measured when the row widens.
 */
export function usePriorityFold(
  containerRef: RefObject<HTMLElement | null>,
  /** Identifies the item set; a change re-measures and re-observes. */
  itemsKey: string,
): PriorityFold {
  // Until measured, assume everything fits: the first layout effect corrects
  // it before paint.
  const [fold, setFold] = useState<PriorityFold>({ visible: Number.MAX_SAFE_INTEGER, shown: true });

  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const measure = () => {
      const items = Array.from(container.querySelectorAll<HTMLElement>(":scope > [data-fold-item]"));
      const style = getComputedStyle(container);
      const next: PriorityFold =
        style.display === "none"
          ? // Hidden by the compact layout: nothing is inline, and there is no
            // room to measure against until it reappears (the observer fires).
            { visible: 0, shown: false }
          : {
              visible: foldCount(container.clientWidth, items.map((item) => item.offsetWidth), parseFloat(style.columnGap) || 0),
              shown: true,
            };
      setFold((prev) => (prev.visible === next.visible && prev.shown === next.shown ? prev : next));
    };

    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    for (const item of Array.from(container.querySelectorAll<HTMLElement>(":scope > [data-fold-item]"))) {
      observer.observe(item);
    }
    return () => observer.disconnect();
  }, [containerRef, itemsKey]);

  return fold;
}
