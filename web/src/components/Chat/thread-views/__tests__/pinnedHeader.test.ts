import { describe, expect, it } from "vitest";
import {
  measureRows,
  resolvePinnedUserMessage,
  TIMELINE_ROW_INDEX_ATTR,
  type MeasuredRow,
} from "../pinnedHeader";

// These test the REAL resolver the component uses. The previous version of
// this file re-implemented the rule locally, which meant it kept passing while
// the component was wrong — both reported defects were live the whole time it
// was green.

/** Mirrors InterleavedTimeline's userMessageForItem construction. */
function buildUserMessageForItem(items: Array<{ role: "user" | "assistant" }>) {
  const mapping: (number | null)[] = [];
  let current: number | null = null;
  for (let i = 0; i < items.length; i++) {
    if (items[i].role === "user") current = i;
    mapping.push(current);
  }
  return mapping;
}

/** u0, a1, u2, a3 — two sections. */
const TWO_SECTIONS = buildUserMessageForItem([
  { role: "user" },
  { role: "assistant" },
  { role: "user" },
  { role: "assistant" },
]);

function rows(...specs: Array<[index: number, top: number, height: number]>): MeasuredRow[] {
  return specs.map(([index, top, height]) => ({ index, top, bottom: top + height }));
}

function resolve(
  measured: MeasuredRow[],
  { line = 0, previousPinned = null as number | null, releaseHysteresisPx = 24 } = {},
) {
  return resolvePinnedUserMessage({
    rows: measured,
    userMessageForItem: TWO_SECTIONS,
    line,
    previousPinned,
    releaseHysteresisPx,
  });
}

describe("resolvePinnedUserMessage", () => {
  it("pins nothing at the top of the transcript", () => {
    // Every row is below the line; nothing has scrolled away to breadcrumb.
    expect(resolve(rows([0, 10, 100], [1, 110, 400]))).toBeNull();
  });

  it("pins the heading of the section that owns the top of the viewport", () => {
    // u2 is above the line, its section (a3) fills the viewport.
    expect(resolve(rows([2, -400, 440], [3, 40, 3000]))).toBe(2);
  });

  // Defect (a). The old rule dropped the pin the moment the heading became the
  // first visible row — including when 95% of it was above the fold, which is
  // exactly when the header earns its place.
  it("pins a heading that is mostly, but not entirely, scrolled off the top", () => {
    expect(resolve(rows([2, -420, 440], [3, 20, 3000]))).toBe(2);
  });

  it("hands off exactly when the next heading's top edge crosses the line", () => {
    // Still 60px below the line: the previous section still owns the top.
    expect(resolve(rows([0, -3000, 100], [1, -2900, 2960], [2, 60, 440]))).toBe(0);
    // Crossed: the new heading takes over.
    expect(resolve(rows([0, -3120, 100], [1, -3020, 2960], [2, -60, 440]))).toBe(2);
  });

  // The line is the header's own bottom edge, not the viewport top — the
  // header occludes that band, so a heading is "taken over" once it is behind
  // the header rather than once it is off-screen.
  it("measures the crossing against the header's bottom edge", () => {
    const geometry = rows([0, -3000, 100], [1, -2900, 2960], [2, 40, 440]);
    // With no header showing, a heading 40px down has not crossed the top.
    expect(resolve(geometry, { line: 0 })).toBe(0);
    // With a 72px header, that same heading is behind it, so it takes over.
    expect(resolve(geometry, { line: 72 })).toBe(2);
  });

  // Defect (b), the boundary case. The header's height sets the line, so a
  // heading resting on the line would otherwise be un-pinned by a sub-pixel
  // correction and re-pinned by the geometry that restores — a shake.
  it("holds a pin through a sub-pixel wobble around the line", () => {
    const atLine = rows([2, 0, 440], [3, 440, 3000]);
    const justBelow = rows([2, 1.5, 440], [3, 441.5, 3000]);

    expect(resolve(atLine, { previousPinned: 2 })).toBe(2);
    // A 1.5px drift below the line must NOT release an established pin.
    expect(resolve(justBelow, { previousPinned: 2 })).toBe(2);
  });

  it("releases the pin and hands back once the previous section owns the top", () => {
    // Rows are contiguous: u2 has scrolled back down to y=30, so the band
    // above it belongs to a1, whose section is headed by u0.
    const handedBack = rows([0, -3000, 100], [1, -2900, 2930], [2, 30, 440]);
    expect(resolve(handedBack, { previousPinned: 2 })).toBe(0);
  });

  // The asymmetry itself: identical geometry, opposite answers, decided only
  // by whether this heading is already the one in the header. Entering the
  // pinned state costs strictly less than leaving it, which is what makes the
  // boundary unable to oscillate.
  it("applies the band to releasing a pin but not to engaging one", () => {
    // u2 rests 1.5px below the line — a sub-pixel correction away from it.
    const onTheLine = rows([2, 1.5, 440], [3, 441.5, 3000]);

    expect(resolve(onTheLine, { previousPinned: 2 })).toBe(2);
    expect(resolve(onTheLine, { previousPinned: null })).toBeNull();
  });

  it("pins a heading that is scrolled out of the rendered window entirely", () => {
    // Only the section body is rendered; the heading is above the window, so
    // it is off-screen by definition.
    expect(resolve(rows([3, -200, 3000]))).toBe(2);
  });

  it("returns null when nothing is rendered", () => {
    expect(resolve([])).toBeNull();
  });

  // The mapping is positional, so an insertion above the viewport shifts every
  // index. Resolving from live geometry each time is what keeps this correct —
  // there is no remembered index to go stale.
  it("follows the shifted heading after rows are inserted above the viewport", () => {
    const after = buildUserMessageForItem([
      { role: "user" },      // 0
      { role: "assistant" }, // 1
      { role: "assistant" }, // 2  <- inserted
      { role: "assistant" }, // 3  <- inserted
      { role: "user" },      // 4  <- same heading, shifted
      { role: "assistant" }, // 5  <- same body row, shifted
    ]);

    expect(
      resolvePinnedUserMessage({
        rows: rows([4, -400, 440], [5, 40, 3000]),
        userMessageForItem: after,
        line: 0,
        previousPinned: null,
        releaseHysteresisPx: 24,
      }),
    ).toBe(4);
  });
});

describe("measureRows", () => {
  it("reads row indices and offsets relative to the scroller's top edge", () => {
    const scroller = document.createElement("div");
    scroller.getBoundingClientRect = () => ({ top: 120 }) as DOMRect;

    for (const [index, top, height] of [
      [1, 100, 40],
      [0, 60, 40],
    ] as const) {
      const row = document.createElement("div");
      row.setAttribute(TIMELINE_ROW_INDEX_ATTR, String(index));
      row.getBoundingClientRect = () =>
        ({ top, bottom: top + height }) as DOMRect;
      scroller.appendChild(row);
    }

    // Sorted by index, and offsets are scroller-relative (60 - 120 = -60).
    expect(measureRows(scroller)).toEqual([
      { index: 0, top: -60, bottom: -20 },
      { index: 1, top: -20, bottom: 20 },
    ]);
  });

  it("ignores elements without a timeline index", () => {
    const scroller = document.createElement("div");
    scroller.getBoundingClientRect = () => ({ top: 0 }) as DOMRect;
    scroller.appendChild(document.createElement("div"));
    expect(measureRows(scroller)).toEqual([]);
  });
});
