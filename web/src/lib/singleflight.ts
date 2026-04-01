/**
 * Singleflight - ensures only one execution is in-flight for a given key at a time.
 *
 * When multiple callers request the same key simultaneously, only one async operation
 * runs and all callers receive the same result. This prevents duplicate API calls
 * during hot reloads, rapid re-renders, or reconnection scenarios.
 *
 * The key insight is that the Map check and set happen SYNCHRONOUSLY before any
 * async work starts, eliminating TOCTOU race conditions.
 *
 * @example
 * ```ts
 * import { singleflight } from './singleflight';
 *
 * async function loadData(id: string) {
 *   return singleflight(`load:${id}`, async () => {
 *     const response = await fetch(`/api/data/${id}`);
 *     return response.json();
 *   });
 * }
 *
 * // Even if called 100 times simultaneously, only 1 fetch occurs
 * await Promise.all(Array(100).fill(null).map(() => loadData('123')));
 * ```
 */

const inflight = new Map<string, Promise<unknown>>();

// Track deduplicated calls for debugging
const dedupeCount = new Map<string, number>();

/**
 * Execute an async function with singleflight deduplication.
 *
 * @param key - Unique key for this operation (e.g., "loadChats:projectId")
 * @param fn - Async function to execute
 * @returns Promise that resolves to the function's result
 */
export function singleflight<T>(key: string, fn: () => Promise<T>): Promise<T> {
  // Synchronous check - if already in-flight, return existing promise
  const existing = inflight.get(key);
  if (existing) {
    // Track deduplicated calls
    const count = (dedupeCount.get(key) || 0) + 1;
    dedupeCount.set(key, count);
    // Log every 100 dedupes or on first dedupe
    if (count === 1 || count % 100 === 0) {
      console.warn(`[singleflight] Deduplicated ${count} calls for "${key}"`);
    }
    return existing as Promise<T>;
  }

  // Reset dedupe count when starting new flight
  dedupeCount.delete(key);
  console.log(`[singleflight] Starting: ${key}`);

  // Synchronous set - register BEFORE starting async work
  // This is critical: no async gap between check and set
  const promise = fn().finally(() => {
    // Log final dedupe count if any
    const finalCount = dedupeCount.get(key) || 0;
    if (finalCount > 0) {
      console.warn(`[singleflight] Completed "${key}" - deduplicated ${finalCount} total calls`);
    } else {
      console.log(`[singleflight] Completed: ${key}`);
    }
    // Clean up after completion (success or failure)
    inflight.delete(key);
    dedupeCount.delete(key);
  });

  inflight.set(key, promise);
  return promise;
}

/**
 * Check if a singleflight operation is currently in-flight.
 * Useful for debugging or UI indicators.
 *
 * @param key - The singleflight key to check
 * @returns true if an operation with this key is in progress
 */
export function isInflight(key: string): boolean {
  return inflight.has(key);
}

/**
 * Get the number of currently in-flight operations.
 * Useful for debugging.
 */
export function getInflightCount(): number {
  return inflight.size;
}

/**
 * Clear all in-flight tracking (for testing purposes only).
 * WARNING: This does not cancel the actual promises.
 */
export function clearInflight(): void {
  inflight.clear();
}
