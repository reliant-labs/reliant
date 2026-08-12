/**
 * `/m/account` — who you are signed in as, and how to stop being.
 *
 * The mobile surface declares `settings: false` and `mobileAccount: true`, and
 * this screen is the whole of the second one. It is not a trimmed settings
 * page: the full tree is fifteen sections of authoring and integration config
 * that has no business on a phone. What a phone genuinely cannot do without is
 * ending a session — a user whose provider credential expired, or who is
 * signed into the wrong account, otherwise has to find a desktop.
 *
 * Sign-out delegates to `authStore.signOut`, which is the same call the
 * desktop `AccountSettings` makes. That store action resets seven other stores
 * in a specific order (navigation first, so chat cleanup fires before the chat
 * store is emptied); reimplementing any part of it here would leave a
 * half-cleared app behind on a surface that has no way to recover from one.
 *
 * The desktop component is deliberately NOT imported: it pulls in
 * `LinkedAccounts` and the identity-linking OAuth flows, which are a settings
 * concern and would drag the shell with them.
 *
 * Rendered two ways: as the standalone `/m/account` route (no `onBack`,
 * header has no back button), and folded into `MobileSettingsScreen` as its
 * "Account" section (`onBack` provided). The account/sign-out UI is
 * identical either way — only the header's back affordance differs.
 */

import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { LogOut, Moon, Sun, User } from "lucide-react";
import { useAuthStore } from "../../store/authStore";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { cn } from "../../lib/utils";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MobileBackButton,
  MobileCardGroup,
  MobileRowIcon,
  MobileScreenBody,
  MobileScreenHeader,
} from "./MobileChrome";

/**
 * Read the mode from the DOM rather than from settings.
 *
 * `index.html` applies the persisted theme (or the OS preference) to
 * `<html class="dark">` before React mounts, and `useThemeInitialization` may
 * later re-apply it from the database. The class is therefore the only value
 * that is always correct at first paint; the settings key is empty for a user
 * who has never chosen explicitly, and reading that would render the toggle in
 * the wrong position on every OS-dark phone.
 */
function currentlyDark(): boolean {
  return document.documentElement.classList.contains("dark");
}

function ThemeToggle() {
  const [dark, setDark] = useState(currentlyDark);

  const apply = (next: boolean) => {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    void settingsSync
      .setSetting(SETTINGS_KEYS.THEME, next ? "dark" : "light")
      .catch(() => {
        // The class is already applied and localStorage already holds the
        // value, so a failed database sync costs this device nothing — it just
        // won't follow the user to another one. Not worth an error banner on
        // the one screen someone opens when something else has gone wrong.
      });
    // The same pair AppearanceSettings emits. Monaco, the diff viewers and the
    // header all re-read their palettes off these, and they are mounted
    // whenever a chat is open behind this screen.
    window.dispatchEvent(new CustomEvent("theme-applied"));
    window.dispatchEvent(new CustomEvent("appearance-updated"));
  };

  return (
    <div className="flex min-h-16 items-center justify-between gap-4 px-4 py-3">
      <span className="text-sm text-foreground">Theme</span>
      <div className="flex items-center gap-1 rounded-lg border border-border p-1">
        <button
          type="button"
          onClick={() => apply(false)}
          aria-pressed={!dark}
          className={cn(
            "flex min-h-[44px] min-w-[44px] items-center justify-center gap-1.5 rounded-md px-3 text-sm",
            // `bg-primary/10`, not `bg-muted` — this row now lives inside a
            // `MobileCardGroup`, and in dark mode `--surface-raised` (the
            // card's own background) resolves to `--muted`, so a `bg-muted`
            // selected state would disappear into the card around it.
            !dark
              ? "bg-primary/10 font-medium text-foreground"
              : "text-muted-foreground",
          )}
        >
          <Sun className="h-4 w-4" />
          Light
        </button>
        <button
          type="button"
          onClick={() => apply(true)}
          aria-pressed={dark}
          className={cn(
            "flex min-h-[44px] min-w-[44px] items-center justify-center gap-1.5 rounded-md px-3 text-sm",
            dark
              ? "bg-primary/10 font-medium text-foreground"
              : "text-muted-foreground",
          )}
        >
          <Moon className="h-4 w-4" />
          Dark
        </button>
      </div>
    </div>
  );
}

