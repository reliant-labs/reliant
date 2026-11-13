// Copyright (c) 2025 Reliant Labs

/**
 * Settings › AI providers, for the active org: what the org gives the person
 * ("From Acme", "Paid by Acme"), and their two choices for the org — whose
 * accounts first, and what happens when the org's can't serve a chat (Ask me /
 * Use my accounts / Wait) — with Reset back to the defaults. The choices are
 * the setting provider.org_funding, written whole.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { OrgProviderAccountAccess } from "@/gen/reliant/v1/org_provider_account_pb";

const mocks = vi.hoisted(() => ({
  setting: undefined as string | undefined,
  written: [] as Array<{ key: string; value: string }>,
}));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ user: { id: "u-me" }, session: {} }),
}));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    active: { id: "org-a", name: "Acme", slug: "acme", role: "member", isOwner: false, isDefault: true },
    orgs: [{ id: "org-a" }, { id: "org-b" }],
  }),
}));
vi.mock("@/hooks/provider-account-queries", () => ({
  useProviderAccounts: () => ({
    kind: "ready",
    accounts: [
      { id: "seat", provider: "claude", providerDisplayName: "Claude subscription", label: "me@acme.com", funding: "org_seat", fundingOrgId: "org-a" },
      { id: "own", provider: "claude", providerDisplayName: "Claude subscription", label: "me@home.com" },
      { id: "other-seat", provider: "codex", providerDisplayName: "Codex", label: "me@beta.com", funding: "org_seat", fundingOrgId: "org-b" },
    ],
  }),
}));
vi.mock("@/lib/settingsPersistence", () => ({
  readSetting: async () => (mocks.setting === undefined ? { status: "missing" } : { status: "found", value: mocks.setting }),
  upsertStringSetting: async (key: string, value: string) => {
    mocks.written.push({ key, value });
    mocks.setting = value;
  },
}));

import { OrgFundingSection } from "../OrgFundingSection";

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <OrgFundingSection />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocks.setting = JSON.stringify({ "org-gone": { order: "org_only" } });
  mocks.written = [];
  setTransport(
    mockTransport({
      "OrgProviderAccountService.ListOrgProviderAccounts": () => ({
        accounts: [
          { account: { id: "k1", provider: "anthropic", providerDisplayName: "Anthropic API", label: "Acme prod", status: 1 }, access: OrgProviderAccountAccess.MEMBERS },
          { account: { id: "k2", provider: "openai", providerDisplayName: "OpenAI", label: "Admins only", status: 1 }, access: OrgProviderAccountAccess.ADMINS },
        ],
        callerIsAdmin: false,
        addingEnabled: true,
      }),
    }),
  );
});

describe("OrgFundingSection", () => {
  it("lists what the org gives the person: its keys open to them, and their seats it pays for", async () => {
    renderSection();
    const from = await screen.findByTestId("org-funding-from-org");
    expect(within(from).getByText("From Acme")).toBeInTheDocument();
    expect(within(from).getByText("Acme prod")).toBeInTheDocument();
    expect(within(from).queryByText("Admins only")).toBeNull();
    const paid = screen.getByTestId("org-funding-paid-by-org");
    expect(within(paid).getByText("me@acme.com")).toBeInTheDocument();
    expect(within(paid).queryByText("me@home.com")).toBeNull();
    expect(within(paid).queryByText("me@beta.com")).toBeNull();
  });

  it("shows the defaults, writes a choice, drops entries of orgs the person left, and resets", async () => {
    renderSection();
    const order = await screen.findByTestId("org-funding-order");
    const onLimit = screen.getByTestId("org-funding-on-limit");
    expect(order).toHaveValue("org_first");
    expect(onLimit).toHaveValue("ask");
    expect(screen.queryByTestId("org-funding-reset")).toBeNull();

    fireEvent.change(onLimit, { target: { value: "use_personal" } });
    await waitFor(() => expect(mocks.written).toHaveLength(1));
    expect(mocks.written[0].key).toBe("provider.org_funding");
    expect(JSON.parse(mocks.written[0].value)).toEqual({ "org-a": { on_org_limit: "use_personal" } });
    expect(screen.getByTestId("org-funding-explain")).toHaveTextContent("continues on your own account");

    fireEvent.click(await screen.findByTestId("org-funding-reset"));
    await waitFor(() => expect(mocks.written).toHaveLength(2));
    expect(JSON.parse(mocks.written[1].value)).toEqual({});
    await waitFor(() => expect(screen.getByTestId("org-funding-on-limit")).toHaveValue("ask"));
  });

  it("Only Acme's accounts locks the on-limit choice: the person's own never serve", async () => {
    mocks.setting = JSON.stringify({ "org-a": { order: "org_only" } });
    renderSection();
    await waitFor(() => expect(screen.getByTestId("org-funding-order")).toHaveValue("org_only"));
    expect(screen.getByTestId("org-funding-on-limit")).toBeDisabled();
    expect(screen.getByTestId("org-funding-explain")).toHaveTextContent("Your own accounts never serve chats in Acme");
  });
});
