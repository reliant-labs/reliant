// Copyright (c) 2025 Reliant Labs

import Button from "@/components/forge-ui/button";

import { CLAIM_INTO_ACCOUNT_COPY } from "./claimCopy";
import { useSignInToClaim } from "./ExistingAccount";

interface Props {
  /** The signed-in account's email, when the session carries one. */
  email: string | undefined;
  working: boolean;
  onClaim: () => void;
}

/**
 * Claim path 2 (design §4): the person is signed in to a real account, so
 * claiming joins the site's organization as its owner instead of upgrading a
 * temporary account. It says so before they confirm, and offers the way out
 * when this is not the account they meant.
 */
export function ClaimIntoAccount({ email, working, onClaim }: Props) {
  const other = useSignInToClaim();
  return (
    <div className="space-y-4" data-testid="claim-into-account">
      <div className="space-y-1 rounded-lg border border-border bg-card p-4">
        <p className="text-sm font-medium text-foreground">{CLAIM_INTO_ACCOUNT_COPY.heading}</p>
        {email && (
          <p className="text-sm text-foreground" data-testid="claim-signed-in-as">
            {CLAIM_INTO_ACCOUNT_COPY.signedInAs(email)}
          </p>
        )}
        <p className="text-sm text-muted-foreground">{CLAIM_INTO_ACCOUNT_COPY.body}</p>
        <p className="text-sm text-muted-foreground">{CLAIM_INTO_ACCOUNT_COPY.freeSite}</p>
      </div>
      <Button
        onClick={onClaim}
        loading={working}
        variant="primary"
        size="xl"
        fullWidth
        // Opened mostly from a phone: the one action on the page clears the 44px touch minimum.
        className="!h-12"
        data-testid="claim-into-account-button"
      >
        {CLAIM_INTO_ACCOUNT_COPY.button}
      </Button>
      <div className="text-center">
        <button
          type="button"
          onClick={() => void other.go()}
          disabled={other.working || working}
          className="text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
          data-testid="claim-switch-account"
        >
          {CLAIM_INTO_ACCOUNT_COPY.switchAccount}
        </button>
      </div>
    </div>
  );
}
