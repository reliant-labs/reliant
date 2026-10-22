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

/** The header's bottom edge: a typical one-line pinned header. */
const LINE = 48;

/** A user-message row: the toolbar band above the bubble, plus the bubble. */
const USER_ROW_PX = 66;

function rows(...specs: Array<[index: number, top: number, height: number]>): MeasuredRow[] {
  return specs.map(([index, top, height]) => ({ index, top, bottom: top + height }));
}

function resolve(
  measured: MeasuredRow[],
  { line = LINE, previousPinned = null as number | null, releaseHysteresisPx = 8 } = {},
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
    // Nothing has scrolled away, so there is nothing to breadcrumb.
    expect(resolve(rows([0, 32, USER_ROW_PX], [1, 98, 400]))).toBeNull();
  });

  // The reported "it doesn't go away at the top". The first row starts 32px
  // down (the scroll-back loader's space), which is INSIDE the header band, so
  // the old top-edge rule could never release it: scrolled all the way up, the
  // header sat over the very message it named.
  it("releases the header at the top even though the first row starts inside the header band", () => {
    expect(resolve(rows([0, 32, USER_ROW_PX], [1, 98, 400]), { previousPinned: 0 })).toBeNull();
  });

  // A short first message — mobile reserves no toolbar band above the bubble —
  // ends inside the release band, so geometry alone would keep it pinned.
  it("releases the header at the top even when the first message is shorter than the header", () => {
    expect(resolve(rows([0, 32, 20], [1, 52, 400]), { previousPinned: 0 })).toBeNull();
  });

  it("pins a user message once it has scrolled entirely behind the header", () => {
    expect(resolve(rows([2, -30, USER_ROW_PX], [3, 36, 3000]))).toBe(2);
  });

  // The reported "doesn't swap out naturally". u2's top edge has crossed the
  // line but its bubble is still on screen just below the header; the old rule
  // swapped here, printing the same message twice, one above the other.
  //
  // Note the answer is u0, not null: while the next message is still visible,
  // the reader is in u0's section, and the header must never blank mid-chat.
  it("keeps the previous section's heading while the next user message is still visible", () => {
    expect(resolve(rows([1, -2900, 2940], [2, 40, USER_ROW_PX], [3, 106, 3000]))).toBe(0);
  });

  it("hands off at the moment the incoming user message clears the line", () => {
    // Bottom edge 1px below the line: a sliver is still visible.
    expect(resolve(rows([1, -2900, 2883], [2, -17, USER_ROW_PX], [3, 49, 3000]))).toBe(0);
    // Bottom edge 1px above it: entirely behind the header, which takes it over.
    expect(resolve(rows([1, -2900, 2881], [2, -19, USER_ROW_PX], [3, 47, 3000]))).toBe(2);
  });

  // The line is the header's bottom edge, not the viewport top — the header
  // occludes that band, so a message is "taken over" once it is behind the
  // header rather than once it is off-screen.
  it("measures the crossing against the header's bottom edge", () => {
    const geometry = rows([1, -2900, 2870], [2, -30, USER_ROW_PX], [3, 36, 3000]);
    // With no header height at all, a message 36px down has not gone.
    expect(resolve(geometry, { line: 0 })).toBe(0);
    // With a 48px header, that same message is behind it.
    expect(resolve(geometry, { line: 48 })).toBe(2);
  });

  it("holds a pin through a sub-pixel wobble around the line", () => {
    const atLine = rows([2, LINE - USER_ROW_PX, USER_ROW_PX], [3, LINE, 3000]);
    const justBelow = rows([2, LINE - USER_ROW_PX + 1.5, USER_ROW_PX], [3, LINE + 1.5, 3000]);

    expect(resolve(atLine, { previousPinned: 2 })).toBe(2);
    // A 1.5px drift below the line must NOT release an established pin.
    expect(resolve(justBelow, { previousPinned: 2 })).toBe(2);
  });

  // The asymmetry itself: identical geometry, opposite answers, decided only
  // by whether this message is already the one in the header. Entering the
  // pinned state costs strictly less than leaving it, which is what makes the
  // boundary unable to oscillate.
  it("applies the band to releasing a pin but not to engaging one", () => {
    const justBelow = rows([1, -2900, 2851.5], [2, LINE - USER_ROW_PX + 1.5, USER_ROW_PX]);

    expect(resolve(justBelow, { previousPinned: 2 })).toBe(2);
    expect(resolve(justBelow, { previousPinned: null })).toBe(0);
  });

  it("releases the pin and hands back once the message clears the band", () => {
    const handedBack = rows([1, -2900, 2891], [2, -9, USER_ROW_PX], [3, 57, 3000]);
    expect(resolve(handedBack, { previousPinned: 2 })).toBe(0);
  });

  it("pins a heading that is scrolled out of the rendered window entirely", () => {
    // Only the section body is rendered; its heading is above the window, so
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
        rows: rows([4, -30, USER_ROW_PX], [5, 36, 3000]),
        userMessageForItem: after,
        line: LINE,
        previousPinned: null,
        releaseHysteresisPx: 8,
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
