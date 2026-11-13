// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Organization › AI accounts: an admin adds, pauses and reorders
 * the org's keys and binds team workspaces; a member sees each key's provider,
 * label and status only. While the deployment has not enabled org accounts the
 * page says so, offers no Add, and keeps every way to stop the org's spend; a
 * refused add says why in words, never the server's prose.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { OrgProviderAccountAccess } from "@/gen/reliant/v1/org_provider_account_pb";
import { ProviderAccountStatus, ProviderAuthKind } from "@/gen/reliant/v1/provider_account_pb";
import type { MyOrg } from "@/services/controlPlane/orgs";

const mocks = vi.hoisted(() => ({
  org: null as MyOrg | null,
  isAdmin: true,
  addingEnabled: true,
  add: vi.fn(),
  update: vi.fn(),
  reorder: vi.fn(),
  bind: vi.fn(),
  mine: [] as unknown[],
}));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ user: { id: "u-me" }, session: { access_token: "t" } }),
}));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({ active: mocks.org, orgs: mocks.org ? [mocks.org] : [], isLoading: false }),
}));
vi.mock("@/components/Settings/cloud/members/useMembers", () => ({
  useMembers: () => ({ data: { pages: [{ members: [{ userId: "cp-1", externalId: "u-dana", name: "Dana", email: "dana@acme.com" }] }] } }),
}));
vi.mock("@/hooks/provider-account-queries", () => ({
  useProviderAccounts: () => ({ kind: "ready", accounts: mocks.mine }),
}));

import { OrgAIAccountsPage } from "../OrgAIAccountsPage";

const acme = (role: string): MyOrg => ({ id: "org-a", name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true });

const key = (id: string, label: string, priority: number, extra: Record<string, unknown> = {}) => ({
  account: {
    id,
    provider: "anthropic",
    providerDisplayName: "Anthropic API",
    authKind: ProviderAuthKind.API_KEY,
    label,
    priority,
    status: ProviderAccountStatus.AVAILABLE,
    maskedKey: mocks.isAdmin ? "…a1b2" : undefined,
    ...extra,
  },
  access: OrgProviderAccountAccess.MEMBERS,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <OrgAIAccountsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [mocks.add, mocks.update, mocks.reorder, mocks.bind]) fn.mockReset();
  mocks.org = acme("admin");
  mocks.isAdmin = true;
  mocks.addingEnabled = true;
  mocks.mine = [];
  mocks.add.mockResolvedValue({ created: true });
  mocks.update.mockResolvedValue({});
  mocks.reorder.mockResolvedValue({});
  mocks.bind.mockResolvedValue({ created: true });
  setTransport(
    mockTransport({
      "OrgProviderAccountService.ListOrgProviderAccounts": () => ({
        accounts: [key("k1", "Acme prod", 0), key("k2", "Acme backup", 1, { status: ProviderAccountStatus.NEEDS_SIGN_IN })],
        callerIsAdmin: mocks.isAdmin,
        addingEnabled: mocks.addingEnabled,
      }),
      "OrgProviderAccountService.ListOrgProviderWorkspaces": () => ({
        workspaces: [{ provider: "claude", externalOrgId: "ws-1", displayName: "Acme Inc.", boundByUserId: "u-dana", boundAt: "2026-11-13T00:00:00Z" }],
        seats: [{ accountId: "s1", userId: "u-dana", provider: "claude", email: "dana@acme.com", attributionSource: "workspace", externalOrgId: "ws-1" }],
      }),
      "OrgProviderAccountService.AddOrgProviderAPIKey": (req) => mocks.add(req),
      "OrgProviderAccountService.UpdateOrgProviderAccount": (req) => mocks.update(req),
      "OrgProviderAccountService.ReorderOrgProviderAccounts": (req) => mocks.reorder(req),
      "OrgProviderAccountService.BindOrgProviderWorkspace": (req) => mocks.bind(req),
    }),
  );
});

