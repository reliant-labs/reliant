import { useAuthStore } from "@/store/authStore";

/**
 * Only genuine anonymous Supabase sessions carry `is_anonymous === true`;
 * api-key / mock / dev synthetic users set it false and must not be pushed
 * through an identity link they cannot complete. Read imperatively so the
 * check sees the session as it is at the moment of the action.
 */
export function isAnonymousSession(): boolean {
  const user = useAuthStore.getState().user as { is_anonymous?: boolean } | null;
  return user?.is_anonymous === true;
}
