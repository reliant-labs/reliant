// Copyright (c) 2025 Reliant Labs

/**
 * ProviderAccountService: Alex's own two AI accounts, an Anthropic key and an
 * OpenAI key (settings.ts says both providers are connected), each paid for
 * by Alex. Every chat runs on the Anthropic one: the composer's "who pays"
 * badge shows only for an org's account, so it stays hidden, as it does for
 * anyone on their own keys.
 */
import { ProviderAccountFunding, ProviderAccountService, ProviderAccountStatus, ProviderAuthKind, UsageSource } from "@/gen/reliant/v1/provider_account_pb";

import { DAY } from "../../shopStory";
import { isoAgo } from "../../workspace";
import { serve } from "../serve";

const account = (id: string, provider: string, providerDisplayName: string, maskedKey: string, priority: number) => ({
  id, provider, providerDisplayName, authKind: ProviderAuthKind.API_KEY, label: `${providerDisplayName} key`, maskedKey, priority,
  status: ProviderAccountStatus.AVAILABLE, windows: [], usageSource: UsageSource.UNSPECIFIED, createdAt: isoAgo(20 * DAY), usingCredits: false,
  funding: ProviderAccountFunding.PERSONAL,
});

const ANTHROPIC = () => account("shop-pa-anthropic", "anthropic", "Anthropic", "sk-ant-…a1ec", 0);
const OPENAI = () => account("shop-pa-openai", "openai", "OpenAI", "sk-…7f3d", 1);

export function providerAccountHandlers() {
  return serve(ProviderAccountService, {
    listProviderAccounts: () => ({ accounts: [ANTHROPIC(), OPENAI()] }),
    getChatProviderAccount: () => ({ account: ANTHROPIC() }),
  });
}
