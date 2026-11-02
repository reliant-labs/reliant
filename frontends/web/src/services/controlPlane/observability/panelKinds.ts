// Copyright (c) 2025 Reliant Labs

/**
 * Every kind the console DRAWS, a superset of what the panel builder offers:
 * a dashboard from a release or a newer forge may carry heatmap and text
 * panels. Any other kind is one a newer forge added, drawn as "not yet", never
 * as an error, and never sent to QueryPanel.
 */
export const DRAWN_KINDS: ReadonlySet<string> = new Set(["line", "number", "table", "bar", "heatmap", "sql", "search", "text", "funnel", "issues", "sessions"]);

/** Whether a panel of `kind` is read through QueryPanel: not a search (a link), a text panel (prose), or a kind this console does not draw. */
export function panelRunsQuery(kind: string): boolean {
  return DRAWN_KINDS.has(kind) && kind !== "search" && kind !== "text";
}
