// Copyright (c) 2025 Reliant Labs

/**
 * The composer's "who pays" badge: it names the org when the chat runs on an
 * account the org pays for, and is absent on the person's own.
 */

import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { ProviderAccountFunding } from "@/gen/reliant/v1/provider_account_pb";
import type { ProviderAccount } from "@/types/providerAccount";

const mocks = vi.hoisted(() => ({ account: undefined as Record<string, unknown> | undefined }));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ user: { id: "u-me" }, session: {} }),
}));
vi.mock("@/hooks/useOrgs", () => ({
  useMyOrgs: () => ({ data: { orgs: [{ id: "org-a", name: "Acme" }] } }),
}));

import { FundingBadge, fundingBadgeText } from "../FundingBadge";

const acct = (funding: ProviderAccount["funding"]): ProviderAccount =>
  ({ id: "a", provider: "anthropic", providerDisplayName: "Anthropic API", label: "Acme prod", funding, fundingOrgId: "org-a" }) as ProviderAccount;

describe("fundingBadgeText", () => {
  it("says who pays for each org funding, and nothing for the person's own", () => {
    expect(fundingBadgeText(acct("org_key"), "Acme")).toBe("Acme · Anthropic API key");
    expect(fundingBadgeText({ ...acct("org_seat"), providerDisplayName: "Claude subscription" }, "Acme")).toBe("Acme · Claude subscription seat");
    expect(fundingBadgeText(acct("org_wallet"), "Acme")).toBe("Acme · Reliant credits");
    expect(fundingBadgeText(acct("personal"), "Acme")).toBeNull();
    expect(fundingBadgeText(acct(undefined), "Acme")).toBeNull();
    expect(fundingBadgeText(undefined, "Acme")).toBeNull();
  });
});

describe("FundingBadge", () => {
  beforeEach(() => {
    setTransport(mockTransport({ "ProviderAccountService.GetChatProviderAccount": () => ({ account: mocks.account }) }));
  });
  const renderBadge = () =>
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <FundingBadge chatId="chat-1" />
      </QueryClientProvider>,
    );

  it("shows the org's key a chat runs on", async () => {
    mocks.account = { id: "k1", provider: "anthropic", providerDisplayName: "Anthropic API", label: "Acme prod", funding: ProviderAccountFunding.ORG_KEY, fundingOrgId: "org-a" };
    renderBadge();
    expect(await screen.findByTestId("funding-badge")).toHaveTextContent("Acme · Anthropic API key");
  });

  it("is absent on the person's own account", async () => {
    mocks.account = { id: "p1", provider: "claude", providerDisplayName: "Claude subscription", label: "me", funding: ProviderAccountFunding.PERSONAL };
    renderBadge();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("funding-badge")).toBeNull();
  });
});
