/**
 * A tour callout never covers its own target (calloutPlacement.ts): beside it
 * when there is room, flipped to whichever side has room when not, always on
 * screen, and the least-covering spot only when no side can hold it.
 */
import { describe, expect, it } from "vitest";

import { BELOW_FIRST, overlapArea, placeCallout, type Box, type CalloutSide } from "../calloutPlacement";

const VIEWPORT = { width: 1440, height: 900 };
const CARD = { width: 320, height: 220 };
const MARGIN = 16;

const box = (p: { top: number; left: number }, size = CARD): Box => ({ ...p, ...size });
const onScreen = (b: Box, viewport = VIEWPORT) =>
  b.left >= MARGIN - 0.5 && b.top >= MARGIN - 0.5 && b.left + b.width <= viewport.width - MARGIN + 0.5 && b.top + b.height <= viewport.height - MARGIN + 0.5;

/** Whether any side could hold the card, by the same rule. */
function someSideFits(target: Box, viewport = VIEWPORT, gap = 16): boolean {
  return (
    target.left + target.width + gap + CARD.width <= viewport.width - MARGIN ||
    target.left - gap - CARD.width >= MARGIN ||
    target.top + target.height + gap + CARD.height <= viewport.height - MARGIN ||
    target.top - gap - CARD.height >= MARGIN
  );
}

describe("placeCallout", () => {
  it("goes beside the target first: a sidebar gets its card on the right", () => {
    const sidebar = { top: 56, left: 0, width: 240, height: 800 };
    const placed = placeCallout(sidebar, CARD, VIEWPORT);
    expect(placed.side).toBe("right");
    expect(placed.overlaps).toBe(false);
    expect(overlapArea(sidebar, box(placed))).toBe(0);
  });

  it("flips to the other side at the right edge", () => {
    const button = { top: 300, left: 1300, width: 120, height: 32 };
    expect(placeCallout(button, CARD, VIEWPORT).side).toBe("left");
  });

  it("goes below or above a full-width row (a trace row, a span row)", () => {
    const row = { top: 200, left: 260, width: 1160, height: 28 };
    expect(placeCallout(row, CARD, VIEWPORT).side).toBe("bottom");
    const low = { top: 760, left: 260, width: 1160, height: 28 };
    expect(placeCallout(low, CARD, VIEWPORT).side).toBe("top");
  });

  it("honours a preferred side when it has room, and flips when it doesn't", () => {
    const target = { top: 400, left: 600, width: 200, height: 40 };
    expect(placeCallout(target, CARD, VIEWPORT, { preferred: "top" }).side).toBe("top");
    const nearTop = { top: 20, left: 600, width: 200, height: 40 };
    const placed = placeCallout(nearTop, CARD, VIEWPORT, { preferred: "top", order: BELOW_FIRST });
    expect(placed.side).toBe("bottom");
    expect(overlapArea(nearTop, box(placed))).toBe(0);
  });

  it("keeps clear of a bottom bar when asked", () => {
    const target = { top: 500, left: 200, width: 1000, height: 40 };
    const placed = placeCallout(target, CARD, VIEWPORT, { order: BELOW_FIRST, bottomReserve: 176 });
    expect(placed.side).toBe("top");
    expect(placed.top + CARD.height).toBeLessThanOrEqual(VIEWPORT.height - 176 - MARGIN);
  });

  it("never covers the target when any side has room, and stays on screen (a sweep of targets)", () => {
    let checked = 0;
    for (let top = 0; top <= 860; top += 43) {
      for (let left = 0; left <= 1400; left += 70) {
        for (const [width, height] of [
          [24, 24],
          [120, 32],
          [600, 28],
          [1160, 28],
          [240, 800],
          [700, 500],
        ]) {
          const target = { top, left, width: Math.min(width, VIEWPORT.width - left), height: Math.min(height, VIEWPORT.height - top) };
          for (const preferred of ["auto", "top", "bottom", "left", "right"] as const) {
            const placed = placeCallout(target, CARD, VIEWPORT, { preferred: preferred as CalloutSide | "auto" });
            expect(onScreen(box(placed)), JSON.stringify({ target, preferred, placed })).toBe(true);
            if (someSideFits(target)) {
              expect(placed.overlaps).toBe(false);
              expect(overlapArea(target, box(placed)), JSON.stringify({ target, preferred, placed })).toBe(0);
            }
            checked++;
          }
        }
      }
    }
    expect(checked).toBeGreaterThan(5000);
  });

  it("covers as little as it can when nothing fits, and says it overlaps", () => {
    const huge = { top: 40, left: 40, width: 1360, height: 820 };
    const placed = placeCallout(huge, CARD, VIEWPORT);
    expect(placed.overlaps).toBe(true);
    expect(onScreen(box(placed))).toBe(true);
  });
});
