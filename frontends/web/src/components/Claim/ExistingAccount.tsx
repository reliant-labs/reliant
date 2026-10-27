// Copyright (c) 2025 Reliant Labs

import Button from "@/components/forge-ui/button";

import { EXISTING_ACCOUNT_COPY } from "./claimCopy";

/**
 * The "I already have a Reliant account" branch of a claim. THE ONE PLACE W11
 * replaces: it will enable the button (join the site's org as its owner) and
 * drop the redeploy hint. Until then the button is disabled and the hint is the
 * answer, because joining an org needs users to belong to more than one
 * (design §9 M1a) and a signed-in redeploy gets the same site into their org.
 *
 * Shown where the person has just learned they cannot take the new-account
 * path: signed in already, or an email/provider that is already registered.
 */
export function ExistingAccount() {
  return (
    <div className="space-y-3 rounded-lg border border-border bg-card p-4" data-testid="claim-existing-account">
      <div>
        <p className="text-sm font-medium text-foreground">{EXISTING_ACCOUNT_COPY.heading}</p>
        <p id="claim-existing-account-hint" className="mt-1 text-sm text-muted-foreground">
          Have your agent run <code className="rounded bg-background px-1 py-0.5 font-mono text-xs text-foreground">{EXISTING_ACCOUNT_COPY.command}</code>{" "}
          and deploy again under your account.
        </p>
      </div>
      <Button
        variant="outline"
        fullWidth
        disabled
        aria-describedby="claim-existing-account-hint"
        data-testid="claim-existing-account-button"
      >
        Claim with my existing account
      </Button>
    </div>
  );
}