interface MobileAccountScreenProps {
  /**
   * When provided, this screen is rendered as a sub-screen of
   * `MobileSettingsScreen` (the "Account" row) instead of the standalone
   * `/m/account` route, and the header shows a back chevron that calls this
   * instead of navigating.
   */
  onBack?: () => void;
}

export function MobileAccountScreen({ onBack }: MobileAccountScreenProps = {}) {
  const navigate = useNavigate();
  const user = useAuthStore((s) => s.user);
  const signOut = useAuthStore((s) => s.signOut);

  const [confirming, setConfirming] = useState(false);
  const [isSigningOut, setIsSigningOut] = useState(false);
  const [error, setError] = useState("");

  const name =
    (user?.user_metadata?.full_name as string | undefined) ??
    (user?.user_metadata?.name as string | undefined);
  const email = user?.email;
  const providers = [
    ...new Set(
      (user?.identities ?? [])
        .map((i) => i.provider)
        .filter((p) => p !== "anonymous"),
    ),
  ];

  const handleSignOut = async () => {
    setIsSigningOut(true);
    setError("");
    try {
      await signOut();
      // Navigate explicitly rather than leaving it to AuthGuard. The guard
      // does eventually bounce an unauthenticated user, but there is a render
      // gap where MobileShell — now project-less — re-runs its onboarding
      // check and can throw the user at `/onboarding` instead of `/auth`.
      await navigate({ to: "/auth", search: { redirect: undefined } });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not sign out");
      setIsSigningOut(false);
      setConfirming(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="Account"
        leading={
          onBack ? (
            <MobileBackButton onClick={onBack} label="Back to settings" />
          ) : (
            // Reached directly from the drawer rather than nested under
            // Settings. Without an affordance here the screen is inescapable
            // except via the browser's back gesture.
            <MobileMenuButton />
          )
        }
      />

      <MobileScreenBody>
        <MobileCardGroup>
          <div className="flex min-h-16 items-center gap-3 px-4 py-3">
            <MobileRowIcon icon={User} />
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-foreground">
                {name || email || "Signed in"}
              </p>
              <p className="truncate text-xs text-muted-foreground">
                {/* Falling back to the provider list keeps this honest for an
                    OAuth identity that exposes no email, and for the anonymous
                    sessions the app hands out before an upgrade. */}
                {name && email
                  ? email
                  : providers.length > 0
                    ? `Connected via ${providers.join(", ")}`
                    : "Anonymous session"}
              </p>
            </div>
          </div>
          <ThemeToggle />
        </MobileCardGroup>

        <MobileCardGroup>
          <div className="p-4">
            {confirming ? (
              // Two taps, not a `confirm()` dialog: the native one is unstyled
              // on mobile Safari and, on a phone, it lands under the thumb that
              // just tapped Sign out.
              <div className="flex flex-col gap-2">
                <p className="text-sm text-muted-foreground">
                  Sign out of this account?
                </p>
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => setConfirming(false)}
                    disabled={isSigningOut}
                    className="flex min-h-[44px] flex-1 items-center justify-center rounded-lg border border-border text-sm font-medium text-foreground active:bg-foreground/5 disabled:opacity-60"
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    onClick={() => void handleSignOut()}
                    disabled={isSigningOut}
                    className="flex min-h-[44px] flex-1 items-center justify-center rounded-lg bg-destructive text-sm font-medium text-destructive-foreground active:opacity-80 disabled:opacity-60"
                  >
                    {isSigningOut ? "Signing out…" : "Sign out"}
                  </button>
                </div>
              </div>
            ) : (
              <button
                type="button"
                onClick={() => setConfirming(true)}
                className="flex min-h-[44px] w-full items-center justify-center gap-2 rounded-lg border border-border text-sm font-medium text-destructive active:bg-foreground/5"
              >
                <LogOut className="h-4 w-4" />
                Sign out
              </button>
            )}

            {error && (
              <p className="mt-2 text-center text-xs text-destructive">{error}</p>
            )}
          </div>
        </MobileCardGroup>
      </MobileScreenBody>
    </div>
  );
}
