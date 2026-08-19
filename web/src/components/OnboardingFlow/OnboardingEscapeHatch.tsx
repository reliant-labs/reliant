/**
 * The way out of `/onboarding`.
 *
 * Onboarding is a `fixed inset-0 z-40` overlay on its own route, so none of
 * the app chrome that normally carries a Settings link renders behind it —
 * the Header is suppressed (`showHeader` gates on `!inOnboarding`) and the
 * Sidebar needs a selected project, which is precisely what a user stuck here
 * does not have.
 *
 * That made the flow a dead end whenever it could not be completed: a user
 * whose daemon never provisions cannot finish onboarding, cannot reach
 * `/settings`, and therefore cannot sign out. Sign-out in particular has to
 * work from here, because switching accounts is the one repair a stranded
 * user can perform without our help.
 *
 * `/settings` is reachable mid-onboarding: it is gated on auth alone, and the
 * only redirects back to `/onboarding` come from `ModernApp` (which mounts on
 * `/` and `/project/$projectId`) and `MobileShell` (`/m/*`), so neither fires
 * on the settings route.
 */
import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { LogOut, Settings } from "lucide-react";
import { cn } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { useAuthStore } from "@/store/authStore";

export function OnboardingEscapeHatch({ className }: { className?: string }) {
  const navigate = useNavigate();
  const signOut = useAuthStore((s) => s.signOut);
  const [isSigningOut, setIsSigningOut] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSignOut = async () => {
    if (isSigningOut) return;
    setIsSigningOut(true);
    setError(null);
    try {
      await signOut();
      // Navigate explicitly rather than leaving it to AuthGuard. The guard
      // does bounce an unauthenticated user eventually, but there is a render
      // gap in which this route's own "needs onboarding" logic can re-assert
      // itself and put the user straight back here.
      await navigate({ to: "/auth", search: { redirect: undefined } });
    } catch (err) {
      logger.warn("[OnboardingEscapeHatch] sign out failed", err);
      setError(err instanceof Error ? err.message : "Could not sign out");
      setIsSigningOut(false);
    }
  };

  const buttonClass =
    "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium " +
    "text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground " +
    "disabled:cursor-not-allowed disabled:opacity-60";

  return (
    <div className={cn("flex flex-col items-end gap-1", className)}>
      <div className="flex items-center gap-1">
        <button
          type="button"
          onClick={() => navigate({ to: "/settings" })}
          className={buttonClass}
          data-testid="onboarding-settings-link"
        >
          <Settings className="h-3.5 w-3.5" />
          Settings
        </button>
        <button
          type="button"
          onClick={handleSignOut}
          disabled={isSigningOut}
          className={buttonClass}
          data-testid="onboarding-sign-out"
        >
          <LogOut className="h-3.5 w-3.5" />
          {isSigningOut ? "Signing out…" : "Sign out"}
        </button>
      </div>
      {error && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
