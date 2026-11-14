// Copyright (c) 2025 Reliant Labs

/**
 * Members › Permissions › AI keys: a people × keys grid for the org's keys open
 * to "Selected people". Each tick sends the key's whole new set of people; a
 * refused edit reverts the box. Keys open to everyone or to admins are not in
 * the grid, and a member sees no grid at all.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { OrgProviderAccountAccess } from "@/gen/reliant/v1/org_provider_account_pb";
import type { OrgMember } from "@/services/controlPlane/members";

const mocks = vi.hoisted(() => ({ isAdmin: true, grants: ["u-dana"] as string[], set: vi.fn() }));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ user: { id: "u-me" }, session: {} }),
}));

import { AIAccountGrants } from "../AIAccountGrants";

const org = { id: "org-a", name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true };
const member = (externalId: string, name: string): OrgMember => ({
  userId: `cp-${externalId}`,
  externalId,
  name,
  email: `${name.toLowerCase()}@acme.com`,
  avatarUrl: "",
  role: "member",
  joinedAt: null,
  isFounder: false,
});
const members = [member("u-dana", "Dana"), member("u-eli", "Eli")];

let qc: QueryClient;
function renderGrid() {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AIAccountGrants org={org} members={members} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocks.isAdmin = true;
  mocks.grants = ["u-dana"];
  mocks.set.mockReset();
  mocks.set.mockImplementation(async (req: { userIds: string[] }) => {
    mocks.grants = req.userIds;
    return {};
  });
  setTransport(
    mockTransport({
      "OrgProviderAccountService.ListOrgProviderAccounts": () => ({
        accounts: [
          {
            account: { id: "k-sel", provider: "anthropic", providerDisplayName: "Anthropic API", label: "Research key", status: 1 },
            access: OrgProviderAccountAccess.SELECTED,
            grants: mocks.grants.map((userId) => ({ userId, grantedByUserId: "u-me", grantedAt: "2026-11-13T00:00:00Z" })),
          },
          {
            account: { id: "k-all", provider: "anthropic", providerDisplayName: "Anthropic API", label: "Everyone key", status: 1 },
            access: OrgProviderAccountAccess.MEMBERS,
          },
        ],
        callerIsAdmin: mocks.isAdmin,
        addingEnabled: true,
      }),
      "OrgProviderAccountService.SetOrgProviderAccountGrants": (req) => mocks.set(req),
    }),
  );
});

describe("AIAccountGrants", () => {
  it("has a column for each selected-people key only, ticked from the grants", async () => {
    renderGrid();
    expect(await screen.findByRole("columnheader", { name: /Research key/ })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: /Everyone key/ })).toBeNull();
    expect(screen.getByRole("checkbox", { name: "Dana may use Research key" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Eli may use Research key" })).not.toBeChecked();
  });

  it("sends the key's whole new set on a tick", async () => {
    renderGrid();
    fireEvent.click(await screen.findByRole("checkbox", { name: "Eli may use Research key" }));
    await waitFor(() => expect(mocks.set).toHaveBeenCalledTimes(1));
    expect(mocks.set.mock.calls[0][0]).toMatchObject({ orgId: "org-a", accountId: "k-sel", userIds: ["u-dana", "u-eli"] });
    await waitFor(() => expect(screen.getByRole("checkbox", { name: "Eli may use Research key" })).toBeChecked());
  });

  it("reverts a refused edit and says so", async () => {
    mocks.set.mockRejectedValue(
      new ConnectError("only an owner or admin", Code.PermissionDenied, { "x-forge-error-reason": "org_admin_required" }),
    );
    renderGrid();
    fireEvent.click(await screen.findByRole("checkbox", { name: "Dana may use Research key" }));
    expect(await screen.findByTestId("ai-grants-error")).toHaveTextContent("Only an owner or admin of this organization can change its AI accounts.");
    await waitFor(() => expect(screen.getByRole("checkbox", { name: "Dana may use Research key" })).toBeChecked());
  });

  it("shows nothing to a member", async () => {
    mocks.isAdmin = false;
    const { container } = renderGrid();
    await waitFor(() => expect(qc.getQueryCache().getAll().every((q) => q.state.status === "success")).toBe(true));
    expect(container).toBeEmptyDOMElement();
  });
});
