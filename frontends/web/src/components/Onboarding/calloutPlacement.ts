/**
 * Where a tour callout goes so that it never covers the thing it points at.
 *
 * Tries each side in order (the preferred one first) and takes the first
 * where the callout fits between the target and the viewport's edge, wholly
 * on screen (a target scrolled below the fold has no room "above" it that is
 * not also below the fold). Along
 * the other axis it is centred on the target and then kept on screen, which
 * can't make it overlap: the side already separates them. Only when no side
 * fits (a target nearly as big as the screen) does it fall back to the
 * on-screen spot that covers the least of the target, and says so.
 *
 * Pure, so the "never overlaps" rule is a unit test (calloutPlacement.test.ts)
 * rather than a hope.
 */

export type CalloutSide = "top" | "bottom" | "left" | "right";

export interface Box {
  top: number;
  left: number;
  width: number;
  height: number;
}

export interface Size {
  width: number;
  height: number;
}

export interface CalloutPlacement {
  top: number;
  left: number;
  side: CalloutSide;
  /** No side had room: this is the least-covering spot, and it overlaps the target. */
  overlaps: boolean;
}

export interface PlaceOptions {
  /** A side to try first; "auto" uses `order` as is. */
  preferred?: CalloutSide | "auto";
  /** The sides in the order to try them. Default: beside the target first (right, left), then below, above. */
  order?: readonly CalloutSide[];
  /** Space between the callout and the target. */
  gap?: number;
  /** Space kept free at each viewport edge. */
  margin?: number;
  /** Space kept free at the bottom of the viewport beyond `margin` (a fixed bar). */
  bottomReserve?: number;
}

export const BESIDE_FIRST: readonly CalloutSide[] = ["right", "left", "bottom", "top"];
export const BELOW_FIRST: readonly CalloutSide[] = ["bottom", "top", "right", "left"];

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(value, Math.max(min, max)));
}

/** The area two boxes share. */
export function overlapArea(a: Box, b: Box): number {
  const w = Math.min(a.left + a.width, b.left + b.width) - Math.max(a.left, b.left);
  const h = Math.min(a.top + a.height, b.top + b.height) - Math.max(a.top, b.top);
  return w > 0 && h > 0 ? w * h : 0;
}

export function placeCallout(target: Box, size: Size, viewport: Size, options: PlaceOptions = {}): CalloutPlacement {
  const gap = options.gap ?? 16;
  const margin = options.margin ?? 16;
  const minTop = margin;
  const maxTop = viewport.height - (options.bottomReserve ?? 0) - margin - size.height;
  const minLeft = margin;
  const maxLeft = viewport.width - margin - size.width;
  const base = options.order ?? BESIDE_FIRST;
  const order = options.preferred && options.preferred !== "auto" ? [options.preferred, ...base.filter((s) => s !== options.preferred)] : base;

  const at = (side: CalloutSide): { top: number; left: number; fits: boolean } => {
    const centreTop = clamp(target.top + target.height / 2 - size.height / 2, minTop, maxTop);
    const centreLeft = clamp(target.left + target.width / 2 - size.width / 2, minLeft, maxLeft);
    switch (side) {
      case "right": {
        const left = target.left + target.width + gap;
        return { top: centreTop, left, fits: left >= minLeft && left <= maxLeft };
      }
      case "left": {
        const left = target.left - gap - size.width;
        return { top: centreTop, left, fits: left >= minLeft && left <= maxLeft };
      }
      case "bottom": {
        const top = target.top + target.height + gap;
        return { top, left: centreLeft, fits: top >= minTop && top <= maxTop };
      }
      case "top": {
        const top = target.top - gap - size.height;
        return { top, left: centreLeft, fits: top >= minTop && top <= maxTop };
      }
    }
  };

  for (const side of order) {
    const spot = at(side);
    if (spot.fits) return { top: spot.top, left: spot.left, side, overlaps: false };
  }

  // No side has room. Every side's spot pulled on screen, and the four
  // corners: whichever covers the least of the target (the first in order on a tie).
  const candidates: { top: number; left: number; side: CalloutSide }[] = order.map((side) => {
    const spot = at(side);
    return { top: clamp(spot.top, minTop, maxTop), left: clamp(spot.left, minLeft, maxLeft), side };
  });
  for (const [top, left] of [
    [minTop, maxLeft],
    [maxTop, maxLeft],
    [minTop, minLeft],
    [maxTop, minLeft],
  ] as const) {
    candidates.push({ top: clamp(top, minTop, maxTop), left: clamp(left, minLeft, maxLeft), side: top === minTop ? "top" : "bottom" });
  }
  let best = candidates[0];
  let bestArea = Infinity;
  for (const c of candidates) {
    const area = overlapArea(target, { top: c.top, left: c.left, ...size });
    if (area < bestArea) [best, bestArea] = [c, area];
  }
  return { top: best.top, left: best.left, side: best.side, overlaps: bestArea > 0 };
}
