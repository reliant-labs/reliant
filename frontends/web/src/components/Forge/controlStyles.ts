// Copyright (c) 2025 Reliant Labs

/**
 * THE CONSOLE'S CONTROL VOCABULARY, ONCE.
 *
 * The console's pages put filters, toggles and selects directly above charts
 * and tables, and they used to be drawn like the data: the structural
 * `border-border` (1.3-1.4:1 against the card in every scheme), a fill one
 * step off the page, and a selected option that was a raised pill
 * (`bg-surface shadow-sm`, 1.2:1 against its track in dark) or `bg-ink/8`
 * (1.2:1). A toolbar read as one more row of the chart. So:
 *
 *   - A control's edge is `border-border-strong`, forge's control edge. The
 *     console scope (`.forge-console`, src/index.css) raises it to at least
 *     3:1 against the page and the card in every scheme, light and dark
 *     (WCAG 1.4.11; pinned by Settings/__tests__/consoleControlEdge.test.ts),
 *     so forge-ui's own inputs and outline buttons pick it up too.
 *     Structure (cards, panels, rows) keeps `border-border`.
 *   - Selected is the primary tint, primary ink and a primary ring: the same
 *     "this narrows the view" signal as a set filter chip (ObsScopeBar, forge-ui
 *     filter_bar), never a grey step. `primary`, not `accent`: a dialog portals
 *     out of the `.forge-ui` scope that remaps `accent`.
 *
 *   CONTROL_FIELD     a select, text input or textarea: edge, inset fill, hover, focus ring
 *   SEGMENTED         the track of mutually exclusive options (Linear · Log)
 *   segmentTone(on)   the colours of one option in it
 *   toggleTone(on)    the edge and colours of a standalone on/off filter (a log level)
 *
 * Geometry (height, padding, type size) stays with the call site.
 */

const FOCUS = "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export const CONTROL_FIELD = `rounded-md border border-border-strong bg-surface-sunken text-ink placeholder:text-ink-muted transition-colors hover:border-ink-muted ${FOCUS}`;

export const SEGMENTED = "inline-flex items-center rounded-lg border border-border-strong bg-surface-sunken p-0.5";

const SELECTED = "bg-primary/12 font-medium text-primary-ink ring-1 ring-inset ring-primary";

export function segmentTone(on: boolean): string {
  return on ? SELECTED : "text-ink-muted hover:bg-ink/5 hover:text-ink";
}

export function toggleTone(on: boolean): string {
  return on ? "border border-primary bg-primary/12 font-medium text-primary-ink" : "border border-border-strong bg-surface text-ink hover:bg-ink/5";
}
