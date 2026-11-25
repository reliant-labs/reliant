/**
 * A session that is always `user`, signed in and settled, with no bearer
 * token: the public demo's visitor, and a signed-in user in tests. Nothing
 * behind it can sign out or refresh, so it never changes. forge-web-runtime's
 * `fixedSession`: build it once, so the provider resolves its token once.
 */
export { fixedSession } from "@reliantlabs/forge-web-runtime/session";
