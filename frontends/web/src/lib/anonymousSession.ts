import { getAuthIdentity } from "@/lib/auth/use-auth";

/**
 * Only genuine anonymous Supabase sessions are anonymous; api-key / mock /
 * dev synthetic users and forge sessions are not, and must not be pushed
 * through an identity link they cannot complete. Read imperatively so the
 * check sees the session as it is at the moment of the action.
 */
export function isAnonymousSession(): boolean {
  return getAuthIdentity()?.isAnonymous === true;
}