describe("OrgAIAccountsPage", () => {
  it("an admin sees each key whole, adds one, and the key is sent once and never shown", async () => {
    renderPage();
    const rows = await screen.findAllByTestId("org-key-row");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("Acme prod")).toBeInTheDocument();
    expect(within(rows[0]).getByText("…a1b2")).toBeInTheDocument();
    expect(within(rows[1]).getByTestId("org-key-status")).toHaveTextContent("The provider rejected this key: rotate or remove it");

    fireEvent.click(screen.getByTestId("org-key-add"));
    fireEvent.change(screen.getByTestId("org-key-label"), { target: { value: "Acme ops" } });
    fireEvent.change(screen.getByTestId("org-key-secret"), { target: { value: "sk-ant-secret-0001" } });
    fireEvent.change(screen.getByTestId("org-key-access"), { target: { value: "selected" } });
    expect(screen.getByTestId("org-key-secret")).toHaveAttribute("type", "password");
    fireEvent.click(screen.getByTestId("org-key-add-submit"));
    await waitFor(() => expect(mocks.add).toHaveBeenCalledTimes(1));
    expect(mocks.add.mock.calls[0][0]).toMatchObject({
      orgId: "org-a",
      provider: "anthropic",
      label: "Acme ops",
      apiKey: "sk-ant-secret-0001",
      access: OrgProviderAccountAccess.SELECTED,
    });
    await waitFor(() => expect(screen.queryByTestId("org-key-secret")).toBeNull());
  });

  it("shows a refused key in the provider's words, and keeps the dialog open", async () => {
    mocks.add.mockRejectedValue(new ConnectError("the provider rejected this key", Code.InvalidArgument));
    renderPage();
    await screen.findAllByTestId("org-key-row");
    fireEvent.click(screen.getByTestId("org-key-add"));
    fireEvent.change(screen.getByTestId("org-key-label"), { target: { value: "x" } });
    fireEvent.change(screen.getByTestId("org-key-secret"), { target: { value: "bad" } });
    fireEvent.click(screen.getByTestId("org-key-add-submit"));
    expect(await screen.findByTestId("org-key-add-error")).toHaveTextContent("The provider rejected this key.");
    expect(screen.getByTestId("org-key-secret")).toBeInTheDocument();
  });

  it("while the deployment has not enabled org accounts: no Add, a note, and pause still works", async () => {
    mocks.addingEnabled = false;
    renderPage();
    await screen.findAllByTestId("org-key-row");
    expect(screen.getByTestId("org-accounts-disabled")).toHaveTextContent("isn't enabled on this deployment");
    expect(screen.queryByTestId("org-key-add")).toBeNull();
    fireEvent.click(screen.getAllByTestId("org-key-actions")[0]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Pause" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(mocks.update.mock.calls[0][0]).toMatchObject({ orgId: "org-a", accountId: "k1", paused: true });
  });

  it("names the refusal reason of a gated add in words", async () => {
    mocks.add.mockRejectedValue(
      new ConnectError("adding organization AI accounts is not enabled on this deployment", Code.FailedPrecondition, {
        "x-forge-error-reason": "org_provider_accounts_disabled",
      }),
    );
    renderPage();
    await screen.findAllByTestId("org-key-row");
    fireEvent.click(screen.getByTestId("org-key-add"));
    fireEvent.change(screen.getByTestId("org-key-label"), { target: { value: "x" } });
    fireEvent.change(screen.getByTestId("org-key-secret"), { target: { value: "sk" } });
    fireEvent.click(screen.getByTestId("org-key-add-submit"));
    expect(await screen.findByTestId("org-key-add-error")).toHaveTextContent("Adding organization AI accounts isn't enabled on this deployment yet.");
  });

  it("reorders within a provider by sending the whole new order", async () => {
    renderPage();
    await screen.findAllByTestId("org-key-row");
    fireEvent.click(screen.getByRole("button", { name: "Move Acme backup up" }));
    await waitFor(() => expect(mocks.reorder).toHaveBeenCalledTimes(1));
    expect(mocks.reorder.mock.calls[0][0]).toMatchObject({ orgId: "org-a", provider: "anthropic", accountIds: ["k2", "k1"] });
  });

  it("lists bound workspaces and their seats, and binds from the admin's own team sign-in", async () => {
    mocks.mine = [
      { id: "mine-team", provider: "claude", authKind: "oauth", plan: "team", label: "me@acme.com", organizationName: "Acme Inc.", status: "available", windows: [], usageSource: "unknown", priority: 0, providerDisplayName: "Claude", createdAt: "" },
      { id: "mine-pro", provider: "claude", authKind: "oauth", plan: "pro", label: "me@home.com", status: "available", windows: [], usageSource: "unknown", priority: 1, providerDisplayName: "Claude", createdAt: "" },
    ];
    renderPage();
    const ws = await screen.findByTestId("org-workspace-row");
    expect(ws).toHaveTextContent("Acme Inc.");
    expect(within(ws).getByTestId("org-seat-row")).toHaveTextContent("Dana");
    const pick = screen.getByTestId("org-workspace-pick");
    expect(within(pick).getAllByRole("option")).toHaveLength(2); // the placeholder and the one team sign-in
    fireEvent.change(pick, { target: { value: "mine-team" } });
    fireEvent.click(screen.getByTestId("org-workspace-bind"));
    await waitFor(() => expect(mocks.bind).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a", accountId: "mine-team" })));
  });

  it("a member sees provider, label and status, whether it is open to them, and no actions or workspaces", async () => {
    mocks.org = acme("member");
    mocks.isAdmin = false;
    renderPage();
    const rows = await screen.findAllByTestId("org-key-row");
    expect(within(rows[0]).getByText("Acme prod")).toBeInTheDocument();
    expect(within(rows[0]).getByTestId("org-key-usable")).toHaveTextContent("Open to you");
    expect(within(rows[1]).getByTestId("org-key-status")).toHaveTextContent("Not working: an admin needs to fix it");
    expect(screen.queryByTestId("org-key-actions")).toBeNull();
    expect(screen.queryByTestId("org-key-add")).toBeNull();
    expect(screen.queryByTestId("org-workspaces")).toBeNull();
  });
});
