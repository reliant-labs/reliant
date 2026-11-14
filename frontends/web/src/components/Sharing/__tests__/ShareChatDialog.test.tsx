/**
 * The owner's share toggle: it says what the org's members will and will not
 * see before sharing, shares or stops sharing, and shows a refusal in place.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";

const state = vi.hoisted(() => ({ chat: undefined as Record<string, unknown> | undefined }));
const mocks = vi.hoisted(() => ({
  share: vi.fn(async () => ({})),
  unshare: vi.fn(async () => ({})),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/chat-queries", () => ({ useChat: () => ({ data: state.chat }) }));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ orgs: [{ id: "acme", name: "Acme", role: "member" }] }) }));
vi.mock("@/hooks/sharing-queries", () => ({
  useSetChatShared: () => ({ share: mocks.share, unshare: mocks.unshare, isPending: false }),
}));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));

const { ShareChatDialog, SHARED_CHAT_SEES, SHARED_CHAT_DOES_NOT_SEE } = await import("../ShareChatDialog");

beforeEach(() => {
  vi.clearAllMocks();
  state.chat = { id: "c1", orgId: "acme", visibility: ResourceVisibility.PRIVATE };
});

describe("ShareChatDialog", () => {
  it("discloses what members will and will not see, then shares", async () => {
    const onClose = vi.fn();
    render(<ShareChatDialog chatId="c1" open onClose={onClose} />);
    expect(screen.getByText("Share with Acme", { selector: "h2, h3, [id]" })).toBeInTheDocument();
    for (const item of [...SHARED_CHAT_SEES, ...SHARED_CHAT_DOES_NOT_SEE]) {
      expect(screen.getByText(item)).toBeInTheDocument();
    }
    expect(screen.getByText(/Tool results: file contents and command output/)).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("share-chat-confirm"));
    await waitFor(() => expect(mocks.share).toHaveBeenCalledWith("c1"));
    expect(mocks.unshare).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Shared with Acme");
  });

  it("stops sharing a shared chat", async () => {
    state.chat = { id: "c1", orgId: "acme", visibility: ResourceVisibility.ORG };
    render(<ShareChatDialog chatId="c1" open onClose={vi.fn()} />);
    expect(screen.queryByText(SHARED_CHAT_SEES[0])).not.toBeInTheDocument();
    expect(screen.getByTestId("share-chat-confirm")).toHaveTextContent("Stop sharing");
    fireEvent.click(screen.getByTestId("share-chat-confirm"));
    await waitFor(() => expect(mocks.unshare).toHaveBeenCalledWith("c1"));
    expect(mocks.share).not.toHaveBeenCalled();
  });

  it("shows a refusal in place and stays open", async () => {
    mocks.share.mockRejectedValueOnce(new Error("sharing needs a session in the app"));
    const onClose = vi.fn();
    render(<ShareChatDialog chatId="c1" open onClose={onClose} />);
    fireEvent.click(screen.getByTestId("share-chat-confirm"));
    expect(await screen.findByRole("alert")).toHaveTextContent("sharing needs a session in the app");
    expect(onClose).not.toHaveBeenCalled();
  });
});
