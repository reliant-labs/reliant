import type { SessionAuth, SessionUser } from "./port";

/**
 * A session that is always `user`, signed in and settled, with no bearer
 * token: the public demo's visitor, and a signed-in user in tests. Nothing
 * behind it can sign out or refresh, so it never changes.
 */
export function fixedSession(user: SessionUser): SessionAuth {
  return { user, isAuthenticated: true, isLoading: false, getToken: async () => null };
}
