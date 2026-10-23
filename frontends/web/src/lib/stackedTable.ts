/**
 * Stacked tables: a `<table>` that reads as a list of cards on a phone.
 *
 * The console's lists (services, releases, environments, alerts, …) are real
 * tables, which is right on a desktop and unreadable at 390px: eight columns
 * either squeeze to a few characters each or scroll sideways inside a card.
 * Below `md` a table carrying `STACKED_TABLE_CLASS` instead lays each row out
 * as a card — its title cell leading (the one marked `data-stack-title`, else
 * the first), every other cell as a "Label · value" line (index.css,
 * "Stacked tables").
 *
 * The label is the column's header text, so the table's own `<thead>` stays
 * the one place a column is named: `useStackedTableLabels` copies each
 * header's text onto the body cells under it as `data-label` (honouring
 * `colSpan`), and keeps doing so as rows arrive, re-sort or change. React
 * never sets `data-label`, so the two never fight over the attribute.
 *
 * Desktop is untouched: the CSS only applies below `md`, and a desktop table
 * with labels on its cells renders exactly as before.
 */

import { useCallback, useEffect, useRef } from "react";

export const STACKED_TABLE_CLASS = "stack-narrow";

/** The header text of each column index, expanding `colSpan`. */
function columnLabels(table: HTMLTableElement): string[] {
  const headRow = table.tHead?.rows[table.tHead.rows.length - 1];
  if (!headRow) return [];
  const labels: string[] = [];
  for (const cell of Array.from(headRow.cells)) {
    // A visually empty header (row actions) is often sr-only text: still a name.
    const text = (cell.textContent ?? "").trim().replace(/\s+/g, " ");
    for (let i = 0; i < Math.max(1, cell.colSpan); i += 1) labels.push(text);
  }
  return labels;
}

/** Write each body cell's column label onto it. Exported for tests. */
export function labelStackedTable(table: HTMLTableElement): void {
  const labels = columnLabels(table);
  if (labels.length === 0) return;
  for (const body of Array.from(table.tBodies)) {
    for (const row of Array.from(body.rows)) {
      const cells = Array.from(row.cells);
      // The row's title leads its card and needs no label: the cell marked
      // `data-stack-title`, else the first.
      const title = cells.find((cell) => cell.hasAttribute("data-stack-title")) ?? cells[0];
      let column = 0;
      for (const cell of cells) {
        const label = labels[column] ?? "";
        // A cell spanning columns (an empty state, a detail line) has no
        // single column to name.
        const next = cell === title || cell.colSpan > 1 ? "" : label;
        if (next) {
          if (cell.getAttribute("data-label") !== next) cell.setAttribute("data-label", next);
        } else if (cell.hasAttribute("data-label")) {
          cell.removeAttribute("data-label");
        }
        column += Math.max(1, cell.colSpan);
      }
    }
  }
}

/**
 * A callback ref for a stacked `<table>`: labels its cells now and whenever
 * its rows change. Pair it with `className={STACKED_TABLE_CLASS}`.
 */
export function useStackedTableLabels(): (table: HTMLTableElement | null) => void {
  const observer = useRef<MutationObserver | null>(null);
  useEffect(() => () => observer.current?.disconnect(), []);
  return useCallback((table: HTMLTableElement | null) => {
    observer.current?.disconnect();
    observer.current = null;
    if (!table) return;
    labelStackedTable(table);
    if (typeof MutationObserver === "undefined") return;
    // Rows and header text only: our own attribute writes are not observed,
    // so labelling cannot re-trigger itself.
    const next = new MutationObserver(() => labelStackedTable(table));
    next.observe(table, { childList: true, subtree: true, characterData: true });
    observer.current = next;
  }, []);
}
