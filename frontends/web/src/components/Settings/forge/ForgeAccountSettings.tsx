// Settings › Account when forge's login broker signs people in
// (AUTH_PROVIDER "forge"). Everything here is the broker's account routes
// (lib/auth/account.ts — forge#2663's API): the address, the password,
// connected GitHub/Google, passkeys and the places the account is signed in.
// Every "needs a recent proof" action goes through RecentProof's dialog.
//
// Signing out and deleting the account are the same as in Supabase mode: the
// auth store's signOut, and reliant's AccountService (DeleteAccountDialog).

import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { LogOut, Trash2, User } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { fetchAuthOptions } from "@/lib/auth/native-login";
import { useAuth } from "@/lib/auth/use-auth";
import { authPath } from "@/lib/paths";
import { useAuthStore } from "@/store/authStore";
import { DeleteAccountDialog } from "../DeleteAccountDialog";
import { SettingsPageHeader } from "../SettingsPageHeader";
import { ConnectedAccountsSection } from "./ConnectedAccountsSection";
import { EmailSection } from "./EmailSection";
import { PasskeysSection } from "./PasskeysSection";
import { PasswordSection } from "./PasswordSection";
import { RecentProofProvider } from "./RecentProof";
import { SessionsSection } from "./SessionsSection";
import { InlineError } from "./parts";
import { useForgeAccount } from "./useForgeAccount";

export function ForgeAccountSettings() {
  const navigate = useNavigate();
  const { identity, signOut } = useAuth();
  const { load, reload } = useForgeAccount();
  const [passkeysOffered, setPasskeysOffered] = useState(false);
  const [isSigningOut, setIsSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<string | null>(null);
  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);

  useEffect(() => {
    let live = true;
    void fetchAuthOptions().then((o) => live && setPasskeysOffered(o.methods.includes("passkey")));
    return () => {
      live = false;
    };
  }, []);

  // An address change moves the identity the rest of the app shows: re-read it.
  const onAccountChanged = useCallback(() => {
    reload();
    void useAuthStore.getState().refreshSession?.().catch(() => {});
  }, [reload]);

  const handleSignOut = async () => {
    setIsSigningOut(true);
    setSignOutError(null);
    try {
      await signOut();
      navigate({ to: authPath(), search: { redirect: undefined } });
    } catch (error) {
      console.error("Sign out failed:", error);
      setSignOutError("Couldn't sign out. Please try again.");
    } finally {
      setIsSigningOut(false);
    }
  };

  const handleAccountDeleted = async () => {
    setIsDeleteDialogOpen(false);
    try {
      await signOut();
    } catch (error) {
      console.error("Sign out after account deletion failed:", error);
    }
    navigate({ to: authPath(), search: { redirect: undefined } });
  };

  const account = load.state === "ready" ? load.account : null;
  const email = account?.email || identity?.email || "";

  return (
    <RecentProofProvider>
      <div className="max-w-[var(--form-max)] space-y-6">
        <SettingsPageHeader
          title="Account"
          description="Your sign-in details: your address, password, connected accounts, passkeys and the devices you're signed in on."
        />

        <div className="space-y-4 rounded-xl border border-border bg-card p-6" data-testid="forge-account-settings">
          <div className="flex items-start gap-3">
            <div className="rounded-md border border-border/60 bg-background p-2">
              <User className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
            </div>
            <div className="min-w-0">
              <p className="truncate text-ui font-semibold text-foreground">{identity?.name || email || "Signed in"}</p>
              {identity?.name && email && <p className="truncate text-meta text-muted-foreground">{email}</p>}
            </div>
          </div>

          {load.state === "loading" && (
            <p className="text-meta text-muted-foreground" role="status">
              Loading your account…
            </p>
          )}
          {load.state === "error" && (
            <div className="space-y-2">
              <InlineError message={load.message} />
              <Button variant="outline" size="sm" onClick={reload}>
                Try again
              </Button>
            </div>
          )}
          {account && (
            <div className="space-y-4 border-t border-border-subtle pt-4">
              <EmailSection email={account.email} emailVerified={account.emailVerified} onChanged={onAccountChanged} />
              <PasswordSection hasPassword={account.hasPassword} onChanged={reload} />
              <ConnectedAccountsSection account={account} onChanged={reload} />
              <PasskeysSection passkeys={account.passkeys} offered={passkeysOffered} onChanged={reload} />
              <SessionsSection sessions={account.sessions} onChanged={reload} />
            </div>
          )}

          <div className="space-y-2 border-t border-border-subtle pt-4">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void handleSignOut()}
              disabled={isSigningOut}
              leftIcon={<LogOut className="h-3.5 w-3.5" />}
            >
              {isSigningOut ? "Signing out…" : "Sign out"}
            </Button>
            <InlineError message={signOutError} />
          </div>
        </div>

        <div className="space-y-4 rounded-xl border border-destructive/30 bg-card p-6">
          <div>
            <h3 className="mb-1 text-ui font-semibold text-foreground">Delete account</h3>
            <p className="text-ui text-muted-foreground">
              Permanently delete your projects, chats and connected provider accounts. This cannot be undone.
            </p>
          </div>
          <Button
            variant="danger"
            size="sm"
            onClick={() => setIsDeleteDialogOpen(true)}
            leftIcon={<Trash2 className="h-3.5 w-3.5" />}
          >
            Delete account
          </Button>
        </div>

        <DeleteAccountDialog
          isOpen={isDeleteDialogOpen}
          onClose={() => setIsDeleteDialogOpen(false)}
          onDeleted={() => void handleAccountDeleted()}
        />
      </div>
    </RecentProofProvider>
  );
}
