// Copyright (c) 2025 Reliant Labs

/**
 * An input's default, as a person reads it — for READ-ONLY surfaces (the
 * workflow detail page's Inputs table). Never raw proto JSON.
 *
 * Why a separate formatter from paramUtils' formatValueForDisplay: that one
 * feeds EDITORS, whose text round-trips back into the value, so for anything
 * it does not recognise it falls back to JSON.stringify. On a read-only
 * surface that fallback leaked protobuf-es internals verbatim — a model
 * input's default rendered as
 *
 *   {"$typeName":"reliant.v1.ModelSelector","id":"","tags":["flagship"],"providers":[]}
 *
 * This one knows the shapes a default actually takes, says them in words,
 * and for anything else still never shows a `$typeName` or an empty field.
 */

/** A ModelSelector, as protobuf-es materialises it (or as YAML spells it). */
interface ModelSelectorLike {
  id?: string;
  tags?: string[];
  providers?: string[];
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isModelSelector(value: Record<string, unknown>): value is ModelSelectorLike & Record<string, unknown> {
  // A protobuf-es message says what it is; trust that over the field names.
  if (typeof value.$typeName === "string") return value.$typeName === "reliant.v1.ModelSelector";
  const keys = Object.keys(value).filter((key) => key !== "$typeName");
  return (
    keys.length > 0 &&
    keys.every((key) => key === "id" || key === "tags" || key === "providers") &&
    (typeof value.id === "string" || Array.isArray(value.tags))
  );
}

/**
 * "claude-sonnet-4", "flagship (any provider)", "flagship, fast via anthropic".
 * An exact id wins over tags because that is how the selector resolves.
 */
export function formatModelSelector(selector: ModelSelectorLike): string {
  const providers = (selector.providers ?? []).filter(Boolean);
  const via = providers.length > 0 ? ` via ${providers.join(", ")}` : "";
  if (selector.id) return `${selector.id}${via}`;
  const tags = (selector.tags ?? []).filter(Boolean);
  if (tags.length > 0) return `${tags.join(", ")}${via || " (any provider)"}`;
  return providers.length > 0 ? `Any model${via}` : "Any model";
}

/** Drop protobuf-es bookkeeping and empty fields, so what is left is the value. */
function meaningfulEntries(value: Record<string, unknown>): Array<[string, unknown]> {
  return Object.entries(value).filter(([key, field]) => {
    if (key.startsWith("$")) return false;
    if (field === undefined || field === null || field === "") return false;
    if (Array.isArray(field) && field.length === 0) return false;
    if (isPlainObject(field) && meaningfulEntries(field).length === 0) return false;
    return true;
  });
}

function formatScalar(value: string | number | boolean | bigint): string {
  if (typeof value === "boolean") return value ? "On" : "Off";
  if (typeof value === "string") return value === "" ? '""' : value;
  return String(value);
}

/**
 * The default's display text, or undefined when there is none worth showing
 * (no default, or one with nothing in it). Callers render nothing — or a dash
 * under a labelled column — for undefined.
 */
export function formatInputDefault(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return formatScalar(value);
  }
  if (Array.isArray(value)) {
    const items = value.map((item) => formatInputDefault(item)).filter((item): item is string => item !== undefined);
    return items.length > 0 ? items.join(", ") : undefined;
  }
  if (isPlainObject(value)) {
    if (isModelSelector(value)) return formatModelSelector(value);
    const entries = meaningfulEntries(value);
    if (entries.length === 0) return undefined;
    // A wrapper with one meaningful field ({ text: "…" }, { id: "…" }) is that field.
    if (entries.length === 1) return formatInputDefault(entries[0]![1]);
    return entries.map(([key, field]) => `${key}: ${formatInputDefault(field) ?? ""}`).join(" · ");
  }
  return undefined;
}
