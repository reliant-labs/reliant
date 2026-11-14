// Copyright (c) 2025 Reliant Labs

/**
 * The composer's "who pays" badge (control-plane
 * docs/design/org-provider-accounts.md §7): shown only when the account the
 * chat is running on is NOT the person's own.
 *
 *   Acme · Anthropic key        an org's own API key
 *   Acme · Claude seat          the person's seat in a team workspace Acme pays for
 *   Acme · Reliant credits      the Reliant key minted in Acme
 *
 * Absent for the person's own accounts: that is the default, and not news.
 * The account comes from GetChatProviderAccount (shared with the usage chip's
 * cache), which names an org key as any member sees it, never its key.
 */

import { Building2 } from "lucide-react";

import { useGetChatProviderAccount } from "@/hooks/provider-account-service-hooks_gen";
import { useMyOrgs } from "@/hooks/useOrgs";
import { cn } from "@/lib/utils";
import { hasControlPlane } from "@/services/controlPlane/config";
import { useAuthStore } from "@/store/authStore";
import { accountFromProto, isProviderAccountsUnavailable, type ProviderAccount } from "@/types/providerAccount";

import { COMPOSER_CONTROL } from "./composerControl";

/** "Acme · Anthropic key", or null for the person's own account. */
export function fundingBadgeText(account: ProviderAccount | undefined, orgName: string | undefined): string | null {
  if (!account?.funding || account.funding === "personal") return null;
  const org = orgName || "Your organization";
  switch (account.funding) {
    case "org_key":
      return `${org} · ${account.providerDisplayName} key`;
    case "org_seat":
      return `${org} · ${account.providerDisplayName} seat`;
    case "org_wallet":
      return `${org} · Reliant credits`;
  }
}

export function FundingBadge({ chatId }: { chatId?: string }) {
  const hasSession = useAuthStore((s) => !!s.session);
  const orgs = useMyOrgs();
  const q = useGetChatProviderAccount(
    { chatId: chatId ?? "" },
    {
      enabled: hasControlPlane && hasSession && !!chatId,
      staleTime: 15_000,
      refetchInterval: 60_000,
      retry: (failures, error) => !isProviderAccountsUnavailable(error) && failures < 1,
    },
  );
  const proto = q.data?.account;
  const account = proto ? accountFromProto(proto) : undefined;
  const orgName = orgs.data?.orgs.find((o) => o.id === account?.fundingOrgId)?.name;
  const text = fundingBadgeText(account, orgName);
  if (!text || !account) return null;
  return (
    <span
      data-testid="funding-badge"
      data-funding={account.funding}
      title={`This chat runs on ${account.label}, paid for by ${orgName ?? "your organization"}.`}
      className={cn(
        "inline-flex max-w-[16rem] items-center gap-1 rounded-full border border-border px-2.5 text-2xs font-medium text-muted-foreground",
        COMPOSER_CONTROL,
      )}
    >
      <Building2 className="h-3 w-3 shrink-0" aria-hidden="true" />
      <span className="truncate">{text}</span>
    </span>
  );
}
