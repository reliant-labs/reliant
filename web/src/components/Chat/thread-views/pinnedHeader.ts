/**
 * Which user message the timeline pins to the top of the transcript.
 *
 * The pin is a breadcrumb: it names the user message that heads the section
 * you are currently reading, for the case where that message has itself
 * scrolled out of sight. So the question it answers is geometric — "whose
 * section owns the top of the viewport right now" — and it has to be answered
 * from measured edges.
 *
 * It used to be answered from Virtuoso's `rangeChanged.startIndex`, and both
 * reported defects came from that single choice:
 *
 *   - `startIndex` is the first RENDERED row, inflated by `overscan` and
 *     `increaseViewportBy`, so it is not the visual top and it is not
 *     monotonic. Recorded scrollDebug frames from a real session show it
 *     stepping 115 → 111 → 112 → 105 → 115 → 104 across consecutive animation
 *     frames with no user input at all. Driving a visible overlay from that
 *     toggles it on and off between frames — the reported jitter.
 *
 *   - A row can be several viewport heights tall, so `startIndex` cannot
 *     change at all while you scroll through one long assistant message. The
 *     handoff moment is not expressible in row indices — the reported "swaps
 *     at the wrong level".
 *
 * Rows are measured in DATA space (see the index-space note in
 * InterleavedTimeline): the DOM carries `data-timeline-index`, which is this
 * component's own attribute, deliberately NOT Virtuoso's `data-item-index` —
 * that one is SHIFTED by `firstItemIndex` and would silently offset every
 * lookup into the positional mapping.
 */

/** A rendered row's vertical extent, in the scroller's client coordinates. */
export interface MeasuredRow {
  /** DATA-space index into timelineItems. */
  index: number;
  /** Distance from the scroller's top edge to the row's top edge. */
  top: number;
  /** Distance from the scroller's top edge to the row's bottom edge. */
  bottom: number;
}

export interface PinnedHeaderInput {
  /** Rendered rows, ascending by index. */
  rows: MeasuredRow[];
  /** Positional map: item index → index of the user message heading its section. */
  userMessageForItem: (number | null)[];
  /**
   * The crossing line, measured down from the scroller's top edge. A heading
   * at or above this line has been taken over by the header and is pinned.
   */
  line: number;
  /** Currently pinned index, for the release hysteresis below. */
  previousPinned: number | null;
  /**
   * Slack applied ONLY to releasing the current pin, never to engaging one.
   *
   * The header is an opaque overlay whose own height sets `line`, so the
   * decision reads a geometry the decision itself produces. Without slack, a
   * heading resting within a pixel of the line can be unpinned by a sub-pixel
   * layout correction, which restores the geometry that pins it again — the
   * boundary oscillation that reads as shake. An asymmetric band cannot
   * oscillate: leaving the pinned state costs strictly more than entering it.
   */
  releaseHysteresisPx: number;
}

/**
 * Resolve the pinned user message from measured geometry.
 *
 * Returns the DATA-space index of the user message to pin, or null for no
 * header at all.
 */
export function resolvePinnedUserMessage({
  rows,
  userMessageForItem,
  line,
  previousPinned,
  releaseHysteresisPx,
}: PinnedHeaderInput): number | null {
  if (rows.length === 0) return null;

  // The row that owns the line: the first one whose bottom edge has not yet
  // passed it. This is the real visual top, and unlike `startIndex` it is
  // unaffected by how much Virtuoso chose to render above it.
  //
  // Falling back to the last row covers the scroller being scrolled past
  // everything rendered — mid-correction, or a jump that has not settled.
  const topRow = rows.find((row) => row.bottom > line) ?? rows[rows.length - 1];

  const candidate = userMessageForItem[topRow.index] ?? null;
  if (candidate === null) return null;

  const headingRow = rows.find((row) => row.index === candidate);

  // The heading is not rendered, which for a section head can only mean it is
  // above the rendered window. It is off-screen by definition, so it pins.
  if (!headingRow) return candidate;

  // Sticky: an already-pinned heading has to clear the line by the hysteresis
  // band before it gives the header up.
  const threshold = candidate === previousPinned ? line + releaseHysteresisPx : line;
  if (headingRow.top <= threshold) return candidate;

  // The heading is below the line with nothing above it — the top of the
  // transcript. Nothing has scrolled away, so there is nothing to breadcrumb.
  return null;
}

/** DOM attribute carrying a row's DATA-space index. */
export const TIMELINE_ROW_INDEX_ATTR = "data-timeline-index";

/**
 * Read the rendered rows' geometry out of the scroller.
 *
 * One batched pass of `getBoundingClientRect` over the rendered rows, which is
 * bounded by the viewport plus overscan rather than by conversation length.
 * Every read happens here so the layout flush is paid exactly once per sample.
 */
export function measureRows(scroller: HTMLElement): MeasuredRow[] {
  const scrollerTop = scroller.getBoundingClientRect().top;
  const elements = scroller.querySelectorAll<HTMLElement>(`[${TIMELINE_ROW_INDEX_ATTR}]`);

  const rows: MeasuredRow[] = [];
  for (const element of elements) {
    const index = Number(element.getAttribute(TIMELINE_ROW_INDEX_ATTR));
    if (!Number.isFinite(index)) continue;
    const rect = element.getBoundingClientRect();
    rows.push({
      index,
      top: rect.top - scrollerTop,
      bottom: rect.bottom - scrollerTop,
    });
  }

  // Virtuoso renders in order, but the resolver's "first row past the line"
  // scan is only correct on a sorted list, so do not depend on that.
  rows.sort((a, b) => a.index - b.index);
  return rows;
}
