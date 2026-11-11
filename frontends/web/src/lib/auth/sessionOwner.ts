/**
 * Whose account this page is acting for, and a check that a credential still
 * belongs to them.
 *
 * Work this page starts for one user can finish after another user's session
 * replaced theirs: with the shared session cookie (sharedSession.ts) a sign-in
 * as someone else on another host swaps the credential under a write that is
 * still waiting. The transport attaches whatever credential is current when a
 * request goes out, so a write queued for A and sent after the swap lands in
 * B's account. A writer captures the owner when it queues the work
 * (currentSessionOwner) and, right before sending, asks whether the
 * credential it is about to send with still belongs to that owner
 * (credentialBelongsTo).
 *
 * authStore sets the owner when it installs an identity, after clearing the
 * previous user's state. No imports: authStore and the settings layer both
 * read it without an import cycle.
 */

/** The owner a writer captured: compare it later with credentialBelongsTo. */
export interface SessionOwner {
  readonly userId: string | null;
  readonly epoch: number;
}

let owner: SessionOwner = { userId: null, epoch: 0 };

/** The account the page acts for now. Synchronous, so a writer can capture it as it queues. */
export function currentSessionOwner(): SessionOwner {
  return owner;
}

/**
 * Record the account the page now acts for (authStore, on every installed or
 * cleared identity). A different user bumps the epoch, which makes everything
 * captured for the previous one stale.
 */
export function setSessionOwner(userId: string | null): void {
  if (userId === owner.userId) return;
  owner = { userId, epoch: owner.epoch + 1 };
}

/** The `sub` claim of a JWT, or undefined for a credential that is not one (an API key). */
function jwtSubject(token: string): string | null | undefined {
  const parts = token.split(".");
  if (parts.length !== 3) return undefined;
  try {
    const base64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const padded = base64 + "=".repeat((4 - (base64.length % 4)) % 4);
    const claims = JSON.parse(atob(padded)) as { sub?: unknown };
    return typeof claims.sub === "string" ? claims.sub : null;
  } catch {
    return undefined;
  }
}

/**
 * Whether `token` may carry work captured for `captured`: the page still acts
 * for the same account, and a session token names that account. A session
 * token for anyone else (the cookie moved to another user before authStore
 * saw it), or one with no owner captured, fails. A credential that is not a
 * JWT (a long-lived API key, never shared through the cookie) is judged by
 * the owner alone.
 */
export function credentialBelongsTo(captured: SessionOwner, token: string): boolean {
  if (captured.epoch !== owner.epoch) return false;
  const subject = jwtSubject(token);
  if (subject === undefined) return true;
  return subject !== null && subject === captured.userId;
}

/** Test seam: forget the recorded owner. */
export function resetSessionOwnerForTests(): void {
  owner = { userId: null, epoch: 0 };
}
