// Copyright (c) 2025 Reliant Labs

import { useState } from "react";

import Button from "@/components/forge-ui/button";
import { hardNavigate } from "@/lib/hardNavigate";
import { logger } from "@/lib/logger";
import { useAuthStore } from "@/store/authStore";

import { EXISTING_ACCOUNT_COPY } from "./claimCopy";

/** Where signing in returns to: the claim page, which picks the claim up from storage. */
export const SIGN_IN_TO_CLAIM_URL = "/auth?redirect=%2Fclaim";

/**
 * Sign out of whatever this browser holds and go to the sign-in screen, which
 * returns here. Used by "I have an account" and by "use a different account".
 * The claim's token is in storage (lib/claimLink.ts), so nothing is lost on the
 * way, and a full page load cannot be overtaken by an AuthGuard that notices
 * the sign-out and redirects to a bare /auth.
 */
export function useSignInToClaim(): { go: () => Promise<void>; working: boolean } {
  const signOut = useAuthStore((s) => s.signOut);
  const [working, setWorking] = useState(false);
  const go = async () => {
    setWorking(true);
    try {
      if (useAuthStore.getState().user) await signOut();
    } catch (err) {
      // A failed sign-out must not trap them here: the sign-in screen is
      // still the way forward, and signing in replaces the session.
      logger.warn("[Claim] sign-out before signing in failed", { message: err instanceof Error ? err.message : "unknown" });
    }
    hardNavigate(SIGN_IN_TO_CLAIM_URL);
  };
  return { go, working };
}

/**
 * The "I already have a Reliant account" branch for someone who is not signed
 * in to it: signed out, or holding the site's own temporary account (on the
 * claim page after the session was released, and on the upgrade screen when the
 * email they pick already has an account). One button: sign in, come back, and
 * ClaimIntoAccount finishes it.
 */
export function ExistingAccount({ leavesTemporaryAccount = false }: { leavesTemporaryAccount?: boolean }) {
  const { go, working } = useSignInToClaim();
  return (
    <div className="space-y-3 rounded-lg border border-border bg-card p-4" data-testid="claim-existing-account">
      <div>
        <p className="text-sm font-medium text-foreground">{EXISTING_ACCOUNT_COPY.heading}</p>
        <p id="claim-existing-account-hint" className="mt-1 text-sm text-muted-foreground">
          {EXISTING_ACCOUNT_COPY.body}
          {leavesTemporaryAccount && ` ${EXISTING_ACCOUNT_COPY.temporary}`}
        </p>
      </div>
      <Button
        variant="outline"
        fullWidth
        loading={working}
        onClick={() => void go()}
        aria-describedby="claim-existing-account-hint"
        data-testid="claim-existing-account-button"
      >
        {EXISTING_ACCOUNT_COPY.button}
      </Button>
    </div>
  );
}
