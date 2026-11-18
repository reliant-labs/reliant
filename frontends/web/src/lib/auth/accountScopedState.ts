/**
 * In-memory caches that hold one account's answers and must be emptied in the
 * same synchronous step as an account change (store/authStore).
 *
 * authStore's full cleanup (clearUserScopedState) is asynchronous: it imports
 * stores lazily to avoid cycles. Until it finishes, the previous account's
 * cached answers would still be served, and work started from them would
 * read as the next account's. A cache registers its reset here instead, so
 * authStore can empty it with no import of its own (settingsSync imports
 * modules that import authStore). No imports.
 */

const resets = new Set<() => void>();

/** Register a synchronous reset run on every account change. Returns an unregister. */
export function onAccountChange(reset: () => void): () => void {
  resets.add(reset);
  return () => resets.delete(reset);
}

/** Empty every registered account-scoped cache, now. */
export function resetAccountScopedStateSync(): void {
  for (const reset of resets) reset();
}
