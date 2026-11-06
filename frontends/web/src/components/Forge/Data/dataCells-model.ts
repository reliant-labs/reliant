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
  /** The whole value: what the popover shows and Copy copies. */
  full: string;
  /** The grid shows less than the whole value: offer the popover. */
  cut: boolean;
}

export function isJsonType(pgType: string): boolean {
  return pgType === "json" || pgType === "jsonb" || pgType === "_json" || pgType === "_jsonb";
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

export function cellDisplay(value: DbCell | undefined, pgType = ""): CellDisplay {
  if (value === null || value === undefined) return { kind: "null", short: "NULL", full: "NULL", cut: false };
  if (typeof value === "boolean") return { kind: "bool", short: String(value), full: String(value), cut: false };
  if (typeof value === "number") return { kind: "number", short: String(value), full: String(value), cut: false };

  let full: string;
  let kind: CellKind;
  let compact: string;
  if (typeof value === "string") {
    const parsed = isJsonType(pgType) ? parsedJson(value) : undefined;
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
  const cut = compact.length > CELL_PREVIEW_CHARS || compact !== full;
  const short = compact.length > CELL_PREVIEW_CHARS ? `${compact.slice(0, CELL_PREVIEW_CHARS)}…` : compact;
  return { kind, short, full, cut };
}

/** A column's type, short enough for a header: `int8`, `text[]`, `timestamptz`. */
export function typeLabel(pgType: string): string {
  return pgType.startsWith("_") ? `${pgType.slice(1)}[]` : pgType;
}

/** Whether a column's values line up as numbers (right-aligned, tabular). */
export function isNumericType(pgType: string): boolean {
  return ["int2", "int4", "int8", "float4", "float8", "numeric", "oid", "money"].includes(pgType);
}
