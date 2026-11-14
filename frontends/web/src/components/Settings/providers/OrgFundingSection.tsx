// Copyright (c) 2025 Reliant Labs

/**
 * Settings › AI providers, for the active org (control-plane
 * docs/design/org-provider-accounts.md §7, §10.4):
 *
 *   From Acme      the org's keys this person may use (label, provider,
 *                  status; never the key)
 *   Paid by Acme   their own sign-ins Acme pays for (a team seat, or the
 *                  Reliant key minted in Acme)
 *   In Acme, use:  Acme's accounts first (recommended) / My accounts first /
 *                  Only Acme's accounts
 *   When Acme's accounts can't serve a chat:  Ask me / Use my accounts / Wait,
 *                  with Reset
 *
 * "Yours" is the provider list below it, unchanged. The two choices are the
 * setting `provider.org_funding` (lib/orgFunding.ts); the server reads it at
 * every model call, and an unreadable value can only ask or wait.
 */

import { ProviderLogo } from "@/components/icons/ProviderLogo";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useOrgFunding } from "@/hooks/org-funding-queries";
import { useOrgProviderAccounts } from "@/hooks/org-provider-account-queries";
import { useProviderAccounts } from "@/hooks/provider-account-queries";
import { cn } from "@/lib/utils";
import {
  DEFAULT_ORG_FUNDING,
  onOrgLimitLabel,
  orderLabel,
  type FundingOrder,
  type OnOrgLimit,
} from "@/lib/orgFunding";
import { hasControlPlane } from "@/services/controlPlane/config";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { useAuthStore } from "@/store/authStore";
import { mayUse } from "@/types/orgProviderAccount";

const ORDERS: readonly FundingOrder[] = ["org_first", "personal_first", "org_only"];
const ON_LIMITS: readonly OnOrgLimit[] = ["ask", "use_personal", "wait"];

export function OrgFundingSection({ variant = "desktop" }: { variant?: "desktop" | "mobile" }) {
  const { active, orgs } = useActiveOrg();
  if (!hasControlPlane || !active) return null;
  return <OrgFunding key={active.id} org={active} orgIds={orgs.map((o) => o.id)} variant={variant} />;
}

