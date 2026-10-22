import type { AuthProvider, AuthUser } from "./provider";
import { useAuthStore } from "../../store/authStore";
import { supabase } from "../supabase";
import type { User } from "@supabase/supabase-js";

function mapUser(user: User | null): AuthUser | null {
  if (!user) return null;
  return {
    id: user.id,
    email: user.email ?? undefined,
    name:
      user.user_metadata?.full_name ??
      user.user_metadata?.name ??
      undefined,
  };
}

export function createSupabaseAuthProvider(): AuthProvider {
  return {
    getToken: async () => {
      const { data } = await supabase.auth.getSession();
      return data.session?.access_token ?? null;
    },
    getUser: () => {
      const state = useAuthStore.getState();
      return mapUser(state.user);
    },
    isAuthenticated: () => {
      const state = useAuthStore.getState();
      return !!state.session;
    },
    isLoading: () => {
      const state = useAuthStore.getState();
      return state.loading;
    },
    login: async () => {
      // Delegate to the existing store's signIn flow
      // Actual login is handled by AuthScreen
      throw new Error("Use the AuthScreen component for login");
    },
    logout: async () => {
      await useAuthStore.getState().signOut();
    },
    onAuthStateChange: (callback) => {
      // Subscribe to Zustand store changes and map to AuthUser
      return useAuthStore.subscribe((state) => {
        callback(mapUser(state.user));
      });
    },
  };
}
