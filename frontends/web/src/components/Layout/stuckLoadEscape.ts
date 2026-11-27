import { authPath } from "@/lib/paths";

/**
 * LoadingSpinner's escape hatch when initialization stalls (e.g. backend 401s
 * on a stale token): sign out and go to sign-in. Its own module so the
 * spinner stays decoupled from auth: the store is imported lazily here, and
 * the public demo, which has no sign-in, swaps this module for its twin
 * (stuckLoadEscape.demo.ts, vite-plugin-demo-bundle.ts).
 */
export const stuckLoadEscape = {
  label: "Sign out",
  busyLabel: "Signing out…",
  async run(): Promise<void> {
    try {
      const { useAuthStore } = await import("../../store/authStore");
      await useAuthStore.getState().signOut();
    } catch {
      // ignore — we'll force-redirect below either way
    }
    window.location.href = authPath();
  },
};
