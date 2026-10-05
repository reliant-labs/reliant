/**
 * Which user message the timeline pins to the top of the transcript.
 *
 * The pin is a breadcrumb: it names the most recent user message that has
 * slid ENTIRELY under the header. So the question it answers is geometric —
 * "which prompt is the reader below, now that it is out of sight" — and it has
 * to be answered from measured edges.
 *
 * Bottom edges, not top edges. A user-message row opens with an empty band
 * reserved for its hover toolbar, so its top edge crosses the header while the
 * bubble is still fully on screen below it. Swapping there printed the same
 * message twice, one above the other — the reported "doesn't swap naturally".
 * Keyed on the bottom edge, the header changes hands at the moment the bubble
 * passes underneath it, which reads as the bubble sticking.
 *
 * The same choice is what lets the header leave at the top of the transcript.
 * The first row starts below the scroll-back loader's space, which is INSIDE
 * the header band, so a top-edge rule saw the first message as already
 * scrolled away and the header sat over the very message it named.
 *
 * It used to be answered from Virtuoso's `rangeChanged.startIndex`, and both
 * reported defects came from that single choice:
 *
 *   - `startIndex` is the first RENDERED row, inflated by `overscan` and
 *     `increaseViewportBy`, so it is not the visual top and it is not
 *     monotonic. Recorded scroll frames from a real session showed it
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
   * The crossing line, measured down from the scroller's top edge: the
   * header's bottom edge. A user message whose bottom edge is at or above it
   * is entirely behind the header, so the header takes it over.
   *
   * Pass the header's height whether or not a header is showing right now.
   * A line that dropped to 0 whenever the header hid would make the decision
   * read a geometry the decision itself produces, and a swap between two
   * headers of different heights could flip it back.
   */
  line: number;
  /** Currently pinned index, for the release hysteresis below. */
  previousPinned: number | null;
  /**
   * Slack applied ONLY to releasing the current pin, never to engaging one.
   *
   * A message resting within a pixel of the line could otherwise be unpinned
   * by a sub-pixel layout correction (a row re-measured, a scroll adjusted),
   * which restores the geometry that pins it again — the boundary oscillation
   * that reads as shake. An asymmetric band cannot oscillate: leaving the
   * pinned state costs strictly more than entering it.
   *
   * Keep it small. At the top of the transcript the first message's bottom
   * edge has to clear `line + releaseHysteresisPx` for the header to leave.
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

  // The top of the transcript is in view: nothing has scrolled away, so there
  // is nothing to breadcrumb, and the reader is owed the first message itself
  // rather than a header drawn over it. The geometry below reaches the same
  // answer only while the first row is taller than the header plus the
  // release band — a short first message (mobile reserves no toolbar band)
  // would otherwise keep the header pinned over it at scrollTop 0.
  if (rows[0].index === 0 && rows[0].top >= 0) return null;

  // The row that owns the line: the first one whose bottom edge has not yet
  // passed it. This is the real visual top, and unlike the first RENDERED row
  // it is unaffected by how much overscan the virtualizer chose to render.
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

  // Sticky: an already-pinned message has to come back out by the hysteresis
  // band before it gives the header up.
  const threshold = candidate === previousPinned ? line + releaseHysteresisPx : line;
  if (headingRow.bottom <= threshold) return candidate;

  // The section's own heading is still at least partly visible, so the band
  // above it belongs to the PREVIOUS section, whose heading has necessarily
  // gone: every row before the line-owning row has its bottom edge above the
  // line. Never blank the header mid-conversation just because the next
  // prompt is on its way up. With no previous section, this is the top.
  return candidate > 0 ? (userMessageForItem[candidate - 1] ?? null) : null;
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
