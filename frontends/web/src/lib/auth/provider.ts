import type { SessionUser } from "../session";

/**
 * @deprecated Use `SessionUser` from `lib/session`. The auth interface moved
 * out of lib/auth/ into the session port; this alias stays one release so
 * anything importing the type keeps compiling.
 */
export type AuthUser = SessionUser;
