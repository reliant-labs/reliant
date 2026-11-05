// Copyright (c) 2025 Reliant Labs

/**
 * Every kind the console DRAWS, which is every kind the panel builder offers.
 * Any other kind is one a newer forge added, drawn as "not yet", never as an
 * error, and never sent to QueryPanel.
 */
export const DRAWN_KINDS: ReadonlySet<string> = new Set(["line", "number", "table", "bar", "heatmap", "sql", "postgres", "search", "text", "funnel", "issues", "sessions"]);

/**
 * Whether a panel of `kind` runs a query: not a search (a link), a text panel
 * (prose), or a kind this console does not draw. A postgres panel's query is
 * AppDatabaseService.QueryDatabasePanel; every other one is QueryPanel.
 */
export function panelRunsQuery(kind: string): boolean {
  return DRAWN_KINDS.has(kind) && kind !== "search" && kind !== "text";
}
