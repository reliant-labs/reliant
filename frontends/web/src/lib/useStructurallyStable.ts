// Copyright (c) 2025 Reliant Labs

import { useRef } from "react";

/**
 * Whether two values hold the same data: arrays and plain objects compared
 * member by member, everything else (functions, class instances, dates) by
 * identity, numbers with Object.is (so NaN equals NaN).
 */
export function structurallyEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!structurallyEqual(a[i], b[i])) return false;
    return true;
  }
  if (Array.isArray(b) || Object.getPrototypeOf(a) !== Object.prototype || Object.getPrototypeOf(b) !== Object.prototype) return false;
  const ka = Object.keys(a);
  if (ka.length !== Object.keys(b).length) return false;
  for (const k of ka) {
    if (!Object.prototype.hasOwnProperty.call(b, k)) return false;
    if (!structurallyEqual((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k])) return false;
  }
  return true;
}

/**
 * `value`, or the previous render's when it holds the same data: for a prop
 * the caller rebuilds every render (a chart's series) that keys expensive
 * work (rebuilding the chart) on identity.
 */
export function useStructurallyStable<T>(value: T): T {
  const ref = useRef(value);
  if (!structurallyEqual(ref.current, value)) ref.current = value;
  return ref.current;
}
