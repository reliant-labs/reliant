// Copyright (c) 2025 Reliant Labs

/**
 * Deep equality for plain data that may hold protobuf-es messages — canvas
 * node data carrying a step, say.
 *
 * Comparing JSON.stringify output is the tempting shortcut, and it throws on
 * a bigint, which is what every int64 field is (an integer input's default,
 * a CelInt literal). This compares values instead: bigints by value, bytes
 * by content, objects by their own keys regardless of order. A key holding
 * `undefined` counts as absent, as it would in JSON.
 */
export function structurallyEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a === "number" && typeof b === "number") return Number.isNaN(a) && Number.isNaN(b);
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;

  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((item, i) => structurallyEqual(item, b[i]));
  }
  if (a instanceof Uint8Array || b instanceof Uint8Array) {
    if (!(a instanceof Uint8Array) || !(b instanceof Uint8Array) || a.length !== b.length) return false;
    return a.every((byte, i) => byte === b[i]);
  }
  if (a instanceof Date || b instanceof Date) {
    return a instanceof Date && b instanceof Date && a.getTime() === b.getTime();
  }

  const definedKeys = (o: object) =>
    Object.keys(o).filter((key) => (o as Record<string, unknown>)[key] !== undefined);
  const aKeys = definedKeys(a);
  const bKeys = definedKeys(b);
  if (aKeys.length !== bKeys.length) return false;
  const aRecord = a as Record<string, unknown>;
  const bRecord = b as Record<string, unknown>;
  return aKeys.every((key) => Object.prototype.hasOwnProperty.call(bRecord, key) && structurallyEqual(aRecord[key], bRecord[key]));
}
