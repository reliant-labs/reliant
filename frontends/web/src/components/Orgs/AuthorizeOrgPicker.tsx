// Copyright (c) 2025 Reliant Labs

import { useEffect, useState } from "react";

import { useMyOrgs } from "@/hooks/useOrgs";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { OrgRow } from "./OrgSwitcher";

/**
 * Which org a credential being approved will act in (CLI login, a paired
 * machine). A token is bound to ONE org when it is minted and never changes,
 * so this is the one place the choice must be explicit and visible.
 *
 * It is the page's own choice, not the app's active org: authorizing a CLI for
 * org B does not move the web to org B. It starts on the org the CLI asked for
 * (`requested`, forge's `--org`) when that is one of the person's orgs, else
 * on their default; and a person with one org sees a plain sentence instead of
 * a choice.
 */
export function useAuthorizeOrg(requested: string | null) {
  const mine = useMyOrgs();
  const orgs = mine.data?.orgs ?? [];
  const [chosen, setChosen] = useState<string | null>(null);

  // The CLI asked for an org the person is not in: say nothing different here
  // (the server refuses it the same way as an unknown one); start on the default.
  const initial =
    (requested && orgs.some((o) => o.id === requested) ? requested : null) ??
    (mine.data?.defaultOrgId && orgs.some((o) => o.id === mine.data?.defaultOrgId) ? mine.data.defaultOrgId : null);

  useEffect(() => {
    if (chosen && !orgs.some((o) => o.id === chosen)) setChosen(null);
  }, [chosen, orgs]);

  const orgId = chosen ?? initial;
  return {
    orgs,
    orgId,
    org: orgs.find((o) => o.id === orgId) ?? null,
    choose: setChosen,
    retry: () => { void mine.refetch(); },
    loading: mine.isLoading,
    /** Approval fails closed until the person can see the org being authorized. */
    failed: Boolean(mine.error),
    ready: !mine.error && Boolean(orgId),
  };
}

export function AuthorizeOrgPicker({
  orgs,
  orgId,
  onChoose,
  disabled,
  noun,
  error,
  onRetry,
}: {
  error?: boolean;
  onRetry?: () => void;
  orgs: MyOrg[];
  orgId: string | null;
  onChoose: (id: string) => void;
  disabled?: boolean;
  /** What is being authorized, for the sentence: "this login", "this machine". */
  noun: string;
}) {
  if (error) return (
    <div role="alert" className="text-sm text-destructive">
      Could not load your organizations. Approval is unavailable until the organization can be confirmed.
      <button type="button" className="ml-2 underline" onClick={onRetry}>Try again</button>
    </div>
  );
  if (orgs.length === 0) return <p className="text-sm text-muted-foreground">Waiting for your organization…</p>;
  const selected = orgs.find((o) => o.id === orgId);

  if (orgs.length === 1) {
    return (
      <div className="flex items-center justify-between text-sm" data-testid="authorize-org-single">
        <span className="text-muted-foreground">Organization</span>
        <span className="ml-3 truncate font-medium">{orgs[0]?.name}</span>
      </div>
    );
  }

  return (
    <fieldset className="space-y-2" disabled={disabled} data-testid="authorize-org">
      <legend className="text-xs uppercase tracking-wide text-muted-foreground">Organization</legend>
      <div role="radiogroup" aria-label="Organization" className="overflow-hidden rounded-md border border-border">
        {orgs.map((o) => (
          <OrgRow key={o.id} org={o} selected={o.id === orgId} onSelect={() => onChoose(o.id)} />
        ))}
      </div>
      {selected && (
        <p className="text-xs text-muted-foreground">
          {noun.charAt(0).toUpperCase() + noun.slice(1)} will act in <strong className="font-medium text-foreground">{selected.name}</strong>{" "}
          and cannot be moved to another organization later.
        </p>
      )}
    </fieldset>
  );
}
