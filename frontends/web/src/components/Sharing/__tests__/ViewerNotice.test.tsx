/**
 * The viewer's read-only notice in place of the composer: whose chat it is
 * and what read-only means; and, for an admin or owner of the chat's org
 * only, "Make private".
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({ role: "member", activeChatId: "c1" as string | null }));
const mocks = vi.hoisted(() => ({
  unshare: vi.fn(async () => ({})),
  clearCurrentChat: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({ orgs: [{ id: "acme", name: "Acme", role: state.role }] }),
}));
vi.mock("@/hooks/sharing-queries", () => ({
  useOrgMemberNames: () => new Map([["dana", "Dana"]]),
  useSetChatShared: () => ({ unshare: mocks.unshare, isPending: false }),
}));
vi.mock("@/store/chatStore", () => ({
  useChatStore: Object.assign(vi.fn(), {
    getState: () => ({ activeChatId: state.activeChatId, clearCurrentChat: mocks.clearCurrentChat }),
  }),
}));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));

const { ViewerNotice, canModerateShares } = await import("../ViewerNotice");

const chat = { id: "c1", userId: "dana", orgId: "acme", viewerAccess: true } as never;

beforeEach(() => {
  vi.clearAllMocks();
  state.role = "member";
  state.activeChatId = "c1";
});

describe("ViewerNotice", () => {
  it("says whose chat it is and that only they can act in it", () => {
    render(<ViewerNotice chat={chat} />);
    const note = screen.getByRole("note", { name: "Read-only chat" });
    expect(note).toHaveTextContent("Shared by Dana with Acme · Read only");
    expect(note).toHaveTextContent("Only Dana can send messages, approve tool calls or answer questions");
    expect(screen.queryByTestId("viewer-make-private")).not.toBeInTheDocument();
  });

  it("lets an org admin make it private, and leaves the chat they can no longer read", async () => {
    state.role = "admin";
    render(<ViewerNotice chat={chat} />);
    fireEvent.click(screen.getByTestId("viewer-make-private"));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Make private" }));
    await waitFor(() => expect(mocks.unshare).toHaveBeenCalledWith("c1"));
    expect(mocks.clearCurrentChat).toHaveBeenCalledWith(null);
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Made private");
  });

  it("only admins and owners moderate", () => {
    expect(canModerateShares("owner")).toBe(true);
    expect(canModerateShares("admin")).toBe(true);
    expect(canModerateShares("member")).toBe(false);
    expect(canModerateShares(undefined)).toBe(false);
  });
});
