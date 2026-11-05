// Retry-safe ids for sent messages.
//
// The id is how the server tells a repeat of a send from a new message: a
// send whose response was lost may well have been saved, and sending it again
// under a new id saves it twice. So an id belongs to the LOGICAL send — the
// same message to the same place — and every attempt at that send reuses it.
//
// A send that fails keeps its id for the next attempt at the same message, and
// a send that succeeds retires it. The key says what "the same message" is, so
// a message the user edited, or sent somewhere else, is a new one: the server
// refuses one id for two messages. Kept in memory.

/** A fresh id for a message the client is about to send. */
export function newClientMessageId(): string {
  return crypto.randomUUID();
}

export interface AttemptIds {
  /** The id for this logical send: the one an earlier failed attempt used, else a new one. */
  idFor(key: string): string;
  /** The send is settled (it succeeded, or the id is spent): the next one is new. */
  end(key: string): void;
  /** Forget every kept id. For sign-out. */
  clear(): void;
}

const MAX_KEPT_ATTEMPTS = 32;

export function createAttemptIds(newId: () => string = newClientMessageId): AttemptIds {
  const ids = new Map<string, string>();
  return {
    idFor(key) {
      const kept = ids.get(key);
      if (kept) return kept;
      const id = newId();
      ids.set(key, id);
      if (ids.size > MAX_KEPT_ATTEMPTS) {
        const oldest = ids.keys().next().value;
        if (oldest !== undefined) ids.delete(oldest);
      }
      return id;
    },
    end(key) {
      ids.delete(key);
    },
    clear() {
      ids.clear();
    },
  };
}

// ── Transcript ids of a send ────────────────────────────────────────────────
//
// A send in flight is an optimistic transcript entry, retired by any arriving
// user message (mergeMessages). A send that failed for good is renamed to the
// failed prefix and marked "Not sent", with Retry and Remove. It keeps the
// "optimistic-" prefix on purpose — it has no server row, so everything that
// skips local-only messages skips it too — but NOT "optimistic-user-": no
// arriving message, and no snapshot, describes it, so nothing may retire it
// except the user.

export const OPTIMISTIC_USER_PREFIX = "optimistic-user-";
export const FAILED_SEND_PREFIX = "optimistic-failed-";

export function optimisticSendId(clientMessageId: string): string {
  return OPTIMISTIC_USER_PREFIX + clientMessageId;
}

export function failedSendId(clientMessageId: string): string {
  return FAILED_SEND_PREFIX + clientMessageId;
}

/**
 * A transcript entry that exists only in this client — a send in flight or one
 * that failed for good. It has no server row, so no snapshot or replay is
 * coming to describe it.
 */
export function isLocalOnlyMessageId(messageId: string): boolean {
  return messageId.startsWith("optimistic-");
}

/** The client message id of a failed send's transcript entry, or null. */
export function clientIdOfFailedSend(messageId: string): string | null {
  return messageId.startsWith(FAILED_SEND_PREFIX)
    ? messageId.slice(FAILED_SEND_PREFIX.length)
    : null;
}
