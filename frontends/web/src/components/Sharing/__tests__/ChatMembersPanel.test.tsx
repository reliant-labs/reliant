/**
 * The Members panel (chat-collaboration.md §2.2, §3.2): the owner and
 * managers add, change and remove; reply members see who a request waits on;
 * viewers see the owner and a count; anyone may remove their own grant;
 * removing works with granting off; an empty state; the 25-member cap.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatMemberRole } from "@/gen/reliant/v1/sharing_pb";

const mocks = vi.hoisted(() => ({
  data: undefined as Record<string, unknown> | undefined,
  selfId: "olive" as string | null,
  revoke: vi.fn(async () => ({ changed: true })),
  leaveSharedChat: vi.fn(() => true),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/chat-member-queries", () => ({
  useChatMembers: () => ({ data: mocks.data, isLoading: false, error: null }),
  useRevokeChatMember: () => ({ revoke: mocks.revoke, isPending: false }),
}));
vi.mock("@/hooks/sharing-queries", () => ({
  useOrgMemberNames: () =>
    new Map([
      ["olive", "Olive Owner"],
      ["dana", "Dana"],
      ["max", "Max"],
      ["rae", "Rae"],
    ]),
}));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (select: (s: { user: { id: string } | null }) => unknown) =>
    select({ user: mocks.selfId ? { id: mocks.selfId } : null }),
}));
vi.mock("@/store/openChat", () => ({ leaveSharedChat: mocks.leaveSharedChat }));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));
vi.mock("../GrantChatMemberDialog", () => ({
  GrantChatMemberDialog: ({ open, target, callerIsOwner }: { open: boolean; target?: { name: string } | null; callerIsOwner: boolean }) =>
    open ? (
      <div data-testid="grant-dialog">
        {target ? `change ${target.name}` : "add"} {callerIsOwner ? "as owner" : "as manager"}
      </div>
    ) : null,
}));

const { ChatMembersPanel } = await import("../ChatMembersPanel");

const m = (userId: string, role: ChatMemberRole) => ({ userId, role, grantedBy: "olive" });

function answer(over: Record<string, unknown>) {
  mocks.data = {
    ownerUserId: "olive",
    members: [],
    memberCount: 0,
    callerRole: ChatMemberRole.UNSPECIFIED,
    grantsEnabled: true,
    ...over,
  };
}

function renderPanel(sharedWithOrg = false) {
  const onClose = vi.fn();
  render(<ChatMembersPanel chatId="c1" orgId="acme" open onClose={onClose} sharedWithOrg={sharedWithOrg} />);
  return { onClose };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.selfId = "olive";
  answer({});
});

describe("ChatMembersPanel", () => {
  it("shows the owner an empty state and the way to add someone", () => {
    renderPanel();
    expect(screen.getByTestId("chat-members-empty")).toHaveTextContent("No one else has their own access yet");
    expect(screen.getByTestId("chat-members-empty")).toHaveTextContent("They start with View");
    expect(screen.getByTestId("chat-members-list")).toHaveTextContent("Olive Owner (you)");
    expect(screen.getByTestId("chat-members-count")).toHaveTextContent("0 of 25 people");
    fireEvent.click(screen.getByTestId("chat-members-add"));
    expect(screen.getByTestId("grant-dialog")).toHaveTextContent("add as owner");
  });

  it("lets the owner change and remove each member", async () => {
    answer({ members: [m("dana", ChatMemberRole.VIEW), m("max", ChatMemberRole.MANAGE)], memberCount: 2 });
    renderPanel();
    expect(screen.getByTestId("chat-member-dana")).toHaveTextContent("View");
    expect(screen.getByTestId("chat-member-max")).toHaveTextContent("Manage");
    fireEvent.click(screen.getByTestId("chat-member-change-dana"));
    expect(screen.getByTestId("grant-dialog")).toHaveTextContent("change Dana as owner");
    fireEvent.click(screen.getByTestId("chat-member-remove-max"));
    await waitFor(() => expect(mocks.revoke).toHaveBeenCalledWith("c1", "max"));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Removed Max");
  });

  it("gives a manager the same controls, and their own row a leave instead", () => {
    mocks.selfId = "max";
    answer({ members: [m("dana", ChatMemberRole.REPLY), m("max", ChatMemberRole.MANAGE)], memberCount: 2, callerRole: ChatMemberRole.MANAGE });
    renderPanel();
    expect(screen.getByTestId("chat-members-list")).not.toHaveTextContent("Olive Owner (you)");
    expect(screen.getByTestId("chat-member-remove-dana")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-member-remove-max")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-member-max")).toHaveTextContent("Max (you)");
    expect(screen.getByTestId("chat-member-leave")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("chat-members-add"));
    expect(screen.getByTestId("grant-dialog")).toHaveTextContent("add as manager");
  });

  it("shows a reply member who a request waits on, with no controls but their own leave", async () => {
    mocks.selfId = "dana";
    answer({ members: [m("dana", ChatMemberRole.REPLY), m("max", ChatMemberRole.MANAGE)], memberCount: 3, callerRole: ChatMemberRole.REPLY });
    renderPanel(true);
    expect(screen.queryByTestId("chat-members-add")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-member-remove-max")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-member-change-max")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-members-hidden")).toHaveTextContent("1 more can view.");
    fireEvent.click(screen.getByTestId("chat-member-leave"));
    expect(screen.getByTestId("chat-member-leave-confirm")).toHaveTextContent("You can still view it while it is shared");
    fireEvent.click(screen.getByTestId("chat-member-leave-confirm-button"));
    await waitFor(() => expect(mocks.revoke).toHaveBeenCalledWith("c1", "dana"));
    // Still readable through the org share: nothing to close.
    expect(mocks.leaveSharedChat).not.toHaveBeenCalled();
  });

  it("shows a viewer the owner and a count, and lets a grantee of a private chat leave it", async () => {
    mocks.selfId = "rae";
    answer({ members: [], memberCount: 2, callerRole: ChatMemberRole.VIEW });
    const { onClose } = renderPanel(false);
    expect(screen.getByTestId("chat-members-list")).toHaveTextContent("Olive Owner");
    expect(screen.getByTestId("chat-members-hidden")).toHaveTextContent("2 people have their own access.");
    expect(screen.queryByTestId("chat-members-add")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-members-empty")).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("chat-member-leave"));
    expect(screen.getByTestId("chat-member-leave-confirm")).toHaveTextContent("until someone adds you again");
    fireEvent.click(screen.getByTestId("chat-member-leave-confirm-button"));
    await waitFor(() => expect(mocks.revoke).toHaveBeenCalledWith("c1", "rae"));
    expect(onClose).toHaveBeenCalled();
    expect(mocks.leaveSharedChat).toHaveBeenCalledWith("c1");
  });

  it("offers an org-wide viewer of a shared chat nothing to leave", () => {
    mocks.selfId = "rae";
    answer({ members: [], memberCount: 1, callerRole: ChatMemberRole.VIEW });
    renderPanel(true);
    expect(screen.queryByTestId("chat-member-leave")).not.toBeInTheDocument();
  });

  it("keeps removal when granting is turned off, and offers no add or change", async () => {
    answer({ members: [m("dana", ChatMemberRole.REPLY)], memberCount: 1, grantsEnabled: false });
    renderPanel();
    expect(screen.queryByTestId("chat-members-add")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-member-change-dana")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-members-grants-off")).toHaveTextContent("You can still remove anyone's access");
    fireEvent.click(screen.getByTestId("chat-member-remove-dana"));
    await waitFor(() => expect(mocks.revoke).toHaveBeenCalledWith("c1", "dana"));
  });

  it("stops adding at 25 members", () => {
    const members = Array.from({ length: 25 }, (_, i) => m(`u${i}`, ChatMemberRole.VIEW));
    answer({ members, memberCount: 25 });
    renderPanel();
    expect(screen.getByTestId("chat-members-add")).toBeDisabled();
    expect(screen.getByTestId("chat-members-full")).toHaveTextContent("Remove someone to add another");
    expect(screen.getByTestId("chat-members-count")).toHaveTextContent("25 of 25 people");
  });

  it("shows a refused removal in place", async () => {
    mocks.revoke.mockRejectedValueOnce(new Error("the server said no"));
    answer({ members: [m("dana", ChatMemberRole.VIEW)], memberCount: 1 });
    renderPanel();
    fireEvent.click(screen.getByTestId("chat-member-remove-dana"));
    expect(await screen.findByRole("alert")).toHaveTextContent("the server said no");
  });
});
