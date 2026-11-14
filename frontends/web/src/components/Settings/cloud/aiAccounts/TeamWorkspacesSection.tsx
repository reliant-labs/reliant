// Copyright (c) 2025 Reliant Labs

/**
 * Team workspaces: the Claude Team/Enterprise and ChatGPT Business/Team
 * workspaces the org pays for, and the members' seats they matched
 * (org-provider-accounts §3.4, D3). Owners and admins only.
 *
 * A workspace is bound from one of the admin's OWN sign-ins on a team plan,
 * never by typing an id: the server reads the workspace from that sign-in, so
 * nobody can bind a workspace they are not in. Members' sign-ins in a bound
 * workspace are then paid for by the org, and serve their chats in this org
 * only. Unbinding, or releasing one seat, makes it personal again.
 */

import { useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { ProviderLogo } from "@/components/icons/ProviderLogo";
import { useProviderAccounts } from "@/hooks/provider-account-queries";
import { useOrgProviderWorkspaces } from "@/hooks/org-provider-account-queries";
import {
  useBindOrgProviderWorkspace,
  useReleaseOrgProviderSeat,
  useUnbindOrgProviderWorkspace,
} from "@/hooks/org-provider-account-service-hooks_gen";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { orgAccountErrorMessage, type OrgSeat, type OrgWorkspace } from "@/types/orgProviderAccount";
import type { ProviderAccount } from "@/types/providerAccount";

/** Plans whose sign-in is a seat in a workspace an org can pay for. */
const TEAM_PLANS = new Set(["team", "business", "enterprise"]);

/** The admin's own sign-ins a workspace can be bound from. */
export function bindableAccounts(accounts: ProviderAccount[]): ProviderAccount[] {
  return accounts.filter(
    (a) => (a.provider === "claude" || a.provider === "codex") && a.authKind === "oauth" && !!a.plan && TEAM_PLANS.has(a.plan) && !a.fundingOrgId,
  );
}

const providerName = (p: string) => (p === "claude" ? "Claude" : p === "codex" ? "ChatGPT" : p);

interface TeamWorkspacesSectionProps {
  org: MyOrg;
  /** Sign-in subject → name, from the roster. */
  names: ReadonlyMap<string, string>;
}

export function TeamWorkspacesSection({ org, names }: TeamWorkspacesSectionProps) {
  const q = useOrgProviderWorkspaces(org.id, true);
  const mine = useProviderAccounts();
  const bind = useBindOrgProviderWorkspace();
  const unbind = useUnbindOrgProviderWorkspace();
  const release = useReleaseOrgProviderSeat();
  const [pick, setPick] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [unbinding, setUnbinding] = useState<OrgWorkspace | null>(null);
  const [releasing, setReleasing] = useState<OrgSeat | null>(null);

  const candidates = useMemo(() => bindableAccounts(mine?.kind === "ready" ? mine.accounts : []), [mine]);
  const workspaces = q.data?.workspaces ?? [];
  const seats = q.data?.seats ?? [];
  const who = (userId: string) => names.get(userId) ?? "A member";

  const act = async (what: string, run: () => Promise<unknown>) => {
    setError(null);
    try {
      await run();
    } catch (err) {
      setError(orgAccountErrorMessage(err, `Could not ${what}. Try again.`));
    }
  };

  return (
    <section aria-labelledby="org-workspaces-heading" className="rounded-lg border border-border bg-card p-4" data-testid="org-workspaces">
      <h2 id="org-workspaces-heading" className="text-sm font-semibold text-foreground">
        Team workspaces
      </h2>
      <p className="mt-1 text-sm text-muted-foreground">
        A Claude Team or ChatGPT Business workspace {org.name} pays for. Members who sign in to it use their seat for {org.name}'s chats, ahead of
        their own subscription, and never for another organization's.
      </p>
      {error && (
        <p role="alert" className="mt-3 rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="org-workspaces-error">
          {error}
        </p>
      )}

      {workspaces.length > 0 && (
        <ul className="mt-4 divide-y divide-border/60 rounded-md border border-border/60 bg-background">
          {workspaces.map((w) => {
            const matched = seats.filter((s) => s.provider === w.provider && (!s.externalOrgId || s.externalOrgId === w.externalOrgId));
            return (
              <li key={`${w.provider}:${w.externalOrgId}`} className="space-y-2 px-3 py-2.5" data-testid="org-workspace-row">
                <div className="flex flex-wrap items-center gap-2">
                  <ProviderLogo provider={w.provider} />
                  <span className="min-w-0 flex-1 truncate text-sm text-foreground">
                    {providerName(w.provider)} workspace: <span className="font-medium">{w.displayName || w.externalOrgId}</span>
                    <span className="text-muted-foreground"> · bound by {who(w.boundByUserId)}</span>
                  </span>
                  <Button variant="ghost" size="sm" onClick={() => setUnbinding(w)}>
                    Unbind
                  </Button>
                </div>
                {matched.length === 0 ? (
                  <p className="text-xs text-muted-foreground">No member has signed in to it yet.</p>
                ) : (
                  <ul className="space-y-1" aria-label={`Seats in ${w.displayName}`}>
                    {matched.map((s) => (
                      <li key={s.accountId} className="flex items-center gap-2 text-xs" data-testid="org-seat-row">
                        <span className="min-w-0 flex-1 truncate text-foreground">
                          {who(s.userId)}
                          {s.email && <span className="text-muted-foreground"> · {s.email}</span>}
                        </span>
                        <Button variant="ghost" size="xs" onClick={() => setReleasing(s)}>
                          Release
                        </Button>
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <div className="mt-4 flex flex-wrap items-center gap-2">
        {candidates.length === 0 ? (
          <p className="text-xs text-muted-foreground" data-testid="org-workspace-no-candidates">
            To bind a workspace, first connect your own Claude Team or ChatGPT Business sign-in in Settings › AI providers.
          </p>
        ) : (
          <>
            <select
              aria-label="Your team sign-in to bind"
              value={pick}
              onChange={(e) => setPick(e.target.value)}
              className="rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="org-workspace-pick"
            >
              <option value="">Choose one of your team sign-ins…</option>
              {candidates.map((a) => (
                <option key={a.id} value={a.id}>
                  {providerName(a.provider)} · {a.organizationName ?? a.label} ({a.label})
                </option>
              ))}
            </select>
            <Button
              variant="outline"
              size="sm"
              disabled={!pick}
              loading={bind.isPending}
              onClick={() =>
                void act("bind the workspace", async () => {
                  await bind.mutateAsync({ orgId: org.id, accountId: pick });
                  setPick("");
                })
              }
              data-testid="org-workspace-bind"
            >
              Bind workspace
            </Button>
          </>
        )}
      </div>

      <ConfirmationDialog
        open={unbinding !== null}
        title={unbinding ? `Unbind ${unbinding.displayName || "this workspace"}?` : "Unbind workspace?"}
        description={`${org.name} stops paying for its members' seats: each becomes their own account again.`}
        confirmLabel="Unbind"
        variant="warning"
        loading={unbind.isPending}
        onCancel={() => setUnbinding(null)}
        onConfirm={() => {
          const w = unbinding;
          setUnbinding(null);
          if (w) void act("unbind the workspace", () => unbind.mutateAsync({ orgId: org.id, provider: w.provider, externalOrgId: w.externalOrgId }));
        }}
      />
      <ConfirmationDialog
        open={releasing !== null}
        title={releasing ? `Release ${who(releasing.userId)}'s seat?` : "Release seat?"}
        description={`${org.name} stops paying for it, and it becomes their own account again. It is not matched to ${org.name} again on its own.`}
        confirmLabel="Release seat"
        variant="warning"
        loading={release.isPending}
        onCancel={() => setReleasing(null)}
        onConfirm={() => {
          const s = releasing;
          setReleasing(null);
          if (s) void act("release the seat", () => release.mutateAsync({ orgId: org.id, accountId: s.accountId }));
        }}
      />
    </section>
  );
}
