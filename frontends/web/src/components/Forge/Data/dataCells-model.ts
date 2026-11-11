// Copyright (c) 2025 Reliant Labs

/**
 * HOW A CELL READS: in the grid (one line, cut) and in its popover (the whole
 * value, JSON pretty-printed).
 *
 * NULL is its own kind, never the text "null": an empty string, the string
 * "null" and SQL NULL are three different values and must look it. A json or
 * jsonb cell arrives as the JSON value itself (an object, an array) or, from
 * a server that sends it as text, as a string that parses; both pretty-print.
 */

import type { DbCell } from "@/services/controlPlane/observability/appDatabaseApi";

/** A grid cell is cut to this many characters; the popover has the rest. */
export const CELL_PREVIEW_CHARS = 120;

export type CellKind = "null" | "text" | "number" | "bool" | "json";

export interface CellDisplay {
  kind: CellKind;
  /** One line, at most CELL_PREVIEW_CHARS (plus an ellipsis when cut). */
  short: string;
  /** Everything that arrived (the server's cut marker removed): what the popover shows and Copy copies. */
  full: string;
  /** The grid shows less than `full`: offer the popover. */
  cut: boolean;
  /** The SERVER cut the value at 4 KiB: `full` is not the whole value either. */
  serverCut: CellCut | null;
}

// ── Two spellings of a type ─────────────────────────────────────────────────
//
// DescribeDatabase names a column's type as a person reads it ("bigint",
// "timestamp with time zone", "character varying(40)", "text[]"); a
// BrowseTable result column carries Postgres's internal name ("int8",
// "timestamptz", "varchar", "_text"). They are never compared: how a VALUE
// reads (JSON, numbers) is decided by the result column's internal type, and
// what is shown to the reader or offered by the filter builder by the
// described one.

/** A result column's internal type is json or jsonb (or an array of them). */
export function isJsonType(resultType: string): boolean {
  return resultType === "json" || resultType === "jsonb" || resultType === "_json" || resultType === "_jsonb";
}

/** A result column's internal type lines up as numbers (right-aligned, tabular). */
export function isNumericType(resultType: string): boolean {
  return ["int2", "int4", "int8", "float4", "float8", "numeric", "oid", "money"].includes(resultType);
}

const SHORT_TYPES: [RegExp, string][] = [
  [/^timestamp(\(\d+\))? with time zone/, "timestamptz$1"],
  [/^timestamp(\(\d+\))? without time zone/, "timestamp$1"],
  [/^time(\(\d+\))? with time zone/, "timetz$1"],
  [/^time(\(\d+\))? without time zone/, "time$1"],
  [/^character varying/, "varchar"],
  [/^double precision/, "float8"],
];

/**
 * A described type, short enough for a column header: the long SQL spellings
 * compacted ("timestamptz", "varchar(40)", "timestamptz[]"), everything else
 * as described. The full spelling is the header's title.
 */
export function shortTypeLabel(described: string): string {
  for (const [pattern, short] of SHORT_TYPES) {
    if (pattern.test(described)) return described.replace(pattern, short);
  }
  return described;
}

// ── The server's 4 KiB cut ──────────────────────────────────────────────────

/** What the server appends to a cell it cut (forge pkg/dbquery): "…[truncated: N of M bytes shown]". */
const CUT_MARKER = /…\[truncated: (\d+) of (\d+) bytes shown\]$/;

/** How much of a cut cell arrived, when the marker says. */
export interface CellCut {
  shownBytes: number | null;
  totalBytes: number | null;
}

/**
 * Whether the server cut this cell, and the value without its marker.
 * `flagged` is the row's truncated_cells saying so; a server that does not
 * fill truncated_cells yet still ends a cut cell's text with the marker, so
 * either one is enough.
 */
export function cellCut(value: DbCell | undefined, flagged: boolean): { value: DbCell | undefined; cut: CellCut | null } {
  if (typeof value === "string") {
    const match = CUT_MARKER.exec(value);
    if (match) {
      return { value: value.slice(0, match.index), cut: { shownBytes: Number(match[1]), totalBytes: Number(match[2]) } };
    }
  }
  return { value, cut: flagged ? { shownBytes: null, totalBytes: null } : null };
}

/** The popover's sentence about a cut cell. */
export function cutNote(cut: CellCut): string {
  return cut.shownBytes !== null && cut.totalBytes !== null
    ? `Cut by the server: ${cut.shownBytes.toLocaleString("en-US")} of ${cut.totalBytes.toLocaleString("en-US")} bytes shown. The rest is not readable here.`
    : "Cut by the server at 4 KiB: the rest of this value is not readable here.";
}

function parsedJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

/**
 * How a cell reads. `resultType` is the BrowseTable result column's internal
 * type; `flaggedCut` is whether the row's truncated_cells names this cell.
 */
export function cellDisplay(raw: DbCell | undefined, resultType = "", flaggedCut = false): CellDisplay {
  const { value, cut: serverCut } = cellCut(raw, flaggedCut);
  if (value === null || value === undefined) return { kind: "null", short: "NULL", full: "NULL", cut: false, serverCut: null };
  if (typeof value === "boolean") return { kind: "bool", short: String(value), full: String(value), cut: false, serverCut };
  if (typeof value === "number") return { kind: "number", short: String(value), full: String(value), cut: false, serverCut };

  let full: string;
  let kind: CellKind;
  let compact: string;
  if (typeof value === "string") {
    // A cut json value is no longer JSON: it reads as the text that arrived.
    const parsed = isJsonType(resultType) && !serverCut ? parsedJson(value) : undefined;
    if (parsed !== undefined && parsed !== null && typeof parsed === "object") {
      kind = "json";
      full = JSON.stringify(parsed, null, 2);
      compact = JSON.stringify(parsed);
    } else {
      kind = "text";
      full = value;
      compact = oneLine(value);
    }
  } else {
    kind = "json";
    full = JSON.stringify(value, null, 2);
    compact = JSON.stringify(value);
  }
  const cut = compact.length > CELL_PREVIEW_CHARS || compact !== full || !!serverCut;
  const short = compact.length > CELL_PREVIEW_CHARS ? `${compact.slice(0, CELL_PREVIEW_CHARS)}…` : compact;
  return { kind, short, full, cut, serverCut };
}
