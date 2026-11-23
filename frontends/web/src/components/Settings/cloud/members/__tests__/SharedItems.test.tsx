/**
 * Settings › Members › Shared, over the real generated clients on a mock
 * transport.
 *
 * Pinned: only an admin or owner gets the tab; it lists the chats and
 * projects other members shared with the org being looked at, with whose each
 * is; "Make private" asks first, then sets PRIVATE (the server records the
 * admin unshare) and re-reads the list; a refusal shows its words; empty
 * lists and a full page say so.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
const toastSuccess = vi.fn();
vi.mock("@/lib/toast-manager", () => ({ toast: { success: (m: string) => toastSuccess(m), error: vi.fn() } }));

import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { homePath, projectsPath } from "@/lib/paths";
import { renderWithQuery } from "@/test/renderWithQuery";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

import { MembersPage } from "../MembersPage";

const ORG = "org-a";

function myOrgs(role: string) {
  return {
    organizations: [{ id: ORG, name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true }],
    defaultOrgId: ORG,
  };
}

const ROSTER = {
  members: [
    { userId: "u_ada", externalId: "ext_ada", email: "ada@example.com", name: "Ada", role: "owner" },
    { userId: "u_bob", externalId: "ext_bob", email: "bob@example.com", name: "Bob", role: "member" },
  ],
  seats: { members: 2, pendingInvitations: 0, limit: 5 },
};

type Handlers = Parameters<typeof mockTransport>[0];

function setup(role = "admin", overrides: Handlers = {}) {
  let chats = [{ id: "c1", title: "Fix the flaky deploy", userId: "ext_bob", projectId: "p1", sharedAt: "2026-01-02T00:00:00Z" }];
  const calls = {
    listChats: vi.fn((_req: unknown) => ({ chats })),
    listProjects: vi.fn((_req: unknown) => ({ projects: [{ id: "p1", name: "shop", userId: "ext_bob", sharedAt: "2026-01-01T00:00:00Z" }] })),
    setChat: vi.fn((_req: unknown) => {
      chats = [];
      return { changed: true };
    }),
    setProject: vi.fn((_req: unknown) => ({ changed: true })),
  };
  setTransport(
    mockTransport({
      "OrganizationService.ListMyOrganizations": { response: myOrgs(role) },
      "OrganizationService.ListMembers": { response: ROSTER },
      "OrganizationService.ListInvitations": { response: { invitations: [] } },
      "AccessTokenService.ListOrgMemberGrants": { response: { members: [], grantable: [] } },
      "SharingService.ListSharedChats": calls.listChats,
      "SharingService.ListSharedProjects": calls.listProjects,
      "SharingService.SetChatVisibility": calls.setChat,
      "SharingService.SetProjectVisibility": calls.setProject,
      ...overrides,
    }),
  );
  const view = renderWithQuery(<MembersPage />);
  return { ...view, calls };
}

async function openShared() {
  await userEvent.click(await screen.findByRole("tab", { name: "Shared" }));
  return screen.findByTestId("shared-items");
}

beforeEach(() => {
  localStorage.clear();
  toastSuccess.mockReset();
  setTransport(null);
  useActiveOrgStore.setState({ userId: "u_ada", chosenId: ORG, effectiveId: ORG });
  useAuthStore.setState({ user: { id: "u_ada", email: "ada@example.com" } as never });
});

describe("Settings › Members › Shared", () => {
  it("is not offered to a plain member", async () => {
    setup("member");
    await screen.findByRole("tab", { name: "Members" });
    expect(screen.queryByRole("tab", { name: "Shared" })).toBeNull();
  });

  it("lists what other members shared with this org, with whose each is", async () => {
    const { calls } = setup("admin");
    await openShared();
    const chat = await screen.findByTestId("shared-chat-row-c1");
    expect(chat).toHaveTextContent("Fix the flaky deploy");
    await waitFor(() => expect(chat).toHaveTextContent("Shared by Bob"));
    expect(screen.getByTestId("shared-project-row-p1")).toHaveTextContent("shop");
    expect(screen.getByText(/Your own shares are not listed here/)).toBeInTheDocument();
    expect(screen.getByText(/open your chat's Share dialog/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Manage your chats" })).toHaveAttribute("href", homePath());
    expect(screen.getByRole("link", { name: "Manage your projects" })).toHaveAttribute("href", projectsPath());
    expect(screen.getByRole("heading", { name: "Other members' shared chats" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Other members' shared projects" })).toBeInTheDocument();
    expect(calls.listChats).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, limit: 100, offset: 0 }));
    expect(calls.listProjects).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, limit: 100, offset: 0 }));
  });

  it("makes a chat private after asking, and re-reads the list", async () => {
    const { calls } = setup("owner");
    await openShared();
    const row = await screen.findByTestId("shared-chat-row-c1");
    await userEvent.click(within(row).getByRole("button", { name: "Make Fix the flaky deploy private" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("recorded in the organization's share history");
    expect(calls.setChat).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Make private" }));
    await waitFor(() => expect(calls.setChat).toHaveBeenCalledWith(expect.objectContaining({ chatId: "c1", visibility: ResourceVisibility.PRIVATE })));
    expect(await screen.findByTestId("shared-chats-empty")).toHaveTextContent("No other member has shared a chat.");
    expect(toastSuccess).toHaveBeenCalledWith("Made Fix the flaky deploy private");
  });

  it("makes a project private", async () => {
    const { calls } = setup("admin");
    await openShared();
    const row = await screen.findByTestId("shared-project-row-p1");
    await userEvent.click(within(row).getByRole("button", { name: "Make shop private" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Make this project private?");
    await userEvent.click(within(dialog).getByRole("button", { name: "Make private" }));
    await waitFor(() =>
      expect(calls.setProject).toHaveBeenCalledWith(expect.objectContaining({ projectId: "p1", visibility: ResourceVisibility.PRIVATE })),
    );
  });

  it("shows a refusal in words", async () => {
    setup("admin", {
      "SharingService.SetChatVisibility": {
        error: new ConnectError("prose", Code.PermissionDenied, { "x-forge-error-reason": "org_admin_required" }),
      },
    });
    await openShared();
    const row = await screen.findByTestId("shared-chat-row-c1");
    await userEvent.click(within(row).getByRole("button", { name: /Make .* private/ }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Make private" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Only an admin or owner of the organization");
  });

  it("says when nothing is shared, and when a list fills a page", async () => {
    const full = Array.from({ length: 100 }, (_, i) => ({ id: `p${i}`, name: `project ${i}`, userId: "ext_bob" }));
    setup("admin", {
      "SharingService.ListSharedChats": { response: { chats: [] } },
      "SharingService.ListSharedProjects": { response: { projects: full } },
    });
    await openShared();
    expect(await screen.findByTestId("shared-chats-empty")).toHaveTextContent("No other member has shared a chat.");
    expect(await screen.findByTestId("shared-projects-page")).toHaveTextContent("Showing 1–100");
    expect(screen.getByTestId("shared-projects-next")).toHaveTextContent("Next 100");
  });
});
