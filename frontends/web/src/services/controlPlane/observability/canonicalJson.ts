// Copyright (c) 2025 Reliant Labs

/**
 * `value` as the console sends a dashboard: every undefined, null, empty
 * string, empty list, false and object left empty by those dropped,
 * recursively. It is forge's own canonical form (its omitempty fields), and
 * the form forge's validator judges: a field present but empty is one it
 * refuses on a kind that takes none (`"series": []` on a text panel), so the
 * console never sends one.
 *
 * A list keeps every item where it is (an empty funnel step is the author's,
 * and an error to show, not to hide): only the objects in it are
 * canonicalized.
 */
export function canonicalJson(value: unknown): unknown {
  if (value === undefined || value === null || value === "" || value === false) return undefined;
  if (Array.isArray(value)) {
    return value.length ? value.map((item) => (item && typeof item === "object" ? (canonicalJson(item) ?? {}) : item)) : undefined;
  }
  if (typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, field] of Object.entries(value as Record<string, unknown>)) {
      const kept = canonicalJson(field);
      if (kept !== undefined) out[key] = kept;
    }
    return Object.keys(out).length ? out : undefined;
  }
  if (typeof value === "number" && !Number.isFinite(value)) return undefined;
  return value;
}
