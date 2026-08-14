import { useNavigate } from "@tanstack/react-router";

import { useAuthStore } from "@/store/authStore";

/**
 * Navigate to billing, routing anonymous sessions through identity linking
 * first.
 *
 * WHY THE DETOUR: free-tier users run on anonymous Supabase sessions. Billing
 * is not a page an anonymous user can meaningfully complete — a subscription
 * bought against a browser-session identity belongs to nobody reachable, and
 * losing the session loses the purchase along with the work. Sending them
 * straight to /settings/billing is a dead end they discover only after
 * committing.
 *
 * So: anonymous callers go to the EXISTING /upgrade flow (which links a real
 * identity onto the current anonymous account, preserving their work) with
 * returnTo pointed at billing, so they land where they were headed once they
 * have an identity. Everyone else goes straight there.
 *
 * Returns a callback rather than a component so any affordance — button, link,
 * menu item — gets the same behavior without duplicating the anonymous check.
 * Duplicating it is how one caller ends up dead-ending anonymous users.
 */
export function useGoToBilling(): () => void {
  const navigate = useNavigate();
  const user = useAuthStore((s) => s.user);

  return () => {
    // Only genuine anonymous Supabase sessions carry is_anonymous === true;
    // api-key / mock / dev synthetic users set it false, so they are treated
    // as signed in (same rule as useAnonSignInNudge).
    const isAnonymous = !!user && user.is_anonymous === true;

    if (isAnonymous) {
      void navigate({
        to: "/upgrade",
        search: { returnTo: "/settings/billing" },
      });
      return;
    }

    void navigate({ to: "/settings/$section", params: { section: "billing" } });
  };
}
