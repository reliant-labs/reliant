/**
 * Binds a write to the account it was made for, at the last point before it
 * leaves the page.
 *
 * A writer that queued work for user A checks the credential before it sends
 * (settingsPersistence.mayWriteFor), but the transport's auth stage then
 * resolves the credential AGAIN, and with the shared session cookie
 * (lib/auth/sharedSession.ts) that second wait can end holding user B's
 * session. A check made before the send cannot see that; only a check against
 * the header actually attached can.
 *
 * So the writer names its owner on the call (`contextValues` with
 * writeOwnerKey), and writeOwnerInterceptor, the innermost stage of every
 * authed chain (see transport.ts), compares that owner with the Authorization
 * header on the request about to go out. Innermost means it also sees every
 * re-send made below the auth stage (unauthInterceptor's refreshed-token and
 * tokenless retries), and the deadline's fresh requests re-run the whole
 * inner chain. A call that names no owner is untouched.
 */
import { Code, ConnectError, createContextKey, type Interceptor } from "@connectrpc/connect";
import { credentialBelongsTo, type SessionOwner } from "../lib/auth/sessionOwner";

/** The owner a write was queued for; unset (null) for every other call. */
export const writeOwnerKey = createContextKey<SessionOwner | null>(null, {
  description: "the account a write was made for",
});

const WRITE_OWNER_MISMATCH =
  "write dropped: the credential no longer belongs to the account it was made for";

/** Whether `error` is writeOwnerInterceptor refusing to send a write. */
export function isWriteOwnerMismatch(error: unknown): boolean {
  return (
    error instanceof ConnectError &&
    error.code === Code.Aborted &&
    error.rawMessage === WRITE_OWNER_MISMATCH
  );
}

function presentedToken(header: Headers): string | null {
  const value = header.get("Authorization");
  if (!value?.startsWith("Bearer ")) return null;
  return value.slice("Bearer ".length);
}

export const writeOwnerInterceptor: Interceptor = (next) => async (req) => {
  // Hand-built requests in tests carry no contextValues at all.
  const owner = req.contextValues?.get(writeOwnerKey) ?? null;
  if (owner !== null) {
    const token = presentedToken(req.header);
    if (token === null || !credentialBelongsTo(owner, token)) {
      throw new ConnectError(WRITE_OWNER_MISMATCH, Code.Aborted);
    }
  }
  return await next(req);
};
