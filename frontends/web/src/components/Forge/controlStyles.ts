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
 *   - ONE FIELD. Every text input, search box, select and textarea is the
 *     same object: a 32px row (`h-8`), `rounded-md`, the control edge, the
 *     card's own fill (`bg-surface`, so a field reads the same on the page and
 *     inside a card, and the same as the chips beside it), `text-sm` ink with
 *     a muted placeholder, and the one focus ring (`ring-2 ring-ring`). A
 *     leading icon is 14px, muted, 10px in from the left edge, and the text
 *     starts 32px in. The filter bars (forge-ui filter_bar, the console's own
 *     toolbars) and forge-ui's Input/Textarea draw the same edge, ring and
 *     placeholder.
 *
 *   CONTROL_FIELD     a select, text input or textarea: edge, fill, hover, focus ring
 *   FIELD             CONTROL_FIELD at the one size: h-8, px-2.5, text-sm
 *   FIELD_ICON        a leading icon inside a field; pad the field with FIELD_ICON_PAD
 *   FIELD_SHELL       a <label> drawn as the field around an icon and a bare input (FIELD_BARE)
 *   SEGMENTED         the track of mutually exclusive options (Linear · Log)
 *   segmentTone(on)   the colours of one option in it
 *   toggleTone(on)    the edge and colours of a standalone on/off filter (a log level)
 *
 * A call site may still widen a field or make it a touch target
 * (`touch:h-11`); it does not pick its own edge, fill, ring or placeholder.
 */

const FOCUS = "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export const CONTROL_FIELD = `rounded-md border border-border-strong bg-surface text-ink placeholder:text-ink-muted transition-colors hover:border-ink-muted disabled:cursor-not-allowed disabled:opacity-60 ${FOCUS}`;

/** The one field: CONTROL_FIELD at the console's control height. */
export const FIELD = `${CONTROL_FIELD} h-8 px-2.5 text-sm`;

/** A leading icon inside a field (the field's wrapper is `relative`). */
export const FIELD_ICON = "pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted";

/** The left padding of a field that carries FIELD_ICON. */
export const FIELD_ICON_PAD = "pl-8";

/** A <label> drawn as the field, holding an icon and a FIELD_BARE input: the same edge, fill and ring (on focus-within). */
export const FIELD_SHELL =
  "flex h-8 items-center gap-2 rounded-md border border-border-strong bg-surface px-2.5 text-ink-muted transition-colors hover:border-ink-muted focus-within:ring-2 focus-within:ring-ring";

/** The input inside a FIELD_SHELL: no edge or ring of its own. */
export const FIELD_BARE = "min-w-0 flex-1 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none";

export const SEGMENTED = "inline-flex items-center rounded-lg border border-border-strong bg-surface-sunken p-0.5";

const SELECTED = "bg-primary/12 font-medium text-primary-ink ring-1 ring-inset ring-primary";

export function segmentTone(on: boolean): string {
  return on ? SELECTED : "text-ink-muted hover:bg-ink/5 hover:text-ink";
}

export function toggleTone(on: boolean): string {
  return on ? "border border-primary bg-primary/12 font-medium text-primary-ink" : "border border-border-strong bg-surface text-ink hover:bg-ink/5";
}
