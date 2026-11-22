import type { AuthProvider, AuthUser } from "./provider";
import { useAuthStore } from "../../store/authStore";
import type { AuthIdentity } from "./identity";

function mapIdentity(identity: AuthIdentity | null): AuthUser | null {
  if (!identity) return null;
  return { id: identity.id, email: identity.email ?? undefined, name: identity.name ?? undefined };
}

/**
 * The AuthProvider seam (lib/auth/provider.ts) for forge sign-in: the session
 * is the login broker's cookie, so there is no token to hand out (getToken
 * answers null and the transport sends the cookie instead), and who is signed
 * in is the auth store's identity, which the broker's /auth/session fills.
 */
export function createForgeAuthProvider(): AuthProvider {
  return {
    getToken: async () => null,
    getUser: () => mapIdentity(useAuthStore.getState().identity),
    isAuthenticated: () => useAuthStore.getState().identity !== null,
    isLoading: () => useAuthStore.getState().loading,
    login: async () => {
      // The sign-in screen (components/Auth/forge) runs the broker's flows.
      throw new Error("Use the sign-in screen for login");
    },
    logout: async () => {
      await useAuthStore.getState().signOut();
    },
    onAuthStateChange: (callback) =>
      useAuthStore.subscribe((state, previous) => {
        if (state.identity !== previous.identity) callback(mapIdentity(state.identity));
      }),
  };
}