function OrgFunding({ org, orgIds, variant }: { org: MyOrg; orgIds: string[]; variant: "desktop" | "mobile" }) {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const orgAccounts = useOrgProviderAccounts(org.id);
  const mine = useProviderAccounts();
  const funding = useOrgFunding(org.id, orgIds);
  const isAdmin = orgAccounts.data?.callerIsAdmin ?? false;

  const fromOrg = (orgAccounts.data?.accounts ?? []).filter((a) => mayUse(a, isAdmin, userId) !== "no");
  const paidByOrg = (mine?.kind === "ready" ? mine.accounts : []).filter((a) => a.fundingOrgId === org.id);
  const pref = funding.preference;
  const orgGives = fromOrg.length > 0 || paidByOrg.length > 0;
  const selectClass = cn(
    "rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
    variant === "mobile" ? "min-h-[44px]" : "py-1",
  );

  return (
    <section aria-labelledby="org-funding-heading" className="space-y-4 rounded-lg border border-border bg-card p-4" data-testid="org-funding">
      <div>
        <h2 id="org-funding-heading" className="text-sm font-semibold text-foreground">
          In {org.name}
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          {orgGives
            ? `Chats in ${org.name} use the accounts ${org.name} pays for before your own, so your credits are saved.`
            : `${org.name} gives you no AI accounts yet, so your chats here run on your own.`}
        </p>
      </div>

      {fromOrg.length > 0 && (
        <div className="space-y-1.5" data-testid="org-funding-from-org">
          <h3 className="text-xs font-medium text-muted-foreground">From {org.name}</h3>
          <ul className="divide-y divide-border/60 rounded-md border border-border/60 bg-background">
            {fromOrg.map((a) => (
              <li key={a.account.id} className="flex items-center gap-2 px-3 py-2 text-sm">
                <ProviderLogo provider={a.account.provider} />
                <span className="min-w-0 flex-1 truncate text-foreground">{a.account.label}</span>
                <span className="text-xs text-muted-foreground">{a.account.providerDisplayName}</span>
                {a.account.status !== "available" && (
                  <span className={cn("text-xs font-medium", a.account.status === "needs_sign_in" ? "text-destructive-ink" : "text-warning-ink")}>
                    {a.account.status === "paused" ? "Paused" : a.account.status === "limited" ? "Rate-limited" : "Not working"}
                  </span>
                )}
                {mayUse(a, isAdmin, userId) === "maybe" && <span className="text-xs text-muted-foreground">If an admin picked you</span>}
              </li>
            ))}
          </ul>
        </div>
      )}

      {paidByOrg.length > 0 && (
        <div className="space-y-1.5" data-testid="org-funding-paid-by-org">
          <h3 className="text-xs font-medium text-muted-foreground">Paid by {org.name}</h3>
          <ul className="divide-y divide-border/60 rounded-md border border-border/60 bg-background">
            {paidByOrg.map((a) => (
              <li key={a.id} className="flex items-center gap-2 px-3 py-2 text-sm">
                <ProviderLogo provider={a.provider} />
                <span className="min-w-0 flex-1 truncate text-foreground">{a.label}</span>
                <span className="text-xs text-muted-foreground">
                  {a.funding === "org_wallet" ? `${org.name}'s Reliant credits` : `Your seat · ${a.providerDisplayName}`}
                </span>
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground">They serve your chats in {org.name} only, ahead of your own subscription.</p>
        </div>
      )}

      {pref && (
        <div className="space-y-3">
          <label className="flex flex-wrap items-center gap-2 text-sm text-foreground">
            <span>In {org.name}, use</span>
            <select
              aria-label={`In ${org.name}, use`}
              value={pref.order}
              disabled={funding.saving}
              onChange={(e) => void funding.set({ order: e.target.value as FundingOrder }).catch(() => {})}
              className={selectClass}
              data-testid="org-funding-order"
            >
              {ORDERS.map((o) => (
                <option key={o} value={o}>
                  {orderLabel(o, org.name)}
                  {o === "org_first" ? " (recommended)" : ""}
                </option>
              ))}
            </select>
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <label className="flex flex-wrap items-center gap-2 text-sm text-foreground">
              <span>When {org.name}'s accounts can't serve a chat</span>
              <select
                aria-label={`When ${org.name}'s accounts can't serve a chat`}
                value={pref.onOrgLimit}
                disabled={funding.saving || pref.order === "org_only"}
                onChange={(e) => void funding.set({ onOrgLimit: e.target.value as OnOrgLimit }).catch(() => {})}
                className={selectClass}
                data-testid="org-funding-on-limit"
              >
                {ON_LIMITS.map((o) => (
                  <option key={o} value={o}>
                    {onOrgLimitLabel(o)}
                  </option>
                ))}
              </select>
            </label>
            {!funding.loading && (pref.order !== DEFAULT_ORG_FUNDING.order || pref.onOrgLimit !== DEFAULT_ORG_FUNDING.onOrgLimit) && (
              <button
                type="button"
                onClick={() => void funding.reset().catch(() => {})}
                disabled={funding.saving}
                className="text-xs font-medium text-primary-ink hover:underline disabled:opacity-60"
                title={`Back to ${orderLabel(DEFAULT_ORG_FUNDING.order, org.name).toLowerCase()} and Ask me`}
                data-testid="org-funding-reset"
              >
                Reset
              </button>
            )}
          </div>
          <p className="text-xs text-muted-foreground" data-testid="org-funding-explain">
            {pref.order === "org_only"
              ? `Your own accounts never serve chats in ${org.name}: a chat waits for ${org.name}'s accounts.`
              : pref.onOrgLimit === "ask"
                ? `A chat asks before it moves from ${org.name}'s accounts to yours, and waits until you answer.`
                : pref.onOrgLimit === "use_personal"
                  ? `A chat continues on your own account, with a note in the chat, until ${org.name}'s accounts are back.`
                  : `A chat waits for ${org.name}'s accounts and never asks.`}
          </p>
          {funding.error && (
            <p role="alert" className="text-xs text-destructive-ink">
              Couldn't save that choice. Try again.
            </p>
          )}
        </div>
      )}
    </section>
  );
}
